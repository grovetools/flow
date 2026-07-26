package status

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/core/tui/embed"
)

// runsRefreshTick reports whether cmd is the plan-refresh tick.
func runsRefreshTick(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		_, ok := msg.(RefreshTickMsg)
		return ok
	case <-time.After(refreshInterval + time.Second):
		t.Fatal("refresh tick never fired")
		return false
	}
}

func TestRefreshTickMsgStampsLiveness(t *testing.T) {
	m := Model{lastRefreshTickAt: time.Now().Add(-time.Hour)}
	updated, _ := m.Update(RefreshTickMsg(time.Now()))
	if time.Since(updated.(Model).lastRefreshTickAt) > time.Second {
		t.Error("handling a refresh tick did not stamp the loop as alive")
	}
}

// A host that starves the model kills its self-rearming refresh loop for good.
// Regaining focus is the one reliable moment to notice and revive it.
func TestFocusRevivesAStalledRefreshLoop(t *testing.T) {
	m := Model{lastRefreshTickAt: time.Now().Add(-refreshStallThreshold - time.Second)}

	updated, cmd := m.Update(embed.FocusMsg{})
	if !runsRefreshTick(t, cmd) {
		t.Fatal("focus did not re-arm the refresh loop after a stall")
	}

	// The revival is stamped, so a second focus right behind it cannot stack
	// a duplicate tick loop.
	if _, cmd := updated.(Model).Update(embed.FocusMsg{}); cmd != nil {
		t.Error("a focus immediately after a revival re-armed a duplicate loop")
	}
}

func TestFocusLeavesALiveRefreshLoopAlone(t *testing.T) {
	m := Model{lastRefreshTickAt: time.Now()}
	if _, cmd := m.Update(embed.FocusMsg{}); cmd != nil {
		t.Error("focus re-armed the refresh loop while it was still ticking")
	}
}
