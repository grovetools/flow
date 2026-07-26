package view

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/core/tui/embed"

	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/status"
)

// collectMsgs runs cmd, expanding any tea.BatchMsg fan-out, and returns every
// message produced within budget. Commands still pending when the budget
// lapses (tea.Tick timers, for one) are abandoned rather than waited on.
func collectMsgs(cmd tea.Cmd, budget time.Duration) []tea.Msg {
	if cmd == nil {
		return nil
	}
	out := make(chan tea.Msg, 64)
	var wg sync.WaitGroup
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		defer wg.Done()
		if c == nil {
			return
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, inner := range batch {
				wg.Add(1)
				go run(inner)
			}
			return
		}
		if msg == nil {
			return
		}
		select {
		case out <- msg:
		default:
		}
	}
	wg.Add(1)
	go run(cmd)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
	}

	var msgs []tea.Msg
	for {
		select {
		case m := <-out:
			msgs = append(msgs, m)
		default:
			return msgs
		}
	}
}

func hydratedStatusView(t *testing.T) Model {
	t.Helper()
	planDir := writeStatusLoadingPlan(t)
	m := New(Config{PlansDir: filepath.Dir(planDir)})
	updated, loadCmd := m.Update(browser.BrowserPlanSelectedMsg{
		PlanName: "selected-plan", PlanPath: planDir,
	})
	m = updated.(Model)
	if loadCmd == nil {
		t.Fatal("plan selection did not start an async status load")
	}
	updated, _ = m.Update(loadCmd())
	m = updated.(Model)
	if m.s.statusModel == nil {
		t.Fatal("status model was not hydrated")
	}
	return m
}

// The status model owns self-rearming background loops: the 2s plan-refresh
// tick, the daemon SSE listener, and the MsgCh stream listener. Each re-arms
// itself only by handling the message it just produced, so a single dropped
// message kills it for the lifetime of the model. Detouring onto another tab
// (add-job wizard, plans browser, finish wizard) must therefore not starve it,
// or the job table silently stops updating once the user returns to Jobs.
func TestStatusRefreshTickSurvivesTabDetour(t *testing.T) {
	m := hydratedStatusView(t)

	updated, _ := m.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
	m = updated.(Model)
	if m.pager.ActiveIndex() != tabPlans {
		t.Fatalf("pager did not switch to the plans tab, got index %d", m.pager.ActiveIndex())
	}

	_, tickCmd := m.Update(status.RefreshTickMsg(time.Now()))
	msgs := collectMsgs(tickCmd, 3*time.Second)

	var sawRefresh, sawRearm bool
	for _, msg := range msgs {
		switch msg.(type) {
		case status.RefreshMsg:
			sawRefresh = true
		case status.RefreshTickMsg:
			sawRearm = true
		}
	}
	if !sawRefresh {
		t.Error("refresh tick on an inactive Jobs tab produced no plan refresh — the status table is frozen")
	}
	if !sawRearm {
		t.Error("refresh tick on an inactive Jobs tab was not re-armed — the refresh loop is dead for good")
	}
}

// The fan-out must not double-deliver: while the Jobs tab IS active the pager
// already routes to the status page, so exactly one refresh may come back.
func TestStatusRefreshTickIsNotDoubleDeliveredOnJobsTab(t *testing.T) {
	m := hydratedStatusView(t)
	if m.pager.ActiveIndex() != tabJobs {
		t.Fatalf("expected to start on the jobs tab, got index %d", m.pager.ActiveIndex())
	}

	_, tickCmd := m.Update(status.RefreshTickMsg(time.Now()))
	msgs := collectMsgs(tickCmd, 3*time.Second)

	refreshes := 0
	for _, msg := range msgs {
		if _, ok := msg.(status.RefreshMsg); ok {
			refreshes++
		}
	}
	if refreshes != 1 {
		t.Errorf("got %d plan refreshes on the active Jobs tab, want exactly 1", refreshes)
	}
}
