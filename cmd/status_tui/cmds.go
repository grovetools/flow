package status_tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/tui/components/logviewer"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

// Message types
type InitProgramMsg struct{}
type RefreshMsg struct{}
type ArchiveConfirmedMsg struct{ Job *orchestration.Job }
type EditFileAndQuitMsg struct{ FilePath string }
type EditFileInTmuxMsg struct{ Err error }
type TickMsg time.Time
type StatusUpdateMsg string
type RefreshTickMsg time.Time
type RenameCompleteMsg struct{ Err error }
type UpdateDepsCompleteMsg struct{ Err error }
type CreateJobCompleteMsg struct{ Err error }
type JobRunFinishedMsg struct{ Err error }

func renameJobCmd(plan *orchestration.Plan, job *orchestration.Job, newTitle string) tea.Cmd {
	return func() tea.Msg {
		err := orchestration.RenameJob(plan, job, newTitle)
		return RenameCompleteMsg{Err: err}
	}
}

func updateDepsCmd(job *orchestration.Job, newDeps []string) tea.Cmd {
	return func() tea.Msg {
		err := orchestration.UpdateJobDependencies(job, newDeps)
		return UpdateDepsCompleteMsg{Err: err}
	}
}

// blink returns a command that sends a tick message every 500ms for cursor blinking
func blink() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// refreshTick returns a command that sends a refresh message periodically
func refreshTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return RefreshTickMsg(t)
	})
}

// runJobsWithOrchestrator executes jobs using the orchestrator and streams output to the TUI.
func runJobsWithOrchestrator(orchestrator *orchestration.Orchestrator, jobs []*orchestration.Job, program *tea.Program, logFormatPretty bool) tea.Cmd {
	return func() tea.Msg {
		logger := logging.NewLogger("flow-tui")
		logger.WithFields(map[string]interface{}{
			"num_jobs": len(jobs),
		}).Info("runJobsWithOrchestrator started")

		// TUI mode is already set in newStatusTUIModel, but ensure it's set here too
		os.Setenv("GROVE_FLOW_TUI_MODE", "true")

		// Create our custom writer that sends messages to the TUI program
		writer := logviewer.NewStreamWriter(program, "Job Output")

		// Save original logger output
		oldGlobalOutput := logging.GetGlobalOutput()

		// Configure logger outputs based on format preference
		logger.WithFields(map[string]interface{}{
			"logFormatPretty": logFormatPretty,
		}).Info("About to configure log outputs for job execution")

		// Both pretty and structured logs use the same global output
		// So we just set the global output to the writer
		logging.SetGlobalOutput(writer)

		if logFormatPretty {
			// Direct write test to verify writer works
			writer.Write([]byte("=== PRETTY MODE ACTIVATED ===\n"))

			// Send a test message to verify pretty logging is active
			testPretty := logging.NewPrettyLogger()
			testPretty.Status("info", "Pretty log mode active - you should see icons and colors")
		} else {
			// Direct write test to verify writer works
			writer.Write([]byte("=== STRUCTURED MODE ACTIVATED ===\n"))

			// Send a test message to verify structured logging is active
			testLogger := logging.NewLogger("flow-tui")
			testLogger.Info("Structured log mode active")
		}

		// Restore logger output when done
		defer func() {
			logging.SetGlobalOutput(oldGlobalOutput)
		}()

		// Redirect stdout and stderr to the TUI writer to prevent output mangling
		oldStdout := os.Stdout
		oldStderr := os.Stderr

		// Create a pipe to capture stdout/stderr
		r, w, err := os.Pipe()
		if err == nil {
			os.Stdout = w
			os.Stderr = w

			// Start a goroutine to read from the pipe and send to TUI
			done := make(chan struct{})
			go func() {
				buf := make([]byte, 1024)
				for {
					n, err := r.Read(buf)
					if n > 0 {
						writer.Write(buf[:n])
					}
					if err != nil {
						break
					}
				}
				close(done)
			}()

			defer func() {
				w.Close()
				<-done
				r.Close()
				os.Stdout = oldStdout
				os.Stderr = oldStderr
			}()
		}

		// Run jobs sequentially (orchestrator handles parallelism if needed)
		ctx := context.Background()

		for i, job := range jobs {
			logger.WithFields(map[string]interface{}{
				"job_id":       job.ID,
				"job_filename": job.Filename,
				"job_index":    i + 1,
				"total_jobs":   len(jobs),
			}).Info("Executing job via orchestrator")

			// Execute the job using the orchestrator, passing the TUI writer
			if err := orchestrator.ExecuteJobWithWriter(ctx, job, writer); err != nil {
				logger.WithFields(map[string]interface{}{
					"job_id": job.ID,
					"error":  err,
				}).Error("Job execution failed")
				// Return error for this job, but continue would be handled by orchestrator
				return JobRunFinishedMsg{Err: fmt.Errorf("job %s failed: %w", job.ID, err)}
			}

			logger.WithFields(map[string]interface{}{
				"job_id": job.ID,
			}).Info("Job execution completed successfully")
		}

		logger.Info("All jobs completed successfully")
		return JobRunFinishedMsg{Err: nil}
	}
}

// runJobsCmd creates a tea.Cmd that executes one or more jobs in the background,
// streaming their output to a log file for the TUI to display.
// DEPRECATED: This is the old implementation that spawns CLI commands.
// Use runJobsWithOrchestrator instead.
func runJobsCmd(logFile string, planDir string, jobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// This command runs in a goroutine managed by the Bubble Tea runtime.

		// Truncate the log file for the new run.
		f, err := os.Create(logFile)
		if err != nil {
			return JobRunFinishedMsg{Err: fmt.Errorf("failed to create log file: %w", err)}
		}
		defer f.Close()

		// Write initial status to log
		fmt.Fprintf(f, "Starting job execution...\n")
		fmt.Fprintf(f, "Plan directory: %s\n", planDir)
		fmt.Fprintf(f, "Running %d job(s):\n", len(jobs))
		for _, job := range jobs {
			fmt.Fprintf(f, "  - %s (%s)\n", job.Title, job.Filename)
		}
		fmt.Fprintf(f, "\n")
		f.Sync() // Ensure it's written immediately

		// Build the command arguments
		// Run from the plan directory and pass just the filenames
		args := []string{"flow", "plan", "run", "--yes"}
		for _, job := range jobs {
			// Use just the filename, no path
			args = append(args, job.Filename)
		}

		// Log the command being run (format for readability)
		fmt.Fprintf(f, "Command (running from: %s):\n", planDir)
		fmt.Fprintf(f, "  grove flow plan run --yes")
		for _, job := range jobs {
			fmt.Fprintf(f, " %s", job.Filename)
		}
		fmt.Fprintf(f, "\n")
		fmt.Fprintf(f, "================================================================================\n\n")
		f.Sync()

		// Use 'grove flow' to ensure proper environment setup for worktrees
		cmd := exec.Command("grove", args...)
		// Set the working directory to the plan directory
		cmd.Dir = planDir
		cmd.Stdout = f
		cmd.Stderr = f
		// Set an environment variable to indicate the job is run from the TUI
		cmd.Env = append(os.Environ(), "GROVE_FLOW_TUI_MODE=true")

		runErr := cmd.Run()

		// Log completion status
		fmt.Fprintf(f, "\n================================================================================\n")
		if runErr != nil {
			fmt.Fprintf(f, "Job execution failed: %v\n", runErr)
		} else {
			fmt.Fprintf(f, "Job execution completed successfully.\n")
		}
		f.Sync()

		// After completion, return a message with the result.
		return JobRunFinishedMsg{Err: runErr}
	}
}

// refreshPlan reloads the plan from disk
func refreshPlan(planDir string) tea.Cmd {
	return func() tea.Msg {
		return RefreshMsg{}
	}
}

func doArchiveJob(planDir string, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Archive the job by moving it to an archive directory
		archiveDir := filepath.Join(planDir, ".archive")
		if err := os.MkdirAll(archiveDir, 0755); err != nil {
			return err
		}

		oldPath := filepath.Join(planDir, job.Filename)
		newPath := filepath.Join(archiveDir, job.Filename)

		if err := os.Rename(oldPath, newPath); err != nil {
			return err
		}

		return nil // Just return nil, we'll refresh after
	}
}

func doArchiveJobs(planDir string, jobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Archive the jobs by moving them to an archive directory
		archiveDir := filepath.Join(planDir, ".archive")
		if err := os.MkdirAll(archiveDir, 0755); err != nil {
			return err
		}

		for _, job := range jobs {
			oldPath := filepath.Join(planDir, job.Filename)
			newPath := filepath.Join(archiveDir, job.Filename)

			if err := os.Rename(oldPath, newPath); err != nil {
				return err
			}
		}

		return nil // Just return nil, we'll refresh after
	}
}

func editJob(job *orchestration.Job) tea.Cmd {
	// If running inside Neovim plugin, signal to quit and let plugin handle editing
	if os.Getenv("GROVE_NVIM_PLUGIN") == "true" {
		return func() tea.Msg {
			return EditFileAndQuitMsg{FilePath: job.FilePath}
		}
	}

	// Check for tmux popup
	if os.Getenv("TMUX") != "" {
		client, err := tmux.NewClient()
		if err == nil { // Silently ignore if tmux client fails
			isPopup, _ := client.IsPopup(context.Background())
			if isPopup {
				return func() tea.Msg {
					editor := os.Getenv("EDITOR")
					if editor == "" {
						editor = "vi" // Fallback editor
					}
					ctx := context.Background()
					err := client.OpenInEditorWindow(ctx, editor, job.FilePath, "plan", 3, false)
					if err != nil {
						return EditFileInTmuxMsg{Err: err}
					}
					// Close the popup explicitly before quitting
					if err := client.ClosePopup(ctx); err != nil {
						// Log error but continue - the file was opened successfully
						return EditFileInTmuxMsg{Err: fmt.Errorf("failed to close popup: %w", err)}
					}
					return EditFileInTmuxMsg{Err: nil}
				}
			}
		}
	}

	// Original behavior: open editor in the current pane
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	// Use tea.ExecProcess to properly handle terminal control
	return tea.ExecProcess(exec.Command(editor, job.FilePath), func(err error) tea.Msg {
		if err != nil {
			return err
		}
		return RefreshMsg{} // Refresh to show any changes
	})
}

func executePlanResume(job *orchestration.Job) tea.Cmd {
	return tea.ExecProcess(exec.Command("flow", "plan", "resume", job.FilePath),
		func(err error) tea.Msg {
			if err != nil {
				return err // Propagate error to be displayed in the TUI
			}
			return RefreshMsg{} // Refresh TUI on success
		})
}

func viewLogsCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Check if we're in tmux
		if os.Getenv("TMUX") == "" {
			// Not in tmux, fall back to less
			jobSpec := fmt.Sprintf("%s/%s", plan.Name, job.Filename)
			pagerCmd := exec.Command("sh", "-c", fmt.Sprintf("grove aglogs read %s | less -R", jobSpec))

			if err := pagerCmd.Run(); err != nil {
				return StatusUpdateMsg(fmt.Sprintf("Error viewing logs: %v", err))
			}
			return StatusUpdateMsg("Returned from log viewer.")
		}

		// In tmux, open logs in a new window
		ctx := context.Background()
		client, err := tmux.NewClient()
		if err != nil {
			return StatusUpdateMsg(fmt.Sprintf("Error creating tmux client: %v", err))
		}

		session, err := client.GetCurrentSession(ctx)
		if err != nil {
			return StatusUpdateMsg(fmt.Sprintf("Error getting current session: %v", err))
		}

		// Create window name based on job
		windowName := fmt.Sprintf("logs-%s", job.Filename)
		jobSpec := fmt.Sprintf("%s/%s", plan.Name, job.Filename)

		// Create the command to run in the new window
		// Force color output by setting CLICOLOR_FORCE, then pipe to less -R
		// CLICOLOR_FORCE=1 tells programs to always output color, even when piped
		command := fmt.Sprintf("CLICOLOR_FORCE=1 grove aglogs read %s | less -R", jobSpec)

		// Create new window
		err = client.NewWindow(ctx, session, windowName, command)
		if err != nil {
			return StatusUpdateMsg(fmt.Sprintf("Error creating logs window: %v", err))
		}

		// Switch to the new window
		windowTarget := session + ":" + windowName
		if err := client.SwitchClient(ctx, windowTarget); err != nil {
			// Not critical if switch fails, window was still created
			return StatusUpdateMsg("Logs window created.")
		}

		return StatusUpdateMsg("Opened logs in new window.")
	}
}

func setJobCompleted(job *orchestration.Job, plan *orchestration.Plan, completeJobFunc func(*orchestration.Job, *orchestration.Plan, bool) error) tea.Cmd {
	return func() tea.Msg {
		// Use the shared completion function (silent mode for TUI)
		if err := completeJobFunc(job, plan, true); err != nil {
			return err
		}
		return RefreshMsg{} // Refresh to show the status change
	}
}

func setJobStatus(job *orchestration.Job, plan *orchestration.Plan, status orchestration.JobStatus) tea.Cmd {
	return func() tea.Msg {
		sp := orchestration.NewStatePersister()
		if err := sp.UpdateJobStatus(job, status); err != nil {
			return err
		}
		return RefreshMsg{} // Refresh to show the status change
	}
}

func addJobWithDependencies(planDir string, dependencies []string) tea.Cmd {
	// Build the command
	args := []string{"plan", "add", planDir, "-i"}

	// Add dependencies if provided
	for _, dep := range dependencies {
		args = append(args, "-d", dep)
	}

	// Run flow directly - delegation through grove breaks interactive TUI
	return tea.ExecProcess(exec.Command("flow", args...), func(err error) tea.Msg {
		if err != nil {
			return err
		}
		return RefreshMsg{} // Refresh to show the new job
	})
}

// createXmlPlanJob creates a new oneshot job with the "agent-xml" template
// that depends on the selected job.
func createXmlPlanJob(plan *orchestration.Plan, selectedJob *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Create the xml plan job title
		xmlTitle := fmt.Sprintf("xml-plan-%s", selectedJob.Title)

		// Generate a unique ID for the new job
		xmlID := orchestration.GenerateUniqueJobID(plan, xmlTitle)

		// Create the new job
		newJob := &orchestration.Job{
			ID:                  xmlID,
			Title:               xmlTitle,
			Type:                orchestration.JobTypeOneshot,
			Status:              orchestration.JobStatusPending,
			DependsOn:           []string{selectedJob.ID},
			Worktree:            selectedJob.Worktree,
			Template:            "agent-xml",
			PromptBody:          "generate a detailed plan",
			PrependDependencies: true,
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return fmt.Errorf("failed to create xml plan job: %w", err)
		}

		// Return refresh message to update the TUI
		return RefreshMsg{}
	}
}

// createXmlPlanJobWithDeps creates a new oneshot job with the "agent-xml" template
// that depends on multiple selected jobs.
func createXmlPlanJobWithDeps(plan *orchestration.Plan, selectedJobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Create the xml plan job title - use first job's title
		xmlTitle := fmt.Sprintf("xml-plan-%s", selectedJobs[0].Title)

		// Generate a unique ID for the new job
		xmlID := orchestration.GenerateUniqueJobID(plan, xmlTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:                  xmlID,
			Title:               xmlTitle,
			Type:                orchestration.JobTypeOneshot,
			Status:              orchestration.JobStatusPending,
			DependsOn:           depIDs,
			Worktree:            worktree,
			Template:            "agent-xml",
			PromptBody:          "generate a detailed plan",
			PrependDependencies: true,
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return fmt.Errorf("failed to create xml plan job: %w", err)
		}

		// Return refresh message to update the TUI
		return RefreshMsg{}
	}
}

func createImplementationJob(plan *orchestration.Plan, selectedJob *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Create the implementation job title
		implTitle := fmt.Sprintf("impl-%s", selectedJob.Title)

		// Generate a unique ID for the new job
		implID := orchestration.GenerateUniqueJobID(plan, implTitle)

		// Create the new job
		newJob := &orchestration.Job{
			ID:        implID,
			Title:     implTitle,
			Type:      orchestration.JobTypeInteractiveAgent,
			Status:    orchestration.JobStatusPending,
			DependsOn: []string{selectedJob.ID},
			Worktree:  selectedJob.Worktree,
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return fmt.Errorf("failed to create implementation job: %w", err)
		}

		// Return refresh message to update the TUI
		return RefreshMsg{}
	}
}

// createImplementationJobWithDeps creates a new interactive_agent job with "impl-" prefix
// that depends on multiple selected jobs.
func createImplementationJobWithDeps(plan *orchestration.Plan, selectedJobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Create the implementation job title - use first job's title
		implTitle := fmt.Sprintf("impl-%s", selectedJobs[0].Title)

		// Generate a unique ID for the new job
		implID := orchestration.GenerateUniqueJobID(plan, implTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:        implID,
			Title:     implTitle,
			Type:      orchestration.JobTypeInteractiveAgent,
			Status:    orchestration.JobStatusPending,
			DependsOn: depIDs,
			Worktree:  worktree,
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return fmt.Errorf("failed to create implementation job: %w", err)
		}

		// Return refresh message to update the TUI
		return RefreshMsg{}
	}
}

// createXmlPlanJobWithTitle creates an XML plan job with a custom title
func createXmlPlanJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		xmlID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:                  xmlID,
			Title:               customTitle,
			Type:                orchestration.JobTypeOneshot,
			Status:              orchestration.JobStatusPending,
			DependsOn:           depIDs,
			Worktree:            worktree,
			Template:            "agent-xml",
			PromptBody:          "generate a detailed plan",
			PrependDependencies: true,
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return CreateJobCompleteMsg{Err: err}
		}

		return CreateJobCompleteMsg{Err: nil}
	}
}

// createImplementationJobWithTitle creates an implementation job with a custom title
func createImplementationJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		implID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:        implID,
			Title:     customTitle,
			Type:      orchestration.JobTypeInteractiveAgent,
			Status:    orchestration.JobStatusPending,
			DependsOn: depIDs,
			Worktree:  worktree,
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return CreateJobCompleteMsg{Err: err}
		}

		return CreateJobCompleteMsg{Err: nil}
	}
}

func createAgentFromChatJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		jobID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job using the agent-from-chat template
		newJob := &orchestration.Job{
			ID:               jobID,
			Title:            customTitle,
			Type:             orchestration.JobTypeInteractiveAgent,
			Status:           orchestration.JobStatusPending,
			DependsOn:        depIDs,
			Worktree:         worktree,
			Template:         "agent-from-chat",
			GeneratePlanFrom: true,
			PromptBody:       "Implement the detailed plan that will be generated from the dependency.",
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return CreateJobCompleteMsg{Err: err}
		}

		return CreateJobCompleteMsg{Err: nil}
	}
}
