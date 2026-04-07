// Package add is the embeddable "add job" wizard for a flow plan. It
// renders a multi-field form (title, job type, dependencies, template
// or skill picker, prompt) and emits embed.DoneMsg with a constructed
// *orchestration.Job as its Result on submit, or a nil Result on
// cancel. Hosts embed this package instead of calling into cmd.
//
// The standalone flow CLI (`flow plan add -i`) wraps this model via
// embed.RunStandalone; the flow meta-panel at flow/pkg/tui/view hosts
// it as a transient sub-mode launched from the `a` hotkey on the
// status view.
package add

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/tui/keymap"
)

// KeyMap holds the keybindings for the add-job wizard. It embeds the
// core keymap.Base for the standard help/quit keys and extends it with
// form-specific navigation and submission keys.
type KeyMap struct {
	keymap.Base
	Next     key.Binding
	Prev     key.Binding
	Submit   key.Binding
	Toggle   key.Binding
	GoTop    key.Binding
	GoBottom key.Binding
	PageUp   key.Binding
	PageDown key.Binding
}

// NewKeyMap returns the default add-job wizard keymap, with any user
// overrides from the given core config applied. A nil config is
// acceptable and yields the pure defaults.
func NewKeyMap(cfg *config.Config) KeyMap {
	km := KeyMap{
		Base: keymap.NewBase(),
		Next: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next field"),
		),
		Prev: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "prev field"),
		),
		Submit: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "submit"),
		),
		Toggle: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle"),
		),
		GoTop: key.NewBinding(
			key.WithKeys("gg", "home"),
			key.WithHelp("gg/home", "go to top"),
		),
		GoBottom: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G/end", "go to bottom"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("ctrl+u", "pgup"),
			key.WithHelp("ctrl+u/pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("ctrl+d", "pgdown"),
			key.WithHelp("ctrl+d/pgdown", "page down"),
		),
	}
	keymap.ApplyTUIOverrides(cfg, "flow", "plan-add", &km)
	return km
}

// Sections returns all keybinding sections for the add job TUI.
func (k KeyMap) Sections() []keymap.Section {
	return []keymap.Section{
		{
			Name:     "Navigation",
			Bindings: []key.Binding{k.Next, k.Prev, k.GoTop, k.GoBottom, k.PageUp, k.PageDown},
		},
		{
			Name:     "Actions",
			Bindings: []key.Binding{k.Toggle, k.Submit},
		},
		{
			Name:     "System",
			Bindings: []key.Binding{k.Help, k.Quit},
		},
	}
}

// ShortHelp returns key bindings to show in the mini help view.
func (k KeyMap) ShortHelp() []key.Binding {
	return k.Base.ShortHelp()
}

// FullHelp returns keybindings for the expanded help view.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithHelp("", "Navigation")),
			k.Next,
			k.Prev,
			key.NewBinding(key.WithHelp("↑/↓, j/k", "Navigate lists")),
			k.GoTop,
			k.GoBottom,
			k.PageUp,
			k.PageDown,
			key.NewBinding(key.WithHelp("/", "Search lists")),
			key.NewBinding(key.WithHelp("esc", "Clear search")),
		},
		{
			key.NewBinding(key.WithHelp("", "Actions")),
			k.Toggle,
			key.NewBinding(key.WithHelp("enter", "Confirm & Next")),
			key.NewBinding(key.WithHelp("c", "Quick chat setup")),
			key.NewBinding(key.WithHelp("a", "Quick agent setup")),
			key.NewBinding(key.WithHelp("ctrl+s", "Save and exit")),
			key.NewBinding(key.WithHelp(":wq", "Vim save and exit")),
			k.Submit,
			k.Help,
			k.Quit,
		},
	}
}

// clawToggleKey is the hardcoded ctrl+g binding for toggling the
// claw/signal+autonomous mode on interactive_agent jobs. It is not
// part of the user-configurable KeyMap because it's a specialized
// power-user toggle.
var clawToggleKey = key.NewBinding(key.WithKeys("ctrl+g"))
