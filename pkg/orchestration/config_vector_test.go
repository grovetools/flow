package orchestration

import (
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

	v := ComputeJobConfigVector(job, plan, nil, "", nil, nil, briefing)
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

	forward := ComputeJobConfigVector(job, plan, nil, "",
		&jobContextPaths{Cold: cold, Hot: hot}, nil, "")
	swapped := ComputeJobConfigVector(job, plan, nil, "",
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

	v := ComputeJobConfigVector(job, plan, nil, "", nil, nil, "")
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

	v := ComputeJobConfigVector(job, plan, nil, "", nil, nil, "")
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

	bv := ComputeJobConfigVector(base, plan, nil, "", nil, nil, "")
	ev := ComputeJobConfigVector(withEnv, plan, nil, "", nil, nil, "")

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
		v := ComputeJobConfigVector(job, plan, nil, "", nil, nil, briefing)
		if v.Components[reserved] == "attacker-supplied" {
			t.Errorf("reserved key %q was overwritten by a harness-supplied value", reserved)
		}
	}

	// And the executor's own computed value survives intact.
	job := &Job{
		ID: "j1", Type: JobTypeHeadlessAgent,
		ConfigComponents: map[string]string{componentBriefing: "attacker-supplied"},
	}
	v := ComputeJobConfigVector(job, plan, nil, "", nil, nil, briefing)
	if v.Components[componentBriefing] != hashBytes([]byte("REAL")) {
		t.Fatal("computed briefing hash was disturbed by a colliding harness key")
	}
}

// P1-07: the extracted helper must reproduce the original inline precedence
// for all five branches, or the stamped model will disagree with the model the
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
		wantModel  string
		wantSource string
	}{
		{
			name: "CLI override wins over everything",
			cfg:  &ExecutorConfig{ModelOverride: "cli-model"},
			job:  &Job{Model: "job-model"},
			plan: &Plan{
				Config:        &PlanConfig{Model: "plan-model"},
				Orchestration: &Config{OneshotModel: "global-model"},
			},
			wantModel:  "cli-model",
			wantSource: "CLI override",
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
			model, source := resolveEffectiveModel(tc.cfg, tc.job, tc.plan)
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
		&ExecutorConfig{}, &Job{Model: alias}, &Plan{})
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
		plan, nil, "", nil, nil, "")
	if agentV.Provider == "" {
		t.Error("agent-family vector has no Provider")
	}
	if agentV.Model != "claude-opus-4-8" {
		t.Errorf("agent Model = %q, want the job's own model", agentV.Model)
	}

	oneshotV := ComputeJobConfigVector(
		&Job{ID: "j2", Type: JobTypeOneshot}, plan,
		&ExecutorConfig{ModelOverride: "cli-model"}, "", nil, nil, "")
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
		&Plan{Directory: t.TempDir()}, nil, "", nil, nil, "")
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
			&Plan{Directory: dir}, nil, dir, nil, nil, "")

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
			&Plan{Directory: dir}, nil, dir, nil, nil, "")
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
