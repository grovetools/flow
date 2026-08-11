package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/notespace"
	"github.com/grovetools/flow/pkg/orchestration"
)

// resetDemoteFlags restores the demote command's package-level flag state
// between tests — cobra parses into globals, so a test that sets one would
// otherwise leak into every later test in the package.
func resetDemoteFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		demoteWorkspaceFlag = ""
		demoteReasonFlag = ""
		demoteStatusFlag = ""
		demoteDryRunFlag = false
		demoteJSONFlag = false
	})
}

// writeDemoteFixturePlan builds a plan directory with the given jobs, named
// "NN-<name>.md" with the given statuses.
func writeDemoteFixturePlan(t *testing.T, planDir string, statuses map[string]orchestration.JobStatus) {
	t.Helper()
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	name := filepath.Base(planDir)
	if err := os.WriteFile(filepath.Join(planDir, ".grove-plan.yml"), []byte("name: "+name+"\nworktree: \"\"\n"), 0o644); err != nil {
		t.Fatalf("write plan config: %v", err)
	}
	for filename, status := range statuses {
		id := strings.TrimSuffix(filename, ".md")
		content := fmt.Sprintf("---\nid: %s\ntitle: %s\ntype: chat\nstatus: %s\n---\n\nBody of %s\n", id, id, status, id)
		if err := os.WriteFile(filepath.Join(planDir, filename), []byte(content), 0o644); err != nil {
			t.Fatalf("write job %s: %v", filename, err)
		}
	}
}

// TestParseDemoteStatuses pins what --status selects: the unstarted work by
// default, an explicit list when given, and "all" as everything unfinished —
// never a running or already-finished job.
func TestParseDemoteStatuses(t *testing.T) {
	defaults, err := parseDemoteStatuses("")
	if err != nil {
		t.Fatalf("default statuses: %v", err)
	}
	if !defaults[orchestration.JobStatusPending] || !defaults[orchestration.JobStatusPendingUser] {
		t.Errorf("default selection missing pending statuses: %v", defaults)
	}
	if defaults[orchestration.JobStatusCompleted] || defaults[orchestration.JobStatusRunning] {
		t.Errorf("default selection must not include completed/running: %v", defaults)
	}

	explicit, err := parseDemoteStatuses("failed, blocked")
	if err != nil {
		t.Fatalf("explicit statuses: %v", err)
	}
	if len(explicit) != 2 || !explicit[orchestration.JobStatusFailed] || !explicit[orchestration.JobStatusBlocked] {
		t.Errorf("explicit selection wrong: %v", explicit)
	}

	all, err := parseDemoteStatuses("all")
	if err != nil {
		t.Fatalf("all statuses: %v", err)
	}
	if !all[orchestration.JobStatusFailed] || !all[orchestration.JobStatusPending] {
		t.Errorf("'all' should cover failed and pending: %v", all)
	}
	if all[orchestration.JobStatusRunning] || all[orchestration.JobStatusCompleted] || all[orchestration.JobStatusAbandoned] {
		t.Errorf("'all' must exclude running/completed/abandoned: %v", all)
	}
}

// TestResolveDemoteTargets_PlanDirSelectsByStatus verifies a plan-directory
// argument expands to exactly the jobs whose status matches, and that mixing
// it with an explicit job file de-duplicates rather than demoting twice.
func TestResolveDemoteTargets_PlanDirSelectsByStatus(t *testing.T) {
	resetDemoteFlags(t)
	planDir := filepath.Join(t.TempDir(), "plans", "bulk-plan")
	writeDemoteFixturePlan(t, planDir, map[string]orchestration.JobStatus{
		"01-done.md":    orchestration.JobStatusCompleted,
		"02-pending.md": orchestration.JobStatusPending,
		"03-later.md":   orchestration.JobStatusPendingUser,
		"04-running.md": orchestration.JobStatusRunning,
	})

	targets, err := resolveDemoteTargets([]string{planDir})
	if err != nil {
		t.Fatalf("resolveDemoteTargets: %v", err)
	}
	got := map[string]bool{}
	for _, p := range targets {
		got[filepath.Base(p)] = true
	}
	if len(got) != 2 || !got["02-pending.md"] || !got["03-later.md"] {
		t.Errorf("expected the two pending jobs, got %v", got)
	}

	// The same job named twice — once via the plan, once directly — is one target.
	both, err := resolveDemoteTargets([]string{planDir, filepath.Join(planDir, "02-pending.md")})
	if err != nil {
		t.Fatalf("resolveDemoteTargets (mixed): %v", err)
	}
	if len(both) != 2 {
		t.Errorf("expected overlapping arguments to de-duplicate, got %v", both)
	}

	if _, err := resolveDemoteTargets(nil); err == nil {
		t.Error("expected an empty argument list to be refused")
	}
	if _, err := resolveDemoteTargets([]string{filepath.Join(planDir, "no-such-job.md")}); err == nil {
		t.Error("expected a missing path to be refused")
	}
}

func TestResolveTargetNotespaceNeverFallsBackToPositionalParent(t *testing.T) {
	notebookRoot := t.TempDir()
	correct := filepath.Join(notebookRoot, "notespaces", "correct")
	wrong := filepath.Join(t.TempDir(), "wrong")
	if err := os.MkdirAll(correct, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := notespace.InstallNotespace(correct, notespace.NotespaceStamp{
		ID: "01J00000000000000000000001", Name: "correct", Subject: "example.com/org/repo", Kind: "repo",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Notebooks: &config.NotebooksConfig{Definitions: map[string]*config.Notebook{
		"book": {RootDir: notebookRoot},
	}}}

	jobPath := filepath.Join(correct, "plans", "plan", "01-job.md")
	got, err := resolveTargetNotespaceWithConfig(jobPath, cfg)
	if err != nil || got != correct {
		t.Fatalf("target = %q, err = %v, want %q", got, err, correct)
	}

	// The removed implementation climbed from any path containing a plans/
	// component (and finally guessed two parents). That could direct nb new to
	// an unrelated writable directory. Such a path now fails closed.
	positionalTrap := filepath.Join(wrong, "plans", "plan", "01-job.md")
	if got, err := resolveTargetNotespaceWithConfig(positionalTrap, cfg); err == nil {
		t.Fatalf("positional trap resolved to %q; want fail-closed error", got)
	}
}

func TestWorkspaceOverrideRoutesByRecordedNotespaceID(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "same-name")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	firstBook, secondBook := t.TempDir(), t.TempDir()
	firstRoot := filepath.Join(firstBook, "notespaces", "same-name")
	secondRoot := filepath.Join(secondBook, "notespaces", "same-name")
	for i, fixture := range []struct {
		root, id, subject string
	}{
		{firstRoot, "01J00000000000000000000001", "example.com/other/repo"},
		{secondRoot, "01J00000000000000000000002", "example.com/target/repo"},
	} {
		if err := os.MkdirAll(fixture.root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := notespace.InstallNotespace(fixture.root, notespace.NotespaceStamp{
			ID: fixture.id, Name: "same-name", Subject: fixture.subject, Kind: "repo",
		}); err != nil {
			t.Fatalf("install stamp %d: %v", i, err)
		}
	}
	cfg := &config.Config{Notebooks: &config.NotebooksConfig{Definitions: map[string]*config.Notebook{
		"first": {RootDir: firstBook}, "second": {RootDir: secondBook},
	}}}
	machine := &config.MachineConfig{
		Subjects:  map[string]string{repo: "example.com/target/repo"},
		Primaries: map[string]string{"example.com/target/repo": "01J00000000000000000000002"},
	}
	got, err := resolveWorkspaceOverrideWithRouting(repo, cfg, machine)
	if err != nil || got != secondRoot {
		t.Fatalf("override route = %q, err = %v, want ID-selected root %q", got, err, secondRoot)
	}
}

// TestPlanDemote_BulkParksPendingJobs is the "save this plan's pending jobs
// for later" pass end to end: one plan-directory invocation demotes every
// pending job,
// pays the cross-workspace nb note query ONCE for the batch, records the
// reason on each note, and abandons each job — while leaving completed and
// running work untouched.
func TestPlanDemote_BulkParksPendingJobs(t *testing.T) {
	recordPath, setList := installNbStub(t)
	resetDemoteFlags(t)

	tempDir := t.TempDir()
	planDir := filepath.Join(tempDir, "workspaces", "ws", "plans", "bulk-plan")
	writeDemoteFixturePlan(t, planDir, map[string]orchestration.JobStatus{
		"01-done.md":    orchestration.JobStatusCompleted,
		"02-pending.md": orchestration.JobStatusPending,
		"03-later.md":   orchestration.JobStatusPending,
	})

	setList(`[
	  {"path":"/nb/ws/in_progress/pending.md","plan_ref":"plans/bulk-plan","plan_job":"02-pending.md"},
	  {"path":"/nb/ws/in_progress/later.md","plan_ref":"plans/bulk-plan","plan_job":"03-later.md"}
	]`)

	demoteReasonFlag = "parking until Q3"

	if err := runPlanDemote(&cobra.Command{}, []string{planDir}); err != nil {
		t.Fatalf("runPlanDemote: %v", err)
	}

	lines := stubRecordLines(t, recordPath)
	record := strings.Join(lines, "\n")

	// The note query is per-PLAN, not per-job: two jobs, one `nb list`.
	if got := countLines(lines, "list --plan-ref plans/bulk-plan"); got != 1 {
		t.Errorf("expected exactly one plan-notes query for the batch, got %d:\n%s", got, record)
	}

	for _, note := range []string{"pending", "later"} {
		if got := countLines(lines, "move /nb/ws/in_progress/"+note+".md inbox --force"); got != 1 {
			t.Errorf("expected inbox move for %s, record:\n%s", note, record)
		}
		if got := countLines(lines, "--path /nb/ws/inbox/"+note+".md --set plan_ref= --set plan_job= --set demoted_from=plans/bulk-plan"); got != 1 {
			t.Errorf("expected provenance stamp for %s, record:\n%s", note, record)
		}
		if got := countLines(lines, "--set demote_reason=parking until Q3"); got < 1 {
			t.Errorf("expected the reason to be recorded, record:\n%s", record)
		}
	}

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	for _, job := range plan.Jobs {
		switch job.Filename {
		case "01-done.md":
			if job.Status != orchestration.JobStatusCompleted {
				t.Errorf("completed job must be untouched, got %s", job.Status)
			}
		default:
			if job.Status != orchestration.JobStatusAbandoned {
				t.Errorf("job %s: expected abandoned, got %s", job.Filename, job.Status)
			}
		}
	}
}

// TestPlanDemote_DryRunTouchesNothing verifies --dry-run reports the plan it
// would park without moving a note or changing a job status, and that --json
// describes each target.
func TestPlanDemote_DryRunTouchesNothing(t *testing.T) {
	recordPath, setList := installNbStub(t)
	resetDemoteFlags(t)

	tempDir := t.TempDir()
	planDir := filepath.Join(tempDir, "workspaces", "ws", "plans", "dry-plan")
	writeDemoteFixturePlan(t, planDir, map[string]orchestration.JobStatus{
		"01-pending.md": orchestration.JobStatusPending,
	})
	setList(`[{"path":"/nb/ws/in_progress/pending.md","plan_ref":"plans/dry-plan","plan_job":"01-pending.md"}]`)

	demoteDryRunFlag = true
	demoteJSONFlag = true

	stdout := captureStdout(t, func() {
		if err := runPlanDemote(&cobra.Command{}, []string{planDir}); err != nil {
			t.Fatalf("runPlanDemote: %v", err)
		}
	})

	var outcomes []demoteOutcome
	if err := json.Unmarshal([]byte(stdout), &outcomes); err != nil {
		t.Fatalf("parsing --json output %q: %v", stdout, err)
	}
	if len(outcomes) != 1 || outcomes[0].JobFile != "01-pending.md" || !outcomes[0].DryRun {
		t.Fatalf("unexpected dry-run outcomes: %+v", outcomes)
	}
	if outcomes[0].Note != "/nb/ws/in_progress/pending.md" {
		t.Errorf("dry run should name the note it would move, got %q", outcomes[0].Note)
	}

	lines := stubRecordLines(t, recordPath)
	if got := countLines(lines, "move "); got != 0 {
		t.Errorf("dry run must not move notes, record:\n%s", strings.Join(lines, "\n"))
	}

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if plan.Jobs[0].Status != orchestration.JobStatusPending {
		t.Errorf("dry run must not change job status, got %s", plan.Jobs[0].Status)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
