package status_tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/logging/logutil"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/tui/components/logviewer"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

// Update handles messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case InitProgramMsg:
		// Set the program reference from the package-level variable
		// This is called on the first update cycle after Init()
		if initProgramRef != nil {
			m.Program = initProgramRef
		}
		return m, nil

	case LogContentLoadedMsg:
		logger := logging.NewLogger("flow-tui")
		logger.WithFields(map[string]interface{}{
			"has_error":        msg.Err != nil,
			"should_retry":     msg.ShouldRetry,
			"start_streaming":  msg.StartStreaming,
			"content_length":   len(msg.Content),
			"show_logs":        m.ShowLogs,
			"active_log_job":   m.ActiveLogJob != nil,
		}).Info("Received LogContentLoadedMsg")

		if msg.Err != nil {
			m.LogViewer.SetContent(theme.DefaultTheme.Error.Render(fmt.Sprintf("Error loading logs: %v", msg.Err)))
		} else {
			// Apply muted styling to "No logs found" messages
			content := msg.Content
			if strings.HasPrefix(content, "No logs found") {
				content = theme.DefaultTheme.Muted.Render(content)
			}
			m.LogViewer.SetContent(content)
		}

		var cmds []tea.Cmd

		// If we should retry (agent session hasn't started yet), schedule a retry
		if msg.ShouldRetry {
			logger.Info("Scheduling retry for agent log loading")
			cmds = append(cmds, retryLoadAgentLogsAfterDelay())
		}

		// If we should start streaming (session is ready), start the stream
		if msg.StartStreaming && m.ActiveLogJob != nil && msg.LogFilePath != "" {
			logger.WithFields(map[string]interface{}{
				"job_id":       m.ActiveLogJob.ID,
				"log_file_path": msg.LogFilePath,
			}).Info("Starting agent log streaming")
			cmds = append(cmds, streamAgentLogsCmd(msg.LogFilePath, m.Program))
		}

		if len(cmds) > 0 {
			logger.WithFields(map[string]interface{}{
				"num_cmds": len(cmds),
			}).Info("Returning batched commands")
			return m, tea.Batch(cmds...)
		}
		logger.Info("No commands to return")
		return m, nil

	case FrontmatterContentLoadedMsg:
		if m.ActiveDetailPane == FrontmatterPane {
			if msg.Err != nil {
				m.frontmatterViewport.SetContent(theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v", msg.Err)))
			} else {
				m.frontmatterViewport.SetContent(msg.Content)
			}
		}
		return m, nil

	case BriefingContentLoadedMsg:
		if m.ActiveDetailPane == BriefingPane {
			if msg.Err != nil {
				m.briefingViewport.SetContent(theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v", msg.Err)))
			} else {
				m.briefingViewport.SetContent(msg.Content)
			}
		}
		return m, nil

	case EditContentLoadedMsg:
		if m.ActiveDetailPane == EditPane {
			if msg.Err != nil {
				m.editViewport.SetContent(theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v", msg.Err)))
			} else {
				m.editViewport.SetContent(msg.Content)
			}
		}
		return m, nil

	case RetryLoadAgentLogsMsg:
		logger := logging.NewLogger("flow-tui")
		logger.WithFields(map[string]interface{}{
			"show_logs":      m.ShowLogs,
			"has_active_job": m.ActiveLogJob != nil,
		}).Info("Received RetryLoadAgentLogsMsg")

		// Only retry if we're still showing logs for an agent job
		if m.ShowLogs && m.ActiveLogJob != nil {
			// Get fresh job status from the plan (don't use cached ActiveLogJob)
			var currentJob *orchestration.Job
			for _, job := range m.Jobs {
				if job.ID == m.ActiveLogJob.ID {
					currentJob = job
					break
				}
			}

			if currentJob != nil {
				isAgentJob := currentJob.Type == orchestration.JobTypeInteractiveAgent || currentJob.Type == orchestration.JobTypeHeadlessAgent

				logger.WithFields(map[string]interface{}{
					"job_id":       currentJob.ID,
					"is_agent_job": isAgentJob,
					"job_status":   currentJob.Status,
				}).Info("Retry conditions checked")

				// Retry as long as it's an agent job, regardless of status
				// This allows us to pick up logs even if the job completed quickly
				if isAgentJob {
					// Update the active log job reference and retry loading agent logs
					m.ActiveLogJob = currentJob
					logger.Info("Retrying agent log load")
					return m, loadAndStreamAgentLogsCmd(m.Plan, currentJob)
				}
			} else {
				logger.Warn("Could not find current job for retry")
			}
		} else {
			logger.Info("Not retrying - logs not shown or no active job")
		}
		return m, nil

	case RenameCompleteMsg:
		if msg.Err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error renaming job: %v", msg.Err))
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Job renamed successfully.")
		}
		return m, refreshPlan(m.PlanDir)

	case UpdateDepsCompleteMsg:
		if msg.Err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error updating dependencies: %v", msg.Err))
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Dependencies updated successfully.")
		}
		return m, refreshPlan(m.PlanDir)

	case CreateJobCompleteMsg:
		if msg.Err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error creating job: %v", msg.Err))
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Job created successfully.")
		}
		return m, refreshPlan(m.PlanDir)

	case JobRunFinishedMsg:
		// Job run completed
		m.IsRunningJob = false

		if msg.Err != nil {
			// Only show error if it's not just "exit status 1" which is generic
			errStr := msg.Err.Error()
			if errStr == "exit status 1" {
				// Job failed but we already have the details in the log
				m.StatusSummary = theme.DefaultTheme.Warning.Render("Job execution completed with errors. Check the log for details.")
			} else {
				m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Job run failed: %v", msg.Err))
			}
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Job run completed successfully.")
		}

		// Stop following the log file
		m.LogViewer.Stop()

		// Keep the log viewer open so the user can review the output
		// They can press 'v' to close it when ready

		// Return focus to jobs pane
		m.Focus = JobsPane

		// Refresh the plan to show updated statuses
		return m, refreshPlan(m.PlanDir)

	case EditFileInTmuxMsg:
		if msg.Err != nil {
			m.Err = msg.Err
			return m, nil
		}
		return m, tea.Quit

	case TickMsg:
		// Toggle cursor visibility for blinking effect
		m.CursorVisible = !m.CursorVisible
		return m, blink() // Schedule next tick

	case StatusUpdateMsg:
		m.StatusSummary = string(msg)
		return m, refreshPlan(m.PlanDir)

	case RefreshTickMsg:
		return m, tea.Batch(
			refreshPlan(m.PlanDir),
			refreshTick(),
		)

	case RefreshMsg:
		logger := logging.NewLogger("flow-tui")

		// Reload the plan
		plan, err := orchestration.LoadPlan(m.PlanDir)
		if err != nil {
			logger.WithFields(map[string]interface{}{
				"error": err,
			}).Error("Failed to reload plan during refresh")
			m.Err = err
			return m, nil
		}

		// Verify running jobs (check PIDs, clear stale "running" statuses)
		// This needs to be imported from cmd package
		verifyRunningJobStatusHelper(plan)

		// Log any jobs that were marked as interrupted
		var interruptedJobs []string
		for _, job := range plan.Jobs {
			if job.Status == "interrupted" {
				interruptedJobs = append(interruptedJobs, job.Title)
			}
		}
		if len(interruptedJobs) > 0 {
			logger.WithFields(map[string]interface{}{
				"interrupted_jobs": interruptedJobs,
			}).Debug("Cleared stale 'running' statuses during refresh")
		}

		graph, err := orchestration.BuildDependencyGraph(plan)
		if err != nil {
			logger.WithFields(map[string]interface{}{
				"error": err,
			}).Error("Failed to build dependency graph during refresh")
			m.Err = err
			return m, nil
		}

		// Recreate orchestrator with the refreshed plan
		orchConfig := &orchestration.OrchestratorConfig{
			MaxParallelJobs:     1,
			CheckInterval:       5 * time.Second,
			MaxConsecutiveSteps: 20,
			SkipInteractive:     true,
		}
		orch, err := orchestration.NewOrchestrator(plan, orchConfig)
		if err != nil {
			logger.WithFields(map[string]interface{}{
				"error": err,
			}).Error("Failed to recreate orchestrator during refresh - keeping old orchestrator")
			m.StatusSummary = theme.DefaultTheme.Warning.Render(fmt.Sprintf("Warning: Failed to recreate orchestrator: %v", err))
			// IMPORTANT: Don't update m.Plan if orchestrator creation failed
			// This keeps the old orchestrator and old plan in sync
			return m, nil
		} else {
			m.Orchestrator = orch
		}

		// Update model with refreshed data
		m.Plan = plan
		m.Graph = graph
		jobs, parents, indents := flattenJobTreeWithParents(plan)
		m.Jobs = jobs
		m.JobParents = parents
		m.JobIndents = indents

		// Only update status summary if not running a job
		// (preserve the "Running..." message)
		if !m.IsRunningJob {
			m.StatusSummary = formatStatusSummaryHelper(plan)
		}

		// Adjust cursor if needed
		if m.Cursor >= len(m.Jobs) {
			m.Cursor = len(m.Jobs) - 1
		}
		if m.Cursor < 0 && len(m.Jobs) > 0 {
			m.Cursor = 0
		}

		// Clear selections that no longer exist
		newSelected := make(map[string]bool)
		for id := range m.Selected {
			for _, job := range m.Jobs {
				if job.ID == id {
					newSelected[id] = true
					break
				}
			}
		}
		m.Selected = newSelected

		return m, nil

	case ArchiveConfirmedMsg:
		// Perform the actual archive
		return m, tea.Sequence(
			doArchiveJob(m.PlanDir, msg.Job),
			refreshPlan(m.PlanDir),
		)

	case EditFileAndQuitMsg:
		// Print protocol string and quit - Neovim plugin will handle the file opening
		fmt.Printf("EDIT_FILE:%s\n", msg.FilePath)
		return m, tea.Quit

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		m.Help.Width = msg.Width
		m.Help.Height = msg.Height
		m.Help, _ = m.Help.Update(msg)

		// Adjust scroll offset to show cursor at bottom on first render
		m.adjustScrollOffset()

		// Centralized layout calculation
		m.updateLayoutDimensions()

		// Update viewport sizes
		m.frontmatterViewport.Width = m.LogViewerWidth
		m.frontmatterViewport.Height = m.LogViewerHeight
		m.briefingViewport.Width = m.LogViewerWidth
		m.briefingViewport.Height = m.LogViewerHeight
		m.editViewport.Width = m.LogViewerWidth
		m.editViewport.Height = m.LogViewerHeight

		// Start log viewer on first window size message if we have jobs and logs are enabled
		if m.ShowLogs && m.ActiveLogJob == nil && len(m.Jobs) > 0 {
			job := m.Jobs[m.Cursor]
			m.ActiveLogJob = job // Mark as attempted
			workDir, err := orchestration.DetermineWorkingDirectory(m.Plan, job)
			if err == nil {
				node, err := workspace.GetProjectByPath(workDir)
				if err == nil {
					logFile, _, err := logutil.FindLogFileForWorkspace(node)
					if err == nil {
						m.LogViewer = logviewer.New(m.LogViewerWidth, m.LogViewerHeight)
						cmd = m.LogViewer.Start(map[string]string{node.Name: logFile})
						return m, cmd
					}
				}
			}
			// If no logs found, still show the log viewer (it will show empty/waiting state)
			m.LogViewer = logviewer.New(m.LogViewerWidth, m.LogViewerHeight)
		}

		// Update log viewer size if active
		if m.ShowLogs {
			m.LogViewer, cmd = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight})
			return m, cmd
		}
		return m, nil

	case logviewer.LogLineMsg:
		// Delegate log messages to the log viewer
		if m.ShowLogs {
			m.LogViewer, cmd = m.LogViewer.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		// Handle renaming mode
		if m.Renaming {
			switch msg.String() {
			case "enter":
				if m.RenameJobIndex >= 0 && m.RenameJobIndex < len(m.Jobs) {
					jobToRename := m.Jobs[m.RenameJobIndex]
					newTitle := m.RenameInput.Value()
					m.Renaming = false
					m.StatusSummary = "Renaming job..."
					return m, renameJobCmd(m.Plan, jobToRename, newTitle)
				}
			case "esc":
				m.Renaming = false
				m.StatusSummary = ""
				return m, nil
			}
			m.RenameInput, cmd = m.RenameInput.Update(msg)
			return m, cmd
		}

		// Handle job creation mode
		if m.CreatingJob {
			switch msg.String() {
			case "enter":
				customTitle := m.CreateJobInput.Value()
				// If empty, use the placeholder as the title
				if customTitle == "" {
					customTitle = m.CreateJobInput.Placeholder
				}
				m.CreatingJob = false
				m.StatusSummary = "Creating job..."

				// Create the appropriate job type
				if m.CreateJobType == "xml" {
					if len(m.CreateJobDeps) > 0 {
						return m, createXmlPlanJobWithTitle(m.Plan, m.CreateJobDeps, customTitle)
					}
					return m, createXmlPlanJobWithTitle(m.Plan, []*orchestration.Job{m.CreateJobBaseJob}, customTitle)
				} else if m.CreateJobType == "impl" {
					if len(m.CreateJobDeps) > 0 {
						return m, createImplementationJobWithTitle(m.Plan, m.CreateJobDeps, customTitle)
					}
					return m, createImplementationJobWithTitle(m.Plan, []*orchestration.Job{m.CreateJobBaseJob}, customTitle)
				} else if m.CreateJobType == "agent-from-chat" {
					if len(m.CreateJobDeps) > 0 {
						return m, createAgentFromChatJobWithTitle(m.Plan, m.CreateJobDeps, customTitle)
					}
					return m, createAgentFromChatJobWithTitle(m.Plan, []*orchestration.Job{m.CreateJobBaseJob}, customTitle)
				}
			case "esc":
				m.CreatingJob = false
				m.CreateJobBaseJob = nil
				m.CreateJobDeps = nil
				m.StatusSummary = ""
				return m, nil
			}
			m.CreateJobInput, cmd = m.CreateJobInput.Update(msg)
			return m, cmd
		}

		// Handle dependency editing mode
		if m.EditingDeps {
			switch msg.String() {
			case "enter":
				if m.EditDepsJobIndex >= 0 && m.EditDepsJobIndex < len(m.Jobs) {
					jobToEdit := m.Jobs[m.EditDepsJobIndex]
					// Build list of selected dependencies (filenames)
					var newDeps []string
					for _, job := range m.Jobs {
						if m.EditDepsSelected[job.ID] {
							newDeps = append(newDeps, job.Filename)
						}
					}
					m.EditingDeps = false
					m.EditDepsSelected = nil
					m.StatusSummary = "Updating dependencies..."
					return m, updateDepsCmd(jobToEdit, newDeps)
				}
			case "esc":
				m.EditingDeps = false
				m.EditDepsSelected = nil
				m.StatusSummary = ""
				return m, nil
			case " ":
				// Toggle selection
				if m.Cursor >= 0 && m.Cursor < len(m.Jobs) {
					job := m.Jobs[m.Cursor]
					// Don't allow selecting the job being edited
					if m.Cursor != m.EditDepsJobIndex {
						if m.EditDepsSelected[job.ID] {
							delete(m.EditDepsSelected, job.ID)
						} else {
							m.EditDepsSelected[job.ID] = true
						}
					}
				}
				return m, nil
			case "up", "k":
				if m.Cursor > 0 {
					m.Cursor--
					m.adjustScrollOffset()
				}
				return m, nil
			case "down", "j":
				if m.Cursor < len(m.Jobs)-1 {
					m.Cursor++
					m.adjustScrollOffset()
				}
				return m, nil
			}
			return m, nil
		}

		// Handle status picker first
		if m.ShowStatusPicker {
			switch msg.String() {
			case "up", "k":
				if m.StatusPickerCursor > 0 {
					m.StatusPickerCursor--
				}
				return m, nil
			case "down", "j":
				if m.StatusPickerCursor < 8 { // 9 status options (0-8)
					m.StatusPickerCursor++
				}
				return m, nil
			case "enter":
				m.ShowStatusPicker = false
				if m.Cursor < len(m.Jobs) {
					job := m.Jobs[m.Cursor]
					statuses := []orchestration.JobStatus{
						orchestration.JobStatusPending,
						orchestration.JobStatusTodo,
						orchestration.JobStatusHold,
						orchestration.JobStatusRunning,
						orchestration.JobStatusCompleted,
						orchestration.JobStatusFailed,
						orchestration.JobStatusBlocked,
						orchestration.JobStatusNeedsReview,
						orchestration.JobStatusAbandoned,
					}
					return m, tea.Sequence(
						setJobStatus(job, m.Plan, statuses[m.StatusPickerCursor]),
						refreshPlan(m.PlanDir),
					)
				}
				return m, nil
			case "esc", "ctrl+c", "q", "b":
				m.ShowStatusPicker = false
				return m, nil
			default:
				// Any other key while status picker is open - just consume it
				return m, nil
			}
		}

		// Handle confirmation dialog
		if m.ConfirmArchive {
			switch msg.String() {
			case "y", "Y":
				m.ConfirmArchive = false
				if len(m.Selected) > 0 {
					// Archive all selected jobs
					var jobsToArchive []*orchestration.Job
					for id := range m.Selected {
						for _, job := range m.Jobs {
							if job.ID == id {
								jobsToArchive = append(jobsToArchive, job)
								break
							}
						}
					}
					return m, tea.Sequence(
						doArchiveJobs(m.PlanDir, jobsToArchive),
						refreshPlan(m.PlanDir),
					)
				} else if m.Cursor < len(m.Jobs) {
					job := m.Jobs[m.Cursor]
					return m, func() tea.Msg { return ArchiveConfirmedMsg{Job: job} }
				}
			case "n", "N", "ctrl+c", "q":
				m.ConfirmArchive = false
			}
			return m, nil
		}

		// Handle column selection mode first
		if m.columnSelectMode {
			switch msg.String() {
			case "T", "enter", "esc":
				m.columnSelectMode = false
				return m, nil
			case " ":
				// Toggle selection
				if i, ok := m.columnList.SelectedItem().(columnSelectItem); ok {
					m.columnVisibility[i.name] = !m.columnVisibility[i.name]
					// Save state to disk
					_ = saveState(m.columnVisibility)
				}
				return m, nil
			default:
				m.columnList, cmd = m.columnList.Update(msg)
				return m, cmd
			}
		}

		// If help is showing, let it handle key messages (for scrolling and closing)
		if m.Help.ShowAll {
			var cmd tea.Cmd
			m.Help, cmd = m.Help.Update(msg)
			return m, cmd
		}

		// If log viewer is showing and focused, delegate most keys to it first.
		if m.ShowLogs && m.Focus == LogsPane {
			switch msg.String() {
			case "q", "ctrl+c":
				// Let 'q' and 'ctrl+c' be handled by the main logic to quit.
			case "?":
				// Let '?' be handled by the main logic to show help.
			case "l", "f", "b", "m", "p", "v":
				// Let pane switching keys be handled by the main logic.
			case "tab", "shift+tab":
				// Let 'tab' and 'shift+tab' be handled by the main logic to switch focus.
			case "V":
				// Let 'V' be handled by the main logic to toggle layout.
			case "esc":
				// 'esc' closes the detail pane (handled by main logic below)
			case "g":
				if m.WaitingForG {
					// Second 'g' - jump to top of logs
					m.LogViewer.GotoTop()
					m.WaitingForG = false
					return m, nil
				} else {
					// First 'g' - wait for second
					m.WaitingForG = true
					return m, nil
				}
			case "G":
				// Jump to bottom of logs
				m.LogViewer.GotoBottom()
				m.WaitingForG = false // Clear any pending 'g'
				return m, nil
			default:
				// Clear waiting for 'g' if any other key is pressed
				if m.WaitingForG {
					m.WaitingForG = false
				}

				// Delegate other keys to the active viewport for scrolling, etc.
				switch m.ActiveDetailPane {
				case LogsPaneDetail:
					m.LogViewer, cmd = m.LogViewer.Update(msg)
				case FrontmatterPane:
					m.frontmatterViewport, cmd = m.frontmatterViewport.Update(msg)
				case BriefingPane:
					m.briefingViewport, cmd = m.briefingViewport.Update(msg)
				case EditPane:
					m.editViewport, cmd = m.editViewport.Update(msg)
				}
				return m, cmd
			}
		}

		// Handle 'gg' sequence for going to top
		if msg.String() == "g" {
			if m.WaitingForG {
				// Second 'g' - go to top
				m.Cursor = 0
				m.ScrollOffset = 0
				m.WaitingForG = false
			} else {
				// First 'g' - wait for second
				m.WaitingForG = true
			}
			return m, nil
		} else {
			// Any other key resets the 'g' waiting state
			m.WaitingForG = false
		}

		switch {
		case key.Matches(msg, m.KeyMap.Quit):
			return m, tea.Quit

		case key.Matches(msg, m.KeyMap.Help):
			m.Help.Toggle()

		case key.Matches(msg, m.KeyMap.SwitchFocus):
			if m.ShowLogs {
				if m.Focus == JobsPane {
					m.Focus = LogsPane
				} else {
					m.Focus = JobsPane
				}
			}

		case key.Matches(msg, m.KeyMap.ToggleLayout):
			if m.ShowLogs {
				m.LogSplitVertical = !m.LogSplitVertical

				// Centralized layout calculation
				m.updateLayoutDimensions()

				// Update log viewer with new dimensions
				m.LogViewer, cmd = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight})
				return m, cmd
			}
			return m, nil

		case key.Matches(msg, m.KeyMap.Up):
			if m.Cursor > 0 {
				m.Cursor--
				m.adjustScrollOffset()
				if m.ShowLogs && !m.IsRunningJob {
					return m, m.reloadActiveDetailPane()
				}
			}

		case key.Matches(msg, m.KeyMap.Down):
			if m.Cursor < len(m.Jobs)-1 {
				m.Cursor++
				m.adjustScrollOffset()
				if m.ShowLogs && !m.IsRunningJob {
					return m, m.reloadActiveDetailPane()
				}
			}

		case key.Matches(msg, m.KeyMap.GoToBottom):
			if len(m.Jobs) > 0 {
				m.Cursor = len(m.Jobs) - 1
				m.adjustScrollOffset()
			}

		case key.Matches(msg, m.KeyMap.PageUp):
			pageSize := 10
			m.Cursor -= pageSize
			if m.Cursor < 0 {
				m.Cursor = 0
			}
			m.adjustScrollOffset()

		case key.Matches(msg, m.KeyMap.PageDown):
			pageSize := 10
			m.Cursor += pageSize
			if m.Cursor >= len(m.Jobs) {
				m.Cursor = len(m.Jobs) - 1
			}
			m.adjustScrollOffset()

		case key.Matches(msg, m.KeyMap.Select):
			if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				if m.Selected[job.ID] {
					delete(m.Selected, job.ID)
				} else {
					m.Selected[job.ID] = true
				}
			}

		case key.Matches(msg, m.KeyMap.SelectAll):
			for _, job := range m.Jobs {
				m.Selected[job.ID] = true
			}

		case key.Matches(msg, m.KeyMap.SelectNone):
			m.Selected = make(map[string]bool)

		case key.Matches(msg, m.KeyMap.Archive):
			// Archive selected jobs or current job if none selected
			if len(m.Selected) > 0 || m.Cursor < len(m.Jobs) {
				m.ConfirmArchive = true
			}

		case key.Matches(msg, m.KeyMap.Edit):
			if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				return m, editJob(job)
			}

		case key.Matches(msg, m.KeyMap.ViewLogs):
			return m.openDetailPane(LogsPaneDetail)

		case key.Matches(msg, m.KeyMap.ViewFrontmatter):
			return m.openDetailPane(FrontmatterPane)

		case key.Matches(msg, m.KeyMap.ViewBriefing):
			return m.openDetailPane(BriefingPane)

		case key.Matches(msg, m.KeyMap.ViewEdit):
			return m.openDetailPane(EditPane)

		case key.Matches(msg, m.KeyMap.CycleDetailPane):
			// Toggle detail pane visibility (show/hide)
			if m.ActiveDetailPane == NoPane {
				// If closed, open logs pane by default
				return m.openDetailPane(LogsPaneDetail)
			} else {
				// If any pane is open, close it
				m.LogViewer.Stop()
				m.ShowLogs = false
				m.Focus = JobsPane
				m.ActiveLogJob = nil
				m.ActiveDetailPane = NoPane
				m.StatusSummary = ""
				return m, nil
			}

		case key.Matches(msg, m.KeyMap.CloseDetailPane):
			m.LogViewer.Stop()
			m.ShowLogs = false
			m.Focus = JobsPane
			m.ActiveLogJob = nil
			m.ActiveDetailPane = NoPane
			m.StatusSummary = ""
			return m, nil

		case key.Matches(msg, m.KeyMap.Run):
			logger := logging.NewLogger("flow-tui")
			logger.Info("'r' key pressed - checking if jobs can run")

			// Check if a job is already running from the TUI
			if m.IsRunningJob {
				logger.Warn("Job already running in TUI - blocking new run")
				m.StatusSummary = theme.DefaultTheme.Warning.Render("A job is already running. Please wait for it to complete.")
				return m, nil
			}

			// Collect selected jobs or current job
			var candidateJobs []*orchestration.Job
			if len(m.Selected) > 0 {
				for id := range m.Selected {
					for _, job := range m.Jobs {
						if job.ID == id {
							candidateJobs = append(candidateJobs, job)
							break
						}
					}
				}
			} else if m.Cursor < len(m.Jobs) {
				candidateJobs = []*orchestration.Job{m.Jobs[m.Cursor]}
			}

			logger.WithFields(map[string]interface{}{
				"num_candidates": len(candidateJobs),
			}).Info("Filtering candidate jobs by status")

			// Filter out jobs that are not runnable
			var jobsToRun []*orchestration.Job
			var skippedReasons []string
			for _, job := range candidateJobs {
				logger.WithFields(map[string]interface{}{
					"job_id":     job.ID,
					"job_title":  job.Title,
					"job_status": job.Status,
				}).Debug("Checking job status")

				switch job.Status {
				case orchestration.JobStatusPending, orchestration.JobStatusFailed,
					orchestration.JobStatusTodo, orchestration.JobStatusNeedsReview,
					orchestration.JobStatusBlocked, orchestration.JobStatusPendingUser,
					orchestration.JobStatusPendingLLM:
					// These statuses are runnable
					jobsToRun = append(jobsToRun, job)
				case orchestration.JobStatusRunning:
					skippedReasons = append(skippedReasons, fmt.Sprintf("%s is already running", job.Title))
				case orchestration.JobStatusCompleted:
					skippedReasons = append(skippedReasons, fmt.Sprintf("%s is already completed", job.Title))
				case orchestration.JobStatusAbandoned, orchestration.JobStatusHold:
					skippedReasons = append(skippedReasons, fmt.Sprintf("%s is on hold/abandoned", job.Title))
				default:
					// For any other status, skip
					skippedReasons = append(skippedReasons, fmt.Sprintf("%s has status %s", job.Title, job.Status))
				}
			}

			// If no jobs are runnable, show appropriate message
			if len(jobsToRun) == 0 {
				logger.WithFields(map[string]interface{}{
					"skipped_reasons": skippedReasons,
				}).Warn("No runnable jobs after filtering")
				if len(skippedReasons) > 0 {
					m.StatusSummary = theme.DefaultTheme.Warning.Render(skippedReasons[0])
				} else {
					m.StatusSummary = theme.DefaultTheme.Warning.Render("No runnable jobs selected")
				}
				return m, nil
			}

			if len(jobsToRun) > 0 {
				// Clear the log viewer to prepare for new live output
				m.LogViewer.Clear()

				// Start running the jobs asynchronously
				m.IsRunningJob = true
				m.ShowLogs = true
				m.Focus = LogsPane

				// Centralized layout calculation
				m.updateLayoutDimensions()

				m.LogViewer = logviewer.New(m.LogViewerWidth, m.LogViewerHeight)

				// Initialize the log viewer with a WindowSizeMsg to set it to ready state
				m.LogViewer, _ = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight})

				// Update status message
				jobCount := len(jobsToRun)
				if jobCount == 1 {
					m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Running %s...", jobsToRun[0].Title))
				} else {
					m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Running %d job(s)...", jobCount))
				}

				// Run the jobs using the orchestrator with direct output streaming
				var runCmd tea.Cmd
				logger := logging.NewLogger("flow-tui")
				if m.Orchestrator != nil && m.Program != nil {
					logger.WithFields(map[string]interface{}{
						"num_jobs":   len(jobsToRun),
						"use_method": "orchestrator",
					}).Info("Running jobs via orchestrator")
					runCmd = runJobsWithOrchestrator(m.Orchestrator, jobsToRun, m.Program)
				} else {
					logger.WithFields(map[string]interface{}{
						"num_jobs":          len(jobsToRun),
						"use_method":        "subprocess",
						"orchestrator_nil":  m.Orchestrator == nil,
						"program_nil":       m.Program == nil,
					}).Warn("Falling back to subprocess job execution")
					// Fallback to old method if orchestrator is not available
					runCmd = runJobsCmd(m.RunLogFile, m.PlanDir, jobsToRun)
				}

				// For interactive agent jobs, also start agent log streaming
				var cmds []tea.Cmd
				cmds = append(cmds, runCmd)

				// Check if any of the jobs being run are interactive agents
				for _, job := range jobsToRun {
					isAgentJob := job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeHeadlessAgent

					logger.WithFields(map[string]interface{}{
						"job_id":       job.ID,
						"job_type":     job.Type,
						"is_agent_job": isAgentJob,
					}).Debug("Checking if job is agent type")

					if isAgentJob {
						// Set this as the active log job
						m.ActiveLogJob = job

						logger.WithFields(map[string]interface{}{
							"job_id": job.ID,
						}).Info("Starting agent log streaming for running job")

						// Start streaming agent logs (with retry for when session starts)
						cmds = append(cmds, loadAndStreamAgentLogsCmd(m.Plan, job))
						break // Only handle the first agent job
					}
				}

				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.KeyMap.SetCompleted):
			if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				return m, tea.Sequence(
					setJobCompleted(job, m.Plan, completeJobHelper),
					refreshPlan(m.PlanDir),
				)
			}

		case key.Matches(msg, m.KeyMap.SetStatus):
			if m.Cursor < len(m.Jobs) {
				m.ShowStatusPicker = true
				m.StatusPickerCursor = 0
			}

		case key.Matches(msg, m.KeyMap.AddXmlPlan):
			if len(m.Selected) > 0 {
				// Get selected jobs for dependencies
				var selectedJobs []*orchestration.Job
				for id := range m.Selected {
					for _, job := range m.Jobs {
						if job.ID == id {
							selectedJobs = append(selectedJobs, job)
							break
						}
					}
				}
				// Show dialog to edit job title
				m.CreatingJob = true
				m.CreateJobType = "xml"
				m.CreateJobDeps = selectedJobs
				defaultTitle := fmt.Sprintf("xml-plan-%s", selectedJobs[0].Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.CreateJobInput = ti
				return m, textinput.Blink
			} else if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				// Show dialog to edit job title
				m.CreatingJob = true
				m.CreateJobType = "xml"
				m.CreateJobBaseJob = job
				defaultTitle := fmt.Sprintf("xml-plan-%s", job.Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.CreateJobInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.KeyMap.AddJob):
			if len(m.Selected) > 0 {
				// Get selected job IDs for dependencies
				var deps []string
				for id := range m.Selected {
					for _, job := range m.Jobs {
						if job.ID == id {
							deps = append(deps, job.Filename)
							break
						}
					}
				}
				return m, addJobWithDependencies(m.Plan.Directory, deps)
			} else {
				return m, addJobWithDependencies(m.Plan.Directory, nil)
			}

		case key.Matches(msg, m.KeyMap.Implement):
			if len(m.Selected) > 0 {
				// Get selected jobs for dependencies
				var selectedJobs []*orchestration.Job
				for id := range m.Selected {
					for _, job := range m.Jobs {
						if job.ID == id {
							selectedJobs = append(selectedJobs, job)
							break
						}
					}
				}
				// Show dialog to edit job title
				m.CreatingJob = true
				m.CreateJobType = "impl"
				m.CreateJobDeps = selectedJobs
				defaultTitle := fmt.Sprintf("impl-%s", selectedJobs[0].Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.CreateJobInput = ti
				return m, textinput.Blink
			} else if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				// Show dialog to edit job title
				m.CreatingJob = true
				m.CreateJobType = "impl"
				m.CreateJobBaseJob = job
				defaultTitle := fmt.Sprintf("impl-%s", job.Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.CreateJobInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.KeyMap.AgentFromChat):
			if len(m.Selected) > 0 {
				// Get selected jobs for dependencies
				var selectedJobs []*orchestration.Job
				for id := range m.Selected {
					for _, job := range m.Jobs {
						if job.ID == id {
							selectedJobs = append(selectedJobs, job)
							break
						}
					}
				}
				// Show dialog to edit job title
				m.CreatingJob = true
				m.CreateJobType = "agent-from-chat"
				m.CreateJobDeps = selectedJobs
				defaultTitle := fmt.Sprintf("impl-%s", selectedJobs[0].Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.CreateJobInput = ti
				return m, textinput.Blink
			} else if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				// Show dialog to edit job title
				m.CreatingJob = true
				m.CreateJobType = "agent-from-chat"
				m.CreateJobBaseJob = job
				defaultTitle := fmt.Sprintf("impl-%s", job.Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.CreateJobInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.KeyMap.Rename):
			if m.Cursor >= 0 && m.Cursor < len(m.Jobs) {
				m.Renaming = true
				m.RenameJobIndex = m.Cursor
				jobToRename := m.Jobs[m.Cursor]

				ti := textinput.New()
				ti.SetValue(jobToRename.Title)
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.RenameInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.KeyMap.Resume):
			if m.Cursor >= 0 && m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				if (job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeAgent) && job.Status == orchestration.JobStatusCompleted {
					return m, executePlanResume(job)
				}
				m.StatusSummary = theme.DefaultTheme.Error.Render("Only completed interactive agent jobs can be resumed.")
			}

		case key.Matches(msg, m.KeyMap.EditDeps):
			if m.Cursor >= 0 && m.Cursor < len(m.Jobs) {
				m.EditingDeps = true
				m.EditDepsJobIndex = m.Cursor
				jobToEdit := m.Jobs[m.Cursor]

				// Initialize selection with current dependencies
				m.EditDepsSelected = make(map[string]bool)
				for _, depFilename := range jobToEdit.DependsOn {
					// Find job by filename and mark as selected
					for _, job := range m.Jobs {
						if job.Filename == depFilename {
							m.EditDepsSelected[job.ID] = true
							break
						}
					}
				}
				return m, nil
			}

		case key.Matches(msg, m.KeyMap.ToggleSummaries):
			m.ShowSummaries = !m.ShowSummaries

		case key.Matches(msg, m.KeyMap.ToggleColumns):
			m.columnSelectMode = true

		}
	}

	return m, nil
}

// Helper function type declarations - will be set by the cmd package
var (
	VerifyRunningJobStatusFunc func(*orchestration.Plan)
	CompleteJobFunc            func(*orchestration.Job, *orchestration.Plan, bool) error
)

// Helper functions that call the injected functions
func verifyRunningJobStatusHelper(plan *orchestration.Plan) {
	if VerifyRunningJobStatusFunc != nil {
		VerifyRunningJobStatusFunc(plan)
	}
}

func completeJobHelper(job *orchestration.Job, plan *orchestration.Plan, silent bool) error {
	if CompleteJobFunc != nil {
		return CompleteJobFunc(job, plan, silent)
	}
	return nil
}

// reloadActiveDetailPane reloads content for the currently active detail pane
func (m Model) reloadActiveDetailPane() tea.Cmd {
	if m.Cursor >= len(m.Jobs) || m.ActiveDetailPane == NoPane {
		return nil
	}

	job := m.Jobs[m.Cursor]
	m.ActiveLogJob = job

	switch m.ActiveDetailPane {
	case LogsPaneDetail:
		isAgentJob := job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeHeadlessAgent
		isRunning := job.Status == orchestration.JobStatusRunning
		if isAgentJob && isRunning {
			return loadAndStreamAgentLogsCmd(m.Plan, job)
		}
		return loadLogContentCmd(m.Plan, job)
	case FrontmatterPane:
		return loadFrontmatterCmd(job)
	case BriefingPane:
		return loadBriefingCmd(m.Plan, job)
	case EditPane:
		return loadJobFileContentCmd(job)
	}

	return nil
}

// openDetailPane opens a specific detail pane and loads its content
func (m Model) openDetailPane(pane DetailPane) (tea.Model, tea.Cmd) {
	m.ActiveDetailPane = pane
	m.ShowLogs = true
	// Don't auto-focus the detail pane - keep current focus

	if m.Cursor >= len(m.Jobs) {
		return m, nil
	}

	job := m.Jobs[m.Cursor]
	m.ActiveLogJob = job

	// Centralized layout calculation
	m.updateLayoutDimensions()
	m.LogViewer = logviewer.New(m.LogViewerWidth, m.LogViewerHeight)
	m.LogViewer, _ = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight})

	// Update viewport sizes for all panes
	m.frontmatterViewport.Width = m.LogViewerWidth
	m.frontmatterViewport.Height = m.LogViewerHeight
	m.briefingViewport.Width = m.LogViewerWidth
	m.briefingViewport.Height = m.LogViewerHeight
	m.editViewport.Width = m.LogViewerWidth
	m.editViewport.Height = m.LogViewerHeight

	// Trigger content loading for the active pane
	switch pane {
	case LogsPaneDetail:
		isAgentJob := job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeHeadlessAgent
		isRunning := job.Status == orchestration.JobStatusRunning
		m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Loading logs for %s...", job.Title))
		if isAgentJob && isRunning {
			return m, loadAndStreamAgentLogsCmd(m.Plan, job)
		}
		return m, loadLogContentCmd(m.Plan, job)
	case FrontmatterPane:
		m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Loading frontmatter for %s...", job.Title))
		return m, loadFrontmatterCmd(job)
	case BriefingPane:
		m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Loading briefing for %s...", job.Title))
		return m, loadBriefingCmd(m.Plan, job)
	case EditPane:
		m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Loading file content for %s...", job.Title))
		return m, loadJobFileContentCmd(job)
	}

	return m, nil
}
