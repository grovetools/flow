package orchestration

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	grovelogging "github.com/grovetools/core/logging"
)

// initPiSessionTestRepo makes dir a git repository.
//
// resolvePiSessionContextDir resolves the context directory through
// GetProjectGitRoot, which walks up from the plan directory and — for a bare
// temp dir outside any repo — lands on the checkout the TEST is running in.
// Without an enclosing repo of its own, the fixture would silently resolve its
// rules against the flow source tree.
func initPiSessionTestRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init in %s: %v (%s)", dir, err, out)
	}
}

// TestLaunchPiSessionChat_EndToEnd exercises the whole first-launch path in one
// go — preflight, layer freeze, seed synthesis, descriptor, wake, launch — and
// asserts the invariant that ties them together: the bytes in the seed ARE the
// bytes in the frozen layer artifacts. If those two ever diverge, layers.json
// stops being a truthful record of what the session is holding, and the audit
// story this responder inherits from oracle chats quietly becomes fiction.
func TestLaunchPiSessionChat_EndToEnd(t *testing.T) {
	planDir := t.TempDir()
	initPiSessionTestRepo(t, planDir)

	// Rules lines are plain RELATIVE paths, resolved against the context dir —
	// the same shape the layer-engine fixtures use. A glob would instead be
	// expanded against whatever workspace root cx discovers (for a bare temp dir,
	// the process's own repo), making the fixture depend on the checkout it runs
	// in; an absolute path is refused outright as outside any known workspace.
	srcA := filepath.Join(planDir, "alpha.go")
	if err := os.WriteFile(srcA, []byte("package alpha\n\n// a comment\nfunc Alpha() string { return \"alpha\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcB := filepath.Join(planDir, "beta.go")
	if err := os.WriteFile(srcB, []byte("package alpha\n\nfunc Beta() int { return 2 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rulesDir := filepath.Join(planDir, "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rulesPath := filepath.Join(rulesDir, "04-pi.md.rules")
	if err := os.WriteFile(rulesPath, []byte("alpha.go\nbeta.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := &Plan{
		Name:          "pi-smoke",
		Directory:     planDir,
		JobsByID:      map[string]*Job{},
		Orchestration: &Config{AgentTarget: "tmux"},
	}
	job := &Job{
		ID:        "pi-smoke-1",
		Title:     "pi smoke",
		Type:      JobTypeChat,
		Responder: ResponderPiSession,
		Provider:  "pi",
		Model:     "gpt-5.6-sol",
		RulesFile: "rules/04-pi.md.rules",
		Status:    JobStatusPending,
		Filename:  "04-pi.md",
		FilePath:  filepath.Join(planDir, "04-pi.md"),
	}
	plan.Jobs = append(plan.Jobs, job)
	plan.JobsByID[job.ID] = job
	body := "---\nid: pi-smoke-1\ntitle: pi smoke\nstatus: pending\ntype: chat\nresponder: pi-session\n---\n\n" +
		"<!-- grove: {\"template\": \"chat\"} -->\n\nWhat does the seed writer guarantee?\n"
	if err := os.WriteFile(job.FilePath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var launchArgs []string
	restore := swapPiSessionLauncher(func(_ context.Context, _ InteractiveAgentProvider, _ *Job, _ *Plan, _ string, args []string, _ string) error {
		launchArgs = args
		return nil
	})
	defer restore()

	if err := RunPiSessionChat(context.Background(), job, plan); err != nil {
		t.Fatalf("RunPiSessionChat() error = %v", err)
	}

	// --- The layer store exists, exactly as it would for an oracle chat. ---
	layersDir := ContextLayersDir(planDir, job.ID)
	basePath := filepath.Join(layersDir, "00-base.xml")
	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("00-base.xml was not frozen: %v", err)
	}
	manifest, err := LoadLayerManifest(layersDir)
	if err != nil || manifest == nil {
		t.Fatalf("LoadLayerManifest() = (%v, %v), want a frozen manifest", manifest, err)
	}
	if len(manifest.Layers) != 1 || len(manifest.Layers[0].Files) != 2 {
		t.Fatalf("manifest = %d layer(s) / %d file(s), want 1 layer over 2 files", len(manifest.Layers), len(manifest.Layers[0].Files))
	}
	// strip_comments defaults on, so the frozen bytes are the stripped ones.
	if strings.Contains(string(baseBytes), "// a comment") {
		t.Error("frozen layer kept a comment; strip_comments should have removed it")
	}

	// --- The seed embeds those exact bytes. ---
	desc, err := ReadPiSessionDescriptor(planDir, job.ID)
	if err != nil || desc == nil {
		t.Fatalf("ReadPiSessionDescriptor() = (%v, %v), want a descriptor", desc, err)
	}
	seedBytes, err := os.ReadFile(desc.SessionFile)
	if err != nil {
		t.Fatalf("the seed was not written: %v", err)
	}
	seed := string(seedBytes)
	// The bundle rides as one JSON string, so compare on a distinctive
	// inner fragment that survives JSON string encoding unchanged.
	if !strings.Contains(seed, `func Alpha() string { return \"alpha\" }`) {
		t.Error("the seed does not embed the frozen layer content")
	}
	for _, want := range []string{PiSeedStampType, PiSeedFramingType, PiSeedBundleType, PiSeedContractType} {
		if !strings.Contains(seed, want) {
			t.Errorf("the seed is missing a %s entry", want)
		}
	}
	// The contract message must name the chat file the session speaks into —
	// this is how the Phase 3 extension knows where to reconcile from.
	if !strings.Contains(seed, job.FilePath) {
		t.Error("the seed's contract message does not name the chat file")
	}

	// --- The descriptor records the audit facts. ---
	if desc.RulesFile != rulesPath {
		t.Errorf("descriptor rules_file = %q, want %q", desc.RulesFile, rulesPath)
	}
	if len(desc.LayerPaths) != 1 || desc.LayerPaths[0] != basePath {
		t.Errorf("descriptor layer_paths = %v, want [%s]", desc.LayerPaths, basePath)
	}
	if desc.SeedFamily != "openai" || desc.SeedBudget == 0 {
		t.Errorf("descriptor window verdict = {%s budget=%d}, want the openai profile with a budget", desc.SeedFamily, desc.SeedBudget)
	}
	if desc.SeedVersion != piSessionFormatVersion {
		t.Errorf("descriptor seed_format_version = %d, want %d", desc.SeedVersion, piSessionFormatVersion)
	}

	// --- The launch points pi at the seed. ---
	idx := indexOfArg(launchArgs, "--session")
	if idx < 0 || launchArgs[idx+1] != desc.SessionFile {
		t.Errorf("launch argv %v does not point --session at the seed %q", launchArgs, desc.SessionFile)
	}

	// --- A second run must not re-seed. ---
	restoreLiveness := swapPiSessionLiveness(func(string) (bool, int) { return false, 0 })
	defer restoreLiveness()
	if err := RunPiSessionChat(context.Background(), job, plan); err != nil {
		t.Fatalf("second RunPiSessionChat() error = %v", err)
	}
	seedAfter, err := os.ReadFile(desc.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(seedAfter) != seed {
		t.Error("a second run rewrote the seed; the seed must be synthesized exactly once per job")
	}
	entries, err := os.ReadDir(filepath.Dir(desc.SessionFile))
	if err != nil {
		t.Fatal(err)
	}
	sessions := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			sessions++
		}
	}
	if sessions != 1 {
		t.Errorf("session directory holds %d transcripts, want exactly 1", sessions)
	}
}

// TestLaunchPiSessionChat_RefusesWithoutRulesFile: a seeded session with no
// curated context is just a bare agent, which is the one thing this responder
// exists to not be.
func TestLaunchPiSessionChat_RefusesWithoutRulesFile(t *testing.T) {
	planDir := t.TempDir()
	plan := &Plan{Name: "p", Directory: planDir, JobsByID: map[string]*Job{}, Orchestration: &Config{AgentTarget: "tmux"}}
	job := &Job{
		ID: "j1", Type: JobTypeChat, Responder: ResponderPiSession,
		Filename: "04-pi.md", FilePath: filepath.Join(planDir, "04-pi.md"),
	}
	if err := os.WriteFile(job.FilePath, []byte("---\nid: j1\n---\n\nq\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := RunPiSessionChat(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "declares no rules_file") {
		t.Fatalf("RunPiSessionChat() = %v, want a refusal naming the missing rules_file", err)
	}
}

// TestLaunchPiSessionChat_WindowGateBlocksLaunch: an oversized bundle must fail
// BEFORE anything is seeded or launched. A session that boots already at its
// window ceiling gets compacted mid-dialogue, which silently destroys the
// curated context it exists to hold.
func TestLaunchPiSessionChat_WindowGateBlocksLaunch(t *testing.T) {
	planDir := t.TempDir()
	initPiSessionTestRepo(t, planDir)
	// ~1.2MB of Go source ≈ 600K cx tokens, over the 132K budget of a
	// 200K-window Claude.
	big := strings.Repeat("package big\n\nfunc F() int { return 1 }\n", 30_000)
	if err := os.WriteFile(filepath.Join(planDir, "big.go"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	rulesDir := filepath.Join(planDir, "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "big.rules"), []byte("big.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := &Plan{Name: "p", Directory: planDir, JobsByID: map[string]*Job{}, Orchestration: &Config{AgentTarget: "tmux"}}
	job := &Job{
		ID: "j1", Type: JobTypeChat, Responder: ResponderPiSession,
		Model: "claude-sonnet-4-5", RulesFile: "rules/big.rules",
		Filename: "04-pi.md", FilePath: filepath.Join(planDir, "04-pi.md"),
	}
	if err := os.WriteFile(job.FilePath, []byte("---\nid: j1\n---\n\nq\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	launched := false
	restore := swapPiSessionLauncher(func(context.Context, InteractiveAgentProvider, *Job, *Plan, string, []string, string) error {
		launched = true
		return nil
	})
	defer restore()

	err := RunPiSessionChat(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "seed window gate") {
		t.Fatalf("RunPiSessionChat() = %v, want the window gate to refuse", err)
	}
	if launched {
		t.Error("the session was launched despite the window gate refusing")
	}
	if desc, _ := ReadPiSessionDescriptor(planDir, job.ID); desc != nil {
		t.Error("a descriptor was written despite the window gate refusing")
	}
	if _, statErr := os.Stat(ContextLayersDir(planDir, job.ID)); statErr == nil {
		t.Error("context layers were frozen despite the window gate refusing; the gate must run first")
	}
}

// TestExecuteChatJob_PiSessionLaunchFailureIsStamped is the regression guard for
// the silent-launch-failure defect (2026-08-03): a window-gate refusal reached
// only the daemon system log, so the driving coordinator saw a job that had
// simply stopped moving and burned three sibling chats retrying variations of a
// refusal that named its own fix. Contract §7 requires the opposite — `running
// ──launch/preflight failure──► failed`, last_error stamped — so the launch must
// leave all three surfaces populated: the frontmatter status, the frontmatter
// last_error, and the job's own log.
func TestExecuteChatJob_PiSessionLaunchFailureIsStamped(t *testing.T) {
	planDir := t.TempDir()
	initPiSessionTestRepo(t, planDir)
	// Same oversized bundle as the gate test above: ~600K cx tokens against the
	// 132K budget of a 200K-window Claude.
	big := strings.Repeat("package big\n\nfunc F() int { return 1 }\n", 30_000)
	if err := os.WriteFile(filepath.Join(planDir, "big.go"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	rulesDir := filepath.Join(planDir, "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "big.rules"), []byte("big.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	jobPath := filepath.Join(planDir, "04-pi.md")
	content := "---\nid: j1\ntitle: pi chat\nstatus: running\ntype: chat\nresponder: pi-session\n" +
		"provider: pi\nmodel: claude-sonnet-4-5\nrules_file: rules/big.rules\ntemplate: chat\n---\n\n" +
		"<!-- grove: {\"template\": \"chat\"} -->\n\nDesign the thing.\n"
	if err := os.WriteFile(jobPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = "04-pi.md"
	job.FilePath = jobPath

	plan := &Plan{Name: "p", Directory: planDir, JobsByID: map[string]*Job{job.ID: job}, Orchestration: &Config{AgentTarget: "tmux"}}
	plan.Jobs = append(plan.Jobs, job)

	restore := swapPiSessionLauncher(func(context.Context, InteractiveAgentProvider, *Job, *Plan, string, []string, string) error {
		t.Error("the session was launched despite the window gate refusing")
		return nil
	})
	defer restore()

	// The runtime hands executeChatJob a writer that fans out to the job's
	// job.log; a buffer stands in for it here.
	var jobLog bytes.Buffer
	ctx := grovelogging.WithWriter(context.Background(), &jobLog)

	client := &dispatchRecordingLLMClient{}
	err = NewOneShotExecutor(client, nil).Execute(ctx, job, plan)
	if err == nil || !strings.Contains(err.Error(), "seed window gate") {
		t.Fatalf("Execute() = %v, want the window gate to refuse", err)
	}
	if client.called {
		t.Error("LLM client was invoked for a pi-session chat")
	}

	// --- The frontmatter is where the coordinator looks. ---
	if status := frontmatterStatus(t, jobPath); status != string(JobStatusFailed) {
		t.Errorf("frontmatter status = %q, want %q", status, JobStatusFailed)
	}
	fmBytes, readErr := os.ReadFile(jobPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	fm, _, parseErr := ParseFrontmatter(fmBytes)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	lastError, _ := fm["last_error"].(string)
	if !strings.Contains(lastError, "seed window gate") {
		t.Errorf("frontmatter last_error = %q, want the window-gate refusal", lastError)
	}
	// The message must arrive whole, not truncated to a category: it is the
	// error text itself that names the fix.
	if lastError != err.Error() {
		t.Errorf("frontmatter last_error = %q, want the verbatim launch error %q", lastError, err.Error())
	}
	if job.Metadata.LastError != lastError {
		t.Errorf("in-memory LastError = %q, want it consistent with the frontmatter %q", job.Metadata.LastError, lastError)
	}

	// --- And so is the job's own log. ---
	if !strings.Contains(jobLog.String(), "seed window gate") {
		t.Errorf("job.log did not receive the refusal:\n%s", jobLog.String())
	}
}
