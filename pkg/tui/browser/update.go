package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/models"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/theme"
	"gopkg.in/yaml.v3"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planutil"
)

// Update handles messages and advances Model state.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case embed.FocusMsg:
		m.focused = true
		m.loading = true
		return m, tea.Batch(m.reloadPlansCmd(), fetchGitLogCmd(m.cwdGitRoot))

	case embed.BlurMsg:
		// Host withdrew focus. Pause background polling until FocusMsg
		// re-enables it. Clearing the status message avoids stale
		// text when we next get focus.
		m.focused = false
		m.statusMessage = ""
		return m, nil

	case embed.SetWorkspaceMsg:
		// Host switched workspace context. Re-resolve the plans
		// directory for the new workspace and re-fetch.
		if msg.Node == nil {
			return m, nil
		}
		m.cwdGitRoot = msg.Node.Path
		// Resolve plans directory via the same mechanism the CLI uses
		// (coreplan.ResolvePlanDir handles workspace-local paths).
		if planPath := coreplan.ResolvePlanDir(msg.Node.Path, ""); planPath != "" {
			// ResolvePlanDir returns a specific plan dir; its parent is
			// the plans directory.
			m.plansDirectory = filepath.Dir(planPath)
		}
		// Switching workspace changes the entire context; show the
		// placeholder for the new workspace's first load.
		m.loading = true
		m.initialLoaded = false
		return m, tea.Batch(m.reloadPlansCmd(), fetchGitLogCmd(m.cwdGitRoot))

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
		if msg.err != nil {
			m.statusMessage = theme.DefaultTheme.Error.Render(fmt.Sprintf("Review failed: %s", msg.err.Error()))
		} else if msg.output != "" {
			m.statusMessage = theme.DefaultTheme.Success.Render(fmt.Sprintf("%s Plan marked for review", theme.IconSuccess))
		}
		return m, m.reloadPlansCmd()

	case planIndexConnectedMsg:
		if msg.generation != m.streamGeneration {
			msg.cancel()
			return m, nil
		}
		if m.streamCancel != nil {
			m.streamCancel()
		}
		m.streamCancel = msg.cancel
		m.dataSource = "daemon live"
		// Source state and the durable status line must never contradict.
		m.statusMessage = ""
		if msg.snapshot != nil {
			m.hasDaemonSnapshot = true
			m.planIndexRevision = msg.snapshot.Revision
			m.planSummaries = make(map[string]models.PlanSummary, len(msg.snapshot.Plans))
			for _, summary := range msg.snapshot.Plans {
				m.planSummaries[summary.PlanDir] = summary
			}
		}
		m.loading = true
		return m, tea.Batch(m.reloadPlansCmd(), listenPlanIndexCmd(msg.updates, msg.generation))

	case planIndexConnectFailedMsg:
		if msg.generation != m.streamGeneration {
			return m, nil
		}
		m.loading = !m.hasDaemonSnapshot
		if m.hasDaemonSnapshot {
			m.dataSource = "stale · reconnecting"
			m.statusMessage = ""
			return m, planIndexReconnectTick()
		}
		m.dataSource = "local fallback — daemon unavailable"
		m.statusMessage = m.dataSource
		return m, tea.Batch(
			loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived),
			fallbackRefreshTick(),
			planIndexReconnectTick(),
		)

	case planIndexStreamClosedMsg:
		if msg.generation != m.streamGeneration {
			return m, nil
		}
		m.streamCancel = nil
		if m.hasDaemonSnapshot {
			m.dataSource = "stale · reconnecting"
			m.statusMessage = ""
			return m, planIndexReconnectTick()
		}
		m.dataSource = "local fallback — daemon disconnected"
		m.statusMessage = m.dataSource
		return m, tea.Batch(fallbackRefreshTick(), planIndexReconnectTick())

	case planIndexReconnectMsg:
		factory := m.daemonClientFactory()
		if factory == nil || m.dataSource == "daemon live" || m.dataSource == "connecting" {
			return m, nil
		}
		m.dataSource = "connecting"
		m.streamGeneration++
		return m, connectPlanIndexCmd(factory, m.streamGeneration)

	case planIndexStreamMsg:
		if msg.generation != m.streamGeneration {
			return m, nil
		}
		cmds := []tea.Cmd{listenPlanIndexCmd(msg.updates, msg.generation)}
		if snapshot := msg.update.PlanIndexSnapshot; snapshot != nil && snapshot.Revision > m.planIndexRevision {
			m.planIndexRevision = snapshot.Revision
			m.planSummaries = make(map[string]models.PlanSummary, len(snapshot.Plans))
			for _, summary := range snapshot.Plans {
				m.planSummaries[summary.PlanDir] = summary
			}
			m.loading = true
			cmds = append(cmds, m.reloadPlansCmd())
		}
		if delta := msg.update.PlanIndex; delta != nil {
			if delta.Revision != m.planIndexRevision+1 {
				// Pub/sub intentionally drops for slow readers. Reconnect performs a
				// full snapshot fetch rather than guessing across the gap.
				if m.streamCancel != nil {
					m.streamCancel()
					m.streamCancel = nil
				}
				m.dataSource = "connecting"
				m.streamGeneration++
				return m, connectPlanIndexCmd(m.daemonClientFactory(), m.streamGeneration)
			}
			m.planIndexRevision = delta.Revision
			for _, dir := range delta.Removed {
				delete(m.planSummaries, dir)
			}
			for _, summary := range delta.Upserts {
				m.planSummaries[summary.PlanDir] = summary
			}
			m.loading = true
			cmds = append(cmds, m.reloadPlansCmd())
		}
		return m, tea.Batch(cmds...)

	case planListLoadCompleteMsg:
		// A local load started before daemon recovery, or a projection from an
		// older index revision, must not replace the qualified portfolio.
		if m.hasDaemonSnapshot && !msg.portfolio {
			return m, nil
		}
		if msg.portfolio && msg.portfolioGeneration != 0 && msg.portfolioGeneration != m.streamGeneration {
			return m, nil
		}
		if msg.portfolio && msg.planIndexRevision != 0 && msg.planIndexRevision != m.planIndexRevision {
			return m, nil
		}
		m.loading = false
		// Mark the context loaded regardless of success/failure so a
		// transient failing background tick doesn't drop the user back to
		// the loading placeholder for an already-populated list.
		m.initialLoaded = true
		if msg.error != nil {
			m.err = msg.error
			return m, nil
		}
		// Clear any prior error on a successful (re)load — recovery path.
		m.err = nil
		// Refresh sorting is updated-time based, so preserving only the numeric
		// cursor can silently switch the highlighted plan. Re-match by identity.
		selectedKey := m.selectedPlanKey()
		m.plans = msg.plans
		if selectedKey != "" {
			m.selectPlanKey(selectedKey)
		}
		activePlan, _ := state.GetString(stateDir(), coreplan.StateKey)
		m.activePlan = activePlan
		if m.cursor >= len(m.plans) {
			m.cursor = len(m.plans) - 1
		}
		if m.cursor < 0 && len(m.plans) > 0 {
			m.cursor = 0
		}
		return m, nil

	case refreshTickMsg:
		// Skip heavy I/O if a load is already in-flight or the panel
		// isn't focused. Keep the tick heartbeat running so resuming
		// after focus/idle happens on the next period.
		if m.dataSource == "daemon live" || m.dataSource == "connecting" || m.hasDaemonSnapshot {
			return m, nil
		}
		if m.loading || !m.focused {
			return m, fallbackRefreshTick()
		}
		m.loading = true
		return m, tea.Batch(
			loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived),
			fetchGitLogCmd(m.cwdGitRoot),
			fallbackRefreshTick(),
		)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.SetSize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	}

	return m, nil
}

// handleKeyMsg dispatches a keyboard event against the current mode
// (help overlay, notes editor, normal browse). Split from Update so the
// main switch stays readable.
func (m Model) reloadPlansCmd() tea.Cmd {
	if m.hasDaemonSnapshot {
		// Commands run concurrently with later Update calls. Copy the revisioned
		// projection so a delta cannot mutate a map while an older load reads it.
		summaries := make(map[string]models.PlanSummary, len(m.planSummaries))
		for key, summary := range m.planSummaries {
			summaries[key] = summary
		}
		return loadPortfolioCmd(summaries, m.showOnHold, m.planIndexRevision, m.streamGeneration)
	}
	return loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived)
}

func (m Model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.help.ShowAll {
		// Any key closes help.
		m.help.ShowAll = false
		return m, nil
	}

	if m.editingNotes {
		switch msg.String() {
		case "enter":
			if m.editPlanIndex >= 0 && m.editPlanIndex < len(m.plans) {
				plan := m.plans[m.editPlanIndex].Plan
				newNotes := m.notesInput.Value()

				if plan.Config == nil {
					plan.Config = &orchestration.PlanConfig{}
				}
				plan.Config.Notes = newNotes

				configPath := filepath.Join(plan.Directory, ".grove-plan.yml")
				data, err := yaml.Marshal(plan.Config)
				if err == nil {
					_ = os.WriteFile(configPath, data, 0o600)
				}
				m.plans[m.editPlanIndex].Notes = newNotes
			}
			m.editingNotes = false
			return m, nil

		case "esc":
			m.editingNotes = false
			return m, nil

		default:
			var cmd tea.Cmd
			m.notesInput, cmd = m.notesInput.Update(msg)
			return m, cmd
		}
	}

	switch {
	case key.Matches(msg, m.keys.Quit), msg.String() == "ctrl+c":
		if m.embedMode {
			return m, func() tea.Msg { return embed.CloseRequestMsg{} }
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.help.Toggle()
		return m, nil

	case key.Matches(msg, m.keys.NewPlan):
		return m, executeNewPlan()

	case key.Matches(msg, m.keys.Up):
		m.statusMessage = ""
		if m.inRepoNavigationMode {
			if m.repoCursor > 0 {
				m.repoCursor--
				if c := m.fetchSelectedRepoGitLog(); c != nil {
					return m, c
				}
			}
		} else if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		m.statusMessage = ""
		if m.inRepoNavigationMode {
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				selectedPlan := m.plans[m.cursor]
				if m.repoCursor < len(selectedPlan.EcosystemRepoStatuses)-1 {
					m.repoCursor++
					if c := m.fetchSelectedRepoGitLog(); c != nil {
						return m, c
					}
				}
			}
		} else if m.cursor < len(m.plans)-1 {
			m.cursor++
		}
		return m, nil

	case key.Matches(msg, m.keys.ViewPlan):
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			plan := m.plans[m.cursor]
			return m, func() tea.Msg {
				return BrowserPlanSelectedMsg{
					PlanName: plan.Name,
					PlanPath: plan.Plan.Directory,
					Plan:     plan.Plan,
				}
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.OpenPlan):
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			plan := m.plans[m.cursor]
			return m, executePlanOpen(plan.Plan)
		}
		return m, nil

	case key.Matches(msg, m.keys.SetActive):
		if m.selectedArchived() {
			m.statusMessage = archivedReadOnlyMessage
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			selectedPlan := m.plans[m.cursor]
			stateRoot := selectedPlan.WorkspaceRoot
			if stateRoot == "" {
				stateRoot = stateDir()
			}
			if err := state.Set(stateRoot, coreplan.StateKey, selectedPlan.Name); err == nil {
				m.activePlan = selectedPlan.Name
				for i := range m.plans {
					if m.plans[i].WorkspaceRoot == selectedPlan.WorkspaceRoot {
						m.plans[i].Selected = i == m.cursor
					}
				}
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.ReviewPlan):
		if m.selectedArchived() {
			m.statusMessage = archivedReadOnlyMessage
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			plan := m.plans[m.cursor]
			return m, executePlanReview(plan.Plan)
		}
		return m, nil

	case key.Matches(msg, m.keys.EditNotes):
		if m.selectedArchived() {
			m.statusMessage = archivedReadOnlyMessage
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) && !m.editingNotes {
			m.editingNotes = true
			m.editPlanIndex = m.cursor
			ti := textinput.New()
			ti.Placeholder = "Enter notes for this plan..."
			ti.Focus()
			ti.CharLimit = 200
			ti.Width = 50
			if m.plans[m.cursor].Notes != "" {
				ti.SetValue(m.plans[m.cursor].Notes)
			}
			m.notesInput = ti
			return m, textinput.Blink
		}
		return m, nil

	case key.Matches(msg, m.keys.FinishPlan):
		if m.selectedArchived() {
			m.statusMessage = archivedReadOnlyMessage
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			plan := m.plans[m.cursor]
			return m, executePlanFinish(plan.Plan)
		}
		return m, nil

	case key.Matches(msg, m.keys.FastForwardUpdate):
		if m.selectedArchived() {
			m.statusMessage = archivedReadOnlyMessage
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			selectedPlan := m.plans[m.cursor]
			if selectedPlan.MergeStatus == "Needs Rebase" || selectedPlan.MergeStatus == "Behind" ||
				strings.Contains(selectedPlan.MergeStatus, "Rebase") || strings.Contains(selectedPlan.MergeStatus, "Behind") {
				m.statusMessage = "Updating branch from main..."
				return m, fastForwardUpdateCmd(selectedPlan)
			}
			m.statusMessage = theme.DefaultTheme.Error.Render("Branch is not in a state that can be updated (status: " + selectedPlan.MergeStatus + ")")
		}
		return m, nil

	case key.Matches(msg, m.keys.FastForwardMain):
		if m.selectedArchived() {
			m.statusMessage = archivedReadOnlyMessage
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			selectedPlan := m.plans[m.cursor]
			if selectedPlan.MergeStatus != "Ready" && !strings.Contains(selectedPlan.MergeStatus, "Ready") {
				m.statusMessage = theme.DefaultTheme.Error.Render(fmt.Sprintf("Cannot merge: branch is not ready (status: %s). Use 'U' to update first.", selectedPlan.MergeStatus))
				return m, nil
			}
			m.statusMessage = "Merging branch to main..."
			return m, fastForwardMainCmd(selectedPlan)
		}
		return m, nil

	case key.Matches(msg, m.keys.ToggleGitLog):
		if m.inRepoNavigationMode {
			m.inRepoNavigationMode = false
			m.showGitLog = false
			m.repoCursor = 0
			m.repoGitLogContent = ""
			m.repoGitLogError = nil
		} else if m.showGitLog {
			m.showGitLog = false
		} else {
			m.showGitLog = true
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				selectedPlan := m.plans[m.cursor]
				if len(selectedPlan.EcosystemRepoStatuses) > 0 {
					m.inRepoNavigationMode = true
					m.repoCursor = 0
					if c := m.fetchSelectedRepoGitLog(); c != nil {
						m.statusMessage = ""
						return m, c
					}
				}
			}
		}
		m.statusMessage = ""
		return m, nil

	case key.Matches(msg, m.keys.ToggleHold):
		m.showOnHold = !m.showOnHold
		m.cursor = 0
		m.statusMessage = fmt.Sprintf("On-hold plans: %v", m.showOnHold)
		return m, m.reloadPlansCmd()

	case key.Matches(msg, m.keys.ToggleArchived):
		m.showArchived = !m.showArchived
		m.cursor = 0
		m.statusMessage = fmt.Sprintf("Archived plans: %v", m.showArchived)
		return m, loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived)

	case key.Matches(msg, m.keys.SetHoldStatus):
		if m.selectedArchived() {
			m.statusMessage = archivedReadOnlyMessage
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			selectedPlan := m.plans[m.cursor]
			plan := selectedPlan.Plan

			currentStatus := ""
			if plan.Config != nil {
				currentStatus = plan.Config.Status
			}

			hold := currentStatus != "hold"
			action := "set to"
			if !hold {
				action = "removed from"
			}

			if err := orchestration.SetHold(plan.Directory, hold); err != nil {
				m.statusMessage = fmt.Sprintf("Failed to update plan: %v", err)
			} else {
				m.statusMessage = fmt.Sprintf("Plan '%s' %s hold", selectedPlan.Name, action)
			}

			return m, loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived)
		}
		return m, nil
	}

	return m, nil
}

// archivedReadOnlyMessage is shown when a mutating row-action is refused
// because the selected plan lives in the archive.
const archivedReadOnlyMessage = "plan is archived (read-only)"

// selectedArchived reports whether the row under the cursor is an
// archived plan. Mutating row-actions (finish, set-active, review,
// notes, hold, merge/update) are refused for archived rows; viewing
// (Enter) and the git-log toggle remain available.
func (m Model) selectedArchived() bool {
	return m.cursor >= 0 && m.cursor < len(m.plans) && m.plans[m.cursor].Archived
}

// fetchSelectedRepoGitLog resolves the repo path for the repo currently
// highlighted in ecosystem navigation mode and returns a git-log fetch
// tea.Cmd, or nil if the repo cannot be located.
func (m Model) fetchSelectedRepoGitLog() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.plans) {
		return nil
	}
	selectedPlan := m.plans[m.cursor]
	if m.repoCursor < 0 || m.repoCursor >= len(selectedPlan.EcosystemRepoStatuses) {
		return nil
	}
	repoStatus := selectedPlan.EcosystemRepoStatuses[m.repoCursor]

	provider, err := planutil.DiscoverWorkspaceProvider()
	if err != nil {
		return nil
	}
	ecosystemRoot, _ := git.GetGitRoot(selectedPlan.Plan.Directory)
	localWorkspaces := provider.LocalWorkspacesInEcosystem(ecosystemRoot)

	// Resolve the anchored container the same way the status load does, then
	// pick the repo's in-container checkout. A bare provider map lookup misses
	// repos that only live inside an anchored/XDG container (no main-checkout
	// entry), which left the git-log pane empty for those groups.
	worktreePath, _ := planutil.ResolveWorktreePath(ecosystemRoot, selectedPlan.Worktree, provider)
	checkPath := planutil.ResolveRepoCheckout(repoStatus.Name, localWorkspaces[repoStatus.Name], worktreePath)
	if checkPath == "" {
		return nil
	}
	return fetchRepoGitLogCmd(checkPath)
}
