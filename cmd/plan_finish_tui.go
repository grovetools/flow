package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fatih/color"
	"github.com/mattsolo1/grove-core/tui/components"
	"github.com/mattsolo1/grove-core/tui/components/help"
	"github.com/mattsolo1/grove-core/tui/keymap"
	"github.com/mattsolo1/grove-core/tui/theme"
)

type finishTUIKeyMap struct {
	keymap.Base
	Toggle     key.Binding
	SelectAll  key.Binding
	SelectNone key.Binding
	Confirm    key.Binding
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
	planName       string
	items          []*cleanupItem
	cursor         int
	confirmed      bool
	quitting       bool
	branchIsMerged bool // Whether the branch is already merged/rebased
	branchExists   bool // Whether the branch exists
	keyMap         finishTUIKeyMap
	help           help.Model
}

func initialFinishTUIModel(planName string, items []*cleanupItem, branchIsMerged bool, branchExists bool) finishTUIModel {
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
		branchExists:   branchExists,
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

// renderInlineDetails shows detailed repository status inline below the table
func (m finishTUIModel) renderInlineDetails(item *cleanupItem) string {
	var b strings.Builder

	// Group repos by status
	merged := []string{}
	needsMerge := []string{}
	needsRebase := []string{}
	notFound := []string{}

	for _, repo := range item.Details {
		switch repo.Status {
		case "merged":
			merged = append(merged, repo.Name)
		case "needs_merge":
			needsMerge = append(needsMerge, repo.Name)
		case "needs_rebase":
			needsRebase = append(needsRebase, repo.Name)
		case "not_found":
			notFound = append(notFound, repo.Name)
		}
	}

	if len(needsRebase) > 0 {
		b.WriteString(theme.DefaultTheme.Error.Bold(true).Render(fmt.Sprintf("Needs rebase (%d): ", len(needsRebase))))
		b.WriteString(theme.DefaultTheme.Error.Render(strings.Join(needsRebase, ", ")))
		b.WriteString("\n")
	}

	if len(needsMerge) > 0 {
		b.WriteString(theme.DefaultTheme.Warning.Bold(true).Render(fmt.Sprintf("Ready to merge (%d): ", len(needsMerge))))
		b.WriteString(theme.DefaultTheme.Warning.Render(strings.Join(needsMerge, ", ")))
		b.WriteString("\n")
	}

	if len(merged) > 0 {
		b.WriteString(theme.DefaultTheme.Success.Bold(true).Render(fmt.Sprintf("Merged (%d): ", len(merged))))
		b.WriteString(theme.DefaultTheme.Success.Render(strings.Join(merged, ", ")))
		b.WriteString("\n")
	}

	if len(notFound) > 0 {
		b.WriteString(theme.DefaultTheme.Muted.Bold(true).Render(fmt.Sprintf("Skipped (%d): ", len(notFound))))
		b.WriteString(theme.DefaultTheme.Muted.Render(strings.Join(notFound, ", ")))
		b.WriteString("\n")
	}

	return b.String()
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
	b.WriteString(components.RenderHeader("Finishing plan: " + m.planName))
	b.WriteString("\n")

	// Branch merge status
	if m.branchExists {
		if m.branchIsMerged {
			b.WriteString(theme.DefaultTheme.Success.Render("✓ Branch merged into main - safe to delete"))
		} else {
			b.WriteString(theme.DefaultTheme.Warning.Render("✗ Branch has commits not in main - review before deleting"))
		}
		b.WriteString("\n\n")
	} else {
		b.WriteString(theme.DefaultTheme.Muted.Render("Branch does not exist"))
		b.WriteString("\n\n")
	}

	// Styles
	focusedStyle := theme.DefaultTheme.Selected
	dimStyle := theme.DefaultTheme.Muted
	enabledCheckboxStyle := theme.DefaultTheme.Success.Bold(true)
	disabledCheckboxStyle := theme.DefaultTheme.Muted
	
	// List items
	for i, item := range m.items {
		var line strings.Builder

		// Cursor indicator
		if m.cursor == i && item.IsAvailable {
			line.WriteString(focusedStyle.Render("> "))
		} else {
			line.WriteString("  ")
		}

		// Checkbox
		if item.IsEnabled {
			line.WriteString(enabledCheckboxStyle.Render("[x] "))
		} else if item.IsAvailable {
			line.WriteString("[ ] ")
		} else {
			line.WriteString(disabledCheckboxStyle.Render("[ ] "))
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

	// Show detailed repo status if available (after the list of items)
	for _, item := range m.items {
		if len(item.Details) > 0 {
			b.WriteString("\n")
			b.WriteString(m.renderInlineDetails(item))
			break // Only one item has details currently
		}
	}

	// Count selected items
	selectedCount := 0
	for _, item := range m.items {
		if item.IsEnabled {
			selectedCount++
		}
	}

	// Help footer - minimal
	b.WriteString("\n")
	helpStyle := theme.DefaultTheme.Muted
	b.WriteString(helpStyle.Render("? help • q quit"))

	return b.String()
}

func runFinishTUI(planName string, items []*cleanupItem, branchIsMerged bool, branchExists bool) error {
	model := initialFinishTUIModel(planName, items, branchIsMerged, branchExists)
	p := tea.NewProgram(model, tea.WithAltScreen())

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