package status_tui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/tui/keymap"
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
	// View operations (TUI-specific)
	ToggleColumns    key.Binding
	ViewLogs         key.Binding
	ViewFrontmatter  key.Binding
	ViewBriefing     key.Binding
	ViewEdit         key.Binding
	CycleDetailPane  key.Binding
	CloseDetailPane  key.Binding
	SwitchFocus      key.Binding
	ToggleLayout     key.Binding
	ToggleFullscreen key.Binding
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
		SetCompleted: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "mark completed"),
		),
		SetStatus: key.NewBinding(
			key.WithKeys("S"),
			key.WithHelp("S", "set status"),
		),
		SetType: key.NewBinding(
			key.WithKeys("Y"),
			key.WithHelp("Y", "set type"),
		),
		SetTemplate: key.NewBinding(
			key.WithKeys("E"),
			key.WithHelp("E", "set template"),
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
			key.WithKeys("i"),
			key.WithHelp("i", "implement selected"),
		),
		AgentFromChat: key.NewBinding(
			key.WithKeys("I"),
			key.WithHelp("I", "agent from chat"),
		),
		Rename: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "rename job"),
		),
		Resume: key.NewBinding(
			key.WithKeys("ctrl+R"),
			key.WithHelp("ctrl+R", "resume job"),
		),
		EditDeps: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "edit dependencies"),
		),
		// View operations
		ToggleColumns: key.NewBinding(
			key.WithKeys("T"),
			key.WithHelp("T", "toggle columns"),
		),
		ViewLogs: key.NewBinding(
			key.WithKeys("l"),
			key.WithHelp("l", "view logs"),
		),
		ViewFrontmatter: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "view frontmatter"),
		),
		ViewBriefing: key.NewBinding(
			key.WithKeys("b"),
			key.WithHelp("b", "view briefing"),
		),
		ViewEdit: key.NewBinding(
			key.WithKeys("m", "p"),
			key.WithHelp("m/p", "preview markdown"),
		),
		CycleDetailPane: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "toggle detail pane"),
		),
		CloseDetailPane: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close detail pane"),
		),
		SwitchFocus: key.NewBinding(
			key.WithKeys("tab", "shift+tab"),
			key.WithHelp("tab/shift+tab", "switch focus"),
		),
		ToggleLayout: key.NewBinding(
			key.WithKeys("V"),
			key.WithHelp("V", "toggle layout"),
		),
		ToggleFullscreen: key.NewBinding(
			key.WithKeys("z"),
			key.WithHelp("z", "fullscreen logs"),
		),
	}

	// Apply TUI-specific overrides from config (uses reflection to map all bindings)
	if cfg != nil && cfg.TUI != nil && cfg.TUI.Keybindings != nil {
		if flowOverrides, ok := cfg.TUI.Keybindings.Overrides["flow"]; ok {
			if overrides, ok := flowOverrides["status"]; ok {
				keymap.ApplyOverrides(&km, overrides)
			}
		}
	}

	return km
}

func (k KeyMap) ShortHelp() []key.Binding {
	// Return just quit - help is shown automatically by the help component
	return []key.Binding{k.Quit}
}

// Sections returns all keybinding sections for the flow status TUI.
// It includes the base sections plus flow-specific sections.
func (k KeyMap) Sections() []keymap.Section {
	return []keymap.Section{
		{
			Name:     "Navigation",
			Bindings: []key.Binding{k.Up, k.Down, k.Top, k.Bottom, k.PageUp, k.PageDown},
		},
		{
			Name:     "Selection",
			Bindings: []key.Binding{k.Select, k.SelectAll, k.SelectNone},
		},
		{
			Name: "Views",
			Bindings: []key.Binding{
				k.SwitchView, k.ToggleColumns, k.ViewLogs, k.ViewFrontmatter,
				k.ViewBriefing, k.ViewEdit, k.TogglePreview, k.CycleDetailPane,
				k.CloseDetailPane, k.SwitchFocus, k.ToggleLayout, k.ToggleFullscreen,
			},
		},
		{
			Name: "Actions",
			Bindings: []key.Binding{
				k.Run, k.Edit, k.SetCompleted, k.SetStatus, k.SetType, k.SetTemplate,
				k.AddJob, k.AddFromRecipe, k.AddXmlPlan, k.Implement, k.Rename,
				k.Resume, k.EditDeps, k.Archive, k.CopyPath, k.Help, k.Quit,
			},
		},
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
