package cmd

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	"github.com/grovetools/core/tui/theme"
	geminimodels "github.com/grovetools/grove-gemini/pkg/models"
)

// Shared TUI types used by plan_init_tui (and historically plan_add_tui
// before it was extracted to flow/pkg/tui/wizards/add). The add wizard
// package now has its own private copies of item/itemDelegate; this
// file keeps the CLI-level init wizard compilable without pulling in
// the wizard package.

// item is a simple string list entry.
type item string

func (i item) FilterValue() string { return string(i) }

// itemDelegate renders item and modelItem entries in a list.Model.
type itemDelegate struct{}

func (d itemDelegate) Height() int                               { return 1 }
func (d itemDelegate) Spacing() int                              { return 0 }
func (d itemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	var str string
	cursor := "  "
	if index == m.Index() {
		cursor = theme.DefaultTheme.Highlight.Render(theme.IconArrow + " ")
	}

	switch i := listItem.(type) {
	case item:
		str = fmt.Sprintf("%s%s", cursor, i)
	case modelItem:
		str = fmt.Sprintf("%s%s", cursor, i.ID)
	default:
		return
	}

	fmt.Fprint(w, str)
}

// Model represents an LLM model option in the CLI plan-init TUI.
type Model struct {
	ID       string
	Provider string
	Note     string
}

// modelItem represents a model in the list.
type modelItem struct {
	Model
}

func (m modelItem) FilterValue() string { return m.ID }
func (m modelItem) Title() string       { return m.ID }
func (m modelItem) Description() string { return fmt.Sprintf("%s - %s", m.Provider, m.Note) }

// getAvailableModels returns the list of available LLM models
// (current, non-legacy only) used by the plan-init wizard's model
// picker.
func getAvailableModels() []Model {
	var models []Model

	// Add current Gemini models
	for _, m := range geminimodels.CurrentModels() {
		models = append(models, Model{
			ID:       m.ID,
			Provider: m.Provider,
			Note:     m.Note,
		})
	}

	// Add current Anthropic models (use alias if available for shorter display)
	for _, m := range anthropicmodels.CurrentModels() {
		id := m.ID
		if m.Alias != "" {
			id = m.Alias // Use shorter alias in TUI
		}
		models = append(models, Model{
			ID:       id,
			Provider: m.Provider,
			Note:     m.Note,
		})
	}

	return models
}
