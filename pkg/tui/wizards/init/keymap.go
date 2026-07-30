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
		// Toggle (t…) namespace member (canon 60 RULE T; was flat `a`).
		// Chord-only, no flat alias (sign-off E4) — and it frees `a`, which is
		// Ring-1 "create the TUI's primary noun" fleet-wide (§5.1). The mode
		// guard in update.go keeps the prefix from arming inside a text field.
		ToggleAdvanced: key.NewBinding(
			key.WithKeys("ta"),
			key.WithHelp("ta", "toggle advanced"),
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

	// Disable every promoted Base binding this wizard does not dispatch. The
	// only key.Matches call in update.go is ToggleAdvanced; every other key is
	// routed by raw msg.String(), and the wizard's own NextField/PrevField/
	// Toggle/Submit/Escape/Insert/Help fields already carry those spellings.
	// Kept enabled: Base.Quit, which Sections() exports. Without this the
	// wizard advertised — and the audit saw — a full vim keymap it never
	// handles, including Base.TogglePreview squatting on flat `v` (the reserved
	// view prefix). Behaviourally inert: no key.Matches consults these fields.
	for _, b := range []*key.Binding{
		&km.Base.Up, &km.Base.Down, &km.Base.Left, &km.Base.Right,
		&km.Base.PageUp, &km.Base.PageDown,
		&km.Base.Home, &km.Base.End, &km.Base.Top, &km.Base.Bottom,
		&km.Base.Help, &km.Base.Confirm, &km.Base.Cancel, &km.Base.Back, &km.Base.Edit,
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

// Namespaces returns the which-key chord namespaces for the plan-init wizard,
// built from the named KeyMap fields (so a user override applied by
// ApplyTUIOverrides is reflected — namespace.go's ConfigKey-stability rule;
// never construct members inline). Single-member, like flow-plan-finish's.
func (k KeyMap) Namespaces() []keymap.Namespace {
	return []keymap.Namespace{
		{Prefix: "t", Label: "Toggle", Bindings: []key.Binding{k.ToggleAdvanced}},
	}
}

// Sections returns all keybinding sections for the plan-init wizard.
func (k KeyMap) Sections() []keymap.Section {
	ns := k.Namespaces()
	return []keymap.Section{
		{
			Name:     "Navigation",
			Bindings: []key.Binding{k.NextField, k.PrevField},
		},
		{
			Name:     "Actions",
			Bindings: []key.Binding{k.Toggle, k.Submit, k.Insert, k.Escape},
		},
		ns[0].Section(),
		{
			Name:     "System",
			Bindings: []key.Binding{k.Help, k.Base.Quit},
		},
	}
}
