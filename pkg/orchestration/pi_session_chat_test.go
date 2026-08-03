package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Schema / veto ---------------------------------------------------------

// TestIsPiSessionResponded pins the predicate triangle: pi-session is its own
// responder, is NOT agent-responded (the two engines differ), and IS
// API-dispatch-vetoed (neither ever reaches an LLM provider).
func TestIsPiSessionResponded(t *testing.T) {
	tests := []struct {
		name                     string
		job                      Job
		piSession, agent, vetoed bool
	}{
		{"chat pi-session", Job{Type: JobTypeChat, Responder: ResponderPiSession}, true, false, true},
		{"chat agent", Job{Type: JobTypeChat, Responder: ResponderAgent}, false, true, true},
		{"chat oracle", Job{Type: JobTypeChat, Responder: ResponderOracle}, false, false, false},
		{"chat default", Job{Type: JobTypeChat}, false, false, false},
		{"interactive_agent pi-session", Job{Type: JobTypeInteractiveAgent, Responder: ResponderPiSession}, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.job.IsPiSessionResponded(); got != tt.piSession {
				t.Errorf("IsPiSessionResponded() = %v, want %v", got, tt.piSession)
			}
			if got := tt.job.IsAgentResponded(); got != tt.agent {
				t.Errorf("IsAgentResponded() = %v, want %v", got, tt.agent)
			}
			if got := tt.job.IsAPIDispatchVetoed(); got != tt.vetoed {
				t.Errorf("IsAPIDispatchVetoed() = %v, want %v", got, tt.vetoed)
			}
		})
	}
}

// TestAcceptsAgentIdleStatus pins the status-ownership half of the responder
// contract: an agent-lifecycle idle event (turn ended, host process alive) is
// news about every ordinary job, and about no pi-session chat — whose statuses
// come only from the explicit flow verbs (contract §7).
func TestAcceptsAgentIdleStatus(t *testing.T) {
	tests := []struct {
		name string
		job  Job
		want bool
	}{
		{"chat pi-session", Job{Type: JobTypeChat, Responder: ResponderPiSession}, false},
		{"chat agent", Job{Type: JobTypeChat, Responder: ResponderAgent}, true},
		{"chat oracle", Job{Type: JobTypeChat, Responder: ResponderOracle}, true},
		{"chat default", Job{Type: JobTypeChat}, true},
		{"interactive_agent", Job{Type: JobTypeInteractiveAgent}, true},
		// A pi-session responder on a non-chat job is not the pi-session
		// responder flavor at all, so it keeps the ordinary behavior.
		{"interactive_agent pi-session", Job{Type: JobTypeInteractiveAgent, Responder: ResponderPiSession}, true},
		{"agent", Job{Type: JobTypeAgent}, true},
		{"oneshot", Job{Type: JobTypeOneshot}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.job.AcceptsAgentIdleStatus(); got != tt.want {
				t.Errorf("AcceptsAgentIdleStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestExecuteChatJob_PiSessionNeverDispatches is the veto proper: a pi-session
// chat ending in a content-bearing user turn — the exact shape that WOULD
// dispatch on the oracle path — must never touch the LLM client. It fails on
// the launch (no agent_target in this bare plan), which is fine: the assertion
// is about what did NOT happen.
func TestExecuteChatJob_PiSessionNeverDispatches(t *testing.T) {
	tmpDir := t.TempDir()
	plan := &Plan{Name: "test-plan", Directory: tmpDir, JobsByID: map[string]*Job{}}

	jobContent := `---
id: pi-chat-1
title: pi-chat
status: pending_user
type: chat
responder: pi-session
provider: pi
template: chat
---

<!-- grove: {"template": "chat"} -->

Design the decomposition for feature X.
`
	jobPath := filepath.Join(tmpDir, "01-pi-chat.md")
	if err := os.WriteFile(jobPath, []byte(jobContent), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = "01-pi-chat.md"
	job.FilePath = jobPath

	if !job.IsPiSessionResponded() {
		t.Fatal("responder: pi-session did not survive LoadJob")
	}

	client := &dispatchRecordingLLMClient{}
	// Execute is expected to fail here: the plan carries no agent_target, so the
	// launch cannot route. What matters is that it failed in the LAUNCH, not in
	// a dispatch.
	_ = NewOneShotExecutor(client, nil).Execute(context.Background(), job, plan)

	if client.called {
		t.Error("LLM client was invoked for a pi-session chat; responder: pi-session must never dispatch to an API")
	}
}

// TestRunPiSessionChat_RefusesWrongResponder: the entry point is exported, so
// it must not be callable against a chat it does not own.
func TestRunPiSessionChat_RefusesWrongResponder(t *testing.T) {
	job := &Job{ID: "j1", Type: JobTypeChat, Responder: ResponderOracle, Filename: "01-x.md"}
	err := RunPiSessionChat(context.Background(), job, &Plan{Directory: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "pi-session") {
		t.Fatalf("RunPiSessionChat() on an oracle chat = %v, want a responder mismatch error", err)
	}
}

// --- Preflight -------------------------------------------------------------

// piSessionPreflightFixture builds a worktree with two source files and a rules
// file selecting them.
func piSessionPreflightFixture(t *testing.T, rules string) (contextDir, rulesPath string) {
	t.Helper()
	contextDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "b.go"), []byte("package a\n\nfunc B() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rulesPath = filepath.Join(contextDir, "job.rules")
	if err := os.WriteFile(rulesPath, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	return contextDir, rulesPath
}

func TestPreflightPiSessionContext_Resolves(t *testing.T) {
	contextDir, rulesPath := piSessionPreflightFixture(t, "*.go\n")
	job := &Job{ID: "j1", Filename: "01-x.md"}

	result, err := PreflightPiSessionContext(context.Background(), job, contextDir, rulesPath)
	if err != nil {
		t.Fatalf("PreflightPiSessionContext() error = %v", err)
	}
	if result.Files != 2 {
		t.Errorf("Files = %d, want 2", result.Files)
	}
	if result.Bytes == 0 || result.CXTokens == 0 {
		t.Errorf("size accounting is empty: bytes=%d tokens=%d", result.Bytes, result.CXTokens)
	}
}

// TestPreflightPiSessionContext_RefusesEmptyResolution is the gate that stops a
// confidently-empty oracle: a rules file that selects nothing must never reach
// the seed writer.
func TestPreflightPiSessionContext_RefusesEmptyResolution(t *testing.T) {
	contextDir, rulesPath := piSessionPreflightFixture(t, "nothing-matches-this-*.zzz\n")
	job := &Job{ID: "j1", Filename: "01-x.md"}

	_, err := PreflightPiSessionContext(context.Background(), job, contextDir, rulesPath)
	if err == nil {
		t.Fatal("PreflightPiSessionContext() succeeded on an empty resolution, want a refusal")
	}
	if !strings.Contains(err.Error(), "resolved 0 files") {
		t.Errorf("error = %v, want it to name the empty resolution", err)
	}
}

// --- Launch argv -----------------------------------------------------------

// recordingInteractiveProvider captures a launch instead of performing one.
type recordingInteractiveProvider struct {
	workDir  string
	args     []string
	briefing string
	launched bool
}

func (p *recordingInteractiveProvider) Launch(_ context.Context, _ *Job, _ *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	p.workDir = workDir
	p.args = append([]string{}, agentArgs...)
	p.briefing = briefingFilePath
	p.launched = true
	return nil
}

// TestStartPiSessionProcess_ArgvShape pins the launch argv. The ordering is
// load-bearing: --session must follow the operator-configured provider args
// (so none of them can redirect it) and must carry a PATH, because pi resolves
// a path-shaped argument as a literal session file rather than a session id.
func TestStartPiSessionProcess_ArgvShape(t *testing.T) {
	planDir := t.TempDir()
	contextDir := t.TempDir()
	plan := &Plan{
		Name:          "test-plan",
		Directory:     planDir,
		JobsByID:      map[string]*Job{},
		Orchestration: &Config{AgentTarget: "tmux"},
	}
	job := &Job{
		ID:        "pi-chat-1",
		Title:     "pi chat",
		Type:      JobTypeChat,
		Responder: ResponderPiSession,
		Provider:  "pi",
		Model:     "gpt-5.6-sol",
		Filename:  "01-pi-chat.md",
		FilePath:  filepath.Join(planDir, "01-pi-chat.md"),
	}
	if err := os.WriteFile(job.FilePath, []byte("---\nid: pi-chat-1\n---\n\nhi\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(planDir, ".artifacts", job.ID, "sessions", "2026-08-03T10-00-00-000Z_abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatal(err)
	}

	recorder := &recordingInteractiveProvider{}
	restoreLauncher := swapPiSessionLauncher(func(ctx context.Context, _ InteractiveAgentProvider, j *Job, p *Plan, workDir string, args []string, briefing string) error {
		return recorder.Launch(ctx, j, p, workDir, args, briefing)
	})
	defer restoreLauncher()

	desc := PiSessionDescriptor{
		JobID:       job.ID,
		JobTitle:    job.Title,
		PlanName:    plan.Name,
		PlanDir:     planDir,
		ChatFile:    job.FilePath,
		WakeFile:    PiSessionWakePath(planDir, job.ID),
		SessionFile: sessionFile,
		SessionID:   "abc",
		ContextDir:  contextDir,
		Model:       job.Model,
	}
	if err := startPiSessionProcess(context.Background(), job, plan, desc); err != nil {
		t.Fatalf("startPiSessionProcess() error = %v", err)
	}
	if !recorder.launched {
		t.Fatal("the provider was never launched")
	}

	sessionIdx := indexOfArg(recorder.args, "--session")
	if sessionIdx < 0 {
		t.Fatalf("argv %v carries no --session", recorder.args)
	}
	if got := recorder.args[sessionIdx+1]; got != sessionFile {
		t.Errorf("--session value = %q, want the seeded session file %q", got, sessionFile)
	}
	if !strings.Contains(recorder.args[sessionIdx+1], string(os.PathSeparator)) {
		t.Error("--session must carry a path: pi resolves a bare id against its session registry, which a synthesized seed is not in")
	}
	// The per-job model flag precedes --session; the provider appends
	// --session-dir after everything, so it must NOT be here yet.
	if modelIdx := indexOfArg(recorder.args, "--model"); modelIdx < 0 || modelIdx > sessionIdx {
		t.Errorf("argv %v: --model must be present and precede --session", recorder.args)
	}
	if indexOfArg(recorder.args, "--session-dir") >= 0 {
		t.Error("--session-dir must be appended by the provider, not by the pi-session launcher")
	}
	if recorder.workDir != contextDir {
		t.Errorf("launch workDir = %q, want the resolved context dir %q", recorder.workDir, contextDir)
	}
	if recorder.briefing == "" {
		t.Error("no kickoff briefing file was written")
	}

	// The descriptor and the wake sentinel must exist BEFORE the launch, or
	// nothing can talk to the process that just started.
	stored, err := ReadPiSessionDescriptor(planDir, job.ID)
	if err != nil || stored == nil {
		t.Fatalf("ReadPiSessionDescriptor() = (%v, %v), want a persisted descriptor", stored, err)
	}
	if stored.SessionFile != sessionFile || stored.Provider != "pi" {
		t.Errorf("descriptor = {session:%q provider:%q}, want {%q pi}", stored.SessionFile, stored.Provider, sessionFile)
	}
	wake, err := ReadPiSessionWake(planDir, job.ID)
	if err != nil || wake == nil {
		t.Fatalf("ReadPiSessionWake() = (%v, %v), want a launch nudge", wake, err)
	}
	if wake.Reason != WakeReasonLaunch {
		t.Errorf("wake reason = %q, want %q", wake.Reason, WakeReasonLaunch)
	}
}

// TestResolvePiSessionProviderSpec_RefusesNonPi: an inherited
// flow.interactive_provider (claude by default) cannot open a Pi session file,
// so it must fail here with a message that says so rather than at launch.
func TestResolvePiSessionProviderSpec_RefusesNonPi(t *testing.T) {
	_, _, err := resolvePiSessionProviderSpec(&Job{ID: "j", Provider: "claude"})
	if err == nil || !strings.Contains(err.Error(), "Pi-family provider") {
		t.Fatalf("resolvePiSessionProviderSpec(claude) = %v, want a Pi-family refusal", err)
	}

	spec, _, err := resolvePiSessionProviderSpec(&Job{ID: "j"})
	if err != nil {
		t.Fatalf("resolvePiSessionProviderSpec(unset) error = %v", err)
	}
	if spec.Name != DefaultPiSessionProvider {
		t.Errorf("unset provider resolved to %q, want %q (never flow.interactive_provider)", spec.Name, DefaultPiSessionProvider)
	}
}

// --- Idempotent run --------------------------------------------------------

// TestRunPiSessionChat_AliveSessionNudgesInsteadOfRelaunching: re-running a
// live pi-session chat must not start a second process or re-seed.
func TestRunPiSessionChat_AliveSessionNudgesInsteadOfRelaunching(t *testing.T) {
	planDir := t.TempDir()
	job, plan := piSessionLaunchedFixture(t, planDir)

	restoreLiveness := swapPiSessionLiveness(func(string) (bool, int) { return true, 4242 })
	defer restoreLiveness()
	launched := false
	restoreLauncher := swapPiSessionLauncher(func(context.Context, InteractiveAgentProvider, *Job, *Plan, string, []string, string) error {
		launched = true
		return nil
	})
	defer restoreLauncher()

	before, _ := ReadPiSessionWake(planDir, job.ID)
	if err := RunPiSessionChat(context.Background(), job, plan); err != nil {
		t.Fatalf("RunPiSessionChat() error = %v", err)
	}
	if launched {
		t.Error("a second pi process was launched over a live session")
	}
	after, err := ReadPiSessionWake(planDir, job.ID)
	if err != nil || after == nil {
		t.Fatalf("ReadPiSessionWake() = (%v, %v), want a nudge", after, err)
	}
	if before != nil && after.Seq <= before.Seq {
		t.Errorf("wake seq did not advance: %d → %d", before.Seq, after.Seq)
	}
}

// TestRunPiSessionChat_DeadSessionResumesWithoutReseeding: the seed is written
// once. A relaunch must reuse the same session file, or the dialogue is lost.
func TestRunPiSessionChat_DeadSessionResumesWithoutReseeding(t *testing.T) {
	planDir := t.TempDir()
	job, plan := piSessionLaunchedFixture(t, planDir)

	desc, err := ReadPiSessionDescriptor(planDir, job.ID)
	if err != nil || desc == nil {
		t.Fatal("fixture did not persist a descriptor")
	}
	seedBefore, err := os.ReadFile(desc.SessionFile)
	if err != nil {
		t.Fatal(err)
	}

	restoreLiveness := swapPiSessionLiveness(func(string) (bool, int) { return false, 0 })
	defer restoreLiveness()
	var launchArgs []string
	restoreLauncher := swapPiSessionLauncher(func(_ context.Context, _ InteractiveAgentProvider, _ *Job, _ *Plan, _ string, args []string, _ string) error {
		launchArgs = args
		return nil
	})
	defer restoreLauncher()

	if err := RunPiSessionChat(context.Background(), job, plan); err != nil {
		t.Fatalf("RunPiSessionChat() error = %v", err)
	}

	seedAfter, err := os.ReadFile(desc.SessionFile)
	if err != nil {
		t.Fatalf("the session file was removed by a resume: %v", err)
	}
	if string(seedAfter) != string(seedBefore) {
		t.Error("the session file was rewritten by a resume; the seed must be written exactly once")
	}
	idx := indexOfArg(launchArgs, "--session")
	if idx < 0 || launchArgs[idx+1] != desc.SessionFile {
		t.Errorf("resume argv %v did not point --session at the existing session file %q", launchArgs, desc.SessionFile)
	}
}

// TestRunPiSessionChat_MissingSessionFileRefuses: a descriptor pointing at a
// vanished session must fail loudly rather than silently seeding a fresh,
// amnesiac session under the same job.
func TestRunPiSessionChat_MissingSessionFileRefuses(t *testing.T) {
	planDir := t.TempDir()
	job, plan := piSessionLaunchedFixture(t, planDir)
	desc, _ := ReadPiSessionDescriptor(planDir, job.ID)
	if err := os.Remove(desc.SessionFile); err != nil {
		t.Fatal(err)
	}

	err := RunPiSessionChat(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("RunPiSessionChat() = %v, want a refusal naming the missing session file", err)
	}
}

// piSessionLaunchedFixture builds a plan+job that has already been launched:
// a descriptor, a session file, and a chat body.
func piSessionLaunchedFixture(t *testing.T, planDir string) (*Job, *Plan) {
	t.Helper()
	plan := &Plan{
		Name:          "test-plan",
		Directory:     planDir,
		JobsByID:      map[string]*Job{},
		Orchestration: &Config{AgentTarget: "tmux"},
	}
	job := &Job{
		ID:        "pi-chat-1",
		Title:     "pi chat",
		Type:      JobTypeChat,
		Responder: ResponderPiSession,
		Provider:  "pi",
		Status:    JobStatusRunning,
		Filename:  "01-pi-chat.md",
		FilePath:  filepath.Join(planDir, "01-pi-chat.md"),
	}
	plan.Jobs = append(plan.Jobs, job)
	plan.JobsByID[job.ID] = job

	body := "---\nid: pi-chat-1\ntitle: pi chat\nstatus: running\ntype: chat\nresponder: pi-session\n---\n\n<!-- grove: {\"template\": \"chat\"} -->\n\nFirst question.\n"
	if err := os.WriteFile(job.FilePath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	sessionDir := filepath.Join(planDir, ".artifacts", job.ID, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(sessionDir, "2026-08-03T10-00-00-000Z_abc.jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"session","version":3,"id":"abc","timestamp":"t","cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WritePiSessionDescriptor(planDir, PiSessionDescriptor{
		JobID:       job.ID,
		JobTitle:    job.Title,
		PlanName:    plan.Name,
		PlanDir:     planDir,
		ChatFile:    job.FilePath,
		WakeFile:    PiSessionWakePath(planDir, job.ID),
		SessionFile: sessionFile,
		SessionID:   "abc",
		ContextDir:  planDir,
		Provider:    "pi",
	}); err != nil {
		t.Fatal(err)
	}
	return job, plan
}

func swapPiSessionLiveness(fn func(string) (bool, int)) func() {
	prev := piSessionLiveness
	piSessionLiveness = fn
	return func() { piSessionLiveness = prev }
}

func swapPiSessionLauncher(fn func(context.Context, InteractiveAgentProvider, *Job, *Plan, string, []string, string) error) func() {
	prev := piSessionLauncher
	piSessionLauncher = fn
	return func() { piSessionLauncher = prev }
}

func indexOfArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
