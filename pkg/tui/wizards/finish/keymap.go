// Package finish is the embeddable "plan finish" wizard. It renders a
// checkbox list of cleanup actions for a plan, lets the user toggle
// which ones to run, and emits embed.DoneMsg with the final []*Item
// slice (carrying per-item IsEnabled flags) as its Result on submit,
// or a nil Result on cancel. Hosts embed this package instead of
// calling into cmd.
//
// The wizard is a pure selection UI: it never invokes any Action or
// Check closures itself. Hosts (the flow CLI wrapper and the flow
// meta-panel) are responsible for building the items, passing them
// in, and executing the enabled actions after DoneMsg.
package finish

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/tui/keymap"
)

// KeyMap holds the keybindings for the plan-finish wizard. It embeds
// the core keymap.Base for the standard help/quit keys plus the
// select-all / select-none bindings, and extends it with the
// wizard-specific toggle and confirm keys.
type KeyMap struct {
	keymap.Base
	Toggle      key.Binding
	Confirm     key.Binding
	ToggleForce key.Binding
}

// NewKeyMap returns the default plan-finish wizard keymap, with any
// user overrides from the given core config applied. A nil config is
// acceptable and yields the pure defaults.
func NewKeyMap(cfg *config.Config) KeyMap {
	km := KeyMap{
		Base: keymap.Load(cfg, "flow.plan-finish"),
		Toggle: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle selection"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "confirm and proceed"),
		),
		ToggleForce: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "toggle FORCE (discards uncommitted work)"),
		),
	}
	// esc dismisses the wizard exactly like q. The wizard has no text entry and
	// no sub-screens, so esc is unambiguous here — unlike the add/init wizards,
	// which bind it to "unfocus this field" / "back one screen". Applied before
	// ApplyTUIOverrides so a user's own binding still wins.
	km.Quit = key.NewBinding(
		key.WithKeys("q", "esc", "ctrl+c"),
		key.WithHelp("q/esc", "quit"),
	)
	keymap.ApplyTUIOverrides(cfg, "flow", "plan-finish", &km)
	return km
}

// ShortHelp returns key bindings to show in the mini help view.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Toggle, k.Confirm, k.ToggleForce, k.Quit}
}

// FullHelp returns keybindings for the expanded help view.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Toggle, k.SelectAll, k.SelectNone},
		{k.ToggleForce, k.Confirm, k.Help, k.Quit},
	}
}

// Sections returns all keybinding sections for the plan finish TUI.
func (k KeyMap) Sections() []keymap.Section {
	return []keymap.Section{
		{
			Name:     "Navigation",
			Bindings: []key.Binding{k.Up, k.Down},
		},
		{
			Name:     "Selection",
			Bindings: []key.Binding{k.Toggle, k.SelectAll, k.SelectNone},
		},
		{
			Name:     "Danger",
			Bindings: []key.Binding{k.ToggleForce},
		},
		{
			Name:     "Actions",
			Bindings: []key.Binding{k.Confirm},
		},
		{
			Name:     "System",
			Bindings: []key.Binding{k.Help, k.Quit},
		},
	}
}
