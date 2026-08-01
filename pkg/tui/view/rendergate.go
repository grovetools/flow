package view

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/status"
)

// clockDirtyInterval is how often a refresh tick is let through as a normal,
// dirtying message so the clock-driven parts of the job table advance. One
// minute is the coarsest cell those parts have: formatRelativeTime's finest
// step is "%dm ago" (status/view.go), and the initializing marker's grace is
// two minutes (status/view.go isInitializing). Anything finer would be paying
// for renders nothing on screen could show.
const clockDirtyInterval = time.Minute

// RenderNeutral returns flow's render-neutrality predicate for
// compositor.WithRenderNeutral — the wrapper-hosted equivalent of treemux's
// classifyRenderMsg, which the compositor gained the hook for in job 86.
//
// status.RefreshTickMsg is the 2s plan-refresh poll. Its handler
// (status/update.go) re-arms the tick and then does nothing else unless
// something moved: the reload only goes out when the plan directory's
// fingerprint changed, and it comes back as a typed RefreshMsg that dirties
// normally, as does the running-token refresh. So on an unchanged plan the
// tick is exactly the shape the contract is for — no user-visible consequence
// that is not carried by a follow-up message.
//
// Dirtying for it anyway cost a full render of the job table every 2 seconds,
// which was the entire residual idle cost of `flow plan status`: 2.12% of a
// core on an 85-job plan against 1.02% on a 4-job one (job 84's audit — the
// delta is the table, not the tick).
//
// The exception is the wall clock, which is a dirt source with no message
// behind it: the UPDATED and COMPLETED columns render relative times, and the
// "initializing" marker expires on its own. Mirroring treemux's
// Gate.ClockDirty, the first tick of each clockDirtyInterval is not neutral,
// so those keep advancing at the only granularity they can show — one render
// a minute in place of thirty.
//
// The browser's local-fallback tick (browser.RefreshTickMsg) is classified on
// the same terms and shares the same clock budget. Sharing lastDirty across
// both types is not a shortcut: what the exception owes is one repaint per
// granule of whatever is on screen, and a dirtying tick of either type buys
// exactly that — a host renders its whole view, not the sub-model the message
// came from.
//
// treemux consults this predicate too, from its own gate
// (app.classifyRenderMsg), rather than re-listing flow's ticks where they
// would rot. With a Plan panel live the 2s tick was costing it one full chrome
// frame every two seconds (job 84 §4).
//
// The returned closure keeps unsynchronised state: the compositor consults it
// only from the bubbletea event loop, and one predicate belongs to one
// wrapper.
func RenderNeutral() func(tea.Msg) bool {
	var lastDirty time.Time
	return func(msg tea.Msg) bool {
		now, ok := refreshTickAt(msg)
		if !ok {
			return false
		}
		// now.Before guards a backwards clock step: without it, a jump back
		// would suppress the clock frame until wall time caught up again.
		if now.Sub(lastDirty) >= clockDirtyInterval || now.Before(lastDirty) {
			lastDirty = now
			return false
		}
		return true
	}
}

// refreshTickAt recognises the poll ticks of both sub-models and returns the
// instant the tick carries — the input the clock exception is keyed on.
func refreshTickAt(msg tea.Msg) (time.Time, bool) {
	switch tick := msg.(type) {
	case status.RefreshTickMsg:
		return time.Time(tick), true
	case browser.RefreshTickMsg:
		return time.Time(tick), true
	}
	return time.Time{}, false
}
