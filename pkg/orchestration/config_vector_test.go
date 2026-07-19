package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/grovetools/eval/pkg/record"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
)

// writeFile writes content to a file under dir and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return p
}

func TestComputeConfigVectorHashesBriefingBytes(t *testing.T) {
	dir := t.TempDir()
	briefing := writeFile(t, dir, "briefing.xml", "<prompt>hello</prompt>")

	job := &Job{ID: "j1", Type: JobTypeHeadlessAgent}
	plan := &Plan{Directory: dir}

	v := ComputeJobConfigVector(job, plan, "", "", nil, nil, briefing)
	got := v.Components[componentBriefing]
	if got == "" {
		t.Fatal("briefing component missing")
	}
	// sha256 of the exact rendered bytes, lowercase hex, full length (D37).
	want := hashBytes([]byte("<prompt>hello</prompt>"))
	if got != want {
		t.Fatalf("briefing hash = %s, want %s", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("hash length = %d, want 64", len(got))
	}
}

// Cold-then-hot ordering is load-bearing: the two bundles are concatenated
// before hashing, so swapping them must produce a different hash. If this test
// ever passes with the order swapped, the concatenation has been replaced by
// something order-insensitive and two different contexts will collide.
func TestContextHashIsOrderSensitive(t *testing.T) {
	dir := t.TempDir()
	cold := writeFile(t, dir, "cold.md", "COLD")
	hot := writeFile(t, dir, "hot.md", "HOT")

	job := &Job{ID: "j1", Type: JobTypeOneshot}
	plan := &Plan{Directory: dir}

	forward := ComputeJobConfigVector(job, plan, "", "",
		&jobContextPaths{Cold: cold, Hot: hot}, nil, "")
	swapped := ComputeJobConfigVector(job, plan, "", "",
		&jobContextPaths{Cold: hot, Hot: cold}, nil, "")

	if forward.Components[componentContext] == "" {
		t.Fatal("context component missing")
	}
	if forward.Components[componentContext] == swapped.Components[componentContext] {
		t.Fatal("cold/hot ordering is not reflected in the context hash")
	}
}

// A job with no context source at all must omit the key entirely rather than
// record the hash of nothing — absent means "not measured" (D4/D10).
func TestContextKeyOmittedWhenNoSource(t *testing.T) {
	job := &Job{ID: "j1", Type: JobTypeInteractiveAgent}
	plan := &Plan{Directory: t.TempDir()}

	v := ComputeJobConfigVector(job, plan, "", "", nil, nil, "")
	if _, present := v.Components[componentContext]; present {
		t.Fatal("context key present despite there being no context source")
	}
	for _, key := range []string{componentPrompt, componentSkills, componentPlan, componentBriefing} {
		if _, present := v.Components[key]; present {
			t.Fatalf("%q present despite no source", key)
		}
	}
}

func TestPlanComponentHashesPromptBody(t *testing.T) {
	job := &Job{ID: "j1", Type: JobTypeHeadlessAgent, PromptBody: "do the thing"}
	plan := &Plan{Directory: t.TempDir()}

	v := ComputeJobConfigVector(job, plan, "", "", nil, nil, "")
	if v.Components[componentPlan] != hashBytes([]byte("do the thing")) {
		t.Fatalf("plan component = %q", v.Components[componentPlan])
	}
}

// P1-06: a harness-supplied component lands inside the hash, which is the
// whole point — stamping it after the fact would leave the axis outside
// ConfigHash and let D6 merge genuinely distinct environments.
func TestHarnessSuppliedComponentsParticipateInHash(t *testing.T) {
	plan := &Plan{Directory: t.TempDir()}
	base := &Job{ID: "j1", Type: JobTypeHeadlessAgent, PromptBody: "body"}
	withEnv := &Job{
		ID: "j1", Type: JobTypeHeadlessAgent, PromptBody: "body",
		ConfigComponents: map[string]string{"env": "deadbeef"},
	}

	bv := ComputeJobConfigVector(base, plan, "", "", nil, nil, "")
	ev := ComputeJobConfigVector(withEnv, plan, "", "", nil, nil, "")

	if ev.Components["env"] != "deadbeef" {
		t.Fatalf("env component = %q, want deadbeef", ev.Components["env"])
	}
	if bv.Hash() == ev.Hash() {
		t.Fatal("harness-supplied component did not change the vector hash")
	}
}

// A harness must not be able to shadow an executor-owned component: that would
// let an external input forge the identity of the run's real configuration.
func TestReservedComponentKeysAreRefused(t *testing.T) {
	dir := t.TempDir()
	briefing := writeFile(t, dir, "b.xml", "REAL")
	plan := &Plan{Directory: dir}

	for _, reserved := range []string{
		componentPrompt, componentContext, componentMemory,
		componentSkills, componentPlan, componentBriefing,
	} {
		job := &Job{
			ID: "j1", Type: JobTypeHeadlessAgent,
			ConfigComponents: map[string]string{reserved: "attacker-supplied"},
		}
		v := ComputeJobConfigVector(job, plan, "", "", nil, nil, briefing)
		if v.Components[reserved] == "attacker-supplied" {
			t.Errorf("reserved key %q was overwritten by a harness-supplied value", reserved)
		}
	}

	// And the executor's own computed value survives intact.
	job := &Job{
		ID: "j1", Type: JobTypeHeadlessAgent,
		ConfigComponents: map[string]string{componentBriefing: "attacker-supplied"},
	}
	v := ComputeJobConfigVector(job, plan, "", "", nil, nil, briefing)
	if v.Components[componentBriefing] != hashBytes([]byte("REAL")) {
		t.Fatal("computed briefing hash was disturbed by a colliding harness key")
	}
}

// P1-07: the extracted helper must reproduce the original inline precedence
// for all six branches, or the stamped model will disagree with the model the
// run actually used.
//
// Every case asserts the returned MODEL as well as the source. Asserting only
// the source (or only `model != ""`) is worthless here: the failure this test
// exists to catch is a branch returning the wrong model — e.g. a CLI override
// that reports source "CLI override" while handing back the job's model — and
// a source-only assertion passes straight through that. Each branch is given a
// distinct sentinel model so no two branches can be confused for each other.
func TestResolveEffectiveModelPrecedence(t *testing.T) {
	// Every input that a lower-precedence branch would pick is populated in
	// each case, so a branch that fires out of order returns a *different*
	// sentinel and the model assertion catches it.
	tests := []struct {
		name       string
		cfg        *ExecutorConfig
		job        *Job
		plan       *Plan
		directive  *ChatDirective
		wantModel  string
		wantSource string
	}{
		{
			name:      "CLI override wins over everything",
			cfg:       &ExecutorConfig{ModelOverride: "cli-model"},
			job:       &Job{Model: "job-model"},
			directive: &ChatDirective{Model: "directive-model"},
			plan: &Plan{
				Config:        &PlanConfig{Model: "plan-model"},
				Orchestration: &Config{OneshotModel: "global-model"},
			},
			wantModel:  "cli-model",
			wantSource: "CLI override",
		},
		{
			// The chat turn directive (`<!-- grove: {"model": ...} -->`) sits
			// at priority 2: it beats the job's own frontmatter, which is the
			// whole point of a per-turn model. Omitting this branch here is
			// what let the stamp and the chat run disagree.
			name:      "chat directive beats job frontmatter",
			cfg:       &ExecutorConfig{},
			job:       &Job{Model: "job-model"},
			directive: &ChatDirective{Model: "directive-model"},
			plan: &Plan{
				Config:        &PlanConfig{Model: "plan-model"},
				Orchestration: &Config{OneshotModel: "global-model"},
			},
			wantModel:  "directive-model",
			wantSource: "chat directive",
		},
		{
			// A directive with no model must not shadow the frontmatter —
			// every chat turn has a directive object, most carry no model.
			name:      "empty directive model falls through to job frontmatter",
			cfg:       &ExecutorConfig{},
			job:       &Job{Model: "job-model"},
			directive: &ChatDirective{Template: "chat"},
			plan: &Plan{
				Config:        &PlanConfig{Model: "plan-model"},
				Orchestration: &Config{OneshotModel: "global-model"},
			},
			wantModel:  "job-model",
			wantSource: "job frontmatter",
		},
		{
			name: "job frontmatter beats plan config",
			cfg:  &ExecutorConfig{},
			job:  &Job{Model: "job-model"},
			plan: &Plan{
				Config:        &PlanConfig{Model: "plan-model"},
				Orchestration: &Config{OneshotModel: "global-model"},
			},
			wantModel:  "job-model",
			wantSource: "job frontmatter",
		},
		{
			name: "plan config beats global config",
			cfg:  &ExecutorConfig{},
			job:  &Job{},
			plan: &Plan{
				Config:        &PlanConfig{Model: "plan-model"},
				Orchestration: &Config{OneshotModel: "global-model"},
			},
			wantModel:  "plan-model",
			wantSource: "plan config",
		},
		{
			// The fifth branch (config_vector.go:153). It was missing from
			// this table entirely, so nothing checked that the global
			// oneshot_model is ever consulted.
			name:       "global config beats the default fallback",
			cfg:        &ExecutorConfig{},
			job:        &Job{},
			plan:       &Plan{Orchestration: &Config{OneshotModel: "global-model"}},
			wantModel:  "global-model",
			wantSource: "global config",
		},
		{
			name:       "falls back to the default when nothing is set",
			cfg:        &ExecutorConfig{},
			job:        &Job{},
			plan:       &Plan{},
			wantModel:  anthropicmodels.DefaultModel,
			wantSource: "default fallback",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model, source := resolveEffectiveModel(tc.cfg, tc.job, tc.plan, tc.directive)
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
			if model != tc.wantModel {
				t.Errorf("model = %q, want %q", model, tc.wantModel)
			}
		})
	}
}

// The chain's last act is alias resolution: the stamped model must be the full
// API id the run actually used, not the shorthand the user typed. The alias is
// taken from the models package rather than hard-coded so a renamed alias
// cannot quietly turn this into a no-op.
func TestResolveEffectiveModelResolvesAliases(t *testing.T) {
	aliases := anthropicmodels.Aliases()
	if len(aliases) == 0 {
		t.Fatal("no model aliases defined; this test can no longer prove anything")
	}
	names := make([]string, 0, len(aliases))
	for a := range aliases {
		names = append(names, a)
	}
	sort.Strings(names)
	alias := names[0]
	fullID := aliases[alias]
	if alias == fullID {
		t.Fatalf("alias %q resolves to itself; pick a real alias", alias)
	}

	model, source := resolveEffectiveModel(
		&ExecutorConfig{}, &Job{Model: alias}, &Plan{}, nil)
	if source != "job frontmatter" {
		t.Errorf("source = %q, want %q", source, "job frontmatter")
	}
	if model != fullID {
		t.Errorf("model = %q, want the resolved id %q (alias %q was not expanded)",
			model, fullID, alias)
	}
}

// Agent families carry a Provider (there is a real agent runtime); oneshot and
// chat are grove-internal API calls and must not.
func TestProviderScalarByFamily(t *testing.T) {
	plan := &Plan{Directory: t.TempDir()}

	agentV := ComputeJobConfigVector(
		&Job{ID: "j1", Type: JobTypeHeadlessAgent, Model: "claude-opus-4-8"},
		plan, "", "", nil, nil, "")
	if agentV.Provider == "" {
		t.Error("agent-family vector has no Provider")
	}
	if agentV.Model != "claude-opus-4-8" {
		t.Errorf("agent Model = %q, want the job's own model", agentV.Model)
	}

	oneshotV := ComputeJobConfigVector(
		&Job{ID: "j2", Type: JobTypeOneshot}, plan,
		"cli-model", "", nil, nil, "")
	if oneshotV.Provider != "" {
		t.Errorf("oneshot vector carries Provider %q, want empty", oneshotV.Provider)
	}
}

// An agent job whose frontmatter omits a model legitimately has none — the
// vector must not invent a default, which would claim a configuration the run
// did not have.
func TestAgentModelStaysEmptyWhenUnset(t *testing.T) {
	v := ComputeJobConfigVector(
		&Job{ID: "j1", Type: JobTypeHeadlessAgent},
		&Plan{Directory: t.TempDir()}, "", "", nil, nil, "")
	if v.Model != "" {
		t.Fatalf("Model = %q, want empty for an agent job with no declared model", v.Model)
	}
}

// P1-08: full-length commit hash, and a clean empty on a non-git directory.
func TestWorktreeCommitScalar(t *testing.T) {
	t.Run("git repo yields the full 40-char hash", func(t *testing.T) {
		dir := t.TempDir()
		for _, args := range [][]string{
			{"init"},
			{"config", "user.email", "test@example.com"},
			{"config", "user.name", "Test"},
			{"commit", "--allow-empty", "-m", "initial"},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				// Not a skip: git is a hard dependency of the code under test
				// (the worktree-commit scalar shells out to it), so a missing
				// or broken git means this assertion cannot run — and a test
				// that quietly skips its only assertion reads as a pass while
				// protecting nothing.
				t.Fatalf("git setup %v failed (%v): %s", args, err, out)
			}
		}

		v := ComputeJobConfigVector(&Job{ID: "j1", Type: JobTypeHeadlessAgent},
			&Plan{Directory: dir}, "", dir, nil, nil, "")

		if len(v.WorktreeCommit) != 40 {
			t.Fatalf("worktree commit = %q (len %d), want a full 40-char hash",
				v.WorktreeCommit, len(v.WorktreeCommit))
		}
		if strings.ContainsAny(v.WorktreeCommit, " \n\t") {
			t.Fatalf("worktree commit has surrounding whitespace: %q", v.WorktreeCommit)
		}
	})

	t.Run("non-git dir yields empty and no failure", func(t *testing.T) {
		dir := t.TempDir()
		v := ComputeJobConfigVector(&Job{ID: "j1", Type: JobTypeHeadlessAgent},
			&Plan{Directory: dir}, "", dir, nil, nil, "")
		if v.WorktreeCommit != "" {
			t.Fatalf("worktree commit = %q, want empty outside a git repo", v.WorktreeCommit)
		}
	})
}

// P1-05: the artifact must round-trip back into a record.ConfigVector, and
// carry the hash computed at stamp time.
func TestWriteConfigVectorArtifactRoundTrips(t *testing.T) {
	dir := t.TempDir()
	v := record.ConfigVector{
		Model:          "anthropic/claude-fable-5",
		Provider:       "claude",
		WorktreeCommit: strings.Repeat("a", 40),
		Components:     map[string]string{"briefing": "h1", "plan": "h2"},
	}

	if err := WriteConfigVectorArtifact(dir, "job-1", v); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(ConfigVectorArtifactPath(dir, "job-1"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var back record.ConfigVector
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("artifact does not parse as a record.ConfigVector: %v", err)
	}
	if back.Model != v.Model || back.Provider != v.Provider ||
		back.WorktreeCommit != v.WorktreeCommit ||
		back.Components["briefing"] != "h1" {
		t.Fatalf("round-trip mismatch: %+v", back)
	}

	var withHash struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(raw, &withHash); err != nil {
		t.Fatalf("unmarshal hash: %v", err)
	}
	if withHash.Hash != v.Hash() {
		t.Fatalf("recorded hash %q != vector hash %q", withHash.Hash, v.Hash())
	}

	// Re-stamping is a whole-file overwrite, so it is idempotent.
	if err := WriteConfigVectorArtifact(dir, "job-1", v); err != nil {
		t.Fatalf("re-write: %v", err)
	}
	raw2, _ := os.ReadFile(ConfigVectorArtifactPath(dir, "job-1"))
	if string(raw) != string(raw2) {
		t.Fatal("re-stamping the same vector produced different bytes")
	}
}

func TestReadConfigVectorArtifactMissing(t *testing.T) {
	if _, _, ok := ReadConfigVectorArtifact(t.TempDir(), "nope"); ok {
		t.Fatal("ok=true for an absent config vector")
	}
}

// newDirectiveChatFixture builds a runnable chat job whose single user turn
// carries a `model:` directive, plus the rules file and swept source the chat
// path requires (a chat whose rules resolve zero files fails the empty-freeze
// gate before the turn ever fires). The plan dir is git-inited but never
// committed, so WorktreeCommit is empty for every fixture — two fixtures
// therefore differ in nothing but the directive model.
func newDirectiveChatFixture(t *testing.T, jobModel, directiveModel string) (*Plan, *Job) {
	t.Helper()

	front := "rules_file: ctx.rules\n"
	if jobModel != "" {
		front += "model: " + jobModel + "\n"
	}
	// A user turn is a grove directive carrying a template (the parser reads a
	// template-less directive as an LLM response), with the ask quoted beneath
	// it — exactly the shape `flow` writes back into a chat file and a user
	// then edits to add `"model"`.
	body := `Kick off the chat.

<!-- grove: {"template": "chat", "model": "` + directiveModel + `"} -->
> Please answer.`

	plan, job := newChatJobFixture(t, front, body)
	gitInitDir(t, plan.Directory)
	if err := os.MkdirAll(filepath.Join(plan.Directory, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Directory, "src", "a.go"),
		[]byte("package src\n\nvar A = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Directory, "ctx.rules"),
		[]byte("src/a.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return plan, job
}

// runChatTurnAndReadVector runs one mock chat turn and returns the vector the
// turn stamped, parsed back off disk.
func runChatTurnAndReadVector(t *testing.T, plan *Plan, job *Job) record.ConfigVector {
	t.Helper()
	mockResponse := filepath.Join(t.TempDir(), "mock-response.md")
	if err := os.WriteFile(mockResponse, []byte("mock response"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockResponse)

	if err := NewOneShotExecutor(NewMockLLMClient(), nil).
		Execute(context.Background(), job, plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	raw, err := os.ReadFile(ConfigVectorArtifactPath(plan.Directory, job.ID))
	if err != nil {
		t.Fatalf("chat turn stamped no config vector: %v", err)
	}
	var v record.ConfigVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("stamped vector does not parse: %v", err)
	}
	return v
}

// The BLOCKER regression test, and the one the helper-level coverage could not
// stand in for: this exercises the chat CALL PATH, executeChatJob → stamp, not
// resolveEffectiveModel in isolation. The chat path used to run its own private
// copy of the precedence chain — the copy that knew about directives — while
// the stamp ran a copy that did not, so a turn dispatched to the directive's
// model recorded the frontmatter's.
//
// The two models are deliberately distinct sentinels rather than real ids: the
// assertion must pin WHICH BRANCH won, and a literal expectation cannot be
// satisfied by an alias table (alias expansion has its own test) or by any
// other branch of the chain firing.
func TestChatTurnStampsTheDirectiveModel(t *testing.T) {
	const (
		jobModel       = "chat-frontmatter-model"
		directiveModel = "chat-directive-model"
	)
	plan, job := newDirectiveChatFixture(t, jobModel, directiveModel)

	v := runChatTurnAndReadVector(t, plan, job)

	if v.Model != directiveModel {
		t.Fatalf("stamped model = %q, want the directive's %q "+
			"(the turn ran on the directive model; the vector must say so)",
			v.Model, directiveModel)
	}
	if v.Model == jobModel {
		t.Fatalf("stamped model = %q: the vector recorded the frontmatter "+
			"model for a turn the directive redirected", v.Model)
	}
}

// The consequence that makes the above a data-corruption bug rather than a
// cosmetic one: ConfigHash is the key the whole D6 comparison matrix joins on,
// so two turns on genuinely different models MUST NOT collide.
//
// Read the control assertion before changing this test. A bare
// `vA.Hash() != vB.Hash()` would be CONFOUNDED and therefore worthless here:
// the `plan` component hashes the job's prompt body, and the directive comment
// carrying the model lives in that body, so the two fixtures' hashes differ on
// those bytes whether or not the model scalar was stamped at all. The load-
// bearing assertion is instead the control swap — take the vector the B turn
// really stamped and change NOTHING but the model to A's — which can only pass
// when the model the turn ran on actually reached the key. Under a stamp that
// ignores directives both turns stamp the same model, the swap is a no-op, and
// this fails.
func TestChatTurnsOnDifferentModelsDoNotShareAConfigHash(t *testing.T) {
	const modelA = "chat-model-alpha"
	const modelB = "chat-model-beta"

	planA, jobA := newDirectiveChatFixture(t, "", modelA)
	vA := runChatTurnAndReadVector(t, planA, jobA)

	planB, jobB := newDirectiveChatFixture(t, "", modelB)
	vB := runChatTurnAndReadVector(t, planB, jobB)

	if vA.Model != modelA || vB.Model != modelB {
		t.Fatalf("stamped models = (%q, %q), want (%q, %q)",
			vA.Model, vB.Model, modelA, modelB)
	}

	// The two turns sent byte-identical prompts: the directive selects the
	// model, it does not change what was asked. So "same request, different
	// model" is exactly the pair of conditions D6 must keep apart.
	if vA.Components[componentBriefing] != vB.Components[componentBriefing] {
		t.Fatalf("briefing hashes differ (%s vs %s); the fixtures no longer "+
			"isolate the model", vA.Components[componentBriefing], vB.Components[componentBriefing])
	}

	control := vB
	control.Model = vA.Model
	if control.Hash() == vB.Hash() {
		t.Fatalf("swapping the stamped model (%q -> %q) left ConfigHash at %s; "+
			"the model is invisible to the join key and two conditions merge",
			vB.Model, vA.Model, vB.Hash())
	}

	if vA.Hash() == vB.Hash() {
		t.Fatalf("two chat turns on different models share ConfigHash %s; "+
			"the D6 join would treat them as one condition", vA.Hash())
	}
}
