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
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/tui/components"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/core/tui/components/table"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/util/delegation"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/sirupsen/logrus"
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

// EcosystemRepoStatus holds detailed status for a single repo in an ecosystem plan.
type EcosystemRepoStatus struct {
	Name        string
	MergeStatus string
	GitStatus   *git.StatusInfo
}

// PlanListItem represents a plan in the TUI list
type PlanListItem struct {
	Plan                  *orchestration.Plan
	Name                  string
	JobCount              int
	Status                string
	StatusParts           map[string]int        // For detailed status breakdown
	LastUpdated           time.Time             // When the plan was last modified
	Worktree              string                // Worktree associated with the plan
	GitStatus             *git.StatusInfo       // Git status information for the worktree
	ReviewStatus          string                // Review status like "In Progress"
	MergeStatus           string                // Merge status: "Ready", "Needs Rebase", "Merged"
	Notes                 string                // User notes/description
	EcosystemRepoStatuses []EcosystemRepoStatus // Detailed status for each repo in an ecosystem plan
}

// planListTUIModel represents the TUI state
type planListTUIModel struct {
	plans                []PlanListItem
	cursor               int
	width                int
	height               int
	err                  error
	loading              bool
	plansDirectory       string
	cwdGitRoot           string // Git root from where the command was run
	statusMessage        string
	help                 help.Model
	keys                 planListKeyMap
	activePlan           string
	editingNotes         bool
	notesInput           textinput.Model
	editPlanIndex        int
	showGitLog           bool   // To toggle the view
	gitLogContent        string // To store the git log output
	gitLogError          error  // To store any errors
	showOnHold           bool   // Whether to show on-hold plans
	inRepoNavigationMode bool   // When true, navigating repos instead of plans
	repoCursor           int    // Cursor position in ecosystem repo list
	repoGitLogContent    string // Git log for selected repo
	repoGitLogError      error  // Error from repo git log
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
	FastForwardMain   key.Binding
	FastForwardUpdate key.Binding
	ToggleGitLog      key.Binding
	ToggleHold        key.Binding
	SetHoldStatus     key.Binding
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
			k.FastForwardUpdate,
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
	FastForwardUpdate: key.NewBinding(
		key.WithKeys("U"),
		key.WithHelp("U", "update from main"),
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
		key.WithKeys("h"),
		key.WithHelp("h", "hold/unhold plan"),
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

// repoGitLogMsg is sent when the git log fetch for a specific repo is complete
type repoGitLogMsg struct {
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

func fetchRepoGitLogCmd(repoPath string) tea.Cmd {
	return func() tea.Msg {
		if repoPath == "" {
			return repoGitLogMsg{err: fmt.Errorf("no repo path provided")}
		}

		// Using --color=always ensures that the color codes are captured in the output for raw rendering
		cmd := exec.Command("git", "log", "--oneline", "--decorate", "--color=always", "--graph", "--all", "--max-count=20")
		cmd.Dir = repoPath

		output, err := cmd.CombinedOutput()
		if err != nil {
			return repoGitLogMsg{err: fmt.Errorf("git log failed: %w: %s", err, string(output))}
		}

		return repoGitLogMsg{content: string(output)}
	}
}

func runPlanTUI(cmd *cobra.Command, args []string) error {
	// Check for TTY
	if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("TUI mode requires an interactive terminal")
	}

	// Check if we're in a notebook directory - if so, redirect user to project
	cwd, _ := os.Getwd()
	if project, notebookRoot, _ := workspace.GetProjectFromNotebookPath(cwd); notebookRoot != "" {
		workspaceName := workspace.ExtractWorkspaceNameFromNotebookPath(cwd, notebookRoot)
		if project != nil {
			return fmt.Errorf("you are in the notebook directory for '%s'.\n"+
				"Run this command from the project directory instead:\n\n"+
				"  cd %s", workspaceName, project.Path)
		}
		return fmt.Errorf("you are in a notebook directory for '%s'.\n"+
			"Run this command from the associated project directory instead.", workspaceName)
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
		fmt.Fprintln(os.Stderr, "WARNING:  Warning: The 'flow.plans_directory' config is deprecated. Please configure 'notebook.root_dir' in your global grove.yml instead.")
	}

	plansDirectory, err := locator.GetPlansDir(node)
	if err != nil {
		return fmt.Errorf("could not resolve plans directory: %w", err)
	}

	// Get git root from the CWD workspace node
	cwdGitRoot := node.Path
	if cwdGitRoot == "" {
		// Fallback: try to get git root from current directory
		cwdGitRoot, _ = git.GetGitRoot(".")
	}

	// Create and run TUI
	model := newPlanListTUIModel(plansDirectory, cwdGitRoot)
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

func newPlanListTUIModel(plansDirectory string, cwdGitRoot string) planListTUIModel {
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
		cwdGitRoot:     cwdGitRoot,
		help:           helpModel,
		keys:           planListKeys,
		activePlan:     activePlan,
		showGitLog:     false, // Off by default
	}
}

func (m planListTUIModel) Init() tea.Cmd {
	return tea.Batch(
		loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold),
		fetchGitLogCmd(m.cwdGitRoot),
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
			m.statusMessage = theme.DefaultTheme.Success.Render(fmt.Sprintf("%s %s", theme.IconSuccess, msg.message))
		}
		return m, nil

	case gitLogMsg:
		m.gitLogContent = msg.content
		m.gitLogError = msg.err
		return m, nil

	case repoGitLogMsg:
		m.repoGitLogContent = msg.content
		m.repoGitLogError = msg.err
		return m, nil

	case reviewCompleteMsg:
		// Review command completed, show status and reload the plans list
		if msg.err != nil {
			m.statusMessage = theme.DefaultTheme.Error.Render(fmt.Sprintf("Review failed: %s", msg.err.Error()))
		} else if msg.output != "" {
			// Show success message from the review command
			m.statusMessage = theme.DefaultTheme.Success.Render(fmt.Sprintf("%s Plan marked for review", theme.IconSuccess))
		}
		return m, loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold)

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
			loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold),
			fetchGitLogCmd(m.cwdGitRoot),
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
			if m.inRepoNavigationMode {
				// Navigate repos within ecosystem
				if m.repoCursor > 0 {
					m.repoCursor--
					// Fetch git log for newly selected repo
					if m.cursor >= 0 && m.cursor < len(m.plans) {
						selectedPlan := m.plans[m.cursor]
						if m.repoCursor < len(selectedPlan.EcosystemRepoStatuses) {
							repoStatus := selectedPlan.EcosystemRepoStatuses[m.repoCursor]
							// Get repo path
							logger := logrus.New()
							logger.SetLevel(logrus.WarnLevel)
							discoveryService := workspace.NewDiscoveryService(logger)
							discoveryResult, _ := discoveryService.DiscoverAll()
							provider := workspace.NewProvider(discoveryResult)
							localWorkspaces := provider.LocalWorkspaces()
							if repoPath, exists := localWorkspaces[repoStatus.Name]; exists {
								return m, fetchRepoGitLogCmd(repoPath)
							}
						}
					}
				}
			} else {
				// Navigate plans
				if m.cursor > 0 {
					m.cursor--
				}
			}

		case key.Matches(msg, m.keys.Down):
			m.statusMessage = "" // Clear status on navigation
			if m.inRepoNavigationMode {
				// Navigate repos within ecosystem
				if m.cursor >= 0 && m.cursor < len(m.plans) {
					selectedPlan := m.plans[m.cursor]
					if m.repoCursor < len(selectedPlan.EcosystemRepoStatuses)-1 {
						m.repoCursor++
						// Fetch git log for newly selected repo
						if m.repoCursor < len(selectedPlan.EcosystemRepoStatuses) {
							repoStatus := selectedPlan.EcosystemRepoStatuses[m.repoCursor]
							// Get repo path
							logger := logrus.New()
							logger.SetLevel(logrus.WarnLevel)
							discoveryService := workspace.NewDiscoveryService(logger)
							discoveryResult, _ := discoveryService.DiscoverAll()
							provider := workspace.NewProvider(discoveryResult)
							localWorkspaces := provider.LocalWorkspaces()
							if repoPath, exists := localWorkspaces[repoStatus.Name]; exists {
								return m, fetchRepoGitLogCmd(repoPath)
							}
						}
					}
				}
			} else {
				// Navigate plans
				if m.cursor < len(m.plans)-1 {
					m.cursor++
				}
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
				return m, executePlanReview(plan.Plan)
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

		case key.Matches(msg, m.keys.FastForwardUpdate):
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				selectedPlan := m.plans[m.cursor]
				// Allow updating for "Needs Rebase", "Behind", or ecosystem statuses containing these
				if selectedPlan.MergeStatus == "Needs Rebase" || selectedPlan.MergeStatus == "Behind" ||
					strings.Contains(selectedPlan.MergeStatus, "Rebase") || strings.Contains(selectedPlan.MergeStatus, "Behind") {
					m.statusMessage = "Updating branch from main..."
					return m, fastForwardUpdateCmd(selectedPlan)
				}
				m.statusMessage = theme.DefaultTheme.Error.Render("Branch is not in a state that can be updated (status: " + selectedPlan.MergeStatus + ")")
			}

		case key.Matches(msg, m.keys.FastForwardMain):
			// M key - rebase worktree on main, then fast-forward main
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				selectedPlan := m.plans[m.cursor]

				// Pre-flight check before attempting merge
				// Allow "Ready" or ecosystem statuses containing "Ready" (e.g., "3 Ready")
				if selectedPlan.MergeStatus != "Ready" && !strings.Contains(selectedPlan.MergeStatus, "Ready") {
					m.statusMessage = theme.DefaultTheme.Error.Render(fmt.Sprintf("Cannot merge: branch is not ready (status: %s). Use 'U' to update first.", selectedPlan.MergeStatus))
					return m, nil
				}

				m.statusMessage = "Merging branch to main..."
				return m, fastForwardMainCmd(selectedPlan)
			}

		case key.Matches(msg, m.keys.ToggleGitLog):
			if m.inRepoNavigationMode {
				// Exit repo navigation mode
				m.inRepoNavigationMode = false
				m.showGitLog = false
				m.repoCursor = 0
				m.repoGitLogContent = ""
				m.repoGitLogError = nil
			} else if m.showGitLog {
				// Turn off git log view
				m.showGitLog = false
			} else {
				// Turn on git log view
				m.showGitLog = true
				// If current plan has ecosystem repos, enter repo navigation mode
				if m.cursor >= 0 && m.cursor < len(m.plans) {
					selectedPlan := m.plans[m.cursor]
					if len(selectedPlan.EcosystemRepoStatuses) > 0 {
						m.inRepoNavigationMode = true
						m.repoCursor = 0
						// Fetch git log for first repo
						if len(selectedPlan.EcosystemRepoStatuses) > 0 {
							repoStatus := selectedPlan.EcosystemRepoStatuses[0]
							// Get repo path
							logger := logrus.New()
							logger.SetLevel(logrus.WarnLevel)
							discoveryService := workspace.NewDiscoveryService(logger)
							discoveryResult, _ := discoveryService.DiscoverAll()
							provider := workspace.NewProvider(discoveryResult)
							localWorkspaces := provider.LocalWorkspaces()
							if repoPath, exists := localWorkspaces[repoStatus.Name]; exists {
								return m, fetchRepoGitLogCmd(repoPath)
							}
						}
					}
				}
			}
			m.statusMessage = "" // Clear status message when toggling
			return m, nil

		case key.Matches(msg, m.keys.ToggleHold):
			m.showOnHold = !m.showOnHold
			m.cursor = 0 // Reset cursor to top
			m.statusMessage = fmt.Sprintf("On-hold plans: %v", m.showOnHold)
			return m, loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold)

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
				return m, loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold)
			}
		}
	}

	return m, nil
}

func (m planListTUIModel) View() string {
	// Apply padding to the entire TUI
	padStyle := lipgloss.NewStyle().PaddingLeft(1).PaddingTop(1)

	if m.loading {
		return padStyle.Render("Loading plans...\n")
	}

	if m.err != nil {
		return padStyle.Render(theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v\n", m.err)))
	}

	var s strings.Builder

	// If help is visible, show it and return
	if m.help.ShowAll {
		return padStyle.Render(m.help.View())
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
		return padStyle.Render(s.String())
	}

	if len(m.plans) == 0 {
		s.WriteString("No plans found in directory.\n")
		s.WriteString("\n")
		s.WriteString(m.help.View())
		return padStyle.Render(s.String())
	}

	// Render table using grove-core table component
	table := m.renderPlanTable()

	// Help text
	help := m.help.View()

	// Conditionally render and combine views
	if m.showGitLog {
		var detailPane string
		var detailTitle string = "Details"

		if m.cursor >= 0 && m.cursor < len(m.plans) {
			selectedPlan := m.plans[m.cursor]
			if len(selectedPlan.EcosystemRepoStatuses) > 0 {
				detailTitle = "Ecosystem Repository Status"
				detailPane = m.renderEcosystemStatusPane()

				// If in repo navigation mode, also show git log for selected repo
				if m.inRepoNavigationMode {
					var repoLogPane string
					if m.repoGitLogError != nil {
						repoLogPane = theme.DefaultTheme.Error.Render(m.repoGitLogError.Error())
					} else {
						repoLogPane = m.repoGitLogContent
					}

					// Fixed height for git log - show up to 10 commits
					const gitLogHeight = 10

					// Limit to calculated height and pad to ensure consistent height
					lines := strings.Split(repoLogPane, "\n")
					if len(lines) > gitLogHeight {
						lines = lines[:gitLogHeight]
					}
					// Pad with empty lines to maintain consistent height
					for len(lines) < gitLogHeight {
						lines = append(lines, "")
					}
					repoLogPane = strings.Join(lines, "\n")

					boxStyle := theme.DefaultTheme.Box.Copy().Padding(0, 1).MarginLeft(4).MarginTop(-1).Width(60)
					repoLogPaneStyled := boxStyle.Render(repoLogPane)

					// Get selected repo name for title
					var repoName string
					if m.repoCursor >= 0 && m.repoCursor < len(selectedPlan.EcosystemRepoStatuses) {
						repoName = selectedPlan.EcosystemRepoStatuses[m.repoCursor].Name
					}

					// Stack ecosystem table and repo git log side by side
					ecosystemTitle := lipgloss.NewStyle().MarginLeft(2).Render(theme.DefaultTheme.Bold.Render(detailTitle))
					gitLogTitle := lipgloss.NewStyle().MarginLeft(4).Render(theme.DefaultTheme.Bold.Render(fmt.Sprintf("Git Log - %s", repoName)))

					// Create left column (ecosystem status)
					leftColumn := lipgloss.JoinVertical(lipgloss.Left,
						ecosystemTitle,
						detailPane,
					)

					// Create right column (git log)
					rightColumn := lipgloss.JoinVertical(lipgloss.Left,
						gitLogTitle,
						repoLogPaneStyled,
					)

					// Join columns horizontally
					detailsView := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, rightColumn)

					mainContent := lipgloss.JoinVertical(lipgloss.Left,
						table,
						detailsView,
					)
					s.WriteString(mainContent)
					s.WriteString("\n")
					s.WriteString(help)
					if m.statusMessage != "" {
						s.WriteString("\n\n")
						s.WriteString(theme.DefaultTheme.Success.Render(m.statusMessage))
					}
					return padStyle.Render(s.String())
				}
			} else {
				detailTitle = theme.IconGit + " Git Repository Log"
				detailPane = m.renderGitLogPane()
			}
		} else {
			detailTitle = theme.IconGit + " Git Repository Log"
			detailPane = m.renderGitLogPane()
		}

		// Using JoinVertical to stack the elements
		styledDetailTitle := lipgloss.NewStyle().MarginLeft(2).Render(theme.DefaultTheme.Bold.Render(detailTitle))
		mainContent := lipgloss.JoinVertical(lipgloss.Left,
			table,
			styledDetailTitle,
			detailPane,
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

	// Apply padding to entire view (padStyle already declared at top of function)
	return padStyle.Render(s.String())
}

// renderGitLogPane renders the git log pane with dynamic height
func (m planListTUIModel) renderGitLogPane() string {
	var content string
	if m.gitLogError != nil {
		content = theme.DefaultTheme.Error.Render(m.gitLogError.Error())
	} else {
		// The git log output already contains ANSI color codes, so we just render the raw string.
		content = m.gitLogContent
	}

	// Fixed height for git log - show up to 10 commits
	const gitLogHeight = 10

	// Limit to calculated height and pad to ensure consistent height
	lines := strings.Split(content, "\n")
	if len(lines) > gitLogHeight {
		lines = lines[:gitLogHeight]
	}
	// Pad with empty lines to maintain consistent height
	for len(lines) < gitLogHeight {
		lines = append(lines, "")
	}
	content = strings.Join(lines, "\n")

	// Use a lipgloss box to frame the content with left margin and width limit
	boxStyle := theme.DefaultTheme.Box.Copy().Padding(0, 1).MarginLeft(2).Width(60)
	return boxStyle.Render(content)
}

// renderEcosystemStatusPane renders the detailed status of each repository in an ecosystem plan.
func (m planListTUIModel) renderEcosystemStatusPane() string {
	if m.cursor < 0 || m.cursor >= len(m.plans) {
		return ""
	}
	selectedPlan := m.plans[m.cursor]
	if len(selectedPlan.EcosystemRepoStatuses) == 0 {
		return "No ecosystem repository details to display."
	}

	headers := []string{"REPO", "GIT STATUS", "MERGE STATUS"}
	var rows [][]string

	for _, repoStatus := range selectedPlan.EcosystemRepoStatuses {
		// format git status
		var gitText string
		if repoStatus.GitStatus != nil {
			gs := repoStatus.GitStatus
			var parts []string
			if gs.IsDirty {
				parts = append(parts, theme.DefaultTheme.Warning.Render(theme.IconWarning + " Dirty"))
			} else {
				parts = append(parts, theme.DefaultTheme.Success.Render(theme.IconSuccess + " Clean"))
			}
			if gs.AheadCount > 0 {
				parts = append(parts, theme.DefaultTheme.Success.Render(fmt.Sprintf("%s%d", theme.IconArrowUp, gs.AheadCount)))
			}
			if gs.BehindCount > 0 {
				parts = append(parts, theme.DefaultTheme.Error.Render(fmt.Sprintf("%s%d", theme.IconArrowDown, gs.BehindCount)))
			}
			gitText = strings.Join(parts, " ")
		} else {
			gitText = theme.DefaultTheme.Muted.Render("-")
		}

		// format merge status
		var mergeText string
		switch repoStatus.MergeStatus {
		case "Ready":
			mergeText = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Ready")
		case "Needs Rebase":
			mergeText = theme.DefaultTheme.Warning.Render(theme.IconWarning + " Needs Rebase")
		case "Behind":
			mergeText = theme.DefaultTheme.Info.Render(theme.IconInfo + " Behind")
		case "Conflicts":
			mergeText = theme.DefaultTheme.Error.Render(theme.IconError + " Conflicts")
		case "Merged", "Synced":
			mergeText = theme.DefaultTheme.Muted.Render(theme.IconMerge + " Synced")
		default:
			mergeText = theme.DefaultTheme.Muted.Render(repoStatus.MergeStatus)
		}

		rows = append(rows, []string{
			repoStatus.Name,
			gitText,
			mergeText,
		})
	}

	var tableOutput string
	if m.inRepoNavigationMode {
		tableOutput = table.SelectableTable(headers, rows, m.repoCursor)
	} else {
		tableOutput = table.SimpleTable(headers, rows)
	}

	// Add left margin to align with title and main table
	return lipgloss.NewStyle().MarginLeft(0).Render(tableOutput)
}

func (m planListTUIModel) renderPlanTable() string {
	if len(m.plans) == 0 {
		return ""
	}

	// Prepare headers like grove ws plans list
	headers := []string{"PLAN", "STATUS", "WORKTREE", "GIT", "MERGE", "REVIEWED", "NOTES", "UPDATED"}

	// Prepare rows with emoji status indicators and formatting
	rows := make([][]string, len(m.plans))
	for i, plan := range m.plans {
		// Generate emoji status like the example
		statusText := m.formatStatusWithEmoji(plan)

		// Format last updated with relative time
		updatedText := theme.DefaultTheme.Muted.Render("◦ " + formatRelativeTime(plan.LastUpdated))

		// Add active plan indicator - use bold for emphasis without explicit color
		// Style the rolling plan name differently
		titleText := plan.Name
		if plan.Name == RollingPlanName {
			// Rolling plan: show with parens and dim styling
			titleText = theme.DefaultTheme.Muted.Render("(rolling)")
		}
		if plan.Name == m.activePlan {
			// Use IconSelect for active plan indicator, but only if not also selected by cursor.
			// The SelectableTable will handle the cursor indicator.
			titleText = theme.DefaultTheme.Bold.Render(fmt.Sprintf("%s %s", theme.IconSelect, titleText))
		}

		// Format worktree text
		worktreeText := plan.Worktree
		if worktreeText == "" {
			worktreeText = theme.DefaultTheme.Muted.Render("-")
		} else {
			worktreeText = theme.IconGitBranch + " " + worktreeText
		}

		// Format git status text
		var gitText string
		if plan.GitStatus != nil {
			gs := plan.GitStatus
			var parts []string

			if gs.IsDirty {
				parts = append(parts, theme.DefaultTheme.Warning.Render(theme.IconWarning + " Dirty"))
			} else {
				parts = append(parts, theme.DefaultTheme.Success.Render(theme.IconSuccess + " Clean"))
			}

			if gs.AheadCount > 0 {
				parts = append(parts, theme.DefaultTheme.Success.Render(fmt.Sprintf("%s%d", theme.IconArrowUp, gs.AheadCount)))
			}
			if gs.BehindCount > 0 {
				parts = append(parts, theme.DefaultTheme.Error.Render(fmt.Sprintf("%s%d", theme.IconArrowDown, gs.BehindCount)))
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

		// Format merge status text
		var mergeText string
		switch plan.MergeStatus {
		case "Ready":
			mergeText = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Ready")
		case "Needs Rebase":
			mergeText = theme.DefaultTheme.Warning.Render(theme.IconWarning + " Needs Rebase")
		case "Behind":
			mergeText = theme.DefaultTheme.Info.Render(theme.IconInfo + " Behind")
		case "Conflicts":
			mergeText = theme.DefaultTheme.Error.Render(theme.IconError + " Conflicts")
		case "Merged", "Synced":
			mergeText = theme.DefaultTheme.Muted.Render(theme.IconMerge + " Synced")
		default:
			mergeText = theme.DefaultTheme.Muted.Render(plan.MergeStatus)
		}

		// Format reviewed status (based on plan config status)
		var reviewedText string
		switch plan.ReviewStatus {
		case "Review":
			// Show checkmark for reviewed plans
			reviewedText = theme.DefaultTheme.Success.Render(theme.IconSuccess)
		case "Hold":
			reviewedText = theme.DefaultTheme.Warning.Render("Hold")
		case "Finished":
			reviewedText = theme.DefaultTheme.Success.Render("Finished")
		default:
			reviewedText = theme.DefaultTheme.Muted.Render("-")
		}

		rows[i] = []string{
			titleText,
			statusText,
			worktreeText,
			gitText,
			mergeText,
			reviewedText,
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
		return theme.IconPending + " no jobs"
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
		emoji = theme.IconError
	} else if running > 0 {
		emoji = theme.IconRunning
	} else if hold > 0 {
		emoji = theme.IconStatusHold
	} else if completed > 0 && (pending > 0 || running > 0) {
		emoji = theme.IconRunning // Represents work in progress
	} else if completed > 0 {
		emoji = theme.IconSuccess
	} else {
		emoji = theme.IconPending
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
func loadPlansListCmd(plansDirectory string, cwdGitRoot string, showOnHold bool) tea.Cmd {
	return func() tea.Msg {
		plans, err := loadPlansList(plansDirectory, cwdGitRoot, showOnHold)
		return planListLoadCompleteMsg{
			plans: plans,
			error: err,
		}
	}
}

func loadPlansList(plansDirectory string, cwdGitRoot string, showOnHold bool) ([]PlanListItem, error) {
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
						Plan:         plan,
						Name:         plan.Name,
						JobCount:     len(plan.Jobs),
						LastUpdated:  lastUpdated,
						Worktree:     worktree,
						Notes:        notes,
						MergeStatus:  "-", // Default value
						ReviewStatus: formatConfigStatus(plan.Config),
					}

					// Fetch git status if this plan has a worktree
					if worktree != "" {
						// Start with the git root from CWD (where the command was run)
						var gitRoot string
						if cwdGitRoot != "" {
							// First, check if the worktree exists in the CWD git root
							worktreePath := filepath.Join(cwdGitRoot, ".grove-worktrees", worktree)
							if _, err := os.Stat(worktreePath); err == nil {
								gitRoot = cwdGitRoot
							}
						}

						// Fallback: try to find the git root by getting the project/workspace for this plan
						if gitRoot == "" {
							project, err := workspace.GetProjectByPath(planPath)
							if err == nil && project != nil {
								// Use the project's path as the git root
								gitRoot = project.Path
							}
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

									// Use ecosystem-aware merge status for ecosystem plans
									if plan.Config != nil && len(plan.Config.Repos) > 0 {
										// Create a workspace provider for ecosystem lookups
										logger := logrus.New()
										logger.SetLevel(logrus.WarnLevel)
										discoveryService := workspace.NewDiscoveryService(logger)
										discoveryResult, err := discoveryService.DiscoverAll()
										if err != nil {
											item.MergeStatus = "err (discovery failed)"
										} else {
											provider := workspace.NewProvider(discoveryResult)
											item.EcosystemRepoStatuses, item.MergeStatus = getEcosystemRepoDetails(plan, worktree, provider)
										}
									} else {
										item.MergeStatus = getMergeStatus(gitRoot, worktree)
									}
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

type reviewCompleteMsg struct {
	output string
	err    error
}

// formatConfigStatus formats the plan's config status for display in the REVIEW column
func formatConfigStatus(config *orchestration.PlanConfig) string {
	if config == nil || config.Status == "" {
		return "-"
	}

	// Return capitalized version of the status
	switch config.Status {
	case "review":
		return "Review"
	case "hold":
		return "Hold"
	case "finished":
		return "Finished"
	default:
		return config.Status // Return as-is if unknown
	}
}

// getReviewStatus determines the review status based on Git state
// NOTE: This function is no longer used - we now use formatConfigStatus instead
// Kept for potential future use or removal in cleanup
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

// getMergeStatus determines if a branch can be fast-forwarded into main.
func getMergeStatus(repoPath, branchName string) string {
	if repoPath == "" || branchName == "" {
		return "-"
	}

	// 1. Check if the branch exists
	branchCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	branchCheckCmd.Dir = repoPath
	if err := branchCheckCmd.Run(); err != nil {
		return "no branch"
	}

	// 2. Determine default branch (main or master)
	defaultBranch := "main"
	mainCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
	mainCheckCmd.Dir = repoPath
	if err := mainCheckCmd.Run(); err != nil {
		masterCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/master")
		masterCheckCmd.Dir = repoPath
		if err := masterCheckCmd.Run(); err != nil {
			return "no main"
		}
		defaultBranch = "master"
	}

	// 3. Get ahead/behind counts
	aheadCount := getCommitCount(repoPath, defaultBranch+".."+branchName)
	behindCount := getCommitCount(repoPath, branchName+".."+defaultBranch)

	if aheadCount == 0 && behindCount == 0 {
		return "Synced"
	}
	if aheadCount > 0 && behindCount == 0 {
		return "Ready"
	}
	if aheadCount == 0 && behindCount > 0 {
		return "Behind"
	}

	// 4. Branches have diverged - check for conflicts
	mergeBaseCmd := exec.Command("git", "merge-base", defaultBranch, branchName)
	mergeBaseCmd.Dir = repoPath
	mergeBaseOutput, err := mergeBaseCmd.Output()
	if err != nil {
		return "err"
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOutput))

	mainRevCmd := exec.Command("git", "rev-parse", defaultBranch)
	mainRevCmd.Dir = repoPath
	mainRevOutput, err := mainRevCmd.Output()
	if err != nil {
		return "err"
	}
	mainRev := strings.TrimSpace(string(mainRevOutput))

	// This part is for detecting if a rebase is needed vs a clean merge
	if mergeBase != mainRev {
		// Check for actual merge conflicts
		mergeTreeCmd := exec.Command("git", "merge-tree", "--write-tree", defaultBranch, branchName)
		mergeTreeCmd.Dir = repoPath
		output, err := mergeTreeCmd.CombinedOutput()
		if err != nil || strings.Contains(string(output), "CONFLICT") {
			return "Conflicts"
		}
		return "Needs Rebase"
	}

	// Should not be reached if logic is correct, but as a fallback
	return "Diverged"
}

// getEcosystemMergeStatus checks merge status across all repos in an ecosystem plan
func getEcosystemMergeStatus(plan *orchestration.Plan, worktree string) string {
	if plan.Config == nil || len(plan.Config.Repos) == 0 {
		return "-"
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	discoveryService := workspace.NewDiscoveryService(logger)
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		return "err"
	}
	provider := workspace.NewProvider(discoveryResult)
	localWorkspaces := provider.LocalWorkspaces()

	statusCounts := make(map[string]int)
	for _, repoName := range plan.Config.Repos {
		repoPath, exists := localWorkspaces[repoName]
		if !exists {
			statusCounts["err"]++
			continue
		}

		status := getMergeStatus(repoPath, worktree)
		statusCounts[status]++
	}

	// Prioritize showing the most critical status (from worst to best)
	if statusCounts["Conflicts"] > 0 {
		if len(statusCounts) == 1 {
			return "Conflicts"
		}
		return fmt.Sprintf("%d Conflicts", statusCounts["Conflicts"])
	}
	if statusCounts["Needs Rebase"] > 0 {
		if len(statusCounts) == 1 {
			return "Needs Rebase"
		}
		return fmt.Sprintf("%d Rebase", statusCounts["Needs Rebase"])
	}
	if statusCounts["Behind"] > 0 {
		if len(statusCounts) == 1 {
			return "Behind"
		}
		return fmt.Sprintf("%d Behind", statusCounts["Behind"])
	}
	if statusCounts["Ready"] > 0 {
		if len(statusCounts) == 1 && statusCounts["Ready"] == len(plan.Config.Repos) {
			return "Ready"
		}
		return fmt.Sprintf("%d Ready", statusCounts["Ready"])
	}
	if statusCounts["Synced"] > 0 || statusCounts["Merged"] > 0 {
		totalSynced := statusCounts["Synced"] + statusCounts["Merged"]
		if len(statusCounts) == 1 && totalSynced == len(plan.Config.Repos) {
			return "Synced"
		}
		return fmt.Sprintf("%d Synced", totalSynced)
	}
	if statusCounts["err"] > 0 {
		return "err"
	}

	return "-"
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
		// Run status TUI through grove delegator (if available)
		tea.ExecProcess(delegation.Command("flow", "plan", "status", "--tui"),
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
		tea.ExecProcess(delegation.Command("flow", "plan", "finish"),
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
		func() tea.Cmd {
			openCmd := delegation.Command("flow", "plan", "open")
			// Ensure the subprocess knows it's being called from a TUI
			openCmd.Env = append(os.Environ(), "GROVE_FLOW_TUI_MODE=true")

			return tea.ExecProcess(openCmd,
				func(err error) tea.Msg {
					// When plan open completes, stay in the TUI
					return nil
				})
		}(),
	)
}

func executePlanReview(plan *orchestration.Plan) tea.Cmd {
	return func() tea.Msg {
		// Set active plan for context
		if err := state.Set("flow.active_plan", plan.Name); err != nil {
			return reviewCompleteMsg{err: err}
		}

		// Execute the review command and capture output
		cmd := exec.Command("grove", "flow", "plan", "review")
		output, err := cmd.CombinedOutput()

		// If there was an error, include the command output in the error message
		if err != nil {
			outputStr := strings.TrimSpace(string(output))
			if outputStr != "" {
				err = fmt.Errorf("%w: %s", err, outputStr)
			}
		}

		return reviewCompleteMsg{
			output: string(output),
			err:    err,
		}
	}
}

// rebaseWorktreeBranch rebases a worktree's branch on the default branch.
func rebaseWorktreeBranch(worktreePath, defaultBranch string) error {
	cmd := exec.Command("git", "rebase", defaultBranch)
	cmd.Dir = worktreePath // CRITICAL: Execute the command within the worktree directory.
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to rebase worktree branch: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

// rebaseAndMergeRepo performs the git operations to rebase a worktree branch onto the default branch
// and then fast-forward the default branch. It also synchronizes the worktree's branch.
func rebaseAndMergeRepo(repoPath, worktreeBranch, defaultBranch string) error {
	// Switch back to default branch in the source repo
	checkoutCmd := exec.Command("git", "checkout", defaultBranch)
	checkoutCmd.Dir = repoPath
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout %s: %s", defaultBranch, strings.TrimSpace(string(output)))
	}

	// Fast-forward default branch to the worktree branch
	mergeCmd := exec.Command("git", "merge", "--ff-only", worktreeBranch)
	mergeCmd.Dir = repoPath
	if output, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fast-forward merge failed: %s", strings.TrimSpace(string(output)))
	}

	// Now, update the worktree's branch to point to the new HEAD of default branch.
	worktreePath := filepath.Join(repoPath, ".grove-worktrees", worktreeBranch)
	if _, err := os.Stat(worktreePath); err == nil {
		// This command must be run inside the worktree to correctly update its HEAD
		resetCmd := exec.Command("git", "reset", "--hard", defaultBranch)
		resetCmd.Dir = worktreePath
		if output, err := resetCmd.CombinedOutput(); err != nil {
			// This is not a fatal error for the merge itself, but we should warn the user.
			fmt.Printf("Warning: failed to sync worktree branch '%s': %s\n", worktreeBranch, strings.TrimSpace(string(output)))
		}
	}

	return nil
}

// getEcosystemRepoDetails fetches detailed git and merge status for each repo in an ecosystem plan.
func getEcosystemRepoDetails(plan *orchestration.Plan, worktree string, provider *workspace.Provider) ([]EcosystemRepoStatus, string) {
	if provider == nil {
		return nil, "err (no provider)"
	}
	localWorkspaces := provider.LocalWorkspaces()

	var details []EcosystemRepoStatus
	statusCounts := make(map[string]int)

	for _, repoName := range plan.Config.Repos {
		repoPath, exists := localWorkspaces[repoName]
		if !exists {
			details = append(details, EcosystemRepoStatus{Name: repoName, MergeStatus: "not found"})
			statusCounts["err"]++
			continue
		}

		gitStatus, _ := git.GetStatus(repoPath)
		mergeStatus := getMergeStatus(repoPath, worktree)
		details = append(details, EcosystemRepoStatus{
			Name:        repoName,
			MergeStatus: mergeStatus,
			GitStatus:   gitStatus,
		})
		statusCounts[mergeStatus]++
	}

	// Determine summary status
	var summaryStatus string
	if statusCounts["Conflicts"] > 0 {
		summaryStatus = fmt.Sprintf("%d Conflicts", statusCounts["Conflicts"])
	} else if statusCounts["Needs Rebase"] > 0 {
		summaryStatus = fmt.Sprintf("%d Rebase", statusCounts["Needs Rebase"])
	} else if statusCounts["Ready"] > 0 {
		summaryStatus = fmt.Sprintf("%d Ready", statusCounts["Ready"])
	} else if statusCounts["Merged"] == len(plan.Config.Repos) {
		summaryStatus = "Merged"
	} else if statusCounts["err"] > 0 {
		summaryStatus = "err"
	} else {
		summaryStatus = "Mixed"
	}

	return details, summaryStatus
}

func fastForwardUpdateCmd(plan PlanListItem) tea.Cmd {
	return func() tea.Msg {
		if plan.Worktree == "" {
			return fastForwardMsg{err: fmt.Errorf("selected plan has no associated worktree")}
		}

		// Ecosystem Plan Logic
		if plan.Plan.Config != nil && len(plan.Plan.Config.Repos) > 0 {
			var results []string
			var errors []string

			logger := logrus.New()
			logger.SetLevel(logrus.WarnLevel)
			discoveryService := workspace.NewDiscoveryService(logger)
			discoveryResult, err := discoveryService.DiscoverAll()
			if err != nil {
				return fastForwardMsg{err: fmt.Errorf("failed to discover workspaces: %w", err)}
			}
			provider := workspace.NewProvider(discoveryResult)
			localWorkspaces := provider.LocalWorkspaces()

			for _, repoName := range plan.Plan.Config.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					errors = append(errors, fmt.Sprintf("%s: repo not found locally", repoName))
					continue
				}

				worktreePath := filepath.Join(repoPath, ".grove-worktrees", plan.Worktree)
				if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
					// worktree for this specific repo doesn't exist, skip
					continue
				}

				defaultBranch := "main" // Simplified default branch logic
				if err := rebaseWorktreeBranch(worktreePath, defaultBranch); err != nil {
					errors = append(errors, fmt.Sprintf("%s: %v", repoName, err))
				} else {
					results = append(results, repoName)
				}
			}

			var summary strings.Builder
			if len(results) > 0 {
				summary.WriteString(fmt.Sprintf("Successfully updated %d repos: %s. ", len(results), strings.Join(results, ", ")))
			}
			if len(errors) > 0 {
				return fastForwardMsg{err: fmt.Errorf(summary.String()+"Failed to update %d repos: %s", len(errors), strings.Join(errors, "; "))}
			}
			return fastForwardMsg{message: summary.String()}
		}

		// Single-Repo Plan Logic
		gitRoot, err := git.GetGitRoot(".")
		if err != nil {
			return fastForwardMsg{err: fmt.Errorf("not in a git repository: %w", err)}
		}
		worktreePath := filepath.Join(gitRoot, ".grove-worktrees", plan.Worktree)

		defaultBranch := "main" // Simplified
		if err := rebaseWorktreeBranch(worktreePath, defaultBranch); err != nil {
			return fastForwardMsg{err: err}
		}

		return fastForwardMsg{message: fmt.Sprintf("Successfully updated branch '%s' from '%s'.", plan.Worktree, defaultBranch)}
	}
}

func fastForwardMainCmd(plan PlanListItem) tea.Cmd {
	return func() tea.Msg {
		if plan.Worktree == "" {
			return fastForwardMsg{err: fmt.Errorf("selected plan has no associated worktree")}
		}

		// Ecosystem Plan Logic
		if plan.Plan.Config != nil && len(plan.Plan.Config.Repos) > 0 {
			var results []string
			var errors []string

			logger := logrus.New()
			logger.SetLevel(logrus.WarnLevel)
			discoveryService := workspace.NewDiscoveryService(logger)
			discoveryResult, err := discoveryService.DiscoverAll()
			if err != nil {
				return fastForwardMsg{err: fmt.Errorf("failed to discover workspaces: %w", err)}
			}
			provider := workspace.NewProvider(discoveryResult)
			localWorkspaces := provider.LocalWorkspaces()

			for _, repoName := range plan.Plan.Config.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					errors = append(errors, fmt.Sprintf("%s: repo not found locally", repoName))
					continue
				}

				// Determine default branch for this specific repo
				defaultBranch := "main"
				if _, err := os.Stat(filepath.Join(repoPath, ".git", "refs", "heads", "main")); os.IsNotExist(err) {
					if _, err := os.Stat(filepath.Join(repoPath, ".git", "refs", "heads", "master")); err == nil {
						defaultBranch = "master"
					} else {
						errors = append(errors, fmt.Sprintf("%s: no main or master branch", repoName))
						continue
					}
				}

				if err := rebaseAndMergeRepo(repoPath, plan.Worktree, defaultBranch); err != nil {
					errors = append(errors, fmt.Sprintf("%s: %v", repoName, err))
				} else {
					results = append(results, repoName)
				}
			}

			var summary strings.Builder
			if len(results) > 0 {
				summary.WriteString(fmt.Sprintf("Successfully merged %d repos: %s. ", len(results), strings.Join(results, ", ")))
			}
			if len(errors) > 0 {
				return fastForwardMsg{err: fmt.Errorf(summary.String()+"Failed to merge %d repos: %s", len(errors), strings.Join(errors, "; "))}
			}
			return fastForwardMsg{message: summary.String()}
		}

		// Single-Repo Plan Logic
		gitRoot, err := git.GetGitRoot(".")
		if err != nil {
			return fastForwardMsg{err: fmt.Errorf("not in a git repository: %w", err)}
		}

		_, currentBranch, err := git.GetRepoInfo(gitRoot)
		if err != nil {
			return fastForwardMsg{err: fmt.Errorf("could not determine current branch: %w", err)}
		}

		defaultBranch := "main"
		checkMainCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
		checkMainCmd.Dir = gitRoot
		if checkMainCmd.Run() != nil {
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

		if err := rebaseAndMergeRepo(gitRoot, plan.Worktree, defaultBranch); err != nil {
			return fastForwardMsg{err: err}
		}

		return fastForwardMsg{message: fmt.Sprintf("Successfully merged '%s' into '%s' and synchronized the worktree.", plan.Worktree, defaultBranch)}
	}
}


