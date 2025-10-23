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
	Worktree    string          // Worktree associated with the plan
	GitStatus   *git.StatusInfo // Git status information for the worktree
	Notes       string          // User notes/description
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
}

// TUI key mappings for plan list
type planListKeyMap struct {
	keymap.Base
	Up         key.Binding
	Down       key.Binding
	ViewPlan   key.Binding
	OpenPlan   key.Binding
	FinishPlan key.Binding
	NewPlan    key.Binding
	SetActive  key.Binding
	EditNotes  key.Binding
}

func (k planListKeyMap) ShortHelp() []key.Binding {
	// Return empty to show no help in footer - all help goes in popup
	return []key.Binding{}
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
			k.FinishPlan,
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
	EditNotes: key.NewBinding(
		key.WithKeys("e"),
		key.WithHelp("e", "edit notes"),
	),
}


// Messages for the plan list TUI
type planListLoadCompleteMsg struct{
	plans []PlanListItem
	error error
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
	}
}

func (m planListTUIModel) Init() tea.Cmd {
	return tea.Batch(
		loadPlansListCmd(m.plansDirectory),
		refreshTick(),
	)
}

func (m planListTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case childExitedMsg:
		// Child TUI (status) exited, likely due to edit action in Neovim
		// Quit this parent TUI too
		return m, tea.Quit

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
			loadPlansListCmd(m.plansDirectory),
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
		s.WriteString(theme.DefaultTheme.Muted.Render("Press ? for help"))
		return s.String()
	}

	// Render table using grove-core table component
	s.WriteString(m.renderPlanTable())

	// Help text
	s.WriteString("\n")
	s.WriteString(theme.DefaultTheme.Muted.Render("Press ? for help"))

	// Display status message at the bottom if any
	if m.statusMessage != "" {
		s.WriteString("\n\n")
		s.WriteString(theme.DefaultTheme.Success.Render(m.statusMessage))
	}

	return s.String()
}

func (m planListTUIModel) renderPlanTable() string {
	if len(m.plans) == 0 {
		return ""
	}

	// Prepare headers like grove ws plans list
	headers := []string{"TITLE", "STATUS", "WORKTREE", "GIT", "NOTES", "UPDATED"}

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

		rows[i] = []string{
			titleText,
			statusText,
			worktreeText,
			gitText,
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
		emoji = "⏸" // on hold
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
func loadPlansListCmd(plansDirectory string) tea.Cmd {
	return func() tea.Msg {
		plans, err := loadPlansList(plansDirectory)
		return planListLoadCompleteMsg{
			plans: plans,
			error: err,
		}
	}
}

func loadPlansList(plansDirectory string) ([]PlanListItem, error) {
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
						// Infer git root from plan path to handle cross-repo scenarios
						gitRoot := findGitRootForWorktree(planPath, worktree)

						if gitRoot != "" {
							worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktree)
							if _, statErr := os.Stat(worktreePath); statErr == nil {
								gitStatus, statusErr := git.GetStatus(worktreePath)
								if statusErr == nil {
									// Override ahead/behind counts to compare against local main, not upstream
									gitStatus.AheadCount = getCommitCount(worktreePath, "main..HEAD")
									gitStatus.BehindCount = getCommitCount(worktreePath, "HEAD..main")
									item.GitStatus = gitStatus
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
// by inferring from the plan path and checking known locations
func findGitRootForWorktree(planPath, worktreeName string) string {
	// Try to infer the repo name from the plan path
	// Typical pattern: ~/Documents/nb/repos/{repo}/main/plans/{plan}
	parts := strings.Split(planPath, string(filepath.Separator))
	var repoName string
	for i, part := range parts {
		if part == "repos" && i+1 < len(parts) {
			repoName = parts[i+1]
			break
		}
	}

	if repoName == "" {
		return ""
	}

	homeDir, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()

	// Build list of candidate git roots to check
	var candidates []string

	// Special case: if repo name is "grove-ecosystem", check the ecosystem root itself
	if repoName == "grove-ecosystem" {
		ecosystemPath := filepath.Join(homeDir, "Code", "grove-ecosystem")
		candidates = append(candidates, ecosystemPath)
	} else {
		// Look for the repo in the grove ecosystem
		candidates = append(candidates,
			filepath.Join(homeDir, "Code", "grove-ecosystem", repoName),
			filepath.Join(homeDir, "Code", "grove-ecosystem", ".grove-worktrees", repoName),
		)
	}

	// Also check current directory patterns
	if strings.Contains(cwd, "grove-ecosystem") {
		ecosystemRoot := cwd[:strings.Index(cwd, "grove-ecosystem")+len("grove-ecosystem")]
		if repoName == "grove-ecosystem" {
			candidates = append(candidates, ecosystemRoot)
		} else {
			candidates = append(candidates,
				filepath.Join(ecosystemRoot, repoName),
				filepath.Join(ecosystemRoot, ".grove-worktrees", repoName),
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
		// Then run the status TUI via 'grove' for workspace-awareness
		tea.ExecProcess(exec.Command("grove", "flow", "plan", "status", "--tui"),
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


