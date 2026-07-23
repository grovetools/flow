package view

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

func TestInitFailureRebuildsPrefilledWizardAndReportsExecutionContext(t *testing.T) {
	plansDir := t.TempDir()
	partial := filepath.Join(plansDir, "kept-name")
	if err := os.Mkdir(partial, 0o755); err != nil {
		t.Fatal(err)
	}

	m := New(Config{PlansDir: plansDir})
	req := &planinit.Request{Dir: "kept-name", Worktree: "kept-worktree", Recipe: "standard-feature", RunInit: true}
	m.mode = modeInitWizard
	m.pendingInitPlanName = req.Dir
	m.lastInitRequest = req

	report := InitExecutionReport{
		ExitCause:        "exited with status 1",
		Executable:       "/opt/grove/bin/flow",
		Command:          `["/opt/grove/bin/flow","plan","init","kept-name"]`,
		WorkingDirectory: "/workspace",
		PlansDir:         plansDir,
		Stderr:           "specific stderr\n",
		JournalPath:      filepath.Join(plansDir, ".init-kept-name.journal.json"),
		Residue:          []string{partial},
		Err:              errors.New("exit status 1"),
	}
	updated, cmd := m.Update(initCompletedMsg{report: report})
	got := updated.(Model)
	if cmd == nil {
		t.Fatal("failure should rebuild the wizard and refresh the portfolio")
	}
	if got.mode != modeInitWizard || !got.initWizardBuilding {
		t.Fatalf("failure left unusable wizard state: mode=%v building=%v", got.mode, got.initWizardBuilding)
	}
	if got.lastInitRequest == nil || got.lastInitRequest.Dir != req.Dir || got.lastInitRequest.Worktree != req.Worktree {
		t.Fatalf("request identity was not preserved: %#v", got.lastInitRequest)
	}
	for _, want := range []string{"specific stderr", "exited with status 1", "/opt/grove/bin/flow", "/workspace", report.JournalPath, partial} {
		if !strings.Contains(got.initFailure, want) {
			t.Errorf("durable failure %q does not contain %q", got.initFailure, want)
		}
	}
}

func TestInitSuccessLoadFailureReturnsToPortfolio(t *testing.T) {
	m := New(Config{PlansDir: t.TempDir()})
	m.mode = modeInitWizard
	m.pendingInitPlanName = "missing-after-success"
	m.lastInitRequest = &planinit.Request{Dir: "missing-after-success"}

	updated, _ := m.Update(initCompletedMsg{report: InitExecutionReport{}})
	got := updated.(Model)
	if got.mode != modeBrowser {
		t.Fatalf("success/load failure mode=%v, want browser", got.mode)
	}
	if got.lastInitRequest != nil {
		t.Fatal("successful init should clear the retry request")
	}
}

func TestExecuteInitSubprocessSeparatesNoisyOutputAndExitCause(t *testing.T) {
	plansDir := t.TempDir()
	report := executeInitSubprocess(&planinit.Request{Dir: "noisy", RunInit: true}, plansDir, t.TempDir(), testInitDeps(
		func(string, ...string) *exec.Cmd {
			return exec.Command("sh", "-c", `printf 'stdout-line\n'; printf 'stderr-line\n' >&2; exit 9`)
		},
		"attempt-noisy",
	))

	if report.Err == nil || report.ExitCode == nil || *report.ExitCode != 9 {
		t.Fatalf("exit evidence = err:%v code:%v", report.Err, report.ExitCode)
	}
	if report.Stdout != "stdout-line\n" || report.Stderr != "stderr-line\n" {
		t.Fatalf("output was not separated: stdout=%q stderr=%q", report.Stdout, report.Stderr)
	}
	for _, want := range []string{"exited with status 9", "stderr-line", "stdout-line", "Journal: " + report.JournalPath} {
		if got := formatInitFailure(report); !strings.Contains(got, want) {
			t.Errorf("failure report %q missing %q", got, want)
		}
	}
}

func TestExecuteInitSubprocessSilentFailureNamesDelegatedExecutable(t *testing.T) {
	report := executeInitSubprocess(&planinit.Request{Dir: "silent", RunInit: true}, t.TempDir(), t.TempDir(), testInitDeps(
		func(string, ...string) *exec.Cmd { return exec.Command("sh", "-c", "exit 7") },
		"attempt-silent",
	))
	got := formatInitFailure(report)
	for _, want := range []string{"exited with status 7", "no stdout or stderr was captured", report.Executable, report.Command, report.WorkingDirectory} {
		if !strings.Contains(got, want) {
			t.Errorf("silent failure %q missing %q", got, want)
		}
	}
}

func TestExecuteInitSubprocessReportsJournalWriteFailure(t *testing.T) {
	deps := testInitDeps(func(string, ...string) *exec.Cmd { return exec.Command("sh", "-c", "exit 1") }, "attempt-journal-error")
	deps.writeJournal = func(string, initJournal) error { return errors.New("rename denied") }
	report := executeInitSubprocess(&planinit.Request{Dir: "journal-error", RunInit: true}, t.TempDir(), t.TempDir(), deps)
	if len(report.JournalWriteErrors) < 2 {
		t.Fatalf("journal errors not independently retained: %#v", report.JournalWriteErrors)
	}
	if got := formatInitFailure(report); !strings.Contains(got, "Journal error:") || !strings.Contains(got, "rename denied") {
		t.Fatalf("journal failure not visible: %q", got)
	}
}

func TestExecuteInitSubprocessDiscoversSiblingResidueWithoutPlanDir(t *testing.T) {
	plansDir := t.TempDir()
	extra := filepath.Join(plansDir, ".init-residue-orphan.journal.json")
	deps := testInitDeps(func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf orphan > "+shellQuote(extra)+"; exit 2")
	}, "attempt-residue")
	report := executeInitSubprocess(&planinit.Request{Dir: "residue", RunInit: true}, plansDir, t.TempDir(), deps)
	if _, err := os.Stat(filepath.Join(plansDir, "residue")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test unexpectedly created plan dir: %v", err)
	}
	for _, want := range []string{extra, initJournalPath(plansDir, "residue")} {
		if !containsString(report.Residue, want) {
			t.Errorf("residue %#v missing %q", report.Residue, want)
		}
	}
}

func TestExecuteInitSubprocessRetryAppendsDistinctAttempts(t *testing.T) {
	plansDir := t.TempDir()
	ids := []string{"attempt-one", "attempt-two"}
	idIndex := 0
	deps := testInitDeps(func(string, ...string) *exec.Cmd { return exec.Command("sh", "-c", "printf retry >&2; exit 1") }, "")
	deps.attemptID = func() string {
		id := ids[idIndex]
		idIndex++
		return id
	}
	for range 2 {
		report := executeInitSubprocess(&planinit.Request{Dir: "retry", RunInit: true}, plansDir, t.TempDir(), deps)
		if report.Err == nil {
			t.Fatal("retry fixture unexpectedly succeeded")
		}
	}
	journal, err := readInitJournal(initJournalPath(plansDir, "retry"), "retry")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Attempts) != 2 || journal.Attempts[0].AttemptID != ids[0] || journal.Attempts[1].AttemptID != ids[1] {
		t.Fatalf("retry evidence was overwritten: %#v", journal.Attempts)
	}
}

func TestExecuteInitSubprocessSuccessFinalizesAfterTransientSiblingWriteFailure(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "transient-write")
	deps := testInitDeps(func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "mkdir -p "+shellQuote(planDir))
	}, "attempt-transient-write")
	writes := 0
	deps.writeJournal = func(path string, journal initJournal) error {
		writes++
		if writes == 2 {
			return errors.New("transient rename failure")
		}
		return atomicWriteInitJournal(path, journal)
	}
	report := executeInitSubprocess(&planinit.Request{Dir: "transient-write", RunInit: true}, plansDir, t.TempDir(), deps)
	if report.Err != nil || report.JournalPath != filepath.Join(planDir, ".init-journal.json") {
		t.Fatalf("successful attempt did not finalize: report=%#v", report)
	}
	if len(report.JournalWriteErrors) == 0 || !strings.Contains(report.JournalWriteErrors[0], "transient rename failure") {
		t.Fatalf("transient write error was lost: %#v", report.JournalWriteErrors)
	}
	if _, err := os.Stat(initJournalPath(plansDir, "transient-write")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sibling journal remains after finalization: %v", err)
	}
}

func TestExecuteInitSubprocessSuccessFinalizesJournalAndRemovesSibling(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "success")
	deps := testInitDeps(func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "mkdir -p "+shellQuote(planDir)+"; printf created")
	}, "attempt-success")
	report := executeInitSubprocess(&planinit.Request{Dir: "success", RunInit: true}, plansDir, t.TempDir(), deps)
	if report.Err != nil || report.ExitCode == nil || *report.ExitCode != 0 {
		t.Fatalf("success report = %#v, err=%v", report, report.Err)
	}
	finalPath := filepath.Join(planDir, ".init-journal.json")
	if report.JournalPath != finalPath {
		t.Fatalf("journal path=%q, want %q", report.JournalPath, finalPath)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final journal missing: %v", err)
	}
	if _, err := os.Stat(initJournalPath(plansDir, "success")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sibling journal remains after success: %v", err)
	}
	journal, err := readInitJournal(finalPath, "success")
	if err != nil || len(journal.Attempts) != 1 || journal.Attempts[0].Phase != "completed" {
		t.Fatalf("final journal is incomplete: journal=%#v err=%v", journal, err)
	}
}

func testInitDeps(command func(string, ...string) *exec.Cmd, attemptID string) initExecutionDeps {
	n := 0
	return initExecutionDeps{
		command:      command,
		writeJournal: atomicWriteInitJournal,
		now: func() time.Time {
			n++
			return time.Unix(int64(n), 0).UTC()
		},
		attemptID: func() string { return attemptID },
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
