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
	Home              key.Binding
	End               key.Binding
	ViewPlan          key.Binding
	OpenPlan          key.Binding
	ViewGit           key.Binding
	FinishPlan        key.Binding
	NewPlan           key.Binding
	SetActive         key.Binding
	ReviewPlan        key.Binding
	EditNotes         key.Binding
	FastForwardMain   key.Binding
	FastForwardUpdate key.Binding
	ToggleGitLog      key.Binding
	ToggleHold        key.Binding
	ToggleArchived    key.Binding
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
			k.Home,
			k.End,
			k.ViewPlan,
			k.OpenPlan,
			k.ViewGit,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Actions")),
			k.NewPlan,
			k.SetActive,
			k.EditNotes,
			k.ReviewPlan,
			k.FinishPlan,
			k.SetHoldStatus,
			k.FastForwardUpdate,
			k.FastForwardMain,
			k.ToggleGitLog,
			k.ToggleHold,
			k.ToggleArchived,
			k.Help,
			k.Quit,
		},
	}
}

// Sections returns all keybinding sections for the plan browser TUI,
// used by the shared help builder to render grouped help screens.
func (k KeyMap) Sections() []keymap.Section {
	return []keymap.Section{
		{
			Name:     "Navigation",
			Bindings: []key.Binding{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End, k.ViewPlan, k.OpenPlan, k.ViewGit},
		},
		{
			Name: "Actions",
			Bindings: []key.Binding{
				k.NewPlan, k.SetActive, k.EditNotes, k.ReviewPlan, k.FinishPlan,
				k.SetHoldStatus, k.FastForwardUpdate, k.FastForwardMain,
			},
		},
		{
			Name:     "View",
			Bindings: []key.Binding{k.ToggleGitLog, k.ToggleHold, k.ToggleArchived},
		},
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
		Home: key.NewBinding(
			key.WithKeys("home"),
			key.WithHelp("home", "first plan"),
		),
		End: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("end", "last plan"),
		),
		ViewPlan: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "view plan details"),
		),
		OpenPlan: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "open plan workspace"),
		),
		ViewGit: key.NewBinding(
			key.WithKeys("V"),
			key.WithHelp("V", "inspect in Git Viewer"),
		),
		FinishPlan: key.NewBinding(
			key.WithKeys("ctrl+x"),
			key.WithHelp("ctrl+x", "finish plan"),
		),
		NewPlan: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "create new plan"),
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
		ToggleGitLog: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "toggle git log"),
		),
		ToggleHold: key.NewBinding(
			key.WithKeys("H"),
			key.WithHelp("H", "toggle on-hold"),
		),
		ToggleArchived: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "toggle archived"),
		),
		SetHoldStatus: key.NewBinding(
			key.WithKeys("h"),
			key.WithHelp("h", "hold/unhold plan"),
		),
	}
	keymap.ApplyTUIOverrides(cfg, "flow", "plan-list", &km)
	// Mutation intentionally remains unavailable until U/M are routed through
	// the shared preview/confirmation lifecycle service. This slice ships only
	// the read-only V handoff.
	km.FastForwardUpdate.SetEnabled(false)
	km.FastForwardMain.SetEnabled(false)
	return km
}
