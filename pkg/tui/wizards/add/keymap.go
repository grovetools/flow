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
	// Quick-action + mode-gated bindings, promoted from raw msg.String()
	// handlers in update.go so help and the registry match reality.
	QuickChat  key.Binding // nav-mode: preset a chat job
	QuickAgent key.Binding // nav-mode: preset an interactive_agent job
	Confirm    key.Binding // nav-mode / in-list: confirm & advance
	ToggleClaw key.Binding // toggle claw on interactive_agent jobs (was clawToggleKey)
}

// Compile-time guard: KeyMap must satisfy SectionedKeyMap by value (NewKeyMap
// is passed by value to help.New / MakeTUIInfo). A near-miss Sections()
// signature would silently fall back to the promoted Base.Sections().
var _ keymap.SectionedKeyMap = KeyMap{}

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
		// "gg" is advertised truthfully: the wizard has no Sequence engine, so
		// update.go hand-rolls the two-press "g" chord timer (mirrors
		// grove-config's gg handler). "home" is the single-press alternate.
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
		// Change (c…) namespace members. Chord-only (E4) — the flat `c` was
		// squatting on the reserved change prefix, and `a` is Ring-1 create
		// (canon 60 §4.3 / §5.1), so both migrate into c… together.
		QuickChat: key.NewBinding(
			key.WithKeys("cc"),
			key.WithHelp("cc", "quick chat setup"),
		),
		QuickAgent: key.NewBinding(
			key.WithKeys("ca"),
			key.WithHelp("ca", "quick agent setup"),
		),
		// "enter" only (not Base's enter/y): "y" must remain typeable in text
		// fields.
		Confirm: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "confirm & next"),
		),
		// Toggle (t…) namespace member (canon 60 RULE T; was ctrl+t). The
		// chord is also host-safe for the reason the old binding existed:
		// ctrl+g is treemux's non-deferrable action-chord ARM, so the claw
		// toggle could never live there; a plain t… chord is not a host
		// global at all. The mode guard in update.go keeps it from arming
		// inside a text field.
		ToggleClaw: key.NewBinding(
			key.WithKeys("ta"),
			key.WithHelp("ta", "toggle claw"),
		),
	}
	keymap.ApplyTUIOverrides(cfg, "flow", "plan-add", &km)

	// Disable every promoted Base binding this wizard does not handle. Kept
	// enabled: Base.Up/Base.Down (list nav is delegated to bubbles lists and
	// handled raw), Help, Quit. All other Base keys — including the ones
	// shadowed by the outer GoTop/GoBottom/PageUp/PageDown/Toggle/Confirm
	// fields (distinct signatures) — are turned off so help + the registry
	// advertise only real keys. Base.Search ("/") and Base.Back ("esc") are
	// off too: "/" filtering is internal to the bubbles list and "esc"
	// (unfocus) is a raw modal key, neither matched via this keymap.
	for _, b := range []*key.Binding{
		&km.Base.Left, &km.Base.Right, &km.Base.Home, &km.Base.End, &km.Base.Top, &km.Base.Bottom,
		&km.Base.PageUp, &km.Base.PageDown, &km.Base.Confirm, &km.Base.Cancel, &km.Base.Back,
		&km.Base.Edit, &km.Base.Delete, &km.Base.Yank, &km.Base.Rename, &km.Base.Refresh, &km.Base.CopyPath,
		&km.Base.Search, &km.Base.SearchNext, &km.Base.SearchPrev, &km.Base.ClearSearch, &km.Base.Grep,
		&km.Base.SwitchView, &km.Base.NextTab, &km.Base.PrevTab, &km.Base.FocusNext, &km.Base.FocusPrev, &km.Base.TogglePreview,
		&km.Base.Tab1, &km.Base.Tab2, &km.Base.Tab3, &km.Base.Tab4, &km.Base.Tab5, &km.Base.Tab6, &km.Base.Tab7, &km.Base.Tab8, &km.Base.Tab9,
		&km.Base.Select, &km.Base.SelectAll, &km.Base.SelectNone,
		&km.Base.FoldOpen, &km.Base.FoldClose, &km.Base.FoldToggle, &km.Base.FoldOpenAll, &km.Base.FoldCloseAll,
	} {
		b.SetEnabled(false)
	}
	return km
}

// Namespaces returns the which-key chord namespaces for the add-job wizard,
// built from the named KeyMap fields (so a user override applied by
// ApplyTUIOverrides is reflected — namespace.go's ConfigKey-stability rule;
// never construct members inline). Order here is the wire order ProcessChord
// relies on.
func (k KeyMap) Namespaces() []keymap.Namespace {
	return []keymap.Namespace{
		{Prefix: "c", Label: "Change", Bindings: []key.Binding{
			k.QuickChat, k.QuickAgent,
		}},
		{Prefix: "t", Label: "Toggle", Bindings: []key.Binding{
			k.ToggleClaw,
		}},
	}
}

// Sections returns the keybinding sections for the add-job wizard, scoped to
// only the keys the wizard actually handles. The Change (c…) namespace fires
// only in navigation mode (unfocused, off the title/prompt text fields) and
// the Toggle (t…) namespace only outside a text field — see the mode guard in
// update.go.
func (k KeyMap) Sections() []keymap.Section {
	ns := k.Namespaces()
	return []keymap.Section{
		keymap.NavigationSection(k.Up, k.Down, k.Next, k.Prev, k.GoTop, k.GoBottom, k.PageUp, k.PageDown),
		keymap.ActionsSection(k.Toggle, k.Submit),
		ns[0].Section(),
		ns[1].Section(),
		keymap.NewSection("Navigation Mode", k.Confirm),
		k.Base.SystemSection(),
	}
}

// ShortHelp returns key bindings to show in the mini help view.
func (k KeyMap) ShortHelp() []key.Binding {
	return k.Base.ShortHelp()
}
