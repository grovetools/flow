package status

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	markdown "github.com/grovetools/core/tui/components/markdown"
	"github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/logging/logutil"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/components/logviewer"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/tui/utils/scrollbar"
	"github.com/grovetools/flow/pkg/orchestration"
)

// Update handles messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case streamMsg:
		// A message arrived via the Model's MsgCh channel from a background
		// streaming goroutine. Re-arm the listener, then dispatch the inner
		// message through Update so it is handled by its normal case.
		updated, innerCmd := m.Update(msg.Inner)
		relisten := m.listenStream()
		if innerCmd != nil {
			return updated, tea.Batch(innerCmd, relisten)
		}
		return updated, relisten

	case embed.FocusMsg:
		// Host gave this panel focus. Nothing additional to do — the model
		// already routes keys through its own Focus state machine once the
		// bubbletea program routes events here.
		return m, nil

	case embed.BlurMsg:
		// Host withdrew focus. Cancel any active log stream so we don't keep
		// spinning goroutines while off-screen.
		if m.StreamCancel != nil {
			m.StreamCancel()
			m.StreamCancel = nil
			m.StreamingJobID = ""
		}
		return m, nil

	case embed.SetWorkspaceMsg:
		// Host switched workspace context. The active plan for a workspace is
		// resolved by the host and passed in via a custom message; the status
		// TUI itself is plan-scoped, so we just clear any stale state and let
		// the host re-initialize us via a fresh New() if the plan changed.
		if msg.Node != nil {
			m.PlanDir = msg.Node.Path
		}
		return m, refreshPlan(m.PlanDir)

	case LogContentLoadedMsg:
		// Guard clause: Discard stale messages for jobs that are no longer selected
		if m.ActiveLogJob == nil || msg.JobID != m.ActiveLogJob.ID {
			// This is a stale message for a job that is no longer selected.
			// Discard it to prevent race conditions where old content overwrites new.
			return m, nil
		}

		// Reset markdown state for fresh log loading
		m.MarkdownInCodeBlock = false

		logger := logging.NewLogger("flow-tui")
		activeJobID := ""
		if m.ActiveLogJob != nil {
			activeJobID = m.ActiveLogJob.ID
		}
		logger.WithFields(map[string]interface{}{
			"msg_job_id":        msg.JobID,
			"active_job_id":     activeJobID,
			"has_error":         msg.Err != nil,
			"should_retry":      msg.ShouldRetry,
			"start_streaming":   msg.StartStreaming,
			"content_length":    len(msg.Content),
			"show_logs":         m.ShowLogs,
			"active_log_job":    m.ActiveLogJob != nil,
			"log_file_path":     msg.LogFilePath,
			"streaming_job_id":  m.StreamingJobID,
			"has_stream_cancel": m.StreamCancel != nil,
		}).Debug("Received LogContentLoadedMsg")

		// Discard messages for jobs we're not currently viewing
		if m.ActiveLogJob == nil || (msg.JobID != "" && msg.JobID != m.ActiveLogJob.ID) {
			logger.WithFields(map[string]interface{}{
				"msg_job_id":    msg.JobID,
				"active_job_id": activeJobID,
			}).Debug("Discarding LogContentLoadedMsg for different/stale job")
			return m, nil
		}

		if msg.Err != nil {
			m.LogViewer.SetContent(theme.DefaultTheme.Error.Render(fmt.Sprintf("Error loading logs: %v", msg.Err)))
		} else {
			// Apply muted styling to "No logs found" messages
			content := msg.Content
			if strings.HasPrefix(content, "No logs found") {
				content = theme.DefaultTheme.Muted.Render(content)
			} else if m.ActiveLogJob != nil {
				// Apply markdown styling for agent job types
				isAgentJob := m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent ||
					m.ActiveLogJob.Type == orchestration.JobTypeHeadlessAgent ||
					m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent
				if isAgentJob {
					content = renderStyledMarkdown(content)
				}
			}
			m.LogViewer.SetContent(content)
		}

		var cmds []tea.Cmd

		// If we should retry (agent session hasn't started yet), schedule a retry
		if msg.ShouldRetry {
			logger.Debug("Scheduling retry for agent log loading")
			cmds = append(cmds, retryLoadAgentLogsAfterDelay())
		}

		// If we should start streaming (session is ready), start the stream
		if msg.StartStreaming && m.ActiveLogJob != nil && msg.LogFilePath != "" {
			// Only start streaming if we're not already streaming for this job,
			// OR if our cancel function is nil (meaning we don't control the active stream)
			if m.StreamingJobID != m.ActiveLogJob.ID || m.StreamCancel == nil {
				logger.WithFields(map[string]interface{}{
					"job_id":        m.ActiveLogJob.ID,
					"log_file_path": msg.LogFilePath,
					"was_streaming": m.StreamingJobID,
				}).Debug("Starting agent log streaming")

				// Cancel any existing stream to prevent leaks
				if m.StreamCancel != nil {
					m.StreamCancel()
				}

				ctx, cancel := context.WithCancel(context.Background())
				m.StreamCancel = cancel
				m.StreamingJobID = m.ActiveLogJob.ID

				cmds = append(cmds, streamAgentLogsCmd(ctx, m.Plan, m.ActiveLogJob, msg.LogFilePath, m.MsgCh))

				// Start status polling for agent jobs that support it
				isAgentWithStatus := m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent ||
					m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent
				if isAgentWithStatus {
					cmds = append(cmds, pollAgentStatusAfterDelay())
				}
			} else {
				logger.WithFields(map[string]interface{}{
					"job_id": m.ActiveLogJob.ID,
				}).Debug("Skipping duplicate stream start - already streaming for this job")
			}
		} else {
			// Current job doesn't need streaming (e.g., non-agent job, or agent job not ready).
			// Stop any active stream to prevent leaks from previous jobs.
			if m.StreamCancel != nil {
				m.StreamCancel()
				m.StreamCancel = nil
				m.StreamingJobID = ""
			}
		}

		if len(cmds) > 0 {
			logger.WithFields(map[string]interface{}{
				"num_cmds": len(cmds),
			}).Debug("Returning batched commands")
			return m, tea.Batch(cmds...)
		}
		logger.Debug("No commands to return")
		return m, nil

	case StreamEndedMsg:
		// The aglogs stream process exited — clear streaming state so it can be restarted
		if m.StreamingJobID == msg.JobID {
			logger := logging.NewLogger("flow-tui")
			logger.WithFields(map[string]interface{}{
				"job_id": msg.JobID,
			}).Debug("Stream ended, clearing streaming state")
			m.StreamingJobID = ""
			if m.StreamCancel != nil {
				m.StreamCancel()
				m.StreamCancel = nil
			}
			// If the job is still running, schedule a retry to restart streaming
			if m.ActiveLogJob != nil && m.ActiveLogJob.ID == msg.JobID &&
				(m.ActiveLogJob.Status == orchestration.JobStatusRunning || m.ActiveLogJob.Status == orchestration.JobStatusIdle) {
				logger.Debug("Job still running after stream ended, scheduling retry")
				return m, retryLoadAgentLogsAfterDelay()
			}
		}
		return m, nil

	case FrontmatterContentLoadedMsg:
		if m.ActiveDetailPane == FrontmatterPane {
			if msg.Err != nil {
				m.frontmatterRawContent = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v", msg.Err))
				m.frontmatterViewport.SetContent(m.frontmatterRawContent)
			} else {
				// Store the raw, unstyled content
				m.frontmatterRawContent = msg.Content
				// Ensure layout dimensions are current before wrapping
				m.updateLayoutDimensions()
				m.frontmatterViewport.Width = m.LogViewerWidth
				m.frontmatterViewport.Height = m.LogViewerHeight - logHeaderHeight
				// Render styled frontmatter and wrap to viewport width - 1 for scrollbar
				styledContent := renderStyledFrontmatter(m.frontmatterRawContent)
				wrappedContent := wrapContentForViewport(styledContent, m.frontmatterViewport.Width-1)
				m.frontmatterViewport.SetContent(wrappedContent)
			}
		}
		return m, nil

	case BriefingContentLoadedMsg:
		if m.ActiveDetailPane == BriefingPane {
			if msg.Err != nil {
				m.briefingRawContent = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v", msg.Err))
				m.briefingViewport.SetContent(m.briefingRawContent)
			} else {
				// Store the raw, unstyled content
				m.briefingRawContent = msg.Content
				// Ensure layout dimensions are current before wrapping
				m.updateLayoutDimensions()
				m.briefingViewport.Width = m.LogViewerWidth
				m.briefingViewport.Height = m.LogViewerHeight - logHeaderHeight
				// Render styled briefing XML and wrap to viewport width - 1 for scrollbar
				styledContent := renderStyledBriefing(m.briefingRawContent)
				wrappedContent := wrapContentForViewport(styledContent, m.briefingViewport.Width-1)
				m.briefingViewport.SetContent(wrappedContent)
			}
		}
		return m, nil

	case EditContentLoadedMsg:
		if m.ActiveDetailPane == EditPane {
			if msg.Err != nil {
				m.editRawContent = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v", msg.Err))
				m.editViewport.SetContent(m.editRawContent)
			} else {
				// Store the raw, unstyled content
				m.editRawContent = msg.Content
				// Ensure layout dimensions are current before wrapping
				m.updateLayoutDimensions()
				m.editViewport.Width = m.LogViewerWidth
				m.editViewport.Height = m.LogViewerHeight - logHeaderHeight
				// Render styled markdown and wrap to viewport width - 1 for scrollbar
				styledContent := renderStyledMarkdown(m.editRawContent)
				wrappedContent := wrapContentForViewport(styledContent, m.editViewport.Width-1)
				m.editViewport.SetContent(wrappedContent)
			}
		}
		return m, nil

	case PollAgentStatusMsg:
		// Only poll if we're showing logs for an agent job
		if m.ShowLogs && m.ActiveDetailPane == LogsPaneDetail && m.ActiveLogJob != nil {
			isAgentJob := m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent ||
				m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent
			if isAgentJob {
				return m, fetchAgentStatusCmd(m.Plan, m.ActiveLogJob)
			}
		}
		return m, nil

	case AgentStatusMsg:
		// Discard if this is for a different job
		if m.ActiveLogJob == nil || msg.JobID != m.ActiveLogJob.ID {
			return m, nil
		}

		if msg.Err != nil {
			logger := logging.NewLogger("flow-tui")
			logger.WithFields(map[string]interface{}{
				"job_id": msg.JobID,
				"error":  msg.Err.Error(),
			}).Debug("Agent status poll failed")
		}

		// Update status with anti-flicker debounce for idle state
		// Requires two consecutive "idle" polls before showing idle (prevents flicker between turns)
		if msg.Err == nil && msg.Status != nil {
			if msg.Status.State == "running" {
				// Running state: update immediately, reset idle confirmation
				m.PendingIdleConfirmation = false
				m.CurrentAgentStatus = msg.Status
				m.updateLayoutDimensions()
			} else if msg.Status.State == "idle" {
				// Idle state: require two consecutive polls to confirm
				if m.PendingIdleConfirmation {
					// Second consecutive idle - confirmed, show idle state
					m.CurrentAgentStatus = msg.Status
					m.updateLayoutDimensions()
				} else {
					// First idle after running - mark as pending, don't update yet
					m.PendingIdleConfirmation = true
					// Keep showing previous status (running) until confirmed
				}
			} else {
				// Unknown state - update anyway
				m.PendingIdleConfirmation = false
				m.CurrentAgentStatus = msg.Status
				m.updateLayoutDimensions()
			}
		}

		// Continue polling if we're still showing this agent job
		if m.ShowLogs && m.ActiveDetailPane == LogsPaneDetail {
			return m, pollAgentStatusAfterDelay()
		}
		return m, nil

	case daemonStreamConnectedMsg:
		// Daemon SSE stream is established: bind the channel + cancel to
		// the model and start listening. Ownership of the stream lifetime
		// now rests with the Model (torn down by Close()).
		m.DaemonConnected = true
		m.streamCh = msg.ch
		m.streamCancel = msg.cancel
		return m, m.listenToDaemon()

	case daemonStateUpdateMsg:
		// Trigger refresh for session updates and all job lifecycle events.
		// Job events (job_submitted, job_started, job_completed, job_failed,
		// job_cancelled, job_pending_user) must trigger a plan refresh so the
		// TUI's dependency graph stays in sync with the daemon.
		switch msg.update.UpdateType {
		case "session",
			"job_submitted", "job_started", "job_completed",
			"job_failed", "job_cancelled", "job_pending_user":
			return m, tea.Batch(
				refreshPlan(m.PlanDir),
				m.listenToDaemon(),
			)
		}
		return m, m.listenToDaemon()

	case daemonStreamErrorMsg:
		m.DaemonConnected = false
		// Fall back to polling (already running)
		return m, nil

	case RetryLoadAgentLogsMsg:
		logger := logging.NewLogger("flow-tui")
		logger.WithFields(map[string]interface{}{
			"show_logs":      m.ShowLogs,
			"has_active_job": m.ActiveLogJob != nil,
		}).Debug("Received RetryLoadAgentLogsMsg")

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
				isAgentJob := currentJob.Type == orchestration.JobTypeInteractiveAgent || currentJob.Type == orchestration.JobTypeHeadlessAgent || currentJob.Type == orchestration.JobTypeIsolatedAgent

				logger.WithFields(map[string]interface{}{
					"job_id":       currentJob.ID,
					"is_agent_job": isAgentJob,
					"job_status":   currentJob.Status,
				}).Debug("Retry conditions checked")

				// Retry as long as it's an agent job, regardless of status
				// This allows us to pick up logs even if the job completed quickly
				if isAgentJob {
					// Update the active log job reference and retry loading agent logs
					m.ActiveLogJob = currentJob
					logger.Debug("Retrying agent log load")
					return m, loadAndStreamAgentLogsCmd(m.Plan, currentJob)
				}
			} else {
				logger.Warn("Could not find current job for retry")
			}
		} else {
			logger.Debug("Not retrying - logs not shown or no active job")
		}
		return m, nil

	case clawResultMsg:
		if msg.Err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error toggling claw: %v", msg.Err))
		} else if msg.Enabled {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Claw enabled (signal + autonomous)")
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Claw disabled")
		}
		return m, refreshPlan(m.PlanDir)

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

	case recipeAddedMsg:
		if msg.err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error adding recipe: %v", msg.err))
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render("* Recipe jobs added successfully.")
		}
		return m, refreshPlan(m.PlanDir)

	case JobCompletedMsg:
		// Job completion finished (from 'c' key)
		if msg.Err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error completing job: %v", msg.Err))
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Job marked as completed.")
		}
		// Refresh the plan to show the updated status
		return m, refreshPlan(m.PlanDir)

	case JobRunFinishedMsg:
		// Job run completed
		m.IsRunningJob = false

		// Check if any of the completed jobs were interactive agents.
		// Their "run" phase is just the launch, so we don't want to stop the log stream.
		containsInteractiveAgent := false
		containsIsolatedAgent := false
		if msg.Jobs != nil {
			for _, job := range msg.Jobs {
				// IMPORTANT: This check should only be for JobTypeInteractiveAgent.
				// Headless agents are blocking and their completion is final.
				if job.Type == orchestration.JobTypeInteractiveAgent {
					containsInteractiveAgent = true
				}
				if job.Type == orchestration.JobTypeIsolatedAgent {
					containsIsolatedAgent = true
				}
			}
		}

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
			if containsInteractiveAgent {
				m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Interactive agent launched successfully.")
			} else if containsIsolatedAgent {
				m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Isolated agent launched. Press 's' to send input.")
				// For isolated agents, activate the input mode
				m.IsolatedAgentInputActive = true
				m.IsolatedAgentInput.Focus()
			} else {
				m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Job run completed successfully.")
			}
		}

		// Only stop the log viewer and return focus to the jobs pane if no interactive or isolated agents were launched.
		// Both types continue running in tmux, so we want to keep streaming their logs.
		if !containsInteractiveAgent && !containsIsolatedAgent {
			// Stop following the log file
			m.LogViewer.Stop()

			// Keep the log viewer open so the user can review the output
			// They can press 'v' to close it when ready

			// Return focus to jobs pane
			m.Focus = FocusJobs
		}

		// Refresh the plan to show updated statuses
		return m, refreshPlan(m.PlanDir)

	case JobSubmittedMsg:
		// Jobs have been submitted to the daemon
		if msg.Err != nil {
			m.IsRunningJob = false
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Failed to submit jobs: %v", msg.Err))
			return m, refreshPlan(m.PlanDir)
		}

		m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Submitted %d job(s) to daemon", len(msg.Jobs)))
		m.Selected = make(map[string]bool) // Clear selection after submission

		// Start streaming logs for the first agent job (or first job)
		var cmds []tea.Cmd
		if m.DaemonClient != nil && len(msg.JobIDs) > 0 {
			// Cancel any existing stream
			if m.StreamCancel != nil {
				m.StreamCancel()
			}
			ctx, cancel := context.WithCancel(context.Background())
			m.StreamCancel = cancel
			m.StreamingJobID = msg.JobIDs[0]

			cmds = append(cmds, streamDaemonLogsCmd(ctx, m.DaemonClient, msg.JobIDs[0], m.MsgCh))
		}
		cmds = append(cmds, refreshPlan(m.PlanDir))
		return m, tea.Batch(cmds...)

	case DaemonJobStatusMsg:
		// Daemon reported a job status change via SSE
		logger := logging.NewLogger("flow-tui")
		logger.WithFields(map[string]interface{}{
			"job_id": msg.JobID,
			"status": msg.Status,
		}).Debug("Received daemon job status update")

		if msg.Status == "completed" || msg.Status == "failed" || msg.Status == "cancelled" || msg.Status == "pending_user" {
			m.IsRunningJob = false

			if msg.Status == "completed" {
				m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Job completed successfully.")
			} else if msg.Status == "failed" {
				errMsg := "Job failed"
				if msg.Error != "" {
					errMsg = fmt.Sprintf("Job failed: %s", msg.Error)
				}
				m.StatusSummary = theme.DefaultTheme.Error.Render(errMsg)
			} else if msg.Status == "pending_user" {
				m.StatusSummary = theme.DefaultTheme.Info.Render("Job awaiting user input.")
			} else {
				m.StatusSummary = theme.DefaultTheme.Warning.Render("Job cancelled.")
			}

			// Stop following, keep viewer open
			m.LogViewer.Stop()
			m.Focus = FocusJobs
		}

		return m, refreshPlan(m.PlanDir)

	case EditFileInTmuxMsg:
		if msg.Err != nil {
			m.Err = msg.Err
			return m, nil
		}
		return m, tea.Quit

	case embed.EditFinishedMsg:
		if msg.Err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render("Editor error: " + msg.Err.Error())
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render("Editor closed")
		}
		// Refresh both the skill pane and the plan itself to pick up edits
		m.refreshSkillPane()
		return m, refreshPlan(m.PlanDir)

	case IsolatedAgentInputSentMsg:
		// Handle response from sending input to isolated agent
		if msg.Err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Failed to send input: %v", msg.Err))
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Sent")
			// Don't capture pane output - log streaming will handle updates
			// Capturing would show the raw input prompt which is redundant
		}
		return m, nil

	case InterruptAgentMsg:
		// Handle response from interrupting an agent
		if msg.Err != nil {
			m.StatusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Failed to interrupt: %v", msg.Err))
		} else {
			m.StatusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Interrupt sent (Ctrl+C)")
		}
		return m, nil

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
		// Skip when daemon is connected — the daemon handles session liveness
		if !m.DaemonConnected {
			orchestration.VerifyRunningJobStatus(plan)
		}

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

		// Auto-refresh skill pane if it's active
		if m.ActiveDetailPane == SkillPane && m.ActiveLogJob != nil {
			m.refreshSkillPane()
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

		// Autorun logic: if autorunning, check for next runnable jobs from the original selection.
		// Instead of relying solely on m.IsRunningJob, verify no originally-selected jobs
		// are still running — this handles the case where multiple parallel jobs finish at
		// different times.
		if m.isAutorunning && m.originalSelection != nil {
			// Check if any originally-selected jobs are still running
			anyStillRunning := false
			anyFailed := false
			for _, job := range m.Jobs {
				if m.originalSelection[job.ID] {
					if job.Status == orchestration.JobStatusRunning {
						anyStillRunning = true
						break
					}
					if job.Status == orchestration.JobStatusFailed {
						anyFailed = true
					}
				}
			}

			if !anyStillRunning {
				// If any originally-selected job failed, halt autorun
				if anyFailed {
					m.isAutorunning = false
					m.originalSelection = nil
					m.IsRunningJob = false
					m.StatusSummary = theme.DefaultTheme.Error.Render("Autorun halted: a job failed.")
				} else {
					// Get the next set of runnable jobs that were in the original selection
					allRunnable := m.Graph.GetRunnableJobs()
					var selectedRunnable []*orchestration.Job
					for _, job := range allRunnable {
						if m.originalSelection[job.ID] {
							selectedRunnable = append(selectedRunnable, job)
						}
					}

					if len(selectedRunnable) > 0 {
						m.StatusSummary = theme.DefaultTheme.Info.Render("Starting next stage...")
						// Clear old selections and select only the newly runnable jobs
						m.Selected = make(map[string]bool)
						for _, job := range selectedRunnable {
							m.Selected[job.ID] = true
						}
						// Trigger the run by sending a 'r' key message
						return m, func() tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}} }
					} else {
						// No more runnable jobs from the selection, autorun is complete
						m.isAutorunning = false
						m.originalSelection = nil
						m.IsRunningJob = false
						m.Selected = make(map[string]bool) // Clear selection when autorun is complete
						m.StatusSummary = theme.DefaultTheme.Success.Render("All jobs completed successfully.")
					}
				}
			}
		}

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
		m.frontmatterViewport.Height = m.LogViewerHeight - logHeaderHeight
		m.briefingViewport.Width = m.LogViewerWidth
		m.briefingViewport.Height = m.LogViewerHeight - logHeaderHeight
		m.editViewport.Width = m.LogViewerWidth
		m.editViewport.Height = m.LogViewerHeight - logHeaderHeight
		m.updateSkillViewportSizes()

		// Re-wrap content for all detail viewports to adapt to the new size
		if m.frontmatterRawContent != "" {
			styledContent := renderStyledFrontmatter(m.frontmatterRawContent)
			wrappedContent := wrapContentForViewport(styledContent, m.frontmatterViewport.Width-1)
			m.frontmatterViewport.SetContent(wrappedContent)
		}
		if m.briefingRawContent != "" {
			styledContent := renderStyledBriefing(m.briefingRawContent)
			wrappedContent := wrapContentForViewport(styledContent, m.briefingViewport.Width-1)
			m.briefingViewport.SetContent(wrappedContent)
		}
		if m.editRawContent != "" {
			styledContent := renderStyledMarkdown(m.editRawContent)
			wrappedContent := wrapContentForViewport(styledContent, m.editViewport.Width-1)
			m.editViewport.SetContent(wrappedContent)
		}
		if m.skillPaneRawContent != "" {
			m.skillPaneViewport.SetContent(wrapContentForViewport(m.skillPaneRawContent, m.skillPaneViewport.Width-1))
		}

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
						m.LogViewer = logviewer.New(m.LogViewerWidth, m.LogViewerHeight-logHeaderHeight)
						cmd = m.LogViewer.Start(map[string]string{node.Name: logFile})
						return m, cmd
					}
				}
			}
			// If no logs found, still show the log viewer (it will show empty/waiting state)
			m.LogViewer = logviewer.New(m.LogViewerWidth, m.LogViewerHeight-logHeaderHeight)
		}

		// Update log viewer size if active
		if m.ShowLogs {
			m.LogViewer, cmd = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight - logHeaderHeight})
			return m, cmd
		}
		return m, nil

	case logviewer.LogLineMsg:
		// Delegate log messages to the log viewer only if the message
		// belongs to the currently active job being viewed.
		if m.ShowLogs && m.ActiveLogJob != nil && msg.Workspace == m.ActiveLogJob.ID {
			// Apply markdown styling for agent job types
			isAgentJob := m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent ||
				m.ActiveLogJob.Type == orchestration.JobTypeHeadlessAgent ||
				m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent
			if isAgentJob {
				msg.Line = styleStreamingLogLine(msg.Line, &m.MarkdownInCodeBlock)
			}
			m.LogViewer, cmd = m.LogViewer.Update(msg)
			return m, cmd
		}
		// Discard the log line if it's for a different job.
		return m, nil

	case tea.KeyMsg:
		// Handle claw dialog
		if m.ClawDialogActive {
			switch msg.String() {
			case "enter":
				m.ClawDialogActive = false
				if m.ClawDisabling {
					// Unclaw
					return m, unclawJobCmd(m.Plan, m.Jobs[m.ClawDialogJobIndex])
				}
				// Enable claw
				idleStr := m.ClawIdleInput.Value()
				idleMinutes := 15
				if n, err := strconv.Atoi(idleStr); err == nil && n > 0 {
					idleMinutes = n
				}
				prompt := m.ClawPromptInput.Value()
				return m, clawJobCmd(m.Plan, m.Jobs[m.ClawDialogJobIndex], idleMinutes, prompt)
			case "esc":
				m.ClawDialogActive = false
				return m, nil
			case "tab", "shift+tab":
				if !m.ClawDisabling {
					m.ClawDialogFocus = 1 - m.ClawDialogFocus
					if m.ClawDialogFocus == 0 {
						m.ClawIdleInput.Focus()
						m.ClawPromptInput.Blur()
					} else {
						m.ClawIdleInput.Blur()
						m.ClawPromptInput.Focus()
					}
				}
				return m, nil
			}
			if !m.ClawDisabling {
				if m.ClawDialogFocus == 0 {
					m.ClawIdleInput, cmd = m.ClawIdleInput.Update(msg)
				} else {
					m.ClawPromptInput, cmd = m.ClawPromptInput.Update(msg)
				}
			}
			return m, cmd
		}

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

		// Handle agent input mode (for isolated_agent and interactive_agent)
		if m.IsolatedAgentInputActive {
			switch msg.String() {
			case "enter":
				if m.ActiveLogJob != nil && m.IsolatedAgentInput.Value() != "" {
					input := m.IsolatedAgentInput.Value()
					m.IsolatedAgentInput.SetValue("") // Clear input
					m.StatusSummary = fmt.Sprintf("Sending: %s", input)
					// Dispatch to the correct send function based on job type
					if m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent {
						return m, sendIsolatedAgentInputCmd(m.ActiveLogJob.ID, input)
					} else if m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent {
						return m, sendInteractiveAgentInputCmd(m.Plan, m.ActiveLogJob, input)
					}
				}
			case "esc":
				// Unfocus the input but keep it visible (for isolated agents, it's always visible)
				m.IsolatedAgentInputActive = false
				m.IsolatedAgentInput.Blur()
				m.Focus = FocusDetailPrimary
				m.StatusSummary = ""
				// Don't call updateLayoutDimensions - the chat box visibility is based on job type, not focus
				return m, nil
			}
			m.IsolatedAgentInput, cmd = m.IsolatedAgentInput.Update(msg)
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
				selectedStatus := statuses[m.StatusPickerCursor]

				// Set status for selected jobs or current job if none selected
				if len(m.Selected) > 0 {
					// Set status for all selected jobs
					var jobsToUpdate []*orchestration.Job
					for id := range m.Selected {
						for _, job := range m.Jobs {
							if job.ID == id {
								jobsToUpdate = append(jobsToUpdate, job)
								break
							}
						}
					}
					return m, tea.Sequence(
						setMultipleJobStatus(jobsToUpdate, m.Plan, selectedStatus),
						refreshPlan(m.PlanDir),
					)
				} else if m.Cursor < len(m.Jobs) {
					// Set status for cursor job only
					job := m.Jobs[m.Cursor]
					return m, tea.Sequence(
						setJobStatus(job, m.Plan, selectedStatus),
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

		// Handle type picker
		if m.ShowTypePicker {
			switch msg.String() {
			case "up", "k":
				if m.TypePickerCursor > 0 {
					m.TypePickerCursor--
				}
				return m, nil
			case "down", "j":
				if m.TypePickerCursor < 7 { // 8 type options (0-7)
					m.TypePickerCursor++
				}
				return m, nil
			case "enter":
				m.ShowTypePicker = false
				types := []orchestration.JobType{
					orchestration.JobTypeShell,
					orchestration.JobTypeOneshot,
					orchestration.JobTypeChat,
					orchestration.JobTypeAgent,
					orchestration.JobTypeInteractiveAgent,
					orchestration.JobTypeHeadlessAgent,
					orchestration.JobTypeGenerateRecipe,
					orchestration.JobTypeFile,
				}
				selectedType := types[m.TypePickerCursor]

				// Set type for selected jobs or current job if none selected
				if len(m.Selected) > 0 {
					// Set type for all selected jobs
					var jobsToUpdate []*orchestration.Job
					for id := range m.Selected {
						for _, job := range m.Jobs {
							if job.ID == id {
								jobsToUpdate = append(jobsToUpdate, job)
								break
							}
						}
					}
					return m, tea.Sequence(
						setMultipleJobType(jobsToUpdate, m.Plan, selectedType),
						refreshPlan(m.PlanDir),
					)
				} else if m.Cursor < len(m.Jobs) {
					// Set type for cursor job only
					job := m.Jobs[m.Cursor]
					return m, tea.Sequence(
						setJobType(job, m.Plan, selectedType),
						refreshPlan(m.PlanDir),
					)
				}
				return m, nil
			case "esc", "ctrl+c", "q", "b":
				m.ShowTypePicker = false
				return m, nil
			default:
				// Any other key while type picker is open - just consume it
				return m, nil
			}
		}

		// Handle template picker
		if m.ShowTemplatePicker {
			switch msg.String() {
			case "up", "k":
				if m.TemplatePickerCursor > 0 {
					m.TemplatePickerCursor--
				}
				return m, nil
			case "down", "j":
				if m.TemplatePickerCursor < 4 { // 5 common templates (0-4)
					m.TemplatePickerCursor++
				}
				return m, nil
			case "enter":
				m.ShowTemplatePicker = false
				templates := []string{
					"", // No template (clear)
					"agent-xml",
					"agent-run",
					"agent-from-chat",
					"chat",
				}
				selectedTemplate := templates[m.TemplatePickerCursor]

				// Set template for selected jobs or current job if none selected
				if len(m.Selected) > 0 {
					// Set template for all selected jobs
					var jobsToUpdate []*orchestration.Job
					for id := range m.Selected {
						for _, job := range m.Jobs {
							if job.ID == id {
								jobsToUpdate = append(jobsToUpdate, job)
								break
							}
						}
					}
					return m, tea.Sequence(
						setMultipleJobTemplate(jobsToUpdate, m.Plan, selectedTemplate),
						refreshPlan(m.PlanDir),
					)
				} else if m.Cursor < len(m.Jobs) {
					// Set template for cursor job only
					job := m.Jobs[m.Cursor]
					return m, tea.Sequence(
						setJobTemplate(job, m.Plan, selectedTemplate),
						refreshPlan(m.PlanDir),
					)
				}
				return m, nil
			case "esc", "ctrl+c", "q", "b":
				m.ShowTemplatePicker = false
				return m, nil
			default:
				// Any other key while template picker is open - just consume it
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
				// Recalculate layout dimensions with new column visibility
				m.updateLayoutDimensions()
				// Update log viewer size if active
				if m.ShowLogs {
					m.LogViewer, cmd = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight - logHeaderHeight})
					return m, cmd
				}
				return m, nil
			case " ":
				// Toggle selection
				if i, ok := m.columnList.SelectedItem().(columnSelectItem); ok {
					m.columnVisibility[i.name] = !m.columnVisibility[i.name]
					// Save state to disk
					_ = saveState(m.columnVisibility, m.LogSplitVertical)
				}
				return m, nil
			default:
				m.columnList, cmd = m.columnList.Update(msg)
				return m, cmd
			}
		}

		// Handle recipe selection mode
		if m.selectingRecipe {
			switch msg.String() {
			case "enter":
				if i, ok := m.recipeList.SelectedItem().(recipeItem); ok {
					m.selectingRecipe = false
					// Get selected jobs as external dependencies
					var externalDeps []string
					for id := range m.Selected {
						for _, job := range m.Jobs {
							if job.ID == id {
								externalDeps = append(externalDeps, job.Filename)
								break
							}
						}
					}
					// Trigger the command to add the recipe
					m.StatusSummary = "Adding jobs from recipe..."
					return m, addJobsFromRecipeCmd(m.Plan, i.name, externalDeps)
				}
			case "esc", "q":
				m.selectingRecipe = false
				m.StatusSummary = ""
				return m, nil
			}
			m.recipeList, cmd = m.recipeList.Update(msg)
			return m, cmd
		}

		// If help is showing, let it handle key messages (for scrolling and closing)
		if m.Help.ShowAll {
			var cmd tea.Cmd
			m.Help, cmd = m.Help.Update(msg)
			return m, cmd
		}

		// Dispatch to focused pane handlers for detail and secondary panes
		if m.ShowLogs && (m.Focus == FocusDetailPrimary || m.Focus == FocusDetailSecondary) {
			// Global keys that should pass through to the main switch
			switch msg.String() {
			case "q", "ctrl+c", "?", "F", "l", "f", "b", "m", "p", "v",
				"tab", "shift+tab", "V", "z", "i", "s", "esc":
				// Let these be handled by the main logic below
			default:
				if m.Focus == FocusDetailPrimary {
					return m.handleDetailPrimaryKey(msg)
				}
				return m.handleDetailSecondaryKey(msg)
			}
		}

		// Handle 'gg' sequence for going to top
		if msg.String() == "g" {
			result, _ := m.Sequence.Process(msg, m.KeyMap.Top)
			if result == keymap.SequenceMatch {
				// gg - go to top
				m.Cursor = 0
				m.ScrollOffset = 0
				m.Sequence.Clear()
			}
			// First 'g' or sequence in progress - sequence is tracking it
			return m, nil
		} else {
			// Any other key clears the sequence
			m.Sequence.Clear()
		}

		switch {
		case key.Matches(msg, m.KeyMap.Quit):
			// Tear down the per-Model daemon stream and wait for listener
			// goroutines to exit before handing control back to bubbletea.
			_ = m.Close()
			return m, tea.Quit

		case key.Matches(msg, m.KeyMap.Help):
			m.Help.Toggle()

		case key.Matches(msg, m.KeyMap.SwitchFocus):
			if m.ShowLogs && !m.LogPaneFullscreen {
				isShiftTab := msg.Type == tea.KeyShiftTab

				if m.ActiveDetailPane == SkillPane {
					// 3-way cycle: Jobs <-> Tree <-> ArtifactViewport
					if !isShiftTab {
						switch m.Focus {
						case FocusJobs:
							m.Focus = FocusDetailPrimary
						case FocusDetailPrimary:
							m.Focus = FocusDetailSecondary
						default:
							m.Focus = FocusJobs
						}
					} else {
						switch m.Focus {
						case FocusJobs:
							m.Focus = FocusDetailSecondary
						case FocusDetailSecondary:
							m.Focus = FocusDetailPrimary
						default:
							m.Focus = FocusJobs
						}
					}
					// Re-render skill pane to update separator highlight
					m.refreshSkillPane()
				} else {
					// 2-way cycle: Jobs <-> Detail
					if m.Focus == FocusJobs {
						m.Focus = FocusDetailPrimary
					} else {
						m.Focus = FocusJobs
					}
				}
			}

		case key.Matches(msg, m.KeyMap.ToggleLayout):
			if m.ShowLogs {
				m.LogSplitVertical = !m.LogSplitVertical
				// Save the new state
				_ = saveState(m.columnVisibility, m.LogSplitVertical)

				// Centralized layout calculation
				m.updateLayoutDimensions()

				// Update viewport sizes for all detail panes
				m.frontmatterViewport.Width = m.LogViewerWidth
				m.frontmatterViewport.Height = m.LogViewerHeight - logHeaderHeight
				m.briefingViewport.Width = m.LogViewerWidth
				m.briefingViewport.Height = m.LogViewerHeight - logHeaderHeight
				m.editViewport.Width = m.LogViewerWidth
				m.editViewport.Height = m.LogViewerHeight - logHeaderHeight
				m.updateSkillViewportSizes()

				// Re-wrap content for all detail viewports to adapt to the new layout
				if m.frontmatterRawContent != "" {
					styledContent := renderStyledFrontmatter(m.frontmatterRawContent)
					wrappedContent := wrapContentForViewport(styledContent, m.frontmatterViewport.Width-1)
					m.frontmatterViewport.SetContent(wrappedContent)
				}
				if m.briefingRawContent != "" {
					styledContent := renderStyledBriefing(m.briefingRawContent)
					wrappedContent := wrapContentForViewport(styledContent, m.briefingViewport.Width-1)
					m.briefingViewport.SetContent(wrappedContent)
				}
				if m.editRawContent != "" {
					styledContent := renderStyledMarkdown(m.editRawContent)
					wrappedContent := wrapContentForViewport(styledContent, m.editViewport.Width-1)
					m.editViewport.SetContent(wrappedContent)
				}
				if m.skillPaneRawContent != "" {
					m.skillPaneViewport.SetContent(wrapContentForViewport(m.skillPaneRawContent, m.skillPaneViewport.Width-1))
				}

				// Update log viewer with new dimensions
				m.LogViewer, cmd = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight - logHeaderHeight})
				return m, cmd
			}
			return m, nil

		case key.Matches(msg, m.KeyMap.ToggleFullscreen):
			if m.ShowLogs {
				m.LogPaneFullscreen = !m.LogPaneFullscreen

				// When entering fullscreen, force focus to logs pane
				if m.LogPaneFullscreen {
					m.Focus = FocusDetailPrimary
				}

				// Centralized layout calculation
				m.updateLayoutDimensions()

				// Update viewport sizes for all detail panes
				m.frontmatterViewport.Width = m.LogViewerWidth
				m.frontmatterViewport.Height = m.LogViewerHeight - logHeaderHeight
				m.briefingViewport.Width = m.LogViewerWidth
				m.briefingViewport.Height = m.LogViewerHeight - logHeaderHeight
				m.editViewport.Width = m.LogViewerWidth
				m.editViewport.Height = m.LogViewerHeight - logHeaderHeight
				m.updateSkillViewportSizes()

				// Re-wrap content for all detail viewports to adapt to the new layout
				if m.frontmatterRawContent != "" {
					styledContent := renderStyledFrontmatter(m.frontmatterRawContent)
					wrappedContent := wrapContentForViewport(styledContent, m.frontmatterViewport.Width-1)
					m.frontmatterViewport.SetContent(wrappedContent)
				}
				if m.briefingRawContent != "" {
					styledContent := renderStyledBriefing(m.briefingRawContent)
					wrappedContent := wrapContentForViewport(styledContent, m.briefingViewport.Width-1)
					m.briefingViewport.SetContent(wrappedContent)
				}
				if m.editRawContent != "" {
					styledContent := renderStyledMarkdown(m.editRawContent)
					wrappedContent := wrapContentForViewport(styledContent, m.editViewport.Width-1)
					m.editViewport.SetContent(wrappedContent)
				}
				if m.skillPaneRawContent != "" {
					m.skillPaneViewport.SetContent(wrapContentForViewport(m.skillPaneRawContent, m.skillPaneViewport.Width-1))
				}

				// Update log viewer with new dimensions
				m.LogViewer, cmd = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight - logHeaderHeight})
				return m, cmd
			}
			return m, nil

		case key.Matches(msg, m.KeyMap.Up):
			if m.Cursor > 0 {
				m.Cursor--
				m.adjustScrollOffset()
				if m.ShowLogs {
					return m.reloadActiveDetailPane()
				}
			}

		case key.Matches(msg, m.KeyMap.Down):
			if m.Cursor < len(m.Jobs)-1 {
				m.Cursor++
				m.adjustScrollOffset()
				if m.ShowLogs {
					return m.reloadActiveDetailPane()
				}
			}

		case key.Matches(msg, m.KeyMap.Bottom):
			if len(m.Jobs) > 0 {
				m.Cursor = len(m.Jobs) - 1
				m.adjustScrollOffset()
				if m.ShowLogs {
					return m.reloadActiveDetailPane()
				}
			}

		case key.Matches(msg, m.KeyMap.PageUp):
			pageSize := 10
			m.Cursor -= pageSize
			if m.Cursor < 0 {
				m.Cursor = 0
			}
			m.adjustScrollOffset()
			if m.ShowLogs {
				return m.reloadActiveDetailPane()
			}

		case key.Matches(msg, m.KeyMap.PageDown):
			pageSize := 10
			m.Cursor += pageSize
			if m.Cursor >= len(m.Jobs) {
				m.Cursor = len(m.Jobs) - 1
			}
			m.adjustScrollOffset()
			if m.ShowLogs {
				return m.reloadActiveDetailPane()
			}

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

		case key.Matches(msg, m.KeyMap.Edit), key.Matches(msg, m.KeyMap.Confirm):
			if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				return m, editJob(job)
			}

		case key.Matches(msg, m.KeyMap.CopyPath):
			if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				path := job.FilePath
				if err := clipboard.WriteAll(path); err != nil {
					m.StatusSummary = fmt.Sprintf("Error copying path: %v", err)
				} else {
					m.StatusSummary = fmt.Sprintf("Copied: %s", path)
				}
			}
			return m, nil

		case key.Matches(msg, m.KeyMap.ViewLogs):
			return m.openDetailPane(LogsPaneDetail)

		case key.Matches(msg, m.KeyMap.ViewFrontmatter):
			return m.openDetailPane(FrontmatterPane)

		case key.Matches(msg, m.KeyMap.ViewBriefing):
			return m.openDetailPane(BriefingPane)

		case key.Matches(msg, m.KeyMap.ViewEdit):
			return m.openDetailPane(EditPane)

		case key.Matches(msg, m.KeyMap.ViewSkillPane):
			return m.openDetailPane(SkillPane)

		case key.Matches(msg, m.KeyMap.CycleDetailPane):
			// Toggle detail pane visibility (show/hide)
			if m.ActiveDetailPane == NoPane {
				// If closed, open logs pane by default
				return m.openDetailPane(LogsPaneDetail)
			} else {
				// If any pane is open, close it
				m.LogViewer.Stop()
				if m.StreamCancel != nil {
					m.StreamCancel()
					m.StreamCancel = nil
				}
				m.StreamingJobID = ""
				m.ShowLogs = false
				m.Focus = FocusJobs
				m.ActiveLogJob = nil
				m.ActiveDetailPane = NoPane
				m.CurrentAgentStatus = nil // Clear agent status when closing pane
				m.StatusSummary = ""
				return m, nil
			}

		case key.Matches(msg, m.KeyMap.CloseDetailPane):
			// For agent jobs (isolated_agent, interactive_agent), Esc should only blur
			// the input (handled above), not close the detail pane. Users can close
			// with 'v' (CycleDetailPane) instead.
			// However, double-Esc (two Esc presses within 500ms) will interrupt the agent.
			isAgentJob := m.ActiveLogJob != nil &&
				(m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent ||
					m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent)
			if isAgentJob {
				now := time.Now()
				doubleEscThreshold := 500 * time.Millisecond

				// Check if this is a double-Esc (within threshold of last Esc press)
				if !m.LastEscPress.IsZero() && now.Sub(m.LastEscPress) < doubleEscThreshold {
					m.LastEscPress = time.Time{} // Reset to prevent triple-Esc triggering again

					// Use daemon cancel if available and streaming a daemon job
					if m.DaemonClient != nil && m.DaemonClient.IsRunning() && m.StreamingJobID != "" {
						m.StatusSummary = theme.DefaultTheme.Warning.Render("Cancelling job via daemon...")
						return m, cancelJobViaDaemonCmd(m.DaemonClient, m.StreamingJobID)
					}

					// Fall back to tmux-based interrupt
					if m.CurrentAgentStatus != nil && m.CurrentAgentStatus.State == "running" {
						m.StatusSummary = theme.DefaultTheme.Warning.Render("Interrupting agent...")
						return m, interruptAgentCmd(m.Plan, m.ActiveLogJob)
					}
					// Agent not running, reset and show message
					m.StatusSummary = theme.DefaultTheme.Muted.Render("Agent not running - nothing to interrupt")
					return m, nil
				}

				// First Esc press - record time and show hint
				m.LastEscPress = now
				m.StatusSummary = theme.DefaultTheme.Muted.Render("Press Esc again to interrupt agent")
				return m, nil
			}
			m.LogViewer.Stop()
			if m.StreamCancel != nil {
				m.StreamCancel()
				m.StreamCancel = nil
			}
			m.StreamingJobID = ""
			m.ShowLogs = false
			m.LogPaneFullscreen = false
			m.Focus = FocusJobs
			m.ActiveLogJob = nil
			m.ActiveDetailPane = NoPane
			m.CurrentAgentStatus = nil // Clear agent status when closing pane
			m.StatusSummary = ""
			// Also clear isolated agent input mode
			m.IsolatedAgentInputActive = false
			m.IsolatedAgentInput.Blur()
			return m, nil

		case key.Matches(msg, m.KeyMap.SendInput):
			// Focus the chat input when viewing an agent job that supports input
			// The 'i' key focuses the input - it doesn't toggle visibility since chat is always shown for these agent types
			isAgentWithInput := m.ActiveLogJob != nil &&
				(m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent ||
					m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent)
			if isAgentWithInput {
				// Ensure logs pane is open and focused
				if m.ActiveDetailPane != LogsPaneDetail {
					// Switch to logs pane
					m.ActiveDetailPane = LogsPaneDetail
					m.ShowLogs = true
				}
				// Focus the input
				m.IsolatedAgentInputActive = true
				m.IsolatedAgentInput.Focus()
				m.Focus = FocusInput
				m.StatusSummary = theme.DefaultTheme.Muted.Render("Type input for agent (Enter to send, Esc to cancel)")
				return m, nil
			}
			// Not an agent job that supports input - show helpful message
			m.StatusSummary = theme.DefaultTheme.Warning.Render("Send input only works for isolated_agent and interactive_agent jobs")
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

			// Determine if we're using the daemon (which has a blocked queue
			// and handles DAG traversal itself).
			usingDaemon := m.DaemonClient != nil && m.DaemonClient.IsRunning()

			// Filter out jobs that are not submittable.
			// When using the daemon, accept any pending/blocked/failed job — the
			// daemon will hold blocked ones and promote them as deps complete.
			// For non-daemon paths, keep the strict IsRunnable() check.
			var jobsToRun []*orchestration.Job
			var skippedReasons []string
			for _, job := range candidateJobs {
				logger.WithFields(map[string]interface{}{
					"job_id":     job.ID,
					"job_title":  job.Title,
					"job_status": job.Status,
				}).Debug("Checking if job is runnable")

				submittable := false
				if usingDaemon {
					// For daemon: accept pending, blocked, failed (retriable), and pending_user chats
					submittable = job.Status == orchestration.JobStatusPending ||
						job.Status == orchestration.JobStatusBlocked ||
						job.Status == orchestration.JobStatusFailed ||
						(job.Type == orchestration.JobTypeChat && job.Status == orchestration.JobStatusPendingUser)
				} else {
					submittable = job.IsRunnable()
				}

				if submittable {
					jobsToRun = append(jobsToRun, job)
				} else {
					var reason string
					switch job.Status {
					case orchestration.JobStatusCompleted:
						reason = "is already completed."
					case orchestration.JobStatusRunning:
						reason = "is already running."
					case orchestration.JobStatusAbandoned, orchestration.JobStatusHold:
						reason = "is on hold/abandoned."
					case orchestration.JobStatusPending, orchestration.JobStatusBlocked, orchestration.JobStatusFailed:
						reason = "is blocked by unmet dependencies."
					default:
						reason = fmt.Sprintf("is not in a runnable state (status: %s).", job.Status)
					}
					skippedReasons = append(skippedReasons, fmt.Sprintf("'%s' %s", job.Title, reason))
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

			// Check if this is an autorun trigger (user selected multiple jobs)
			// Store the original selection so we only run jobs that were selected
			if len(candidateJobs) > 1 && !usingDaemon {
				m.isAutorunning = true
				m.originalSelection = make(map[string]bool)
				for _, job := range candidateJobs {
					m.originalSelection[job.ID] = true
				}
			} else {
				m.isAutorunning = false
				m.originalSelection = nil
			}

			if len(jobsToRun) > 0 {
				// Clear the log viewer to prepare for new live output
				m.LogViewer.Clear()

				// Start running the jobs asynchronously
				m.IsRunningJob = true
				m.ActiveDetailPane = LogsPaneDetail // Switch to log viewer pane
				m.ShowLogs = true
				m.Focus = FocusDetailPrimary

				// Set the active job for the log pane header. If multiple jobs, use the first.
				// This might be overridden later if an agent job is found.
				m.ActiveLogJob = jobsToRun[0]

				// Centralized layout calculation
				m.updateLayoutDimensions()

				m.LogViewer = logviewer.New(m.LogViewerWidth, m.LogViewerHeight-logHeaderHeight)

				// Initialize the log viewer with a WindowSizeMsg to set it to ready state
				m.LogViewer, _ = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight - logHeaderHeight})

				// Update status message
				jobCount := len(jobsToRun)
				if m.isAutorunning {
					m.StatusSummary = theme.DefaultTheme.Info.Render("Autorunning all stages...")
				} else if jobCount == 1 {
					m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Running %s...", jobsToRun[0].Title))
				} else {
					m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Running %d job(s)...", jobCount))
				}

				// Run the jobs: prefer daemon, fall back to orchestrator, then subprocess
				var runCmd tea.Cmd
				if usingDaemon {
					logger.WithFields(map[string]interface{}{
						"num_jobs":   len(jobsToRun),
						"use_method": "daemon",
					}).Info("Submitting jobs via daemon")
					runCmd = submitJobsViaDaemonCmd(m.DaemonClient, m.Plan, jobsToRun)
				} else if m.Orchestrator != nil {
					logger.WithFields(map[string]interface{}{
						"num_jobs":   len(jobsToRun),
						"use_method": "orchestrator",
					}).Info("Running jobs via orchestrator")
					runCmd = runJobsWithOrchestrator(m.Orchestrator, jobsToRun, m.MsgCh)
				} else {
					logger.WithFields(map[string]interface{}{
						"num_jobs":         len(jobsToRun),
						"use_method":       "subprocess",
						"orchestrator_nil": m.Orchestrator == nil,
					}).Warn("Falling back to subprocess job execution")
					// Fallback to old method if orchestrator is not available
					runCmd = runJobsCmd(m.RunLogFile, m.PlanDir, jobsToRun)
				}

				var cmds []tea.Cmd
				cmds = append(cmds, runCmd)

				// For non-daemon paths, also start agent log streaming via aglogs.
				// When using daemon, log streaming starts after JobSubmittedMsg via streamDaemonLogsCmd.
				if !usingDaemon {
					for _, job := range jobsToRun {
						isAgentJob := job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeHeadlessAgent || job.Type == orchestration.JobTypeIsolatedAgent

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
				}

				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.KeyMap.SetCompleted):
			if m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				logger := logging.NewLogger("flow-tui")
				logger.WithFields(map[string]interface{}{
					"job_id":     job.ID,
					"job_title":  job.Title,
					"job_status": job.Status,
				}).Info("'c' key pressed - SetCompleted triggered")

				// Safety check: warn if trying to complete a running job that just started
				if job.Status == orchestration.JobStatusRunning {
					timeSinceStart := time.Since(job.StartTime)
					if timeSinceStart < 5*time.Second {
						logger.WithFields(map[string]interface{}{
							"job_id":           job.ID,
							"time_since_start": timeSinceStart,
						}).Warn("Blocked: attempted to complete job that just started (< 5 seconds)")
						m.StatusSummary = theme.DefaultTheme.Warning.Render(
							fmt.Sprintf("Job '%s' just started %s ago. Wait a moment before completing.", job.Title, timeSinceStart.Round(time.Second)))
						return m, nil
					}
				}

				return m, tea.Sequence(
					setJobCompleted(job, m.Plan, orchestration.CompleteJob),
					refreshPlan(m.PlanDir),
				)
			}

		case key.Matches(msg, m.KeyMap.SetStatus):
			if m.Cursor < len(m.Jobs) {
				m.ShowStatusPicker = true
				m.StatusPickerCursor = 0
			}

		case key.Matches(msg, m.KeyMap.SetType):
			if m.Cursor < len(m.Jobs) || len(m.Selected) > 0 {
				m.ShowTypePicker = true
				m.TypePickerCursor = 0
			}

		case key.Matches(msg, m.KeyMap.SetTemplate):
			if m.Cursor < len(m.Jobs) || len(m.Selected) > 0 {
				m.ShowTemplatePicker = true
				m.TemplatePickerCursor = 0
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

		case key.Matches(msg, m.KeyMap.AddFromRecipe):
			m.selectingRecipe = true
			// Load recipes and populate the list model
			// For now, we'll do this synchronously as the list is small.
			recipes, err := orchestration.ListAllRecipes("") // No dynamic command in TUI context yet
			if err != nil {
				m.StatusSummary = theme.DefaultTheme.Error.Render("Error loading recipes: " + err.Error())
				m.selectingRecipe = false
				return m, nil
			}
			var items []list.Item
			for _, r := range recipes {
				items = append(items, recipeItem{name: r.Name, description: r.Description})
			}
			m.recipeList = list.New(items, recipeDelegate{}, 40, 10)
			m.recipeList.Title = "Select a Recipe to Add"
			m.recipeList.SetShowHelp(false)
			return m, nil

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

		case key.Matches(msg, m.KeyMap.ToggleClaw):
			if m.Cursor >= 0 && m.Cursor < len(m.Jobs) {
				job := m.Jobs[m.Cursor]
				// Only for interactive_agent jobs
				if job.Type == orchestration.JobTypeInteractiveAgent {
					// Check if already clawed (has channels)
					if len(job.Channels) > 0 {
						// Unclaw: show confirmation
						m.ClawDialogActive = true
						m.ClawDialogJobIndex = m.Cursor
						m.ClawDisabling = true
						return m, nil
					}
					// Claw: show config dialog
					m.ClawDialogActive = true
					m.ClawDialogJobIndex = m.Cursor
					m.ClawDisabling = false

					idleInput := textinput.New()
					idleInput.SetValue("15")
					idleInput.Focus()
					idleInput.CharLimit = 4
					idleInput.Width = 6
					m.ClawIdleInput = idleInput

					promptInput := textinput.New()
					promptInput.Placeholder = "optional idle prompt"
					promptInput.CharLimit = 200
					promptInput.Width = 40
					m.ClawPromptInput = promptInput

					m.ClawDialogFocus = 0
					return m, textinput.Blink
				}
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

		case key.Matches(msg, m.KeyMap.ToggleColumns):
			m.columnSelectMode = true

		}
	}

	return m, nil
}

// reloadActiveDetailPane reloads content for the currently active detail pane
func (m Model) reloadActiveDetailPane() (Model, tea.Cmd) {
	if m.Cursor >= len(m.Jobs) || m.ActiveDetailPane == NoPane {
		return m, nil
	}

	job := m.Jobs[m.Cursor]
	m.ActiveLogJob = job

	// Recalculate layout dimensions since chat input visibility depends on job type
	m.updateLayoutDimensions()

	// Clear the current content immediately to avoid showing stale data
	switch m.ActiveDetailPane {
	case LogsPaneDetail:
		m.LogViewer.SetContent(theme.DefaultTheme.Muted.Render(fmt.Sprintf("Loading logs for %s...", job.Title)))
	case FrontmatterPane:
		m.frontmatterViewport.SetContent(theme.DefaultTheme.Muted.Render(fmt.Sprintf("Loading frontmatter for %s...", job.Title)))
	case BriefingPane:
		m.briefingViewport.SetContent(theme.DefaultTheme.Muted.Render(fmt.Sprintf("Loading briefing for %s...", job.Title)))
	case EditPane:
		m.editViewport.SetContent(theme.DefaultTheme.Muted.Render(fmt.Sprintf("Loading file content for %s...", job.Title)))
	case SkillPane:
		m.skillPaneViewport.SetContent(theme.DefaultTheme.Muted.Render(fmt.Sprintf("Loading skills for %s...", job.Title)))
	}

	// Trigger the actual content loading
	switch m.ActiveDetailPane {
	case LogsPaneDetail:
		isAgentJob := job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeHeadlessAgent || job.Type == orchestration.JobTypeIsolatedAgent
		if isAgentJob {
			// Reset current agent status when switching jobs
			m.CurrentAgentStatus = nil
			// Start status polling for running/idle agent jobs
			isRunningOrIdle := job.Status == orchestration.JobStatusRunning || job.Status == orchestration.JobStatusIdle
			isPollingType := job.Type == orchestration.JobTypeIsolatedAgent || job.Type == orchestration.JobTypeInteractiveAgent
			if isRunningOrIdle && isPollingType {
				return m, tea.Batch(loadAndStreamAgentLogsCmd(m.Plan, job), pollAgentStatusAfterDelay())
			}
			return m, loadAndStreamAgentLogsCmd(m.Plan, job)
		}
		return m, loadLogContentCmd(m.Plan, job)
	case FrontmatterPane:
		return m, loadFrontmatterCmd(job)
	case BriefingPane:
		return m, loadBriefingCmd(m.Plan, job)
	case EditPane:
		return m, loadJobFileContentCmd(job)
	case SkillPane:
		m.skillPaneCursor = 0
		result := renderInteractiveSkillPane(m.Plan, job, m.skillPaneCursor, m.LogViewerWidth, m.LogViewerHeight)
		m.skillPaneNodes = result.nodes
		m.skillPaneStateMap = result.stateMap
		m.skillPaneRawContent = result.treeContent
		m.skillPaneViewport.SetContent(wrapContentForViewport(result.treeContent, m.skillPaneViewport.Width-1))
		m.skillArtifactViewport.SetContent(wrapContentForViewport(result.detailContent, m.skillArtifactViewport.Width-1))
		return m, nil
	}

	return m, nil
}

// openDetailPane opens a specific detail pane and loads its content
func (m Model) openDetailPane(pane DetailPane) (tea.Model, tea.Cmd) {
	m.ActiveDetailPane = pane
	m.ShowLogs = true
	// Auto-focus the detail pane when opening it
	m.Focus = FocusDetailPrimary

	if m.Cursor >= len(m.Jobs) {
		return m, nil
	}

	job := m.Jobs[m.Cursor]
	m.ActiveLogJob = job

	// Centralized layout calculation
	m.updateLayoutDimensions()
	m.LogViewer = logviewer.New(m.LogViewerWidth, m.LogViewerHeight-logHeaderHeight)
	m.LogViewer, _ = m.LogViewer.Update(tea.WindowSizeMsg{Width: m.LogViewerWidth, Height: m.LogViewerHeight - logHeaderHeight})

	// Update viewport sizes for all panes
	m.frontmatterViewport.Width = m.LogViewerWidth
	m.frontmatterViewport.Height = m.LogViewerHeight - logHeaderHeight
	m.briefingViewport.Width = m.LogViewerWidth
	m.briefingViewport.Height = m.LogViewerHeight - logHeaderHeight
	m.editViewport.Width = m.LogViewerWidth
	m.editViewport.Height = m.LogViewerHeight - logHeaderHeight
	m.updateSkillViewportSizes()

	// Trigger content loading for the active pane
	switch pane {
	case LogsPaneDetail:
		isAgentJob := job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeHeadlessAgent || job.Type == orchestration.JobTypeIsolatedAgent
		m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Loading logs for %s...", job.Title))
		if isAgentJob {
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
	case SkillPane:
		m.StatusSummary = theme.DefaultTheme.Info.Render(fmt.Sprintf("Loading skills for %s...", job.Title))
		m.skillPaneCursor = 0
		result := renderInteractiveSkillPane(m.Plan, job, m.skillPaneCursor, m.LogViewerWidth, m.LogViewerHeight)
		m.skillPaneNodes = result.nodes
		m.skillPaneStateMap = result.stateMap
		m.skillPaneRawContent = result.treeContent
		m.skillPaneViewport.SetContent(wrapContentForViewport(result.treeContent, m.skillPaneViewport.Width-1))
		m.skillArtifactViewport.SetContent(wrapContentForViewport(result.detailContent, m.skillArtifactViewport.Width-1))
		return m, nil
	}

	return m, nil
}

// wrapContentForViewport delegates to the shared markdown package.
func wrapContentForViewport(content string, width int) string {
	return markdown.WrapForViewport(content, width)
}

// addScrollbarToViewport overlays a scrollbar on viewport content.
func addScrollbarToViewport(vp *viewport.Model) string {
	return scrollbar.Overlay(vp)
}

// updateSkillViewportSizes sets the dimensions for both skill pane viewports (tree + artifact).
func (m *Model) updateSkillViewportSizes() {
	vpWidth := m.LogViewerWidth
	if vpWidth < 10 {
		vpWidth = 10
	}
	m.skillPaneViewport.Width = vpWidth
	m.skillArtifactViewport.Width = vpWidth
	// 1 separator line + 2 newlines between the two viewports
	totalHeight := m.LogViewerHeight - logHeaderHeight - 3
	if totalHeight < 4 {
		totalHeight = 4
	}
	m.skillPaneViewport.Height = totalHeight / 2
	m.skillArtifactViewport.Height = totalHeight - totalHeight/2
}

// refreshSkillPane re-renders the skill pane with the current cursor position.
func (m *Model) refreshSkillPane() {
	if m.ActiveLogJob == nil {
		return
	}
	result := renderInteractiveSkillPane(m.Plan, m.ActiveLogJob, m.skillPaneCursor, m.LogViewerWidth, m.LogViewerHeight)
	m.skillPaneNodes = result.nodes
	m.skillPaneStateMap = result.stateMap
	// Tree goes into the main skill pane viewport
	m.skillPaneRawContent = result.treeContent
	m.skillPaneViewport.SetContent(wrapContentForViewport(result.treeContent, m.skillPaneViewport.Width-1))
	// Detail goes into the artifact viewport
	m.skillArtifactViewport.SetContent(wrapContentForViewport(result.detailContent, m.skillArtifactViewport.Width-1))
}

// handleDetailPrimaryKey dispatches key messages for the primary detail pane.
// When the SkillPane is active, it routes to tree navigation; otherwise to the active viewport.
func (m Model) handleDetailPrimaryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ActiveDetailPane == SkillPane {
		return m.handleSkillTreeKey(msg)
	}
	return m.handleViewportKey(msg, m.ActiveDetailPane)
}

// handleDetailSecondaryKey dispatches key messages for the secondary detail pane (artifact viewport).
func (m Model) handleDetailSecondaryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ActiveDetailPane == SkillPane {
		return m.handleArtifactViewportKey(msg)
	}
	return m, nil
}

// handleViewportKey handles navigation keys for viewport-based detail panes (Logs, Frontmatter, Briefing, Edit).
func (m Model) handleViewportKey(msg tea.KeyMsg, pane DetailPane) (tea.Model, tea.Cmd) {
	// Handle gg/G sequences for jumping to top/bottom
	result, idx := m.Sequence.Process(msg, m.KeyMap.Top, m.KeyMap.Bottom)
	switch result {
	case keymap.SequenceMatch:
		m.Sequence.Clear()
		switch pane {
		case LogsPaneDetail:
			if idx == 0 {
				m.LogViewer.GotoTop()
			} else {
				m.LogViewer.GotoBottom()
			}
		case FrontmatterPane:
			if idx == 0 {
				m.frontmatterViewport.GotoTop()
			} else {
				m.frontmatterViewport.GotoBottom()
			}
		case BriefingPane:
			if idx == 0 {
				m.briefingViewport.GotoTop()
			} else {
				m.briefingViewport.GotoBottom()
			}
		case EditPane:
			if idx == 0 {
				m.editViewport.GotoTop()
			} else {
				m.editViewport.GotoBottom()
			}
		case SkillPane:
			if idx == 0 {
				m.skillPaneViewport.GotoTop()
			} else {
				m.skillPaneViewport.GotoBottom()
			}
		}
		return m, nil
	case keymap.SequencePending:
		return m, nil
	}

	// No sequence match — clear and delegate to the active viewport
	m.Sequence.Clear()

	var cmd tea.Cmd
	switch pane {
	case LogsPaneDetail:
		m.LogViewer, cmd = m.LogViewer.Update(msg)
	case FrontmatterPane:
		m.frontmatterViewport, cmd = m.frontmatterViewport.Update(msg)
	case BriefingPane:
		m.briefingViewport, cmd = m.briefingViewport.Update(msg)
	case EditPane:
		m.editViewport, cmd = m.editViewport.Update(msg)
	case SkillPane:
		m.skillPaneViewport, cmd = m.skillPaneViewport.Update(msg)
	}
	return m, cmd
}

// handleSkillTreeKey handles navigation keys for the skill pane tree view.
func (m Model) handleSkillTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle gg/G sequences
	result, idx := m.Sequence.Process(msg, m.KeyMap.Top, m.KeyMap.Bottom)
	switch result {
	case keymap.SequenceMatch:
		m.Sequence.Clear()
		if idx == 0 {
			m.skillPaneCursor = 0
		} else {
			m.skillPaneCursor = len(m.skillPaneNodes) - 1
		}
		m.refreshSkillPane()
		return m, nil
	case keymap.SequencePending:
		return m, nil
	}

	m.Sequence.Clear()

	if len(m.skillPaneNodes) == 0 {
		return m, nil
	}

	switch msg.String() {
	case "j", "down":
		if m.skillPaneCursor < len(m.skillPaneNodes)-1 {
			m.skillPaneCursor++
			m.refreshSkillPane()
		}
		return m, nil
	case "k", "up":
		if m.skillPaneCursor > 0 {
			m.skillPaneCursor--
			m.refreshSkillPane()
		}
		return m, nil
	case "ctrl+d":
		// Half-page down
		halfPage := len(m.skillPaneNodes) / 4
		if halfPage < 1 {
			halfPage = 1
		}
		m.skillPaneCursor += halfPage
		if m.skillPaneCursor >= len(m.skillPaneNodes) {
			m.skillPaneCursor = len(m.skillPaneNodes) - 1
		}
		m.refreshSkillPane()
		return m, nil
	case "ctrl+u":
		// Half-page up
		halfPage := len(m.skillPaneNodes) / 4
		if halfPage < 1 {
			halfPage = 1
		}
		m.skillPaneCursor -= halfPage
		if m.skillPaneCursor < 0 {
			m.skillPaneCursor = 0
		}
		m.refreshSkillPane()
		return m, nil
	case "/":
		// Enter search mode
		m.skillSearchActive = true
		m.skillSearchInput.Focus()
		return m, nil
	case "e", "enter":
		// Open editor on the selected skill/artifact node
		if m.ActiveLogJob != nil && m.skillPaneCursor < len(m.skillPaneNodes) {
			node := m.skillPaneNodes[m.skillPaneCursor]
			return m, editSkillOrArtifactCmd(m.Plan, m.ActiveLogJob, node)
		}
		return m, nil
	}

	// Delegate remaining keys to the skill pane viewport for scrolling
	var cmd tea.Cmd
	m.skillPaneViewport, cmd = m.skillPaneViewport.Update(msg)
	return m, cmd
}

// handleArtifactViewportKey handles navigation keys for the artifact detail viewport.
func (m Model) handleArtifactViewportKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle gg/G sequences
	result, idx := m.Sequence.Process(msg, m.KeyMap.Top, m.KeyMap.Bottom)
	switch result {
	case keymap.SequenceMatch:
		m.Sequence.Clear()
		if idx == 0 {
			m.skillArtifactViewport.GotoTop()
		} else {
			m.skillArtifactViewport.GotoBottom()
		}
		return m, nil
	case keymap.SequencePending:
		return m, nil
	}

	m.Sequence.Clear()

	var cmd tea.Cmd
	m.skillArtifactViewport, cmd = m.skillArtifactViewport.Update(msg)
	return m, cmd
}
