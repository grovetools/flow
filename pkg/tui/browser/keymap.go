// Package browser is the embeddable plan-browser TUI. It renders the
// per-workspace list of plans with their status, worktree, and git merge
// information, and emits BrowserPlanSelectedMsg when the user picks a
// plan to open. Hosts that embed this model (the `flow plan tui` CLI and
// the flow meta-panel at flow/pkg/tui/view) are responsible for acting
// on selections; the browser itself does not launch the plan status TUI.
package browser

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/tui/keymap"
)

// KeyMap holds the keybindings for the plan browser TUI.
type KeyMap struct {
	keymap.Base
	Up                key.Binding
	Down              key.Binding
	PageUp            key.Binding
	PageDown          key.Binding
	Top               key.Binding
	Bottom            key.Binding
	ViewPlan          key.Binding
	OpenPlan          key.Binding
	ViewGit           key.Binding
	FinishPlan        key.Binding
	NewPlan           key.Binding
	NewRollingPlan    key.Binding
	SetActive         key.Binding
	ReviewPlan        key.Binding
	EditNotes         key.Binding
	FastForwardMain   key.Binding
	FastForwardUpdate key.Binding
	FastForwardAll    key.Binding
	ToggleGitLog      key.Binding
	ToggleHold        key.Binding
	ToggleArchived    key.Binding
	ToggleColumns     key.Binding
	SetHoldStatus     key.Binding
}

// ShortHelp returns the minimal help row shown at the bottom of the TUI.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Quit}
}

// FullHelp returns the grouped help shown when the user presses `?`.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Navigation")),
			k.Up,
			k.Down,
			k.PageUp,
			k.PageDown,
			k.Top,
			k.Bottom,
			k.ViewPlan,
			k.OpenPlan,
			k.ViewGit,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Actions")),
			k.NewPlan,
			k.NewRollingPlan,
			k.SetActive,
			k.EditNotes,
			k.ReviewPlan,
			k.FinishPlan,
			k.SetHoldStatus,
			k.FastForwardUpdate,
			k.FastForwardMain,
			k.FastForwardAll,
			k.ToggleGitLog,
			k.ToggleHold,
			k.ToggleArchived,
			k.ToggleColumns,
			k.Help,
			k.Quit,
		},
	}
}

// Namespaces returns the which-key chord namespaces for the plan browser TUI,
// built from the named KeyMap fields (so a user override applied by
// ApplyTUIOverrides is reflected — namespace.go's ConfigKey-stability rule;
// never construct members inline). Order here is the wire order ProcessChord
// relies on.
func (k KeyMap) Namespaces() []keymap.Namespace {
	return []keymap.Namespace{
		{Prefix: "t", Label: "Toggle", Bindings: []key.Binding{
			k.ToggleGitLog, k.ToggleHold, k.ToggleArchived, k.ToggleColumns,
		}},
		{Prefix: "v", Label: "View", Bindings: []key.Binding{
			k.ViewGit,
		}},
		{Prefix: "c", Label: "Change", Bindings: []key.Binding{
			k.SetHoldStatus,
		}},
	}
}

// Sections returns all keybinding sections for the plan browser TUI,
// used by the shared help builder to render grouped help screens.
func (k KeyMap) Sections() []keymap.Section {
	ns := k.Namespaces()
	return []keymap.Section{
		{
			Name:     "Navigation",
			Bindings: []key.Binding{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom, k.ViewPlan, k.OpenPlan},
		},
		{
			Name: "Actions",
			Bindings: []key.Binding{
				k.NewPlan, k.NewRollingPlan, k.SetActive, k.EditNotes, k.ReviewPlan, k.FinishPlan,
				k.FastForwardUpdate, k.FastForwardMain, k.FastForwardAll,
			},
		},
		ns[0].Section(),
		ns[1].Section(),
		ns[2].Section(),
		{
			Name:     "System",
			Bindings: []key.Binding{k.Help, k.Quit},
		},
	}
}

// NewKeyMap builds the default browser keymap, applying any user overrides
// from the provided core config.
func NewKeyMap(cfg *config.Config) KeyMap {
	km := KeyMap{
		Base: keymap.Load(cfg, "flow.plan-list"),
		Up: key.NewBinding(
			key.WithKeys("k", "up"),
			key.WithHelp("k/↑", "move up"),
		),
		Down: key.NewBinding(
			key.WithKeys("j", "down"),
			key.WithHelp("j/↓", "move down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+d"),
			key.WithHelp("pgdn", "page down"),
		),
		// Canon 60 §7.1: these are the canonical top/bottom motions, and the
		// FIELD names carry the registry action (ConfigKey). They were spelled
		// Home/End, which read as two extra actions instead of the canonical
		// pair — that is reserved-key violation #10. The keys are unchanged
		// apart from adding the fleet-standard gg to Top; do NOT "fix" this
		// with an "end"->"bottom" NormalizeAction alias, which breaks the
		// currently-clean bottom consistency check (see the warning in §7.1).
		Top: key.NewBinding(
			key.WithKeys("gg", "home"),
			key.WithHelp("gg/home", "first plan"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("end/G", "last plan"),
		),
		ViewPlan: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "view plan details"),
		),
		OpenPlan: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "open plan workspace"),
		),
		// View (v…) namespace member (canon 60 §4.1; was flat `V`).
		ViewGit: key.NewBinding(
			key.WithKeys("vg"),
			key.WithHelp("vg", "inspect in Git Viewer"),
		),
		FinishPlan: key.NewBinding(
			key.WithKeys("ctrl+x"),
			key.WithHelp("ctrl+x", "finish plan"),
		),
		NewPlan: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "create new plan"),
		),
		// The rolling plan is the shared home for quick tasks and needs no
		// wizard — a workspace whose plans directory does not exist yet is one
		// keystroke away from having one. The empty-state prompt also accepts
		// enter, which otherwise does nothing with no row under the cursor.
		NewRollingPlan: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "create rolling plan"),
		),
		SetActive: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "set active plan"),
		),
		ReviewPlan: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "review changes"),
		),
		EditNotes: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit notes"),
		),
		FastForwardMain: key.NewBinding(
			key.WithKeys("M"),
			key.WithHelp("M", "merge to main"),
		),
		FastForwardUpdate: key.NewBinding(
			key.WithKeys("U"),
			key.WithHelp("U", "update from main"),
		),
		FastForwardAll: key.NewBinding(
			key.WithKeys("F"),
			key.WithHelp("F", "update all conflict-free plans from main"),
		),
		// Toggle (t…) namespace members (canon 60 RULE T). ToggleGitLog also
		// vacates flat `g`, the reserved goto prefix it was squatting on —
		// after this the fleet's flat `g` is fully free for the gg motion.
		// The letters converge with nb-browser and nav (ta/tc/tg/th).
		ToggleGitLog: key.NewBinding(
			key.WithKeys("tg"),
			key.WithHelp("tg", "toggle git log"),
		),
		ToggleHold: key.NewBinding(
			key.WithKeys("th"),
			key.WithHelp("th", "toggle on-hold"),
		),
		ToggleArchived: key.NewBinding(
			key.WithKeys("ta"),
			key.WithHelp("ta", "toggle archived"),
		),
		ToggleColumns: key.NewBinding(
			key.WithKeys("tc"),
			key.WithHelp("tc", "toggle columns"),
		),
		// Change (c…) namespace member (canon 60 §4.3; was flat `h`, which
		// means "left" in nine other TUIs).
		SetHoldStatus: key.NewBinding(
			key.WithKeys("ch"),
			key.WithHelp("ch", "hold/unhold plan"),
		),
	}
	keymap.ApplyTUIOverrides(cfg, "flow", "plan-list", &km)

	// Disable every promoted Base binding this table browser does not
	// dispatch, including the ones shadowed by the outer
	// Up/Down/PageUp/PageDown/Top/Bottom fields (distinct signatures).
	// Notably Base.TogglePreview squatted on flat `v` and Base.Left/Right on
	// h/l — both of which the v… namespace and the ch chord now need free.
	// Kept enabled: Help and Quit, the only Base keys handled here.
	for _, b := range []*key.Binding{
		&km.Base.Up, &km.Base.Down, &km.Base.Left, &km.Base.Right,
		&km.Base.PageUp, &km.Base.PageDown,
		&km.Base.Home, &km.Base.End, &km.Base.Top, &km.Base.Bottom,
		&km.Base.Confirm, &km.Base.Cancel, &km.Base.Back, &km.Base.Edit,
		&km.Base.Delete, &km.Base.Yank, &km.Base.Rename, &km.Base.Refresh, &km.Base.CopyPath,
		&km.Base.Search, &km.Base.SearchNext, &km.Base.SearchPrev, &km.Base.ClearSearch, &km.Base.Grep,
		&km.Base.SwitchView, &km.Base.NextTab, &km.Base.PrevTab,
		&km.Base.FocusNext, &km.Base.FocusPrev, &km.Base.TogglePreview,
		&km.Base.Tab1, &km.Base.Tab2, &km.Base.Tab3, &km.Base.Tab4, &km.Base.Tab5,
		&km.Base.Tab6, &km.Base.Tab7, &km.Base.Tab8, &km.Base.Tab9,
		&km.Base.Select, &km.Base.SelectAll, &km.Base.SelectNone,
		&km.Base.FoldOpen, &km.Base.FoldClose, &km.Base.FoldToggle,
		&km.Base.FoldOpenAll, &km.Base.FoldCloseAll,
	} {
		b.SetEnabled(false)
	}
	return km
}
