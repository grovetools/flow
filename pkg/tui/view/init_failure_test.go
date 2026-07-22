package view

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

func TestInitFailureRebuildsPrefilledWizardAndReportsStderrAndResidue(t *testing.T) {
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

	updated, cmd := m.Update(initCompletedMsg{output: "specific stderr", err: errors.New("exit status 1")})
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
	for _, want := range []string{"specific stderr", "exit status 1", partial} {
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

	updated, _ := m.Update(initCompletedMsg{})
	got := updated.(Model)
	if got.mode != modeBrowser {
		t.Fatalf("success/load failure mode=%v, want browser", got.mode)
	}
	if got.lastInitRequest != nil {
		t.Fatal("successful init should clear the retry request")
	}
}
