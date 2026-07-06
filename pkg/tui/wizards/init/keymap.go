package planinit

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/tui/keymap"
)

// KeyMap holds the keybindings for the plan-init wizard. It embeds
// keymap.Base for the standard help/quit keys and extends it with
// form-specific navigation and submission keys.
type KeyMap struct {
	keymap.Base
	Toggle         key.Binding
	ToggleAdvanced key.Binding
	NextField      key.Binding
	PrevField      key.Binding
	Submit         key.Binding
	Escape         key.Binding
	Insert         key.Binding
	Help           key.Binding
}

// NewKeyMap returns the default plan-init wizard keymap, with any
// user overrides from the given core config applied. A nil config is
// acceptable and yields the pure defaults.
func NewKeyMap(cfg *config.Config) KeyMap {
	km := KeyMap{
		Base: keymap.NewBase(),
		Toggle: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle checkbox"),
		),
		ToggleAdvanced: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "toggle advanced"),
		),
		NextField: key.NewBinding(
			key.WithKeys("tab", "j"),
			key.WithHelp("tab/j", "next field"),
		),
		PrevField: key.NewBinding(
			key.WithKeys("shift+tab", "k"),
			key.WithHelp("shift+tab/k", "prev field"),
		),
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "submit form"),
		),
		Escape: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "normal mode"),
		),
		Insert: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "insert mode"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "show help"),
		),
	}
	keymap.ApplyTUIOverrides(cfg, "flow", "plan-init", &km)
	return km
}

// ShortHelp returns key bindings to show in the mini help view.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Base.Quit}
}

// FullHelp returns keybindings for the expanded help view.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Navigation")),
			k.NextField,
			k.PrevField,
			key.NewBinding(key.WithKeys("j/k, ↑/↓"), key.WithHelp("j/k, ↑/↓", "navigate list items")),
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Actions")),
			k.Toggle,
			k.Submit,
			k.ToggleAdvanced,
			k.Insert,
			k.Escape,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "General")),
			k.Help,
			k.Base.Quit,
		},
	}
}

// Sections returns all keybinding sections for the plan-init wizard.
func (k KeyMap) Sections() []keymap.Section {
	return []keymap.Section{
		{
			Name:     "Navigation",
			Bindings: []key.Binding{k.NextField, k.PrevField},
		},
		{
			Name:     "Actions",
			Bindings: []key.Binding{k.Toggle, k.Submit, k.ToggleAdvanced, k.Insert, k.Escape},
		},
		{
			Name:     "System",
			Bindings: []key.Binding{k.Help, k.Base.Quit},
		},
	}
}
