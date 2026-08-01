package view

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/compositor"

	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/status"
)

// TestRenderNeutralSkipsIdleRefreshTicks pins the saving: the 2s plan-refresh
// poll must not dirty the compositor, or every tick costs a full render of the
// job table (2.12% of a core on an 85-job plan, job 84).
func TestRenderNeutralSkipsIdleRefreshTicks(t *testing.T) {
	neutral := RenderNeutral()
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	// The first tick is the clock frame; every tick inside the interval after
	// it is neutral.
	if neutral(status.RefreshTickMsg(start)) {
		t.Fatal("the first refresh tick was neutral; nothing had rendered the clock yet")
	}
	for i := 1; i < 30; i++ {
		at := start.Add(time.Duration(i) * 2 * time.Second)
		if !neutral(status.RefreshTickMsg(at)) {
			t.Fatalf("tick at +%s dirtied inside the clock interval", at.Sub(start))
		}
	}
}

// TestRenderNeutralLetsTheClockThrough pins the exception. UPDATED/COMPLETED
// render relative times and the initializing marker expires on a timer, so a
// tick per clockDirtyInterval must still repaint — that dirt has no message
// behind it.
func TestRenderNeutralLetsTheClockThrough(t *testing.T) {
	neutral := RenderNeutral()
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	neutral(status.RefreshTickMsg(start)) // consume the first clock frame

	dirtied := 0
	for i := 1; i <= 150; i++ { // 150 ticks at 2s = 5 minutes
		if !neutral(status.RefreshTickMsg(start.Add(time.Duration(i) * 2 * time.Second))) {
			dirtied++
		}
	}
	if dirtied != 5 {
		t.Errorf("5 minutes of ticks produced %d clock frames, want 5", dirtied)
	}

	// A backwards clock step must not wedge the clock frame off.
	if neutral(status.RefreshTickMsg(start.Add(-time.Hour))) {
		t.Error("a backwards clock step was swallowed as neutral")
	}
}

// inertModel is a child that renders nothing and does nothing, so the only
// thing under test is the wrapper's classification of the message.
type inertModel struct{}

func (inertModel) Init() tea.Cmd                       { return nil }
func (inertModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return inertModel{}, nil }
func (inertModel) View() string                        { return "" }

// TestWrapperSuppressesTheCompositeForRefreshTicks is the end-to-end half:
// the predicate wired into a real compositor.Model, asserted through the only
// thing the wrapper exposes — whether a message arms a composite. No composite
// armed means no child.View, which is the whole saving.
//
// It runs without a WindowSizeMsg on purpose: nothing here needs a compositor
// to exist, and not creating one keeps the test from blitting at the runner.
func TestWrapperSuppressesTheCompositeForRefreshTicks(t *testing.T) {
	m := compositor.NewModel(inertModel{}, compositor.WithRenderNeutral(RenderNeutral()))
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	_, cmd := m.Update(status.RefreshTickMsg(start))
	if cmd == nil {
		t.Fatal("the first refresh tick did not composite; the clock frame is missing")
	}
	// Deliver that composite back, so the wrapper is not merely coalescing
	// behind a tick already in flight — after this, an armed composite is
	// unambiguous.
	m.Update(cmd())
	for i := 1; i < 30; i++ {
		at := start.Add(time.Duration(i) * 2 * time.Second)
		if _, cmd := m.Update(status.RefreshTickMsg(at)); cmd != nil {
			t.Fatalf("refresh tick at +%s armed a composite", at.Sub(start))
		}
	}

	// The reload it dispatches is what repaints, and it still does.
	if _, cmd := m.Update(status.RefreshMsg{}); cmd == nil {
		t.Error("the typed reload reply did not arm a composite")
	}
}

// TestBothPollTicksShareOneClockBudget pins the second classified type and the
// reason the two share lastDirty: the exception owes one repaint per granule
// of whatever is on screen, and a host renders its whole view — not the
// sub-model the message came from. So a status clock frame settles the
// browser's debt in the same minute, and vice versa.
func TestBothPollTicksShareOneClockBudget(t *testing.T) {
	neutral := RenderNeutral()
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	if neutral(browser.RefreshTickMsg(start)) {
		t.Fatal("the first browser tick was neutral; nothing had rendered the clock yet")
	}
	// Inside the interval both types are neutral — the browser tick above
	// already bought the frame.
	if !neutral(status.RefreshTickMsg(start.Add(2 * time.Second))) {
		t.Error("a status tick dirtied on a clock frame the browser tick had already paid for")
	}
	if !neutral(browser.RefreshTickMsg(start.Add(45 * time.Second))) {
		t.Error("a browser tick dirtied inside the clock interval")
	}
	// Past the interval, whichever arrives first takes the next frame.
	if neutral(status.RefreshTickMsg(start.Add(time.Minute))) {
		t.Error("the clock frame did not come due after clockDirtyInterval")
	}
}

// TestRenderNeutralClassifiesNothingElse pins the blast radius: everything the
// user can see the consequence of — keys, the typed reload reply, resizes —
// must still dirty.
func TestRenderNeutralClassifiesNothingElse(t *testing.T) {
	neutral := RenderNeutral()
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}},
		tea.WindowSizeMsg{Width: 80, Height: 24},
		status.RefreshMsg{},
		tea.MouseMsg{},
		nil,
	} {
		if neutral(msg) {
			t.Errorf("%T was classified render-neutral", msg)
		}
	}
}
