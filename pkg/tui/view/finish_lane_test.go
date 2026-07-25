package view

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/embed"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// newFinishHostModel boots the meta-panel into the finish wizard over a
// one-job plan, with the given items in the checklist.
func newFinishHostModel(t *testing.T, items []*finish.Item) Model {
	t.Helper()
	m := newStatusHostModel(t)
	fm := finish.New(finish.Config{PlanName: "t", Items: items, ShowForceToggle: true})
	m.s.finishWizardModel = &fm
	m.mode = modeFinishWizard
	m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabFinishPlan})
	return m
}

// stubFinishNotes replaces the note lifecycle for the duration of a test so it
// does not shell out to `nb`.
func stubFinishNotes(t *testing.T, err error) {
	t.Helper()
	prev := finishPlanNotes
	finishPlanNotes = func(string) ([]orchestration.NoteOutcome, error) { return nil, err }
	t.Cleanup(func() { finishPlanNotes = prev })
}

// TestNoteFailureDoesNotSkipTerminalItems pins that a failure to move the
// plan's linked notes — which has nothing to do with git teardown — cannot
// stop the plan being marked finished and archived. It used to be pushed into
// the same error slice the terminal-item gate reads, so a note-move failure
// silently left every plan un-archived. The CLI never had this problem: it
// reports note outcomes separately.
func TestNoteFailureDoesNotSkipTerminalItems(t *testing.T) {
	stubFinishNotes(t, errors.New("nb unavailable"))

	archived := false
	marked := false
	items := []*finish.Item{
		{
			ID: "mark_finished", Name: "Mark plan finished", IsAvailable: true, IsEnabled: true,
			Action: func() error { marked = true; return nil },
		},
		{
			ID: "archive_plan", Name: "Archive plan directory", IsAvailable: true, IsEnabled: true,
			Action: func() error { archived = true; return nil },
		},
	}
	msg := runEmbeddedFinishActions(finishRunRequest{
		items:    items,
		plan:     &orchestration.Plan{Name: "p"},
		stateDir: t.TempDir(),
	})()
	completed := msg.(finishActionsCompletedMsg)

	if !marked || !archived {
		t.Errorf("a note-move failure must not gate the terminal items: marked=%v archived=%v", marked, archived)
	}
	if len(completed.errs) != 0 {
		t.Errorf("note failures belong in noteErr, not errs: %v", completed.errs)
	}
	if completed.noteErr == nil {
		t.Error("the note failure must still be reported")
	}
}

// TestRetainedWorktreeDoesNotSkipTerminalItems pins the P2 policy decision at
// the host: a repo whose worktree was kept because it still holds uncommitted
// work is a partial success. The plan is still marked finished and archived —
// the alternative left it listed forever, indistinguishable from "Finish Plan
// is broken".
func TestRetainedWorktreeDoesNotSkipTerminalItems(t *testing.T) {
	stubFinishNotes(t, nil)

	archived := false
	items := []*finish.Item{
		{
			ID: "prune_worktree", Name: "Prune git worktree", IsAvailable: true, IsEnabled: true,
			Action: func() error {
				return &plan_finish.RetainedWorktreeError{Details: []string{"core: contains modified or untracked files"}}
			},
		},
		{
			ID: "archive_plan", Name: "Archive plan directory", IsAvailable: true, IsEnabled: true,
			Action: func() error { archived = true; return nil },
		},
	}
	msg := runEmbeddedFinishActions(finishRunRequest{
		items:    items,
		plan:     &orchestration.Plan{Name: "p"},
		stateDir: t.TempDir(),
	})()
	completed := msg.(finishActionsCompletedMsg)

	if !archived {
		t.Error("a retained worktree must not skip archive_plan")
	}
	if len(completed.errs) != 1 {
		t.Fatalf("the retention must still be reported, got %v", completed.errs)
	}
	if !strings.Contains(completed.errs[0].err.Error(), "modified or untracked") {
		t.Errorf("git's own reason must survive to the host: %v", completed.errs[0].err)
	}
}

// TestGenuineFailureStillSkipsTerminalItems is the fence for the above: a real
// failure must still keep the plan resolvable by slug for a retry.
func TestGenuineFailureStillSkipsTerminalItems(t *testing.T) {
	stubFinishNotes(t, nil)

	archived := false
	items := []*finish.Item{
		{
			ID: "prune_worktree", Name: "Prune git worktree", IsAvailable: true, IsEnabled: true,
			Action: func() error { return errors.New("disk on fire") },
		},
		{
			ID: "archive_plan", Name: "Archive plan directory", IsAvailable: true, IsEnabled: true,
			Action: func() error { archived = true; return nil },
		},
	}
	msg := runEmbeddedFinishActions(finishRunRequest{
		items:    items,
		plan:     &orchestration.Plan{Name: "p"},
		stateDir: t.TempDir(),
	})()
	if archived {
		t.Error("a genuine failure must still skip archive_plan")
	}
	if len(msg.(finishActionsCompletedMsg).errs) != 1 {
		t.Errorf("expected the failure to be reported")
	}
}

// TestFinishFailureIsDurable pins that the failure account survives the next
// keypress. The one-shot transient line was cleared by the very next key, so
// the only report the user ever got vanished before it could be read.
func TestFinishFailureIsDurable(t *testing.T) {
	m := newFinishHostModel(t, nil)
	updated, _ := m.Update(finishActionsCompletedMsg{errs: []finishActionError{
		{itemTitle: "Prune git worktree", err: errors.New("core: contains modified or untracked files")},
	}})
	m = updated.(Model)
	if !strings.Contains(m.finishFailure, "modified or untracked") {
		t.Fatalf("failure detail missing: %q", m.finishFailure)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(Model)
	if m.finishTransient != "" {
		t.Error("the transient line should still be one-shot")
	}
	if !strings.Contains(m.finishFailure, "modified or untracked") {
		t.Fatalf("failure account must survive a keypress, got %q", m.finishFailure)
	}
	if !strings.Contains(m.View(), "modified or untracked") {
		t.Error("the durable failure must be rendered")
	}
}

// TestFinishWizardForceReachesTheFactory pins the force plumbing end to end at
// the host: the toggle rides the wizard model, and submitting flips the
// late-bound switch the already-built item closures read.
func TestFinishWizardForceReachesTheFactory(t *testing.T) {
	items := []*finish.Item{{ID: "prune_worktree", Name: "Prune git worktree", IsAvailable: true}}
	m := newFinishHostModel(t, items)
	m.s.finishForce = &plan_finish.ForceSwitch{}

	// Flip the toggle the way the user does.
	updated, _ := m.s.finishWizardModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	fm := updated.(finish.Model)
	*m.s.finishWizardModel = fm
	if !fm.Force() {
		t.Fatal("precondition: wizard force toggle did not flip")
	}

	mdl, _ := m.Update(embed.DoneMsg{Result: items})
	m = mdl.(Model)
	if !m.s.finishForce.Enabled() {
		t.Fatal("the wizard's Force toggle did not reach the factory's force switch")
	}
}

// TestUnsetActivePlanOutsideEcosystemIsNotAnError pins P6. The finish run
// deletes the active-plan key from a directory that, by the time it runs, may
// no longer belong to any grove ecosystem (its worktree was just removed).
// core/state answers ErrNoEcosystemRoot, which is not a failure: there is
// nothing left to unset. Escalating it into the finish result made EVERY
// successful finish report an error, which in turn parked the TUI in status
// mode over an archived plan.
func TestUnsetActivePlanOutsideEcosystemIsNotAnError(t *testing.T) {
	stubFinishNotes(t, nil)
	// A temp dir with no grove marker anywhere above it.
	stateDir := t.TempDir()

	cmd := runEmbeddedFinishActions(finishRunRequest{
		plan:       &orchestration.Plan{Name: "p"},
		stateDir:   stateDir,
		activePlan: "p",
	})
	msg := cmd()
	completed, ok := msg.(finishActionsCompletedMsg)
	if !ok {
		t.Fatalf("unexpected msg type %T", msg)
	}
	if len(completed.errs) != 0 {
		t.Fatalf("a missing ecosystem root must not be reported as a finish failure, got: %v", completed.errs)
	}
}

// TestP6ChainDoesNotParkTheTUIOnAnArchivedPlan walks the whole chain the
// stale-plan-status defect ran through: unset-active-plan reports
// ErrNoEcosystemRoot → errs non-empty → the completion handler takes its
// failure branch → the TUI stays in status mode over a plan whose directory
// has just been archived, whose 2 s refresh then renders
// "Error: plan directory not found". Breaking the first link breaks all of it.
func TestP6ChainDoesNotParkTheTUIOnAnArchivedPlan(t *testing.T) {
	stubFinishNotes(t, nil)
	m := newFinishHostModel(t, nil)

	msg := runEmbeddedFinishActions(finishRunRequest{
		plan:       &orchestration.Plan{Name: "p"},
		stateDir:   t.TempDir(), // no ecosystem root above it
		activePlan: "p",
	})()

	updated, _ := m.Update(msg)
	m = updated.(Model)
	if m.mode != modeBrowser {
		t.Fatalf("mode after a successful finish = %v, want modeBrowser (parked on the archived plan)", m.mode)
	}
	if m.s.statusModel != nil {
		t.Error("the archived plan's status model must be torn down, not left ticking")
	}
}

// TestSuccessfulFinishRoutesToPlanBrowser pins the consequence: with no real
// errors the completion handler must tear down the status model and go back to
// the plan list, not park on the finished plan's status page.
func TestSuccessfulFinishRoutesToPlanBrowser(t *testing.T) {
	m := newFinishHostModel(t, nil)
	updated, _ := m.Update(finishActionsCompletedMsg{})
	m = updated.(Model)
	if m.mode != modeBrowser {
		t.Errorf("mode after a clean finish = %v, want modeBrowser", m.mode)
	}
	if m.s.statusModel != nil {
		t.Error("status model for the finished plan must be torn down")
	}
}

// TestFinishActionsDoNotMutateProcessStdout pins the flow half of a confirmed
// cross-repo rendering defect: the run used to swap the process-global
// os.Stdout/os.Stderr to /dev/null on a tea.Cmd goroutine. A renderer that
// re-resolves its output fd from that global per frame then composes frames
// into /dev/null while marking the front buffer painted — permanent glass
// corruption — and the write itself races the render loop under -race.
func TestFinishActionsDoNotMutateProcessStdout(t *testing.T) {
	want := os.Stdout
	var seen *os.File
	items := []*finish.Item{
		{
			ID: "probe", Name: "probe", IsAvailable: true, IsEnabled: true,
			Action: func() error {
				seen = os.Stdout
				return nil
			},
		},
	}
	cmd := runEmbeddedFinishActions(finishRunRequest{items: items, stateDir: t.TempDir()})
	_ = cmd()
	if seen != want {
		t.Fatalf("os.Stdout was swapped out from under the render loop during the finish run (got %v, want %v)", seen, want)
	}
}

// TestEscLeavesFinishWizard pins the missing binding: esc is a total no-op in
// the finish wizard because the only host esc handler is gated on status mode.
// (q does work and is advertised, so this is a papercut, not a trap.)
func TestEscLeavesFinishWizard(t *testing.T) {
	m := newFinishHostModel(t, nil)
	if m.mode != modeFinishWizard {
		t.Fatalf("precondition: expected modeFinishWizard, got %v", m.mode)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.mode != modeStatus {
		t.Errorf("esc in the finish wizard should return to the plan status view: mode=%v", m.mode)
	}
	if m.s.finishWizardModel != nil {
		t.Error("finish wizard model should be torn down by esc")
	}
}

// TestFinishPageReportsExecutionProgress pins A4′ — the render freeze the user
// actually saw. During execution the wizard model is nil but the pager keeps
// the Finish page active, so its Ready() gate rendered a static, centred
// "Loading wizard…" for the entire (110 s observed) cleanup, with nothing
// repainting until completion.
func TestFinishPageReportsExecutionProgress(t *testing.T) {
	items := []*finish.Item{
		{ID: "noop", Name: "noop", IsAvailable: true, IsEnabled: false},
	}
	m := newFinishHostModel(t, items)
	updated, _ := m.Update(embed.DoneMsg{Result: items})
	m = updated.(Model)

	page := &finishPlanPage{s: m.s}
	ready, loading := page.Ready()
	if !ready && strings.Contains(loading, "Loading wizard") {
		t.Fatalf("finish page still renders the wizard loading placeholder during execution: ready=%v msg=%q", ready, loading)
	}
	if body := page.View(); !strings.Contains(body, "Finishing plan") {
		t.Fatalf("finish page body during execution = %q, want a progress surface", body)
	}
}
