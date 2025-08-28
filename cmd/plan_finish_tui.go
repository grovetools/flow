package cmd

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fatih/color"
)

type finishTUIModel struct {
	planName  string
	items     []*cleanupItem
	cursor    int
	confirmed bool
	quitting  bool
}

func initialFinishTUIModel(planName string, items []*cleanupItem) finishTUIModel {
	// Find first available item for initial cursor position
	cursor := 0
	for i, item := range items {
		if item.IsAvailable {
			cursor = i
			break
		}
	}

	return finishTUIModel{
		planName:  planName,
		items:     items,
		cursor:    cursor,
		confirmed: false,
		quitting:  false,
	}
}

func (m finishTUIModel) Init() tea.Cmd {
	return nil
}

func (m finishTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit

		case "j", "down":
			// Move to next available item or end of list
			for i := m.cursor + 1; i < len(m.items); i++ {
				if m.items[i].IsAvailable {
					m.cursor = i
					break
				}
			}

		case "k", "up":
			// Move to previous available item or start of list  
			for i := m.cursor - 1; i >= 0; i-- {
				if m.items[i].IsAvailable {
					m.cursor = i
					break
				}
			}

		case " ": // Toggle selection
			if m.cursor < len(m.items) && m.items[m.cursor].IsAvailable {
				m.items[m.cursor].IsEnabled = !m.items[m.cursor].IsEnabled
			}

		case "a": // Select all available
			for _, item := range m.items {
				if item.IsAvailable {
					item.IsEnabled = true
				}
			}

		case "n": // Select none
			for _, item := range m.items {
				item.IsEnabled = false
			}

		case "enter":
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
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // Green
	} else if strings.Contains(status, "Exists") || strings.Contains(status, "Running") || strings.Contains(status, "Has links") {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // Yellow  
	} else if strings.Contains(status, "Has changes") || strings.Contains(status, "Checked out") || strings.Contains(status, "commits ahead") {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")) // Red
	} else if strings.Contains(plainStatus, "N/A") || strings.Contains(plainStatus, "Not found") || strings.Contains(plainStatus, "No links") {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // Dim
	}
	
	return lipgloss.NewStyle().Foreground(lipgloss.Color("252")) // Default
}

func (m finishTUIModel) View() string {
	if m.quitting {
		return "\nCleanup aborted.\n"
	}

	var b strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("63")).
		Padding(0, 2).
		MarginBottom(1).
		Align(lipgloss.Center).
		Width(95)

	planNameStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("219")).
		Background(lipgloss.Color("63"))

	header := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("63")).Render("🏁 Finishing Plan: ") +
		planNameStyle.Render(m.planName) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("63")).Render(" 🏁")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n\n")

	// Instructions
	instructionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("147")).
		MarginBottom(1).
		Bold(true)
	b.WriteString(instructionStyle.Render("Select cleanup actions to perform:"))
	b.WriteString("\n\n")

	// Styles
	focusedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	enabledCheckboxStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true) // Green
	disabledCheckboxStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	
	// List items with better spacing and alignment
	for i, item := range m.items {
		var line strings.Builder
		
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
	statusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("147")).
		MarginTop(1).
		MarginBottom(1)
	
	statusText := fmt.Sprintf("Selected: %d actions", selectedCount)
	b.WriteString("\n")
	b.WriteString(statusStyle.Render(statusText))

	// Help footer
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 0, 0, 0).
		MarginTop(1)

	helpText := "j/k ↑/↓: navigate • space: toggle • a: select all • n: select none • enter: confirm • q: quit"
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(helpText))

	return b.String()
}

func runFinishTUI(planName string, items []*cleanupItem) error {
	model := initialFinishTUIModel(planName, items)
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