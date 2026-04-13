package status

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/theme"
)

// ── Jobs Pane ──────────────────────────────────────────────────────────────

// JobsPaneModel is a thin rendering wrapper for the job table.
// status.Model pre-renders the table content and syncs it before View().
type JobsPaneModel struct {
	Content string // Pre-rendered table from renderTableView + scroll indicator
	Width   int
	Height  int
}

// NewJobsPaneModel creates a new empty jobs pane.
func NewJobsPaneModel() *JobsPaneModel {
	return &JobsPaneModel{}
}

func (m *JobsPaneModel) Init() tea.Cmd { return nil }

func (m *JobsPaneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wsm, ok := msg.(tea.WindowSizeMsg); ok {
		m.Width = wsm.Width
		m.Height = wsm.Height
	}
	return m, nil
}

func (m *JobsPaneModel) View() string {
	return m.Content
}

// ── Detail Pane ────────────────────────────────────────────────────────────

// DetailPaneModel is a thin rendering wrapper for the detail content area
// (logs, frontmatter, briefing, edit, skills). status.Model pre-renders the
// content and syncs it before View().
type DetailPaneModel struct {
	Header  string // Pre-rendered header line (job info, scroll position)
	Content string // Pre-rendered viewport/logviewer output
	Width   int
	Height  int
	Focused bool
}

// NewDetailPaneModel creates a new empty detail pane.
func NewDetailPaneModel() *DetailPaneModel {
	return &DetailPaneModel{}
}

func (m *DetailPaneModel) Init() tea.Cmd { return nil }

func (m *DetailPaneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wsm, ok := msg.(tea.WindowSizeMsg); ok {
		m.Width = wsm.Width
		m.Height = wsm.Height
	}
	return m, nil
}

func (m *DetailPaneModel) View() string {
	if m.Content == "" && m.Header == "" {
		return ""
	}
	w := m.Width
	if w < 1 {
		w = 1
	}
	dividerStyle := theme.DefaultTheme.Muted
	if m.Focused {
		dividerStyle = lipgloss.NewStyle().Foreground(theme.DefaultColors.Orange)
	}
	divider := dividerStyle.Render(strings.Repeat("─", w))
	return lipgloss.JoinVertical(lipgloss.Left, divider, m.Header, divider, m.Content)
}

// Focus implements panes.Focusable.
func (m *DetailPaneModel) Focus() tea.Cmd {
	m.Focused = true
	return nil
}

// Blur implements panes.Focusable.
func (m *DetailPaneModel) Blur() {
	m.Focused = false
}

// ── Input Pane ─────────────────────────────────────────────────────────────

// InputPaneModel is a thin rendering wrapper for the agent chat input area.
// It renders the status bar + text input box. status.Model pre-renders the
// content and syncs it before View().
type InputPaneModel struct {
	Content string // Pre-rendered status bar + input box
	Width   int
	Height  int
}

// NewInputPaneModel creates a new empty input pane.
func NewInputPaneModel() *InputPaneModel {
	return &InputPaneModel{}
}

func (m *InputPaneModel) Init() tea.Cmd { return nil }

func (m *InputPaneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wsm, ok := msg.(tea.WindowSizeMsg); ok {
		m.Width = wsm.Width
		m.Height = wsm.Height
	}
	return m, nil
}

func (m *InputPaneModel) View() string {
	return m.Content
}
