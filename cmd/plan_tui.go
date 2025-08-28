package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-flow/pkg/state"
	"github.com/spf13/cobra"
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
	StatusParts map[string]int // For detailed status breakdown
}

// planListTUIModel represents the TUI state
type planListTUIModel struct {
	plans          []PlanListItem
	cursor         int
	width          int
	height         int
	err            error
	showHelp       bool
	loading        bool
	plansDirectory string
}

// TUI key mappings for plan list
type planListKeyMap struct {
	Up        string
	Down      string
	ViewPlan  string
	FinishPlan string
	Quit      string
	Help      string
}

var planListKeys = planListKeyMap{
	Up:        "k",
	Down:      "j", 
	ViewPlan:  "enter",
	FinishPlan: "ctrl+x",
	Quit:      "q",
	Help:      "?",
}

// TUI styles for plan list
var (
	planListTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("205")).
				PaddingBottom(1)

	planListHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("240")).
				PaddingLeft(2).
				PaddingRight(2)

	planListSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("205")).
				Background(lipgloss.Color("236"))

	planListCursorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214")).
				Background(lipgloss.Color("238"))

	planListIndicatorStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("205"))

	planListHelpStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("241")).
				PaddingTop(1)

	planListErrorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("9")).
				Bold(true)

	planListStatusStyles = map[string]lipgloss.Style{
		"completed": lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		"running":   lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		"pending":   lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		"failed":    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		"blocked":   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		"mixed":     lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		"no jobs":   lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	}
)

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

	// Get plans directory from config
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if flowCfg.PlansDirectory == "" {
		return fmt.Errorf("'flow.plans_directory' is not set in your grove.yml configuration")
	}

	plansDirectory, err := expandPath(flowCfg.PlansDirectory)
	if err != nil {
		return fmt.Errorf("could not expand plans_directory path: %w", err)
	}

	// Create and run TUI
	model := newPlanListTUIModel(plansDirectory)
	program := tea.NewProgram(model, tea.WithAltScreen())
	
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("error running plan list TUI: %w", err)
	}

	return nil
}

func newPlanListTUIModel(plansDirectory string) planListTUIModel {
	return planListTUIModel{
		plans:          []PlanListItem{},
		cursor:         0,
		loading:        true,
		plansDirectory: plansDirectory,
	}
}

func (m planListTUIModel) Init() tea.Cmd {
	return loadPlansListCmd(m.plansDirectory)
}

func (m planListTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case planListLoadCompleteMsg:
		m.loading = false
		if msg.error != nil {
			m.err = msg.error
			return m, nil
		}
		m.plans = msg.plans
		// Adjust cursor if needed
		if m.cursor >= len(m.plans) {
			m.cursor = len(m.plans) - 1
		}
		if m.cursor < 0 && len(m.plans) > 0 {
			m.cursor = 0
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Handle help screen
		if m.showHelp {
			switch msg.String() {
			case planListKeys.Help, planListKeys.Quit, "ctrl+c":
				m.showHelp = false
			}
			return m, nil
		}

		// Regular key handling
		switch msg.String() {
		case "ctrl+c", planListKeys.Quit:
			return m, tea.Quit

		case planListKeys.Help:
			m.showHelp = true

		case planListKeys.Up, "up":
			if m.cursor > 0 {
				m.cursor--
			}

		case planListKeys.Down, "down":
			if m.cursor < len(m.plans)-1 {
				m.cursor++
			}

		case planListKeys.ViewPlan:
			// Enter key - view plan status TUI
			if m.cursor < len(m.plans) {
				plan := &m.plans[m.cursor]
				return m, openPlanStatusTUI(plan.Plan)
			}

		case planListKeys.FinishPlan:
			// Ctrl+X key - execute plan finish command
			if m.cursor < len(m.plans) {
				plan := &m.plans[m.cursor]
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
		return planListErrorStyle.Render(fmt.Sprintf("Error: %v\n", m.err))
	}

	var s strings.Builder

	// Show help popup if active
	if m.showHelp {
		helpView := m.renderPlanListHelpScreen()
		return lipgloss.NewStyle().
			Width(m.width).
			Height(m.height).
			Align(lipgloss.Center, lipgloss.Center).
			Render(helpView)
	}

	// Title
	s.WriteString(planListTitleStyle.Render("Plan List"))
	s.WriteString("\n")

	if len(m.plans) == 0 {
		s.WriteString("No plans found in directory.\n")
		s.WriteString("\n")
		s.WriteString(m.renderPlanListHelp())
		return s.String()
	}

	// Table header
	s.WriteString(m.renderPlanListHeader())
	s.WriteString("\n")

	// Plan list
	s.WriteString(m.renderPlanListTable())

	// Help text
	s.WriteString("\n")
	s.WriteString(m.renderPlanListHelp())

	return s.String()
}

func (m planListTUIModel) renderPlanListHeader() string {
	nameHeader := planListHeaderStyle.Render("NAME")
	jobsHeader := planListHeaderStyle.Render("JOBS")
	statusHeader := planListHeaderStyle.Render("STATUS")
	
	// Calculate column widths based on terminal width
	nameWidth := 20
	jobsWidth := 6
	statusWidth := max(30, m.width-nameWidth-jobsWidth-10) // Remaining space

	if m.width > 80 {
		nameWidth = 30
		statusWidth = m.width - nameWidth - jobsWidth - 10
	}

	line := fmt.Sprintf("  %-*s  %-*s  %-*s",
		nameWidth, nameHeader,
		jobsWidth, jobsHeader, 
		statusWidth, statusHeader)
	
	return line
}

func (m planListTUIModel) renderPlanListTable() string {
	var s strings.Builder
	
	nameWidth := 20
	jobsWidth := 6
	statusWidth := max(30, m.width-nameWidth-jobsWidth-10)

	if m.width > 80 {
		nameWidth = 30
		statusWidth = m.width - nameWidth - jobsWidth - 10
	}

	for i, plan := range m.plans {
		// Build the row content
		name := truncateString(plan.Name, nameWidth)
		jobs := fmt.Sprintf("%d", plan.JobCount)
		status := truncateString(plan.Status, statusWidth)

		// Apply status styling to status text
		statusStyle := planListStatusStyles["mixed"] // default
		if strings.Contains(plan.Status, "no jobs") {
			statusStyle = planListStatusStyles["no jobs"]
		} else if strings.Contains(plan.Status, "failed") {
			statusStyle = planListStatusStyles["failed"]
		} else if strings.Contains(plan.Status, "running") {
			statusStyle = planListStatusStyles["running"]
		} else if strings.Contains(plan.Status, "completed") && !strings.Contains(plan.Status, "pending") {
			statusStyle = planListStatusStyles["completed"]
		} else if strings.Contains(plan.Status, "pending") {
			statusStyle = planListStatusStyles["pending"]
		}
		
		styledStatus := statusStyle.Render(status)

		// Build the row
		row := fmt.Sprintf("  %-*s  %-*s  %s",
			nameWidth, name,
			jobsWidth, jobs,
			styledStatus)

		// Apply cursor styling
		if i == m.cursor {
			row = planListCursorStyle.Render(row)
		}

		// Add cursor indicator
		indicators := ""
		if i == m.cursor {
			indicators += planListIndicatorStyle.Render(" ◀")
		}

		s.WriteString(row + indicators + "\n")
	}

	return s.String()
}

func (m planListTUIModel) renderPlanListHelp() string {
	return planListHelpStyle.Render("Press ? for help")
}

func (m planListTUIModel) renderPlanListHelpScreen() string {
	// Create styles matching grove-context
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("241")).
		Padding(2, 3).
		Width(60).
		Align(lipgloss.Center)
	
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("12")).
		MarginBottom(1)
	
	keyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("34")).
		Bold(true)
	
	// Navigation and actions
	helpItems := []string{
		"Navigation:",
		"",
		keyStyle.Render("↑/↓, j/k") + " - Navigate up/down through plans",
		keyStyle.Render("◀") + " - Current position indicator",
		"",
		"Actions:",
		"",
		keyStyle.Render("Enter") + " - View plan status (opens plan status TUI)",
		keyStyle.Render("Ctrl+X") + " - Finish plan (executes plan finish command)",
		"",
		"General:",
		"",
		keyStyle.Render("q") + " - Quit",
		keyStyle.Render("?") + " - Toggle this help",
	}
	
	// Render content
	content := strings.Join(helpItems, "\n")
	
	// Add title and wrap in box
	title := titleStyle.Render("Plan List TUI - Help")
	fullContent := lipgloss.JoinVertical(lipgloss.Center, title, content)
	
	return boxStyle.Render(fullContent)
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
					item := PlanListItem{
						Plan:     plan,
						Name:     plan.Name,
						JobCount: len(plan.Jobs),
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
					if c := statusCounts[orchestration.JobStatusPending] + statusCounts[orchestration.JobStatusPendingUser]; c > 0 {
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

	// Sort plans by name
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})

	return items, nil
}

func openPlanStatusTUI(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		// First set the active job programmatically
		func() tea.Msg {
			if err := state.SetActiveJob(plan.Name); err != nil {
				return err
			}
			return nil
		},
		// Then run the status TUI
		tea.ExecProcess(exec.Command("flow", "plan", "status", "--tui"), 
			func(err error) tea.Msg {
				return nil
			}),
	)
}

func executePlanFinish(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		// First set the active job programmatically  
		func() tea.Msg {
			if err := state.SetActiveJob(plan.Name); err != nil {
				return err
			}
			return nil
		},
		// Then run the finish command
		tea.ExecProcess(exec.Command("flow", "plan", "finish"),
			func(err error) tea.Msg {
				return nil
			}),
	)
}

// Utility functions
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}