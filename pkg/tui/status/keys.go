package status

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"
)

// KeyMap defines the keybindings for the flow status TUI.
// It embeds keymap.Base for standard navigation, actions, search, selection, and fold bindings.
// Only TUI-specific bindings that don't exist in Base are defined here.
type KeyMap struct {
	keymap.Base
	// Job operations (TUI-specific)
	Archive       key.Binding
	AddXmlPlan    key.Binding
	Run           key.Binding
	SetCompleted  key.Binding
	SetStatus     key.Binding
	SetType       key.Binding
	SetTemplate   key.Binding
	AddJob        key.Binding
	AddFromRecipe key.Binding
	Implement     key.Binding
	AgentFromChat key.Binding
	Rename        key.Binding
	Resume        key.Binding
	EditDeps      key.Binding
	DemoteToNote  key.Binding
	// View operations (TUI-specific)
	ToggleColumns    key.Binding
	ViewLogs         key.Binding
	ViewFrontmatter  key.Binding
	ViewBriefing     key.Binding
	ViewEdit         key.Binding
	ViewTokens       key.Binding
	ViewSkillPane    key.Binding
	CloseDetailPane  key.Binding
	SwitchFocus      key.Binding
	FocusLeft        key.Binding // Spatial navigation: focus left pane (jobs)
	FocusRight       key.Binding // Spatial navigation: focus right pane (detail)
	ToggleLayout     key.Binding
	ToggleFullscreen key.Binding
	ViewContext      key.Binding // View context panel (groveterm only)
	ViewNativeAgent  key.Binding // Preview native agent PTY pane (groveterm only)
	ViewMemory       key.Binding // View memory search panel (groveterm only)
	SendInput        key.Binding // For isolated agents: toggle input mode
	ToggleClaw       key.Binding // Enable/disable claw (channels + autonomous)
}

// NewKeyMap creates a new KeyMap with user configuration applied.
// Base bindings (navigation, actions, search, selection, fold) come from keymap.Load().
// Only TUI-specific bindings are defined here.
func NewKeyMap(cfg *config.Config) KeyMap {
	km := KeyMap{
		Base: keymap.Load(cfg, "flow.status"),
		// Job operations
		Archive: key.NewBinding(
			key.WithKeys("X"),
			key.WithHelp("X", "archive selected"),
		),
		AddXmlPlan: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "add XML plan job"),
		),
		Run: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "run job"),
		),
		// Change (c…) namespace. Chord-only — the legacy flat aliases were
		// dropped (sign-off E4, no deprecation window; this is the fleet
		// precedent). "cc" already had no flat key because flat "c" is the
		// change prefix (Process checks MatchesAny before IsPrefixOfAny, so a
		// flat "c" would fire before the chord could arm).
		SetCompleted: key.NewBinding(
			key.WithKeys("cc"),
			key.WithHelp("cc", "mark completed"),
		),
		SetStatus: key.NewBinding(
			key.WithKeys("cs"),
			key.WithHelp("cs", "set status"),
		),
		SetType: key.NewBinding(
			key.WithKeys("ct"),
			key.WithHelp("ct", "set type"),
		),
		SetTemplate: key.NewBinding(
			key.WithKeys("ce"),
			key.WithHelp("ce", "set template"),
		),
		AddJob: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "add job"),
		),
		AddFromRecipe: key.NewBinding(
			key.WithKeys("P"),
			key.WithHelp("P", "add from recipe"),
		),
		Implement: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "new implementation"),
		),
		AgentFromChat: key.NewBinding(
			key.WithKeys("I"),
			key.WithHelp("I", "agent from chat"),
		),
		Rename: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "rename job"),
		),
		// Rebound from "ctrl+R": bubbletea lowercases ctrl chords, so
		// "ctrl+R" never matched. "ctrl+e" is a FreeKeys entry (canonical.go)
		// and unused elsewhere in this TUI. Not "ctrl+r" (reserved: refresh).
		Resume: key.NewBinding(
			key.WithKeys("ctrl+e"),
			key.WithHelp("ctrl+e", "resume job"),
		),
		// Rebound from "ctrl+d": that key is shadowed by Base.PageDown
		// (ctrl+d/pgdown), whose case runs earlier in the Update switch, so
		// the dependency editor could never open. "ctrl+o" is a free key.
		EditDeps: key.NewBinding(
			key.WithKeys("ctrl+o"),
			key.WithHelp("ctrl+o", "edit dependencies"),
		),
		DemoteToNote: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "demote to note"),
		),
		// View operations
		ToggleColumns: key.NewBinding(
			key.WithKeys("T"),
			key.WithHelp("T", "toggle columns"),
		),
		// View (v…) namespace. Chord-only — the legacy flat aliases were dropped
		// (sign-off E4). "vv" (preview job file) never had a flat alias because
		// flat "v" is the view prefix; "vf" (frontmatter) likewise, since its
		// old flat "t" is a reserved toggle prefix.
		ViewLogs: key.NewBinding(
			key.WithKeys("vl"),
			key.WithHelp("vl", "view logs"),
		),
		ViewFrontmatter: key.NewBinding(
			key.WithKeys("vf"),
			key.WithHelp("vf", "view frontmatter"),
		),
		ViewBriefing: key.NewBinding(
			key.WithKeys("vb"),
			key.WithHelp("vb", "view briefing"),
		),
		ViewEdit: key.NewBinding(
			key.WithKeys("vv"),
			key.WithHelp("vv", "preview job file"),
		),
		ViewTokens: key.NewBinding(
			key.WithKeys("vt"),
			key.WithHelp("vt", "view token usage"),
		),
		ViewContext: key.NewBinding(
			key.WithKeys("vc"),
			key.WithHelp("vc", "view context"),
		),
		ViewNativeAgent: key.NewBinding(
			key.WithKeys("va"),
			key.WithHelp("va", "preview agent pane"),
		),
		ViewMemory: key.NewBinding(
			key.WithKeys("vm"),
			key.WithHelp("vm", "memory search"),
		),
		ViewSkillPane: key.NewBinding(
			key.WithKeys("vs"),
			key.WithHelp("vs", "skills"),
		),
		CloseDetailPane: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close detail pane"),
		),
		SwitchFocus: key.NewBinding(
			key.WithKeys("tab", "shift+tab"),
			key.WithHelp("tab/shift+tab", "switch focus"),
		),
		FocusLeft: key.NewBinding(
			key.WithKeys("left"),
			key.WithHelp("←", "focus left"),
		),
		FocusRight: key.NewBinding(
			key.WithKeys("right"),
			key.WithHelp("→", "focus right"),
		),
		ToggleLayout: key.NewBinding(
			key.WithKeys("V"),
			key.WithHelp("V", "toggle h/v split"),
		),
		// Rebound from flat "z": "z" is the reserved fold prefix. "f" is free and
		// mnemonic (fullscreen). No alias — a flat "z" alias would re-flag the
		// squatter.
		ToggleFullscreen: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "fullscreen logs"),
		),
		SendInput: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "input to agent"),
		),
		ToggleClaw: key.NewBinding(
			key.WithKeys("C"),
			key.WithHelp("C", "toggle claw"),
		),
	}

	// Apply TUI-specific overrides from config
	keymap.ApplyTUIOverrides(cfg, "flow", "status", &km)

	// Disable every promoted Base binding this TUI does not handle (verified
	// against every key.Matches(msg, m.KeyMap.*) case in update.go). Handled
	// Base bindings that stay enabled: Up, Down, Top (via the Sequence engine),
	// Bottom, PageUp, PageDown, Select, SelectAll, SelectNone, Edit, Confirm,
	// CopyPath, Help, Quit. Notable disables: SwitchView (tab is consumed by
	// SwitchFocus), TogglePreview (v is consumed by ViewEdit), Base.Rename
	// (shadowed by the outer "rename job" field with a distinct signature),
	// Base.Back/Cancel (esc is handled by CloseDetailPane; ctrl+g unused here).
	for _, b := range []*key.Binding{
		&km.Base.Left, &km.Base.Right, &km.Base.Home, &km.Base.End,
		&km.Base.Back, &km.Base.Cancel, &km.Base.Delete, &km.Base.Yank, &km.Base.Rename, &km.Base.Refresh,
		&km.Base.Search, &km.Base.SearchNext, &km.Base.SearchPrev, &km.Base.ClearSearch, &km.Base.Grep,
		&km.Base.SwitchView, &km.Base.NextTab, &km.Base.PrevTab, &km.Base.FocusNext, &km.Base.FocusPrev, &km.Base.TogglePreview,
		&km.Base.Tab1, &km.Base.Tab2, &km.Base.Tab3, &km.Base.Tab4, &km.Base.Tab5, &km.Base.Tab6, &km.Base.Tab7, &km.Base.Tab8, &km.Base.Tab9,
		&km.Base.FoldOpen, &km.Base.FoldClose, &km.Base.FoldToggle, &km.Base.FoldOpenAll, &km.Base.FoldCloseAll,
	} {
		b.SetEnabled(false)
	}

	return km
}

// Compile-time guard: KeyMap must satisfy SectionedKeyMap by value (KeymapInfo
// passes it by value to MakeTUIInfo). A near-miss Sections() signature would
// silently fall back to the promoted Base.Sections().
var _ keymap.SectionedKeyMap = KeyMap{}

func (k KeyMap) ShortHelp() []key.Binding {
	// Return just quit - help is shown automatically by the help component
	return []key.Binding{k.Quit}
}

// Namespaces returns the which-key chord namespaces for this TUI, built from the
// named KeyMap fields (so any user override applied by ApplyTUIOverrides is
// reflected). The "v" View namespace and the "c" Change namespace group the
// two-key chords declared in NewKeyMap (vl, vf, …, cs, ct, …); the update loop
// arms them through the shared Sequence engine and View() renders the popup.
// Order here is the wire order the update loop's dispatchChord relies on.
func (k KeyMap) Namespaces() []keymap.Namespace {
	return []keymap.Namespace{
		{Prefix: "v", Label: "View", Bindings: []key.Binding{
			k.ViewLogs, k.ViewFrontmatter, k.ViewBriefing, k.ViewEdit,
			k.ViewTokens, k.ViewContext, k.ViewMemory, k.ViewNativeAgent, k.ViewSkillPane,
		}},
		{Prefix: "c", Label: "Change", Bindings: []key.Binding{
			k.SetStatus, k.SetType, k.SetTemplate, k.SetCompleted,
		}},
	}
}

// Sections returns all keybinding sections for the flow status TUI.
// It includes the base sections plus flow-specific sections. The View (v…) and
// Change (c…) namespaces surface as their own sections so the ? overlay and the
// generated registry list the chord members (vl, vf, cs, …) as ordinary
// bindings; the residual Panes section keeps the pane-management controls, and
// the set-family bindings are no longer duplicated into Actions.
func (k KeyMap) Sections() []keymap.Section {
	ns := k.Namespaces()
	return []keymap.Section{
		keymap.NavigationSection(k.Up, k.Down, k.Top, k.Bottom, k.PageUp, k.PageDown),
		keymap.SelectionSection(k.Select, k.SelectAll, k.SelectNone),
		ns[0].Section(),
		ns[1].Section(),
		keymap.NewSectionWithIcon(
			"Panes", theme.IconViewDashboard,
			k.ToggleColumns, k.CloseDetailPane, k.SwitchFocus,
			k.FocusLeft, k.FocusRight, k.ToggleLayout, k.ToggleFullscreen,
		),
		keymap.ActionsSection(
			k.Run, k.Edit, k.Confirm,
			k.AddJob, k.AddFromRecipe, k.AddXmlPlan, k.Implement, k.AgentFromChat, k.Rename,
			k.Resume, k.EditDeps, k.DemoteToNote, k.Archive, k.SendInput, k.ToggleClaw, k.CopyPath,
		),
		k.Base.SystemSection(),
	}
}

// KeymapInfo returns the keymap metadata for the flow status TUI.
// Used by the grove keys registry generator to aggregate all TUI keybindings.
func KeymapInfo() keymap.TUIInfo {
	km := NewKeyMap(nil)
	return keymap.MakeTUIInfo(
		"flow-status",
		"flow",
		"Flow plan status browser and job manager",
		km,
	)
}
