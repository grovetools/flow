package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fatih/color"
	"github.com/mattsolo1/grove-core/tui/components/help"
	"github.com/mattsolo1/grove-core/tui/keymap"
	"github.com/mattsolo1/grove-core/tui/theme"
)

type finishTUIKeyMap struct {
	keymap.Base
	Toggle    key.Binding
	SelectAll key.Binding
	SelectNone key.Binding
	Confirm   key.Binding
}

func newFinishTUIKeyMap() finishTUIKeyMap {
	return finishTUIKeyMap{
		Base: keymap.NewBase(),
		Toggle: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle selection"),
		),
		SelectAll: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "select all"),
		),
		SelectNone: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "select none"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "confirm and proceed"),
		),
	}
}

func (k finishTUIKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Toggle, k.Confirm, k.Help, k.Quit}
}

func (k finishTUIKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Toggle, k.SelectAll, k.SelectNone},
		{k.Confirm, k.Help, k.Quit},
	}
}

type finishTUIModel struct {
	planName      string
	items         []*cleanupItem
	cursor        int
	confirmed     bool
	quitting      bool
	branchIsMerged bool // Whether the branch is already merged/rebased
	keyMap        finishTUIKeyMap
	help          help.Model
}

func initialFinishTUIModel(planName string, items []*cleanupItem, branchIsMerged bool) finishTUIModel {
	// Find first available item for initial cursor position
	cursor := 0
	for i, item := range items {
		if item.IsAvailable {
			cursor = i
			break
		}
	}

	return finishTUIModel{
		planName:       planName,
		items:          items,
		cursor:         cursor,
		confirmed:      false,
		quitting:       false,
		branchIsMerged: branchIsMerged,
		keyMap:         newFinishTUIKeyMap(),
		help:           help.New(newFinishTUIKeyMap()),
	}
}

func (m finishTUIModel) Init() tea.Cmd {
	return nil
}

func (m finishTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keyMap.Quit):
			m.quitting = true
			return m, tea.Quit
			
		case key.Matches(msg, m.keyMap.Help):
			m.help.ShowAll = !m.help.ShowAll

		case key.Matches(msg, m.keyMap.Down):
			// Move to next available item or end of list
			for i := m.cursor + 1; i < len(m.items); i++ {
				if m.items[i].IsAvailable {
					m.cursor = i
					break
				}
			}

		case key.Matches(msg, m.keyMap.Up):
			// Move to previous available item or start of list  
			for i := m.cursor - 1; i >= 0; i-- {
				if m.items[i].IsAvailable {
					m.cursor = i
					break
				}
			}

		case key.Matches(msg, m.keyMap.Toggle):
			if m.cursor < len(m.items) && m.items[m.cursor].IsAvailable {
				m.items[m.cursor].IsEnabled = !m.items[m.cursor].IsEnabled
			}

		case key.Matches(msg, m.keyMap.SelectAll):
			for _, item := range m.items {
				if item.IsAvailable {
					item.IsEnabled = true
				}
			}

		case key.Matches(msg, m.keyMap.SelectNone):
			for _, item := range m.items {
				item.IsEnabled = false
			}

		case key.Matches(msg, m.keyMap.Confirm):
			m.confirmed = true
			return m, tea.Quit
		}
	}

	return m, nil
}

// getStatusStyle returns the appropriate lipgloss style for a status string
func getStatusStyle(status string) lipgloss.Style {
	// Strip ANSI colors to get the plain text
	plainStatus := color.New().Sprint(status)
	
	// Check for specific status patterns and return appropriate colors
	if strings.Contains(status, "Already finished") || strings.Contains(status, "Available") {
		return theme.DefaultTheme.Success // Green
	} else if strings.Contains(status, "Exists") || strings.Contains(status, "Running") || strings.Contains(status, "Has links") {
		return theme.DefaultTheme.Warning // Yellow  
	} else if strings.Contains(status, "Has changes") || strings.Contains(status, "Checked out") || strings.Contains(status, "commits ahead") {
		return theme.DefaultTheme.Error // Red
	} else if strings.Contains(plainStatus, "N/A") || strings.Contains(plainStatus, "Not found") || strings.Contains(plainStatus, "No links") {
		return theme.DefaultTheme.Muted // Dim
	}
	
	return theme.DefaultTheme.Bold // Default
}

func (m finishTUIModel) View() string {
	if m.quitting {
		return "\nCleanup aborted.\n"
	}

	// Show help if active
	if m.help.ShowAll {
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Padding(1).Render("🏁 Plan Finish - Help"),
			m.help.View(),
		)
	}

	var b strings.Builder

	// Header
	headerStyle := theme.DefaultTheme.Header.
		Padding(0, 2).
		MarginBottom(1).
		Align(lipgloss.Center).
		Width(95)

	planNameStyle := theme.DefaultTheme.Selected.
		Background(theme.DefaultTheme.Header.GetBackground())

	header := theme.DefaultTheme.Header.Render("🏁 Finishing Plan: ") +
		planNameStyle.Render(m.planName) +
		theme.DefaultTheme.Header.Render(" 🏁")
	b.WriteString("  ") // Left padding
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n\n")

	// Instructions
	instructionStyle := theme.DefaultTheme.Bold.
		MarginBottom(1)
	b.WriteString("  ") // Left padding
	b.WriteString(instructionStyle.Render("Select cleanup actions to perform:"))
	b.WriteString("\n")
	
	// Add congratulatory message if branch is merged
	if m.branchIsMerged {
		congratsStyle := theme.DefaultTheme.Success.
			Bold(true).
			Padding(0, 1).
			Margin(0, 0, 1, 0)
		
		congratsMessage := "🎉 Branch successfully merged into main! 🎉"
		b.WriteString("\n")
		b.WriteString("  ") // Left padding
		b.WriteString(congratsStyle.Render(congratsMessage))
		b.WriteString("\n")
	}
	
	b.WriteString("\n")

	// Styles
	focusedStyle := theme.DefaultTheme.Selected
	dimStyle := theme.DefaultTheme.Muted
	enabledCheckboxStyle := theme.DefaultTheme.Success.Bold(true)
	disabledCheckboxStyle := theme.DefaultTheme.Muted
	
	// List items with better spacing and alignment
	for i, item := range m.items {
		var line strings.Builder
		
		// Left padding
		line.WriteString("  ")
		
		// Cursor indicator
		if m.cursor == i && item.IsAvailable {
			line.WriteString(focusedStyle.Render("▸ "))
		} else {
			line.WriteString("  ")
		}

		// Checkbox with better styling
		if item.IsEnabled {
			line.WriteString(enabledCheckboxStyle.Render("[✓] "))
		} else if item.IsAvailable {
			line.WriteString("[  ] ")
		} else {
			line.WriteString(disabledCheckboxStyle.Render("[  ] "))
		}

		// Item name with proper width
		nameWidth := 50
		itemName := item.Name
		if len(itemName) > nameWidth {
			itemName = itemName[:nameWidth-3] + "..."
		}
		
		// Format name with proper alignment
		nameFormatted := fmt.Sprintf("%-*s", nameWidth, itemName)
		
		// Status with appropriate color
		statusStyle := getStatusStyle(item.Status)
		statusFormatted := statusStyle.Render(fmt.Sprintf("(%s)", item.Status))

		// Apply styling based on focus and availability
		if m.cursor == i && item.IsAvailable {
			nameFormatted = focusedStyle.Render(nameFormatted)
		} else if !item.IsAvailable {
			nameFormatted = dimStyle.Render(nameFormatted)
		}

		line.WriteString(nameFormatted)
		line.WriteString(" ")
		line.WriteString(statusFormatted)

		b.WriteString(line.String())
		b.WriteString("\n")
	}

	// Count selected items
	selectedCount := 0
	for _, item := range m.items {
		if item.IsEnabled {
			selectedCount++
		}
	}

	// Status line
	statusStyle := theme.DefaultTheme.Bold.
		MarginTop(1).
		MarginBottom(1)
	
	statusText := fmt.Sprintf("Selected: %d actions", selectedCount)
	b.WriteString("\n")
	b.WriteString("  ") // Left padding
	b.WriteString(statusStyle.Render(statusText))

	// Help footer
	helpText := m.help.View()
	b.WriteString("\n")
	b.WriteString(helpText)

	return b.String()
}

func runFinishTUI(planName string, items []*cleanupItem, branchIsMerged bool) error {
	model := initialFinishTUIModel(planName, items, branchIsMerged)
	p := tea.NewProgram(model)

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("error running TUI: %w", err)
	}

	m := finalModel.(finishTUIModel)
	if !m.confirmed {
		return fmt.Errorf("user aborted")
	}

	return nil
}