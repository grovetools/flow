package status_tui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/mattsolo1/grove-core/tui/keymap"
)

type KeyMap struct {
	keymap.Base
	Select          key.Binding
	SelectAll       key.Binding
	SelectNone      key.Binding
	Archive         key.Binding
	AddXmlPlan      key.Binding
	Edit            key.Binding
	Run             key.Binding
	SetCompleted    key.Binding
	SetStatus       key.Binding
	AddJob          key.Binding
	Implement       key.Binding
	AgentFromChat   key.Binding
	Rename          key.Binding
	Resume          key.Binding
	EditDeps        key.Binding
	ToggleSummaries key.Binding
	ToggleView      key.Binding
	GoToTop         key.Binding
	GoToBottom      key.Binding
	PageUp            key.Binding
	PageDown          key.Binding
	ViewLogs          key.Binding
	CycleDetailPane   key.Binding
	SwitchFocus       key.Binding
	ToggleLayout      key.Binding
}

func NewKeyMap() KeyMap {
	return KeyMap{
		Base: keymap.NewBase(),
		Select: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle select"),
		),
		SelectAll: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "select all"),
		),
		SelectNone: key.NewBinding(
			key.WithKeys("N"),
			key.WithHelp("N", "deselect all"),
		),
		Archive: key.NewBinding(
			key.WithKeys("X"),
			key.WithHelp("X", "archive selected"),
		),
		AddXmlPlan: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "add XML plan job"),
		),
		Edit: key.NewBinding(
			key.WithKeys("e", "enter"),
			key.WithHelp("e/enter", "edit job"),
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
		AddJob: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "add job"),
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
			key.WithKeys("ctrl+r"),
			key.WithHelp("ctrl+r", "rename job"),
		),
		Resume: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "resume job"),
		),
		EditDeps: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "edit dependencies"),
		),
		ToggleSummaries: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "toggle summaries"),
		),
		ToggleView: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "toggle view"),
		),
		GoToTop: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("gg", "go to top"),
		),
		GoToBottom: key.NewBinding(
			key.WithKeys("G"),
			key.WithHelp("G", "go to bottom"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("ctrl+u"),
			key.WithHelp("ctrl+u", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "page down"),
		),
		ViewLogs: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "toggle log view"),
		),
		CycleDetailPane: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "cycle detail pane"),
		),
		SwitchFocus: key.NewBinding(
			key.WithKeys("tab", "shift+tab"),
			key.WithHelp("tab/shift+tab", "switch focus"),
		),
		ToggleLayout: key.NewBinding(
			key.WithKeys("V"),
			key.WithHelp("V", "toggle layout"),
		),
	}
}

func (k KeyMap) ShortHelp() []key.Binding {
	// Return just quit - help is shown automatically by the help component
	return []key.Binding{k.Quit}
}

func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Navigation")),
			k.Up,
			k.Down,
			k.GoToTop,
			k.GoToBottom,
			k.PageUp,
			k.PageDown,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Selection")),
			k.Select,
			k.SelectAll,
			k.SelectNone,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Views")),
			k.ToggleView,
			k.ToggleSummaries,
			k.CycleDetailPane,
			k.SwitchFocus,
			k.ToggleLayout,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Actions")),
			k.Run,
			k.Edit,
			k.SetCompleted,
			k.SetStatus,
			k.AddJob,
			k.AddXmlPlan,
			k.Implement,
			k.Rename,
			k.Resume,
			k.EditDeps,
			k.Archive,
			k.Help,
			k.Quit,
		},
	}
}
