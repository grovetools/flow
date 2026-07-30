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
		// Toggle (t…) namespace member (canon 60 RULE T; was flat `f`). This is
		// a deliberately SINGLE-member namespace: FORCE turns
		// `git worktree remove` into its --force form, i.e. it discards
		// uncommitted work, so putting it behind a which-key popup is the
		// destructive-action safety win contract 31 §5.1 asks for, not merely
		// consistency. Chord-only — no flat `f` alias (sign-off E4).
		ToggleForce: key.NewBinding(
			key.WithKeys("tf"),
			key.WithHelp("tf", "toggle FORCE (discards uncommitted work)"),
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

	// Disable every promoted Base binding this wizard does not dispatch
	// (verified against every key.Matches arm in update.go). Kept enabled:
	// Up, Down, SelectAll, SelectNone, Help, Quit — plus the outer
	// Toggle/Confirm/ToggleForce fields, which shadow Base.Select/Base.Confirm
	// with distinct signatures. Without this the wizard advertised — and the
	// audit saw — a full vim keymap it never handles, including Base.TogglePreview
	// squatting on flat `v` (the reserved view prefix) and Base.Left/Right on
	// h/l. Behaviourally inert: key.Matches never consults these fields.
	for _, b := range []*key.Binding{
		&km.Base.Left, &km.Base.Right, &km.Base.PageUp, &km.Base.PageDown,
		&km.Base.Home, &km.Base.End, &km.Base.Top, &km.Base.Bottom,
		&km.Base.Confirm, &km.Base.Cancel, &km.Base.Back, &km.Base.Edit,
		&km.Base.Delete, &km.Base.Yank, &km.Base.Rename, &km.Base.Refresh, &km.Base.CopyPath,
		&km.Base.Search, &km.Base.SearchNext, &km.Base.SearchPrev, &km.Base.ClearSearch, &km.Base.Grep,
		&km.Base.SwitchView, &km.Base.NextTab, &km.Base.PrevTab,
		&km.Base.FocusNext, &km.Base.FocusPrev, &km.Base.TogglePreview,
		&km.Base.Tab1, &km.Base.Tab2, &km.Base.Tab3, &km.Base.Tab4, &km.Base.Tab5,
		&km.Base.Tab6, &km.Base.Tab7, &km.Base.Tab8, &km.Base.Tab9,
		&km.Base.Select,
		&km.Base.FoldOpen, &km.Base.FoldClose, &km.Base.FoldToggle,
		&km.Base.FoldOpenAll, &km.Base.FoldCloseAll,
	} {
		b.SetEnabled(false)
	}
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

// Namespaces returns the which-key chord namespaces for the plan-finish wizard,
// built from the named KeyMap fields (so a user override applied by
// ApplyTUIOverrides is reflected — namespace.go's ConfigKey-stability rule;
// never construct members inline). One namespace with one member: see the
// ToggleForce comment in NewKeyMap for why that is deliberate.
func (k KeyMap) Namespaces() []keymap.Namespace {
	return []keymap.Namespace{
		{Prefix: "t", Label: "Toggle", Bindings: []key.Binding{k.ToggleForce}},
	}
}

// Sections returns all keybinding sections for the plan finish TUI.
func (k KeyMap) Sections() []keymap.Section {
	ns := k.Namespaces()
	return []keymap.Section{
		{
			Name:     "Navigation",
			Bindings: []key.Binding{k.Up, k.Down},
		},
		{
			Name:     "Selection",
			Bindings: []key.Binding{k.Toggle, k.SelectAll, k.SelectNone},
		},
		ns[0].Section(),
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
