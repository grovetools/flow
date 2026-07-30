package browser

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/models"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/keymap"
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
		m.loadGeneration++
		// Returning from Git Viewer completes the adapter lifecycle. Restore the
		// exact qualified row that launched U/M before refreshing it; duplicate
		// handoffs stay blocked until this point.
		if m.refreshPlanKey != "" {
			m.selectPlanKey(m.refreshPlanKey)
			delete(m.actionPending, m.refreshPlanKey)
			m.refreshPlanKey = ""
		}
		return m, m.reloadPlansCmd()

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
		// Resolve plans directory via the same mechanism the CLI uses at
		// launch (cmd/plan_tui.go). Note ResolvePlanDir(dir, "") returns the
		// plans directory itself — filepath.Join(x, "") == Clean(x) — so
		// taking its parent landed one directory too high and scoped the
		// panel at the workspace, matching no plan summary at all.
		if plansDir := coreplan.ResolvePlansDir(msg.Node.Path); plansDir != "" {
			m.plansDirectory = plansDir
		}
		// Switching workspace changes the entire context; show the
		// placeholder for the new workspace's first load.
		m.loading = true
		m.initialLoaded = false
		m.gitLogLoaded = false
		m.gitLogContent = ""
		m.gitLogError = nil
		m.loadGeneration++
		return m, m.reloadPlansCmd()

	case gitLogMsg:
		m.gitLogContent = msg.content
		m.gitLogError = msg.err
		m.gitLogLoaded = true
		return m, nil

	case holdCompleteMsg:
		if _, pending := m.holdPending[msg.key]; !pending {
			return m, nil
		}
		delete(m.holdPending, msg.key)
		if msg.err != nil {
			m.statusMessage = theme.DefaultTheme.Error.Render(fmt.Sprintf("Hold update failed: %v", msg.err))
			return m, nil
		}
		idx := -1
		for i := range m.plans {
			if planItemKey(m.plans[i]) == msg.key {
				idx = i
				break
			}
		}
		if idx >= 0 && m.plans[idx].Plan != nil {
			if m.plans[idx].Plan.Config == nil {
				m.plans[idx].Plan.Config = &orchestration.PlanConfig{}
			}
			if msg.hold {
				m.plans[idx].Plan.Config.Status = "hold"
			} else {
				m.plans[idx].Plan.Config.Status = ""
			}
			m.plans[idx].ReviewStatus = formatConfigStatus(m.plans[idx].Plan.Config)
		}
		if msg.hold && !m.showOnHold && idx >= 0 {
			m.plans = append(m.plans[:idx], m.plans[idx+1:]...)
			if m.cursor > idx {
				m.cursor--
			}
			m.ensureCursorVisible()
		}
		action := "held"
		if !msg.hold {
			action = "unheld"
		}
		m.statusMessage = fmt.Sprintf("Plan %s", action)
		if m.hasDaemonSnapshot {
			return m, nil
		}
		return m, m.reloadPlansCmd()

	case bulkPreviewMsg:
		if msg.generation != m.bulkGeneration {
			return m, nil
		}
		m.bulkPending = false
		m.bulkCandidates = msg.candidates
		m.bulkSkipped = msg.skipped
		if len(msg.candidates) == 0 {
			m.bulkConfirming = len(msg.skipped) > 0
			m.statusMessage = theme.DefaultTheme.Warning.Render("No plan can be fast-forwarded")
			return m, nil
		}
		m.bulkConfirming = true
		m.statusMessage = ""
		return m, nil

	case bulkResultMsg:
		if msg.generation != m.bulkGeneration {
			return m, nil
		}
		m.bulkPending = false
		m.bulkConfirming = false
		m.bulkCandidates = nil
		m.bulkSkipped = nil
		m.statusMessage = bulkResultSummary(msg)
		m.loading = true
		m.loadGeneration++
		return m, m.reloadPlansCmd()

	case repoGitLogMsg:
		m.repoGitLogContent = msg.content
		m.repoGitLogError = msg.err
		return m, nil

	case planDetailMsg:
		if msg.generation != m.detailGeneration || msg.key != m.detailPendingKey || msg.key != m.selectedPlanKey() {
			return m, nil
		}
		m.detailPendingKey = ""
		m.statusMessage = ""
		if msg.err != nil {
			m.statusMessage = theme.DefaultTheme.Error.Render(fmt.Sprintf("Live detail unavailable: %v", msg.err))
			return m, nil
		}
		for i := range m.plans {
			if planItemKey(m.plans[i]) != msg.key {
				continue
			}
			// Preserve the daemon-qualified identity fields; the live load owns
			// only expensive selected-row Git/merge/detail values.
			msg.item.Workspace = m.plans[i].Workspace
			msg.item.WorkspaceRoot = m.plans[i].WorkspaceRoot
			msg.item.Repositories = m.plans[i].Repositories
			msg.item.Selected = m.plans[i].Selected
			msg.item.Key = m.plans[i].Key
			msg.item.Binding = m.plans[i].Binding
			msg.item.ActionTarget = m.plans[i].ActionTarget
			msg.item.Archived = m.plans[i].Archived
			m.plans[i] = msg.item
			break
		}
		if len(msg.item.EcosystemRepoStatuses) > 0 {
			m.inRepoNavigationMode = true
			m.repoCursor = 0
			return m, m.fetchSelectedRepoGitLog()
		}
		return m, fetchGitLogCmd(msg.item.WorkspaceRoot)

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
		m.streamConnecting = false
		if msg.err != nil {
			msg.cancel()
			m.loading = !m.hasDaemonSnapshot
			m.statusMessage = ""
			if m.hasDaemonSnapshot {
				m.dataSource = "stale · reconnecting"
				return m, planIndexReconnectTick()
			}
			m.dataSource = "local fallback — daemon unavailable"
			return m, tea.Batch(
				loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived, m.loadGeneration),
				fallbackRefreshTick(),
				planIndexReconnectTick(),
			)
		}
		if m.streamCancel != nil {
			m.streamCancel()
		}

		// Commit the source, revision and hydrated rows as one state transition.
		// Until this point reconnect continues rendering the retained stale
		// portfolio; it never advertises live data over an older projection.
		selectedKey := m.selectedPlanKey()
		m.streamCancel = msg.cancel
		m.dataSource = "daemon live"
		m.statusMessage = ""
		m.hasDaemonSnapshot = true
		m.planSummaries = make(map[string]models.PlanSummary)
		if msg.snapshot != nil {
			m.planIndexRevision = msg.snapshot.Revision
			m.planSummaries = make(map[string]models.PlanSummary, len(msg.snapshot.Plans))
			for _, summary := range msg.snapshot.Plans {
				m.planSummaries[summary.PlanDir] = summary
			}
		}
		m = m.replacePlanRows(msg.plans, selectedKey)
		if msg.snapshot != nil {
			m.armRenderProbe(msg.snapshot.ScannedAt, msg.snapshot.Revision)
		}
		return m, listenPlanIndexCmd(msg.updates, msg.generation)

	case planIndexConnectFailedMsg:
		if msg.generation != m.streamGeneration {
			return m, nil
		}
		m.streamConnecting = false
		m.loading = !m.hasDaemonSnapshot
		if m.hasDaemonSnapshot {
			m.dataSource = "stale · reconnecting"
			m.statusMessage = ""
			return m, planIndexReconnectTick()
		}
		m.dataSource = "local fallback — daemon unavailable"
		m.statusMessage = ""
		return m, tea.Batch(
			loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived, m.loadGeneration),
			fallbackRefreshTick(),
			planIndexReconnectTick(),
		)

	case planIndexStreamClosedMsg:
		if msg.generation != m.streamGeneration {
			return m, nil
		}
		m.streamConnecting = false
		m.streamCancel = nil
		if m.hasDaemonSnapshot {
			m.dataSource = "stale · reconnecting"
			m.statusMessage = ""
			return m, planIndexReconnectTick()
		}
		m.dataSource = "local fallback — daemon disconnected"
		m.statusMessage = ""
		return m, tea.Batch(fallbackRefreshTick(), planIndexReconnectTick())

	case planIndexReconnectMsg:
		factory := m.daemonClientFactory()
		if factory == nil || m.dataSource == "daemon live" || m.streamConnecting {
			return m, nil
		}
		if m.hasDaemonSnapshot {
			m.dataSource = "stale · reconnecting"
		} else {
			m.dataSource = "connecting"
		}
		m.statusMessage = ""
		m.streamConnecting = true
		m.streamGeneration++
		return m, connectPlanIndexCmd(factory, m.streamGeneration, m.plansDirectory, m.showOnHold, m.showArchived)

	case planIndexStreamMsg:
		if msg.generation != m.streamGeneration {
			return m, nil
		}
		cmds := []tea.Cmd{listenPlanIndexCmd(msg.updates, msg.generation)}
		if snapshot := msg.update.PlanIndexSnapshot; snapshot != nil && snapshot.Revision > m.planIndexRevision {
			m.planIndexRevision = snapshot.Revision
			m.latestSnapshotAt = snapshot.ScannedAt
			m.planSummaries = make(map[string]models.PlanSummary, len(snapshot.Plans))
			for _, summary := range snapshot.Plans {
				m.planSummaries[summary.PlanDir] = summary
			}
			m.loading = true
			cmds = append(cmds, m.reloadPlansCmd())
		}
		if delta := msg.update.PlanIndex; delta != nil {
			// Stream subscription precedes snapshot fetch. A revision already
			// represented by that snapshot can therefore be buffered on the stream;
			// ignore it instead of misclassifying it as a gap and reconnecting forever.
			if delta.Revision <= m.planIndexRevision {
				return m, tea.Batch(cmds...)
			}
			firstRevision := msg.firstRevision
			if firstRevision == 0 {
				firstRevision = delta.Revision
			}
			if firstRevision > m.planIndexRevision+1 {
				// Pub/sub intentionally drops for slow readers. Reconnect performs a
				// full snapshot fetch rather than guessing across the gap.
				if m.streamCancel != nil {
					m.streamCancel()
					m.streamCancel = nil
				}
				m.dataSource = "stale · reconnecting"
				m.statusMessage = ""
				m.streamConnecting = true
				m.streamGeneration++
				return m, connectPlanIndexCmd(m.daemonClientFactory(), m.streamGeneration, m.plansDirectory, m.showOnHold, m.showArchived)
			}
			m.planIndexRevision = delta.Revision
			m.latestSnapshotAt = delta.ScannedAt
			for _, dir := range delta.Removed {
				delete(m.planSummaries, dir)
			}
			for _, summary := range delta.Upserts {
				// Plan-index deltas do not carry a fresh note-index join. Preserve
				// the tagged-note count attached during the baseline fetch.
				if previous, ok := m.planSummaries[summary.PlanDir]; ok && summary.NoteCount == 0 {
					summary.NoteCount = previous.NoteCount
				}
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
		if !msg.portfolio && msg.loadGeneration != 0 && msg.loadGeneration != m.loadGeneration {
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
		m = m.replacePlanRows(msg.plans, m.selectedPlanKey())
		if msg.portfolio {
			m.armRenderProbe(m.latestSnapshotAt, m.planIndexRevision)
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
		m.loadGeneration++
		return m, tea.Batch(
			loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived, m.loadGeneration),
			fallbackRefreshTick(),
		)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.SetSize(msg.Width, msg.Height)
		m.ensureCursorVisible()
		return m, nil

	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	}

	return m, nil
}

// handleKeyMsg dispatches a keyboard event against the current mode
// (help overlay, notes editor, normal browse). Split from Update so the
// main switch stays readable.
func (m Model) replacePlanRows(plans []PlanListItem, selectedKey string) Model {
	m.loading = false
	m.initialLoaded = true
	m.err = nil
	m.plans = m.visiblePlans(plans)
	if selectedKey != "" {
		m.selectPlanKey(selectedKey)
	}
	// Resolve against the panel's workspace, never the treemux process CWD.
	// ActivePlanForPath gives worktree registry identity precedence over mutable
	// state, so a plan worktree always highlights the plan it was created for.
	activePath := m.cwdGitRoot
	if activePath == "" {
		activePath = stateDir()
	}
	m.activePlan = coreplan.ActivePlanForPath(activePath)
	if m.cursor >= len(m.plans) {
		m.cursor = len(m.plans) - 1
	}
	if m.cursor < 0 && len(m.plans) > 0 {
		m.cursor = 0
	}
	m.ensureCursorVisible()
	return m
}

func (m Model) reloadPlansCmd() tea.Cmd {
	if m.hasDaemonSnapshot {
		// Commands run concurrently with later Update calls. Copy the revisioned
		// projection so a delta cannot mutate a map while an older load reads it.
		summaries := make(map[string]models.PlanSummary, len(m.planSummaries))
		for key, summary := range m.planSummaries {
			summaries[key] = summary
		}
		return loadPortfolioCmd(scopedPlanSummaries(summaries, m.plansDirectory), m.showOnHold, m.showArchived, m.planIndexRevision, m.streamGeneration)
	}
	return loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived, m.loadGeneration)
}

func (m Model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.columnSelectMode {
		switch msg.String() {
		// esc closes the column selector. The old flat "T" second exit was
		// dropped with the T -> tc rebind (chord-only, sign-off E4); a chord
		// cannot arm inside this modal anyway.
		case "esc":
			m.columnSelectMode = false
		case "up", "k":
			if m.columnCursor > 0 {
				m.columnCursor--
			}
		case "down", "j":
			if m.columnCursor < len(browserOptionalColumns)-1 {
				m.columnCursor++
			}
		case "enter", " ":
			name := browserOptionalColumns[m.columnCursor]
			m.columnVisibility[name] = !m.columnVisible(name)
			_ = saveColumnVisibility(m.columnVisibility)
		}
		return m, nil
	}

	if m.help.ShowAll {
		// Any key closes help.
		m.help.ShowAll = false
		return m, nil
	}

	if m.bulkConfirming {
		// A confirmed sweep rebases every listed plan, so only an explicit
		// accept starts it; unrecognized keys are swallowed rather than
		// guessed at in either direction.
		switch msg.String() {
		case "y", "Y", "enter":
			if len(m.bulkCandidates) == 0 {
				m.bulkConfirming = false
				m.statusMessage = ""
				return m, nil
			}
			candidates := m.bulkCandidates
			m.bulkConfirming = false
			m.bulkPending = true
			m.statusMessage = fmt.Sprintf("Fast-forwarding %s…", pluralize(len(candidates), "plan"))
			return m, executeBulkFastForwardCmd(candidates, m.bulkGeneration)
		case "esc", "n", "N", "q":
			m.bulkConfirming = false
			m.bulkCandidates = nil
			m.bulkSkipped = nil
			m.statusMessage = "Fast-forward cancelled"
			return m, nil
		}
		return m, nil
	}

	if m.editingNotes {
		switch msg.String() {
		case "enter":
			if m.editPlanIndex >= 0 && m.editPlanIndex < len(m.plans) {
				plan := m.plans[m.editPlanIndex].Plan
				newNotes := m.notesInput.Value()
				hadPlanNote := plan.Config != nil && plan.Config.Notes != ""

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
				hasPlanNote := newNotes != ""
				if hadPlanNote != hasPlanNote {
					if hasPlanNote {
						m.plans[m.editPlanIndex].NoteCount++
					} else if m.plans[m.editPlanIndex].NoteCount > 0 {
						m.plans[m.editPlanIndex].NoteCount--
					}
				}
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

	// A Model built as a bare struct literal (this package's tests do that
	// extensively) has a zero-value host, and ProcessChord is NOT nil-safe the
	// way IsPending/Armed are — it dereferences Sequence. Build the default
	// host on first use so the seam behaves identically however the Model was
	// constructed.
	if m.whichKey.Sequence == nil {
		m.whichKey = keymap.NewWhichKeyHost(nil, m.keys.Namespaces()...)
	}

	// ── Chord seam ───────────────────────────────────────────────────────
	// The reusable which-key host resolves the gg motion (passed as the flat
	// extra) plus the t…/v…/c… namespace chords, arms the popup on a pending
	// prefix, and consumes esc-cancel / stray-while-armed. Top-level-only
	// arming (sign-off E3) comes free: every modal early-return above
	// (column-select, help, bulk-confirm, notes editor) runs first, so a chord
	// can never arm inside one.
	{
		res, matched, chordCmd := m.whichKey.ProcessChord(msg, m.keys.Top)
		switch res {
		case keymap.ChordMatched:
			// Re-synthesize the resolved chord's canonical key so the switch
			// below resolves it via key.Matches — but only when the pressed key
			// is not already one of the binding's keys. Top carries both "gg"
			// and the retained flat "home"; rewriting a "home" press to "gg"
			// would be harmless here but is wrong in general.
			if len(matched.Keys()) > 0 && !key.Matches(msg, matched) {
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(matched.Keys()[0])}
			}
		case keymap.ChordPending:
			// Armed; chordCmd is the delayed popup re-render tick for a
			// namespace prefix (nil for the flat gg motion).
			return m, chordCmd
		case keymap.ChordConsumed:
			// esc dismissed the popup, or a stray key closed an armed namespace
			// menu — swallow it.
			return m, nil
		case keymap.ChordNone:
			// Not a chord — fall through unchanged.
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

	case key.Matches(msg, m.keys.ToggleColumns):
		m.columnSelectMode = true
		m.columnCursor = 0
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
			m.ensureCursorVisible()
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
			m.ensureCursorVisible()
		}
		return m, nil

	case key.Matches(msg, m.keys.PageUp):
		page := m.visibleRowCount()
		m.cursor -= page
		m.scrollOffset -= page
		m.ensureCursorVisible()
		return m, nil

	case key.Matches(msg, m.keys.PageDown):
		page := m.visibleRowCount()
		m.cursor += page
		m.scrollOffset += page
		m.ensureCursorVisible()
		return m, nil

	case key.Matches(msg, m.keys.Top):
		m.cursor = 0
		m.ensureCursorVisible()
		return m, nil

	case key.Matches(msg, m.keys.Bottom):
		m.cursor = len(m.plans) - 1
		m.ensureCursorVisible()
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

	case key.Matches(msg, m.keys.ViewGit):
		if !m.selectedBindingValid("inspect git") {
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			target := m.plans[m.cursor].ActionTarget
			if target.PlanDir == "" || target.ContainerPath == "" {
				m.statusMessage = theme.DefaultTheme.Error.Render("Cannot inspect git: qualified workspace target unavailable")
				return m, nil
			}
			return m, func() tea.Msg {
				return embed.OpenGitRequest{Target: target, Operation: embed.GitOperationInspect}
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.OpenPlan):
		if !m.selectedBindingValid("open") {
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			plan := m.plans[m.cursor]
			if m.hosted {
				workspacePath := plan.ActionTarget.ContainerPath
				if workspacePath == "" {
					workspacePath = plan.Binding.ContainerPath
				}
				if workspacePath == "" && plan.Name == RollingPlanName {
					workspacePath = plan.WorkspaceRoot
				}
				return m, func() tea.Msg {
					return embed.SwitchWorkspaceRequestMsg{Path: workspacePath, FocusPanel: "shell"}
				}
			}
			return m, executePlanOpen(plan.Plan)
		}
		return m, nil

	case key.Matches(msg, m.keys.SetActive):
		if m.selectedArchived() {
			m.statusMessage = archivedReadOnlyMessage
			return m, nil
		}
		if !m.selectedBindingValid("set active") {
			return m, nil
		}
		if fixedPlan, fixed := fixedWorktreePlan(m.cwdGitRoot); fixed {
			m.activePlan = fixedPlan
			m.statusMessage = fmt.Sprintf("Active plan is fixed by this worktree: %s", fixedPlan)
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			selectedPlan := m.plans[m.cursor]
			stateRoot := m.cwdGitRoot
			if stateRoot == "" {
				stateRoot = stateDir()
			}
			if err := state.Set(stateRoot, coreplan.StateKey, selectedPlan.Name); err == nil {
				m.activePlan = selectedPlan.Name
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
		if !m.selectedBindingValid("finish") {
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(m.plans) {
			plan := m.plans[m.cursor]
			if m.hosted {
				workspacePath := plan.ActionTarget.ContainerPath
				if workspacePath == "" {
					workspacePath = plan.Binding.ContainerPath
				}
				if workspacePath == "" && plan.Name == RollingPlanName {
					workspacePath = plan.WorkspaceRoot
				}
				return m, func() tea.Msg {
					return embed.SwitchWorkspaceRequestMsg{
						Path: workspacePath, FocusPanel: "plan",
						FocusTabIndex: 4, HasFocusTab: true,
					}
				}
			}
			return m, func() tea.Msg {
				return BrowserPlanFinishRequestedMsg{
					PlanName: plan.Name,
					PlanPath: plan.Plan.Directory,
					Plan:     plan.Plan,
				}
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.FastForwardUpdate), key.Matches(msg, m.keys.FastForwardMain):
		action := "update"
		operation := embed.GitOperationUpdateOnly
		if key.Matches(msg, m.keys.FastForwardMain) {
			action = "land"
			operation = embed.GitOperationLand
		}
		if !m.selectedBindingValid(action) {
			return m, nil
		}
		if m.cursor < 0 || m.cursor >= len(m.plans) {
			return m, nil
		}
		item := m.plans[m.cursor]
		key := planItemKey(item)
		target := item.ActionTarget
		if key == "" || target.PlanDir == "" || target.ContainerPath == "" || len(target.Repos) == 0 {
			m.statusMessage = theme.DefaultTheme.Error.Render("Cannot " + action + ": exact qualified repository target unavailable")
			return m, nil
		}
		if m.actionPending == nil {
			m.actionPending = make(map[string]embed.GitOperation)
		}
		if pending, ok := m.actionPending[key]; ok {
			m.statusMessage = theme.DefaultTheme.Warning.Render("Plan action already in flight: " + string(pending))
			return m, nil
		}
		m.actionPending[key] = operation
		m.refreshPlanKey = key
		m.statusMessage = "Opening fresh " + action + " preview…"
		return m, func() tea.Msg {
			return embed.OpenGitRequest{Target: target, Operation: operation}
		}

	case key.Matches(msg, m.keys.FastForwardAll):
		if m.bulkPending {
			m.statusMessage = theme.DefaultTheme.Warning.Render("Bulk fast-forward already in flight")
			return m, nil
		}
		targets, skipped := m.collectBulkTargets()
		if len(targets) == 0 {
			m.bulkCandidates = nil
			m.bulkSkipped = skipped
			m.bulkConfirming = len(skipped) > 0
			m.statusMessage = theme.DefaultTheme.Warning.Render("No plan can be fast-forwarded")
			return m, nil
		}
		m.bulkGeneration++
		m.bulkPending = true
		m.bulkConfirming = false
		m.bulkCandidates = nil
		m.bulkSkipped = nil
		m.statusMessage = fmt.Sprintf("Preflighting %s for fast-forward…", pluralize(len(targets), "plan"))
		return m, previewBulkFastForwardCmd(targets, skipped, m.bulkGeneration)

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
			m.ensureCursorVisible()
			if m.cursor >= 0 && m.cursor < len(m.plans) {
				selectedPlan := m.plans[m.cursor]
				if m.hasDaemonSnapshot {
					if summary, ok := m.planSummaries[planItemKey(selectedPlan)]; ok {
						m.detailGeneration++
						m.detailPendingKey = planItemKey(selectedPlan)
						m.statusMessage = "Loading selected live detail…"
						return m, loadSelectedPlanDetailCmd(summary, m.detailGeneration)
					}
				}
				if len(selectedPlan.EcosystemRepoStatuses) > 0 {
					m.inRepoNavigationMode = true
					m.repoCursor = 0
					if c := m.fetchSelectedRepoGitLog(); c != nil {
						m.statusMessage = ""
						return m, c
					}
				} else if !m.gitLogLoaded {
					m.statusMessage = ""
					return m, fetchGitLogCmd(selectedPlan.WorkspaceRoot)
				}
			}
		}
		m.ensureCursorVisible()
		m.statusMessage = ""
		return m, nil

	case key.Matches(msg, m.keys.ToggleHold):
		m.showOnHold = !m.showOnHold
		m.loadGeneration++
		m.statusMessage = fmt.Sprintf("On-hold plans: %v", m.showOnHold)
		m.ensureCursorVisible()
		return m, m.reloadPlansCmd()

	case key.Matches(msg, m.keys.ToggleArchived):
		m.showArchived = !m.showArchived
		m.loadGeneration++
		m.statusMessage = fmt.Sprintf("Archived plans: %v", m.showArchived)
		return m, m.reloadPlansCmd()

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
			action := "hold"
			if currentStatus == "hold" {
				action = "unhold"
			}
			if !m.selectedBindingValid(action) {
				return m, nil
			}
			key := planItemKey(selectedPlan)
			if m.holdPending == nil {
				m.holdPending = make(map[string]bool)
			}
			if _, pending := m.holdPending[key]; pending {
				return m, nil
			}

			hold := currentStatus != "hold"
			m.holdPending[key] = hold
			if hold {
				m.statusMessage = "Holding…"
			} else {
				m.statusMessage = "Unholding…"
			}
			return m, setHoldCmd(key, plan.Directory, hold)
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

// selectedBindingValid refuses path-sensitive actions unless registry and plan
// config agree on one live, qualified container.
func (m *Model) selectedBindingValid(action string) bool {
	if m.cursor < 0 || m.cursor >= len(m.plans) {
		return false
	}
	binding := m.plans[m.cursor].Binding
	if binding.Valid() {
		return true
	}
	// Rolling is intentionally attached to the primary checkout rather than a
	// registry worktree, so its qualified workspace root is its valid target.
	if action == "open" && m.plans[m.cursor].Name == RollingPlanName && m.plans[m.cursor].WorkspaceRoot != "" {
		return true
	}
	health := binding.Health
	if health == "" {
		health = coreplan.BindingUnbound
	}
	m.statusMessage = theme.DefaultTheme.Error.Render(fmt.Sprintf("Cannot %s: %s", action, health))
	return false
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
