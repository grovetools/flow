package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/state"
	"github.com/mattsolo1/grove-core/tui/components"
	"github.com/mattsolo1/grove-core/tui/components/help"
	"github.com/mattsolo1/grove-core/tui/components/table"
	"github.com/mattsolo1/grove-core/tui/keymap"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Plan TUI command - interactive version of `flow plan list`
var planTUICmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch interactive TUI for browsing and managing plans",
	Long: `Launch an interactive TUI that provides a navigable view of all plans
in your plans directory, similar to 'flow plan list' but with interactive features.

Features:
- Navigate through all plans with keyboard (↑/↓, j/k)
- View plan status details (Enter key)
- Execute plan finish command (Ctrl+X)
- Real-time plan list display`,
	Args: cobra.NoArgs,
	RunE: runPlanTUI,
}

// PlanListItem represents a plan in the TUI list
type PlanListItem struct {
	Plan        *orchestration.Plan
	Name        string
	JobCount    int
	Status      string
	StatusParts map[string]int  // For detailed status breakdown
	LastUpdated time.Time       // When the plan was last modified
	Worktree     string          // Worktree associated with the plan
	GitStatus    *git.StatusInfo // Git status information for the worktree
	ReviewStatus string          // Review status like "In Progress"
	Notes        string          // User notes/description
}

// planListTUIModel represents the TUI state
type planListTUIModel struct {
	plans          []PlanListItem
	cursor         int
	width          int
	height         int
	err            error
	loading        bool
	plansDirectory string
	statusMessage  string
	help           help.Model
	keys           planListKeyMap
	activePlan     string
	editingNotes   bool
	notesInput     textinput.Model
	editPlanIndex  int
	showGitLog     bool   // To toggle the view
	gitLogContent  string // To store the git log output
	gitLogError    error  // To store any errors
	showOnHold     bool   // Whether to show on-hold plans
}

// TUI key mappings for plan list
type planListKeyMap struct {
	keymap.Base
	Up              key.Binding
	Down            key.Binding
	ViewPlan        key.Binding
	OpenPlan        key.Binding
	FinishPlan      key.Binding
	NewPlan         key.Binding
	SetActive       key.Binding
	ReviewPlan      key.Binding
	EditNotes       key.Binding
	FastForwardMain key.Binding
	ToggleGitLog    key.Binding
	ToggleHold      key.Binding
	SetHoldStatus   key.Binding
}

func (k planListKeyMap) ShortHelp() []key.Binding {
	// Return just the quit binding for the short help view.
	return []key.Binding{k.Quit}
}

func (k planListKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Navigation")),
			k.Up,
			k.Down,
			k.ViewPlan,
			k.OpenPlan,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Actions")),
			k.NewPlan,
			k.SetActive,
			k.EditNotes,
			k.ReviewPlan,
			k.FinishPlan,
			k.SetHoldStatus,
			k.FastForwardMain,
			k.ToggleGitLog,
			k.ToggleHold,
			k.Help,
			k.Quit,
		},
	}
}

var planListKeys = planListKeyMap{
	Base: keymap.NewBase(),
	Up: key.NewBinding(
		key.WithKeys("k", "up"),
		key.WithHelp("k/↑", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("j", "down"),
		key.WithHelp("j/↓", "move down"),
	),
	ViewPlan: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "view plan details"),
	),
	OpenPlan: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "open plan workspace"),
	),
	FinishPlan: key.NewBinding(
		key.WithKeys("ctrl+x"),
		key.WithHelp("ctrl+x", "finish plan"),
	),
	NewPlan: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "create new plan"),
	),
	SetActive: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "set active plan"),
	),
	ReviewPlan: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "review changes"),
	),
	EditNotes: key.NewBinding(
		key.WithKeys("e"),
		key.WithHelp("e", "edit notes"),
	),
	FastForwardMain: key.NewBinding(
		key.WithKeys("M"),
		key.WithHelp("M", "merge to main"),
	),
	ToggleGitLog: key.NewBinding(
		key.WithKeys("g"),
		key.WithHelp("g", "toggle git log"),
	),
	ToggleHold: key.NewBinding(
		key.WithKeys("H"),
		key.WithHelp("H", "toggle on-hold"),
	),
	SetHoldStatus: key.NewBinding(
		key.WithKeys("ctrl+h"),
		key.WithHelp("ctrl+h", "hold/unhold plan"),
	),
}


// Messages for the plan list TUI
type planListLoadCompleteMsg struct {
	plans []PlanListItem
	error error
}

// gitLogMsg is sent when the git log fetch is complete
type gitLogMsg struct {
	content string
	err     error
}

// fastForwardMsg is sent when the git fast-forward operation is complete
type fastForwardMsg struct {
	err     error
	message string
}

func fetchGitLogCmd(plansDir string) tea.Cmd {
	return func() tea.Msg {
		gitRoot, err := git.GetGitRoot(plansDir)
		if err != nil {
			// Try CWD as a fallback
			gitRoot, err = git.GetGitRoot(".")
			if err != nil {
				return gitLogMsg{err: fmt.Errorf("not in a git repository: %w", err)}
			}
		}

		// Using --color=always ensures that the color codes are captured in the output for raw rendering
		cmd := exec.Command("git", "log", "--oneline", "--decorate", "--color=always", "--graph", "--all", "--max-count=20")
		cmd.Dir = gitRoot

		output, err := cmd.CombinedOutput()
		if err != nil {
			return gitLogMsg{err: fmt.Errorf("git log failed: %w: %s", err, string(output))}
		}

		return gitLogMsg{content: string(output)}
	}
}

func runPlanTUI(cmd *cobra.Command, args []string) error {
	// Check for TTY
	if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("TUI mode requires an interactive terminal")
	}

	// Get plans directory using NotebookLocator
	node, err := workspace.GetProjectByPath(".")
	if err != nil {
		return fmt.Errorf("could not determine current workspace: %w", err)
	}

	coreCfg, err := config.LoadDefault()
	if err != nil {
		coreCfg = &config.Config{}
	}
	locator := workspace.NewNotebookLocator(coreCfg)

	// Check for deprecated config
	flowCfg, _ := loadFlowConfig()
	if flowCfg != nil && flowCfg.PlansDirectory != "" {
		fmt.Fprintln(os.Stderr, "⚠️  Warning: The 'flow.plans_directory' config is deprecated. Please configure 'notebook.root_dir' in your global grove.yml instead.")
	}

	plansDirectory, err := locator.GetPlansDir(node)
	if err != nil {
		return fmt.Errorf("could not resolve plans directory: %w", err)
	}

	// Create and run TUI
	model := newPlanListTUIModel(plansDirectory)
	program := tea.NewProgram(model, tea.WithAltScreen())

	finalModel, err := program.Run() // Capture the final model
	if err != nil {
		return fmt.Errorf("error running plan list TUI: %w", err)
	}

	// If the TUI exited on the 'new plan' screen, it means the user created a plan.
	// We need to process it.
	if m, ok := finalModel.(planInitTUIModel); ok {
		// User submitted the 'new plan' form, and didn't just quit.
		if !m.quitting && m.nameInput.Value() != "" {
			finalCmd := m.toPlanInitCmd()
			// Execute the plan creation using the same logic as `plan init`
			return RunPlanInit(finalCmd)
		}
	}

	// Otherwise, the user just quit from the list view, so we do nothing.
	return nil
}

func newPlanListTUIModel(plansDirectory string) planListTUIModel {
	helpModel := help.NewBuilder().
		WithKeys(planListKeys).
		WithTitle("Plan List - Help").
		Build()

	// Load active plan
	activePlan, _ := state.GetString("flow.active_plan")

	return planListTUIModel{
		plans:          []PlanListItem{},
		cursor:         0,
		loading:        true,
		plansDirectory: plansDirectory,
		help:           helpModel,
		keys:           planListKeys,
		activePlan:     activePlan,
		showGitLog:     false, // Off by default
	}
}

func (m planListTUIModel) Init() tea.Cmd {
	return tea.Batch(
		loadPlansListCmd(m.plansDirectory, m.showOnHold),
		fetchGitLogCmd(m.plansDirectory),
		refreshTick(),
	)
}

func (m planListTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case childExitedMsg:
		// Child TUI (status) exited, likely due to edit action in Neovim
		// Quit this parent TUI too
		return m, tea.Quit

	case fastForwardMsg:
		if msg.err != nil {
			m.statusMessage = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %s", msg.err.Error()))
		} else {
			m.statusMessage = theme.DefaultTheme.Success.Render(fmt.Sprintf("✓ %s", msg.message))
		}
		return m, nil

	case gitLogMsg:
		m.gitLogContent = msg.content
		m.gitLogError = msg.err
		return m, nil

	case planListLoadCompleteMsg:
		m.loading = false
		if msg.error != nil {
			m.err = msg.error
			return m, nil
		}
		m.plans = msg.plans
		// Reload active plan as well
		activePlan, _ := state.GetString("flow.active_plan")
		m.activePlan = activePlan
		// Adjust cursor if needed
		if m.cursor >= len(m.plans) {
			m.cursor = len(m.plans) - 1
		}
		if m.cursor < 0 && len(m.plans) > 0 {
			m.cursor = 0
		}
		return m, nil

	case refreshTickMsg:
		return m, tea.Batch(
			loadPlansListCmd(m.plansDirectory, m.showOnHold),
			fetchGitLogCmd(m.plansDirectory),
			refreshTick(),
		)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.SetSize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		// If help is visible, only allow closing it - no other actions
		if m.help.ShowAll {
			// Any key closes help
			m.help.ShowAll = false
			return m, nil
		}

		// If editing notes, handle input
		if m.editingNotes {
			switch msg.String() {
			case "enter":
				// Save notes
				if m.editPlanIndex >= 0 && m.editPlanIndex < len(m.plans) {
					plan := m.plans[m.editPlanIndex].Plan
					newNotes := m.notesInput.Value()

					// Update the config
					if plan.Config == nil {
						plan.Config = &orchestration.PlanConfig{}
					}
					plan.Config.Notes = newNotes

					// Save to .grove-plan.yml
					configPath := filepath.Join(plan.Directory, ".grove-plan.yml")
					data, err := yaml.Marshal(plan.Config)
					if err == nil {
						os.WriteFile(configPath, data, 0644)
					}

					// Update local model
					m.plans[m.editPlanIndex].Notes = newNotes
				}
				m.editingNotes = false
				return m, nil

			case "esc":
				// Cancel editing
				m.editingNotes = false
				return m, nil

			default:
				// Update the input
				var cmd tea.Cmd
				m.notesInput, cmd = m.notesInput.Update(msg)
				return m, cmd
			}
		}

		// Regular key handling
		switch {
		case key.Matches(msg, m.keys.Quit), msg.String() == "ctrl+c":
			return m, tea.Quit

		case key.Matches(msg, m.keys.Help):
			m.help.Toggle()
			return m, nil

		case key.Matches(msg, m.keys.NewPlan):
			// Create a new plan init TUI, which will take over.
			// It knows how to return to this list view when the user quits.
			return newPlanInitTUIModel(m.plansDirectory, &PlanInitCmd{}), nil

		case key.Matches(msg, m.keys.Up):
			m.statusMessage = "" // Clear status on navigation
			if m.cursor > 0 {
				m.cursor--
			}

		case key.Matches(msg, m.keys.Down):
			m.statusMessage = "" // Clear status on navigation
			if m.cursor < len(m.plans)-1 {
				m.cursor++
			}

		case key.Matches(msg, m.keys.ViewPlan):
			// Enter key - view plan status TUI
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				plan := m.plans[m.cursor]
				return m, openPlanStatusTUI(plan.Plan)
			}

		case key.Matches(msg, m.keys.OpenPlan):
			// O key - open plan workspace
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				plan := m.plans[m.cursor]
				return m, executePlanOpen(plan.Plan)
			}

		case key.Matches(msg, m.keys.SetActive):
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				selectedPlan := m.plans[m.cursor]
				if err := state.Set("flow.active_plan", selectedPlan.Name); err == nil {
					m.activePlan = selectedPlan.Name
				}
			}

		case key.Matches(msg, m.keys.ReviewPlan):
			// R key - review plan changes
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				plan := m.plans[m.cursor]
				if plan.Worktree != "" {
					return m, executePlanReview(plan.Plan)
				}
				m.statusMessage = "No worktree to review for this plan."
			}

		case key.Matches(msg, m.keys.EditNotes):
			// E key - edit plan notes
			if m.cursor >= 0 && m.cursor < len(m.plans) && !m.editingNotes {
				// Enter edit mode
				m.editingNotes = true
				m.editPlanIndex = m.cursor
				ti := textinput.New()
				ti.Placeholder = "Enter notes for this plan..."
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				// Set initial value from existing notes
				if m.plans[m.cursor].Notes != "" {
					ti.SetValue(m.plans[m.cursor].Notes)
				}
				m.notesInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.keys.FinishPlan):
			// Ctrl+X key - execute plan finish command
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				plan := m.plans[m.cursor]
				return m, executePlanFinish(plan.Plan)
			}

		case key.Matches(msg, m.keys.FastForwardMain):
			// M key - rebase worktree on main, then fast-forward main
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				selectedPlan := m.plans[m.cursor]
				m.statusMessage = "Rebasing and merging to main..."
				return m, fastForwardMainCmd(selectedPlan)
			}

		case key.Matches(msg, m.keys.ToggleGitLog):
			m.showGitLog = !m.showGitLog
			m.statusMessage = "" // Clear status message when toggling
			return m, nil

		case key.Matches(msg, m.keys.ToggleHold):
			m.showOnHold = !m.showOnHold
			m.cursor = 0 // Reset cursor to top
			m.statusMessage = fmt.Sprintf("On-hold plans: %v", m.showOnHold)
			return m, loadPlansListCmd(m.plansDirectory, m.showOnHold)

		case key.Matches(msg, m.keys.SetHoldStatus):
			// Toggle hold status for the selected plan
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				selectedPlan := m.plans[m.cursor]
				planPath := filepath.Join(m.plansDirectory, selectedPlan.Name)

				// Check current status
				currentStatus := ""
				if selectedPlan.Plan.Config != nil {
					currentStatus = selectedPlan.Plan.Config.Status
				}

				// Toggle: if currently "hold", remove it; otherwise set to "hold"
				var newStatus string
				var action string
				if currentStatus == "hold" {
					newStatus = "" // Remove hold status
					action = "removed from"
				} else {
					newStatus = "hold"
					action = "set to"
				}

				// Update the config
				configCmd := &PlanConfigCmd{
					Dir: planPath,
					Set: []string{fmt.Sprintf("status=%s", newStatus)},
				}
				if err := RunPlanConfig(configCmd); err != nil {
					m.statusMessage = fmt.Sprintf("Failed to update plan: %v", err)
				} else {
					m.statusMessage = fmt.Sprintf("Plan '%s' %s hold", selectedPlan.Name, action)
				}

				// Reload the plans list to reflect the change
				return m, loadPlansListCmd(m.plansDirectory, m.showOnHold)
			}
		}
	}

	return m, nil
}

func (m planListTUIModel) View() string {
	if m.loading {
		return "Loading plans...\n"
	}

	if m.err != nil {
		return theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v\n", m.err))
	}

	var s strings.Builder

	// If help is visible, show it and return
	if m.help.ShowAll {
		return m.help.View()
	}

	// If editing notes, show the input field
	if m.editingNotes {
		s.WriteString(components.RenderHeader("Edit Plan Notes"))
		s.WriteString("\n\n")
		if m.editPlanIndex >= 0 && m.editPlanIndex < len(m.plans) {
			s.WriteString(theme.DefaultTheme.Muted.Render("Plan: "))
			s.WriteString(m.plans[m.editPlanIndex].Name)
			s.WriteString("\n\n")
		}
		s.WriteString(m.notesInput.View())
		s.WriteString("\n\n")
		s.WriteString(theme.DefaultTheme.Muted.Render("Press Enter to save, Esc to cancel"))
		return s.String()
	}

	// Header with emoji and count like grove ws plans list
	planCount := len(m.plans)
	headerText := fmt.Sprintf("📋 Flow Plans (%d total)", planCount)
	s.WriteString(components.RenderHeader(headerText))
	s.WriteString("\n\n")

	if len(m.plans) == 0 {
		s.WriteString("No plans found in directory.\n")
		s.WriteString("\n")
		s.WriteString(m.help.View())
		return s.String()
	}

	// Render table using grove-core table component
	table := m.renderPlanTable()

	// Help text
	help := m.help.View()

	// Conditionally render and combine views
	if m.showGitLog {
		logPane := m.renderGitLogPane()
		// Using JoinVertical to stack the elements
		mainContent := lipgloss.JoinVertical(lipgloss.Left,
			table,
			"\n",
			theme.DefaultTheme.Bold.Render("Git Repository Log"),
			logPane,
		)
		s.WriteString(mainContent)
	} else {
		s.WriteString(table)
	}

	s.WriteString("\n")
	s.WriteString(help)

	// Display status message at the bottom if any
	if m.statusMessage != "" {
		s.WriteString("\n\n")
		s.WriteString(theme.DefaultTheme.Success.Render(m.statusMessage))
	}

	return s.String()
}

// renderGitLogPane renders the git log pane
func (m planListTUIModel) renderGitLogPane() string {
	var content string
	if m.gitLogError != nil {
		content = theme.DefaultTheme.Error.Render(m.gitLogError.Error())
	} else {
		// The git log output already contains ANSI color codes, so we just render the raw string.
		content = m.gitLogContent
	}

	// Use a lipgloss box to frame the content
	boxStyle := theme.DefaultTheme.Box.Copy().Padding(0, 1)
	return boxStyle.Render(content)
}

func (m planListTUIModel) renderPlanTable() string {
	if len(m.plans) == 0 {
		return ""
	}

	// Prepare headers like grove ws plans list
	headers := []string{"TITLE", "STATUS", "WORKTREE", "GIT", "REVIEW", "NOTES", "UPDATED"}

	// Prepare rows with emoji status indicators and formatting
	rows := make([][]string, len(m.plans))
	for i, plan := range m.plans {
		// Generate emoji status like the example
		statusText := m.formatStatusWithEmoji(plan)

		// Format last updated with relative time
		updatedText := theme.DefaultTheme.Muted.Render("◦ " + formatRelativeTime(plan.LastUpdated))

		// Add active plan indicator - use bold for emphasis without explicit color
		titleText := plan.Name
		if plan.Name == m.activePlan {
			titleText = theme.DefaultTheme.Bold.Render(fmt.Sprintf("▶ %s", titleText))
		}

		// Format worktree text
		worktreeText := plan.Worktree
		if worktreeText == "" {
			worktreeText = theme.DefaultTheme.Muted.Render("-")
		}

		// Format git status text
		var gitText string
		if plan.GitStatus != nil {
			gs := plan.GitStatus
			var parts []string

			if gs.IsDirty {
				parts = append(parts, theme.DefaultTheme.Warning.Render("● Dirty"))
			} else {
				parts = append(parts, theme.DefaultTheme.Success.Render("✓ Clean"))
			}

			if gs.AheadCount > 0 {
				parts = append(parts, theme.DefaultTheme.Success.Render(fmt.Sprintf("↑%d", gs.AheadCount)))
			}
			if gs.BehindCount > 0 {
				parts = append(parts, theme.DefaultTheme.Error.Render(fmt.Sprintf("↓%d", gs.BehindCount)))
			}

			gitText = strings.Join(parts, " ")
		} else {
			gitText = theme.DefaultTheme.Muted.Render("-")
		}

		// Format notes text (truncate if too long)
		notesText := plan.Notes
		if notesText == "" {
			notesText = theme.DefaultTheme.Muted.Render("-")
		} else if len(notesText) > 30 {
			notesText = notesText[:27] + "..."
		}

		// Format review status text
		var reviewText string
		switch plan.ReviewStatus {
		case "Pending":
			reviewText = theme.DefaultTheme.Info.Render("Pending")
		case "In Progress":
			reviewText = theme.DefaultTheme.Warning.Render("In Progress")
		case "Not Started":
			reviewText = theme.DefaultTheme.Muted.Render("Not Started")
		default:
			reviewText = theme.DefaultTheme.Muted.Render("-")
		}

		rows[i] = []string{
			titleText,
			statusText,
			worktreeText,
			gitText,
			reviewText,
			notesText,
			updatedText,
		}
	}

	// Use SelectableTable to handle cursor highlighting
	// Note: We pass m.cursor directly; the SelectableTable function handles the header offset
	return table.SelectableTable(headers, rows, m.cursor)
}

// formatStatusWithEmoji formats the status text with emoji indicators like grove ws plans list
func (m planListTUIModel) formatStatusWithEmoji(plan PlanListItem) string {
	if plan.StatusParts == nil || len(plan.StatusParts) == 0 {
		return "⏳ no jobs"
	}

	// Count different status types
	completed := plan.StatusParts["completed"]
	running := plan.StatusParts["running"]
	pending := plan.StatusParts["pending"]
	failed := plan.StatusParts["failed"]
	blocked := plan.StatusParts["blocked"]
	hold := plan.StatusParts["hold"]
	abandoned := plan.StatusParts["abandoned"]

	// Build status string with emojis
	var parts []string
	var emoji string

	// Determine primary emoji based on dominant status
	if failed > 0 || blocked > 0 || abandoned > 0 {
		emoji = "❌"
	} else if running > 0 {
		if completed > 0 || pending > 0 {
			emoji = "🚧" // mixed with running
		} else {
			emoji = "⚡" // only running
		}
	} else if completed > 0 && pending > 0 {
		emoji = "🚧" // mixed completed and pending
	} else if hold > 0 {
		emoji = theme.IconStatusHold // on hold
	} else if completed > 0 {
		emoji = "✓" // all completed
	} else {
		emoji = "⏳" // only pending
	}

	// Add count details
	if completed > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", completed))
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	if hold > 0 {
		parts = append(parts, fmt.Sprintf("%d on hold", hold))
	}

	statusText := emoji + " " + strings.Join(parts, ", ")
	
	// Apply color based on primary status
	if failed > 0 || blocked > 0 {
		return theme.DefaultTheme.Error.Render(statusText)
	} else if running > 0 {
		return theme.DefaultTheme.Warning.Render(statusText)
	} else if completed > 0 {
		return theme.DefaultTheme.Success.Render(statusText)
	} else {
		return theme.DefaultTheme.Info.Render(statusText)
	}
}

// formatRelativeTime formats a time as a relative string like "2 hours ago"
func formatRelativeTime(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		minutes := int(diff.Minutes())
		if minutes == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", minutes)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	case diff < 30*24*time.Hour:
		weeks := int(diff.Hours() / (24 * 7))
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	default:
		months := int(diff.Hours() / (24 * 30))
		if months == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	}
}

// Helper functions
func loadPlansListCmd(plansDirectory string, showOnHold bool) tea.Cmd {
	return func() tea.Msg {
		plans, err := loadPlansList(plansDirectory, showOnHold)
		return planListLoadCompleteMsg{
			plans: plans,
			error: err,
		}
	}
}

func loadPlansList(plansDirectory string, showOnHold bool) ([]PlanListItem, error) {
	entries, err := os.ReadDir(plansDirectory)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory %s: %w", plansDirectory, err)
	}

	var items []PlanListItem
	for _, entry := range entries {
		if entry.IsDir() {
			planPath := filepath.Join(plansDirectory, entry.Name())
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
			mdFiles, _ := filepath.Glob(filepath.Join(planPath, "*.md"))

			// A directory is considered a plan if it has a .grove-plan.yml file or contains .md files
			if _, err := os.Stat(planConfigPath); err == nil || len(mdFiles) > 0 {
				plan, err := orchestration.LoadPlan(planPath)
				if err == nil {
					// Filter out finished plans
					if plan.Config != nil && plan.Config.Status == "finished" {
						continue
					}
					// Filter out on-hold plans unless explicitly shown
					if !showOnHold && plan.Config != nil && plan.Config.Status == "hold" {
						continue
					}
					// Get last modification time of the plan directory
					planInfo, err := os.Stat(planPath)
					var lastUpdated time.Time
					if err == nil {
						lastUpdated = planInfo.ModTime()
					} else {
						lastUpdated = time.Now() // fallback to current time
					}

					// Get worktree and notes from plan config
					worktree := ""
					notes := ""
					if plan.Config != nil {
						worktree = plan.Config.Worktree
						notes = plan.Config.Notes
					}

					item := PlanListItem{
						Plan:        plan,
						Name:        plan.Name,
						JobCount:    len(plan.Jobs),
						LastUpdated: lastUpdated,
						Worktree:    worktree,
						Notes:       notes,
					}

					// Fetch git status if this plan has a worktree
					if worktree != "" {
						// Find the git root by getting the project/workspace for this plan
						project, err := workspace.GetProjectByPath(planPath)
						var gitRoot string
						if err == nil && project != nil {
							// Use the project's path as the git root
							gitRoot = project.Path
						}

						// Fallback to trying git.GetGitRoot on plan path
						if gitRoot == "" {
							gitRoot, _ = git.GetGitRoot(planPath)
						}

						// Final fallback to specialized grove-ecosystem logic
						if gitRoot == "" {
							gitRoot = findGitRootForWorktree(planPath, worktree)
						}

						if gitRoot != "" {
							worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktree)
							if _, statErr := os.Stat(worktreePath); statErr == nil {
								gitStatus, statusErr := git.GetStatus(worktreePath)
								if statusErr == nil {
									// Override ahead/behind counts to compare against local main, not upstream
									gitStatus.AheadCount = getCommitCount(worktreePath, "main..HEAD")
									gitStatus.BehindCount = getCommitCount(worktreePath, "HEAD..main")
									item.GitStatus = gitStatus
									// Populate review status
									item.ReviewStatus = getReviewStatus(gitStatus, worktreePath)
								}
							}
						}
					}

					// Calculate status summary
					statusCounts := make(map[orchestration.JobStatus]int)
					for _, job := range plan.Jobs {
						statusCounts[job.Status]++
					}

					// Build status parts for detailed breakdown
					statusParts := make(map[string]int)
					var statusStrParts []string

					if c := statusCounts[orchestration.JobStatusCompleted]; c > 0 {
						statusStrParts = append(statusStrParts, fmt.Sprintf("%d completed", c))
						statusParts["completed"] = c
					}
					if c := statusCounts[orchestration.JobStatusRunning]; c > 0 {
						statusStrParts = append(statusStrParts, fmt.Sprintf("%d running", c))
						statusParts["running"] = c
					}
					if c := statusCounts[orchestration.JobStatusPending] + statusCounts[orchestration.JobStatusPendingUser] + statusCounts[orchestration.JobStatusTodo]; c > 0 {
						statusStrParts = append(statusStrParts, fmt.Sprintf("%d pending", c))
						statusParts["pending"] = c
					}
					if c := statusCounts[orchestration.JobStatusFailed]; c > 0 {
						statusStrParts = append(statusStrParts, fmt.Sprintf("%d failed", c))
						statusParts["failed"] = c
					}
					if c := statusCounts[orchestration.JobStatusBlocked]; c > 0 {
						statusStrParts = append(statusStrParts, fmt.Sprintf("%d blocked", c))
						statusParts["blocked"] = c
					}
					if c := statusCounts[orchestration.JobStatusHold]; c > 0 {
						statusStrParts = append(statusStrParts, fmt.Sprintf("%d on hold", c))
						statusParts["hold"] = c
					}
					if c := statusCounts[orchestration.JobStatusAbandoned]; c > 0 {
						statusStrParts = append(statusStrParts, fmt.Sprintf("%d abandoned", c))
						statusParts["abandoned"] = c
					}

					item.StatusParts = statusParts
					if len(statusStrParts) > 0 {
						item.Status = strings.Join(statusStrParts, ", ")
					} else {
						item.Status = "no jobs"
					}

					items = append(items, item)
				}
			}
		}
	}

	// Sort plans by most recent first (descending by LastUpdated)
	sort.Slice(items, func(i, j int) bool {
		return items[i].LastUpdated.After(items[j].LastUpdated)
	})

	return items, nil
}

type childExitedMsg struct{}

// getReviewStatus determines the review status based on Git state
func getReviewStatus(gitStatus *git.StatusInfo, worktreePath string) string {
	if gitStatus == nil {
		return "-" // No worktree associated
	}

	// Check if an upstream branch is configured (indicates branch has been pushed)
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "@{u}")
	cmd.Dir = worktreePath
	if err := cmd.Run(); err == nil {
		return "Pending"
	}

	// Check for local commits or uncommitted changes
	if gitStatus.AheadCount > 0 || gitStatus.IsDirty {
		return "In Progress"
	}

	return "Not Started"
}

// getCommitCount returns the number of commits in a git rev-list range
func getCommitCount(repoPath, revRange string) int {
	cmd := exec.Command("git", "rev-list", "--count", revRange)
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	count := 0
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &count)
	return count
}

// findGitRootForWorktree attempts to find the git root directory for a worktree
// by using workspace discovery to locate the repository
func findGitRootForWorktree(planPath, worktreeName string) string {
	// Get the workspace node for the plan - this will find which notebook/project it belongs to
	project, err := workspace.GetProjectByPath(planPath)
	if err == nil && project != nil {
		// Check if the worktree exists at project path
		worktreePath := filepath.Join(project.Path, ".grove-worktrees", worktreeName)
		if _, err := os.Stat(worktreePath); err == nil {
			return project.Path
		}

		// If project has a ParentProjectPath (it's a worktree itself), check there
		if project.ParentProjectPath != "" {
			worktreePath := filepath.Join(project.ParentProjectPath, ".grove-worktrees", worktreeName)
			if _, err := os.Stat(worktreePath); err == nil {
				return project.ParentProjectPath
			}
		}

		// If project is in an ecosystem, check the ecosystem root
		if project.RootEcosystemPath != "" {
			worktreePath := filepath.Join(project.RootEcosystemPath, ".grove-worktrees", worktreeName)
			if _, err := os.Stat(worktreePath); err == nil {
				return project.RootEcosystemPath
			}
		}
	}

	// Fallback: try to infer from plan path patterns
	// Pattern: ~/Documents/nb/workspaces/{workspace-name}/plans/{plan}
	parts := strings.Split(planPath, string(filepath.Separator))
	var workspaceName string
	for i, part := range parts {
		if part == "workspaces" && i+1 < len(parts) {
			workspaceName = parts[i+1]
			break
		} else if part == "repos" && i+1 < len(parts) {
			workspaceName = parts[i+1]
			break
		}
	}

	if workspaceName == "" {
		return ""
	}

	homeDir, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()

	// Build list of candidate git roots to check
	var candidates []string

	// Special case: if workspace is "grove-ecosystem", check the ecosystem root
	if workspaceName == "grove-ecosystem" {
		ecosystemPath := filepath.Join(homeDir, "Code", "grove-ecosystem")
		candidates = append(candidates, ecosystemPath)
	} else {
		// Look for the repo in common locations
		candidates = append(candidates,
			filepath.Join(homeDir, "Code", workspaceName),
			filepath.Join(homeDir, "Code", "grove-ecosystem", workspaceName),
			filepath.Join(homeDir, "Code", "grove-ecosystem", ".grove-worktrees", workspaceName),
		)
	}

	// Also check current directory patterns
	if strings.Contains(cwd, "grove-ecosystem") {
		ecosystemRoot := cwd[:strings.Index(cwd, "grove-ecosystem")+len("grove-ecosystem")]
		if workspaceName == "grove-ecosystem" {
			candidates = append(candidates, ecosystemRoot)
		} else {
			candidates = append(candidates,
				filepath.Join(ecosystemRoot, workspaceName),
				filepath.Join(ecosystemRoot, ".grove-worktrees", workspaceName),
			)
		}
	}

	// Check each candidate
	for _, candidate := range candidates {
		// Check if worktree exists at this location
		worktreePath := filepath.Join(candidate, ".grove-worktrees", worktreeName)
		if _, err := os.Stat(worktreePath); err == nil {
			return candidate
		}
	}

	return ""
}

func openPlanStatusTUI(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		// First set the active job programmatically
		func() tea.Msg {
			if err := state.Set("flow.active_plan", plan.Name); err != nil {
				return err
			}
			return nil
		},
		// Run status TUI directly - delegation through grove breaks interactive TUI
		tea.ExecProcess(exec.Command("flow", "plan", "status", "--tui"),
			func(err error) tea.Msg {
				// When in Neovim, quit the parent when child exits (for edit action)
				if os.Getenv("GROVE_NVIM_PLUGIN") == "true" {
					return childExitedMsg{}
				}
				return nil
			}),
	)
}

func executePlanFinish(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		// First set the active job programmatically
		func() tea.Msg {
			if err := state.Set("flow.active_plan", plan.Name); err != nil {
				return err
			}
			return nil
		},
		// Then run the finish command via 'grove' for workspace-awareness
		tea.ExecProcess(exec.Command("grove", "flow", "plan", "finish"),
			func(err error) tea.Msg {
				return nil
			}),
	)
}

func executePlanOpen(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		// First set the active job programmatically
		func() tea.Msg {
			if err := state.Set("flow.active_plan", plan.Name); err != nil {
				return err
			}
			return nil
		},
		// Then run the open command via 'grove' for workspace-awareness
		tea.ExecProcess(exec.Command("grove", "flow", "plan", "open"),
			func(err error) tea.Msg {
				// When plan open completes, stay in the TUI
				return nil
			}),
	)
}

func executePlanReview(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		// Set active plan for context
		func() tea.Msg {
			if err := state.Set("flow.active_plan", plan.Name); err != nil {
				return err
			}
			return nil
		},
		// Execute the review command
		tea.ExecProcess(exec.Command("grove", "flow", "plan", "review"),
			func(err error) tea.Msg {
				// After the command exits, the TUI will redraw.
				return nil
			}),
	)
}

func fastForwardMainCmd(plan PlanListItem) tea.Cmd {
	return func() tea.Msg {
		if plan.Worktree == "" {
			return fastForwardMsg{err: fmt.Errorf("selected plan has no associated worktree")}
		}

		gitRoot, err := git.GetGitRoot(".")
		if err != nil {
			return fastForwardMsg{err: fmt.Errorf("not in a git repository: %w", err)}
		}

		// Check current branch of the main repo
		_, currentBranch, err := git.GetRepoInfo(gitRoot)
		if err != nil {
			return fastForwardMsg{err: fmt.Errorf("could not determine current branch: %w", err)}
		}

		defaultBranch := "main"
		// check if main exists
		checkMainCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
		checkMainCmd.Dir = gitRoot
		if checkMainCmd.Run() != nil {
			// main doesn't exist, check master
			checkMasterCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/master")
			checkMasterCmd.Dir = gitRoot
			if checkMasterCmd.Run() == nil {
				defaultBranch = "master"
			} else {
				return fastForwardMsg{err: fmt.Errorf("neither 'main' nor 'master' branch found")}
			}
		}

		if currentBranch != defaultBranch {
			return fastForwardMsg{err: fmt.Errorf("must be on '%s' branch to fast-forward. current branch: '%s'", defaultBranch, currentBranch)}
		}

		worktreeBranch := plan.Worktree
		tmpBranch := fmt.Sprintf("tmp/ff-%s", worktreeBranch)

		// Create temp branch from worktree branch
		createCmd := exec.Command("git", "branch", tmpBranch, worktreeBranch)
		createCmd.Dir = gitRoot
		if output, err := createCmd.CombinedOutput(); err != nil {
			return fastForwardMsg{err: fmt.Errorf("failed to create temp branch: %s", strings.TrimSpace(string(output)))}
		}

		// Cleanup function to delete temp branch
		cleanup := func() {
			delCmd := exec.Command("git", "branch", "-D", tmpBranch)
			delCmd.Dir = gitRoot
			delCmd.Run() // Ignore errors on cleanup
		}
		defer cleanup()

		// Rebase temp branch onto main (this will checkout tmpBranch)
		rebaseCmd := exec.Command("git", "rebase", defaultBranch, tmpBranch)
		rebaseCmd.Dir = gitRoot
		if output, err := rebaseCmd.CombinedOutput(); err != nil {
			return fastForwardMsg{err: fmt.Errorf("rebase failed: %s", strings.TrimSpace(string(output)))}
		}

		// Switch back to main branch
		checkoutCmd := exec.Command("git", "checkout", defaultBranch)
		checkoutCmd.Dir = gitRoot
		if output, err := checkoutCmd.CombinedOutput(); err != nil {
			return fastForwardMsg{err: fmt.Errorf("failed to checkout main: %s", strings.TrimSpace(string(output)))}
		}

		// Fast-forward main to temp branch
		mergeCmd := exec.Command("git", "merge", "--ff-only", tmpBranch)
		mergeCmd.Dir = gitRoot
		if output, err := mergeCmd.CombinedOutput(); err != nil {
			return fastForwardMsg{err: fmt.Errorf("fast-forward merge failed: %s", strings.TrimSpace(string(output)))}
		}

		// Now, update the worktree's branch to point to the new HEAD of main.
		// We do this by running `git reset --hard` inside the worktree itself.
		worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeBranch)
		resetCmd := exec.Command("git", "reset", "--hard", defaultBranch)
		resetCmd.Dir = worktreePath // CRITICAL: Execute the command within the worktree directory.
		if output, err := resetCmd.CombinedOutput(); err != nil {
			// This is a critical failure. If this doesn't work, the cleanup will fail later.
			// We must report this error clearly.
			return fastForwardMsg{err: fmt.Errorf("failed to reset worktree branch '%s': %s", worktreeBranch, strings.TrimSpace(string(output)))}
		}

		return fastForwardMsg{message: fmt.Sprintf("Successfully merged '%s' into '%s' and synchronized the worktree.", worktreeBranch, defaultBranch)}
	}
}


