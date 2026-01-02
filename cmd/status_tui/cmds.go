package status_tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/sessions"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/tui/components/logviewer"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"gopkg.in/yaml.v3"
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
type recipeAddedMsg struct{ err error }
type JobRunFinishedMsg struct {
	Jobs []*orchestration.Job // The jobs that were executed
	Err  error
}
type RetryLoadAgentLogsMsg struct{}

type FrontmatterContentLoadedMsg struct {
	Content string
	Err     error
}

type BriefingContentLoadedMsg struct {
	Content string
	Err     error
}

type EditContentLoadedMsg struct {
	Content string
	Err     error
}

// retryLoadAgentLogsAfterDelay creates a command that waits and then triggers a retry
func retryLoadAgentLogsAfterDelay() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return RetryLoadAgentLogsMsg{}
	})
}

// LogContentLoadedMsg is sent when historical log content has been loaded from a file.
type LogContentLoadedMsg struct {
	Content        string
	Err            error
	ShouldRetry    bool   // If true, we should retry loading after a delay
	StartStreaming bool   // If true, we should start streaming (agent session is ready)
	LogFilePath    string // The path to the log file to stream
	JobID          string // The ID of the job this message belongs to
}

// loadLogContentCmd creates a command to asynchronously read a job's log file.
func loadLogContentCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		logPath, err := orchestration.GetJobLogPath(plan, job)
		if err != nil {
			return LogContentLoadedMsg{Err: err, JobID: job.ID}
		}

		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			// It's not an error if the log file doesn't exist yet.
			return LogContentLoadedMsg{Content: fmt.Sprintf("No logs found for %s.", job.Title), JobID: job.ID}
		}

		content, err := os.ReadFile(logPath)
		if err != nil {
			return LogContentLoadedMsg{Err: err, JobID: job.ID}
		}

		return LogContentLoadedMsg{Content: string(content), JobID: job.ID}
	}
}

// loadFrontmatterCmd creates a command to load and format a job's frontmatter.
func loadFrontmatterCmd(job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		content, err := os.ReadFile(job.FilePath)
		if err != nil {
			return FrontmatterContentLoadedMsg{Err: err}
		}

		fm, _, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			return FrontmatterContentLoadedMsg{Err: err}
		}

		// Marshal to YAML for pretty printing
		yamlBytes, err := yaml.Marshal(fm)
		if err != nil {
			return FrontmatterContentLoadedMsg{Err: err}
		}

		return FrontmatterContentLoadedMsg{Content: string(yamlBytes)}
	}
}

// loadBriefingCmd finds and loads the most recent briefing file for a job.
func loadBriefingCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		jobArtifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
		pattern := "briefing-*.xml"
		files, err := filepath.Glob(filepath.Join(jobArtifactDir, pattern))
		if err != nil {
			return BriefingContentLoadedMsg{Err: err}
		}

		if len(files) == 0 {
			return BriefingContentLoadedMsg{Content: "No briefing file found for this job."}
		}

		// Find the most recent file
		var mostRecentFile string
		var latestModTime time.Time
		for _, file := range files {
			info, err := os.Stat(file)
			if err == nil {
				if info.ModTime().After(latestModTime) {
					latestModTime = info.ModTime()
					mostRecentFile = file
				}
			}
		}

		if mostRecentFile == "" {
			return BriefingContentLoadedMsg{Content: "Could not determine the most recent briefing file."}
		}

		content, err := os.ReadFile(mostRecentFile)
		if err != nil {
			return BriefingContentLoadedMsg{Err: err}
		}

		return BriefingContentLoadedMsg{Content: string(content)}
	}
}

// loadJobFileContentCmd creates a command to load the raw content of a job file.
func loadJobFileContentCmd(job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		content, err := os.ReadFile(job.FilePath)
		if err != nil {
			return EditContentLoadedMsg{Err: err}
		}
		return EditContentLoadedMsg{Content: string(content)}
	}
}

// loadAndStreamAgentLogsCmd first loads existing agent logs, then triggers streaming.
// This function implements fast-path optimization:
// - For completed jobs: read directly from job.log if available
// - For running jobs: try to get direct transcript path from session registry
func loadAndStreamAgentLogsCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	logger := logging.NewLogger("flow-tui")
	logger.WithFields(map[string]interface{}{
		"job_id": job.ID,
		"plan":   plan.Name,
	}).Info("loadAndStreamAgentLogsCmd called, creating command")

	return func() tea.Msg {
		logger := logging.NewLogger("flow-tui")
		jobSpec := fmt.Sprintf("%s/%s", plan.Name, job.Filename)

		logger.WithFields(map[string]interface{}{
			"job_spec":   jobSpec,
			"job_id":     job.ID,
			"job_status": job.Status,
			"job_type":   job.Type,
		}).Info("loadAndStreamAgentLogsCmd executing")

		// Fast path: read from job.log which contains ANSI-formatted output (for both completed and running jobs)
		jobLogPath, err := orchestration.GetJobLogPath(plan, job)
		logger.WithFields(map[string]interface{}{
			"job_id":       job.ID,
			"job_log_path": jobLogPath,
			"get_path_err": err,
		}).Info("GetJobLogPath result")

		if err == nil {
			statInfo, statErr := os.Stat(jobLogPath)
			logger.WithFields(map[string]interface{}{
				"job_id":   job.ID,
				"log_path": jobLogPath,
				"stat_err": statErr,
				"exists":   statErr == nil,
				"file_size": func() int64 {
					if statInfo != nil {
						return statInfo.Size()
					}
					return 0
				}(),
			}).Info("Fast path: checking job.log file")

			if statErr == nil {
				// Read the job.log file directly - it contains ANSI-formatted aglogs output
				content, readErr := os.ReadFile(jobLogPath)
				logger.WithFields(map[string]interface{}{
					"job_id":      job.ID,
					"read_err":    readErr,
					"content_len": len(content),
				}).Info("Fast path: read job.log result")

				if readErr == nil && len(content) > 0 {
					logger.WithFields(map[string]interface{}{
						"log_path":    jobLogPath,
						"is_running":  job.Status == orchestration.JobStatusRunning,
						"content_len": len(content),
					}).Info("Fast path: successfully read job logs from job.log")

					contentStr := string(content)
					shouldStream := job.Status == orchestration.JobStatusRunning

					if shouldStream {
						// Add a separator before the live stream
						separator := theme.DefaultTheme.Success.Render(strings.Repeat("─", 80))
						streamLabel := theme.DefaultTheme.Success.Render(fmt.Sprintf("  %s new  ", theme.IconSparkle))
						contentStr = contentStr + "\n\n" + separator + "\n" + streamLabel + "\n" + separator + "\n\n"

						// Get the direct transcript path for streaming
						var logSpec string = jobSpec
						if registry, regErr := sessions.NewFileSystemRegistry(); regErr == nil {
							if metadata, findErr := registry.Find(job.ID); findErr == nil && metadata.TranscriptPath != "" {
								logger.WithFields(map[string]interface{}{
									"job_id":          job.ID,
									"transcript_path": metadata.TranscriptPath,
								}).Debug("Found direct transcript path from session registry for streaming")
								logSpec = metadata.TranscriptPath
							}
						}

						return LogContentLoadedMsg{
							Content:        contentStr,
							ShouldRetry:    false,
							StartStreaming: true,
							LogFilePath:    logSpec,
							JobID:          job.ID,
						}
					}

					// Completed job - just show the content
					return LogContentLoadedMsg{
						Content:        contentStr,
						ShouldRetry:    false,
						StartStreaming: false,
						JobID:          job.ID,
					}
				} else {
					logger.WithFields(map[string]interface{}{
						"job_id":      job.ID,
						"read_err":    readErr,
						"content_len": len(content),
					}).Info("Fast path: job.log read failed or empty")
				}
			} else {
				logger.WithFields(map[string]interface{}{
					"job_id":   job.ID,
					"log_path": jobLogPath,
					"stat_err": statErr,
				}).Info("Fast path: job.log stat failed")
			}
		} else {
			logger.WithFields(map[string]interface{}{
				"job_id": job.ID,
				"error":  err,
			}).Info("Fast path: GetJobLogPath failed")
		}

		// Fallback: job.log doesn't exist yet (running job just started)
		logger.WithFields(map[string]interface{}{
			"job_id":     job.ID,
			"job_status": job.Status,
		}).Info("Fast path failed, checking fallback for running job")

		// For agent jobs, if status is pending but we're in the TUI (indicated by being called
		// from runJobsWithOrchestrator), the job is about to start. Treat it like a running job.
		isPending := job.Status == orchestration.JobStatusPending
		isRunning := job.Status == orchestration.JobStatusRunning
		isAgentJob := job.Type == orchestration.JobTypeHeadlessAgent || job.Type == orchestration.JobTypeInteractiveAgent

		logger.WithFields(map[string]interface{}{
			"job_id":       job.ID,
			"is_pending":   isPending,
			"is_running":   isRunning,
			"is_agent_job": isAgentJob,
			"will_stream":  isRunning || (isPending && isAgentJob),
		}).Info("Checking if job should use streaming fallback")

		if isRunning || (isPending && isAgentJob) {
			// Try to get session ID from session registry for aglogs
			var logSpec string = jobSpec
			if registry, regErr := sessions.NewFileSystemRegistry(); regErr == nil {
				if metadata, findErr := registry.Find(job.ID); findErr == nil {
					// Use ClaudeSessionID for aglogs - this is the native session ID
					// (e.g., ses_xxx for opencode, UUID for claude)
					if metadata.ClaudeSessionID != "" {
						logger.WithFields(map[string]interface{}{
							"job_id":            job.ID,
							"claude_session_id": metadata.ClaudeSessionID,
							"provider":          metadata.Provider,
						}).Info("Fast path: found session ID from registry")
						logSpec = metadata.ClaudeSessionID
					} else if metadata.TranscriptPath != "" {
						// Fallback to transcript path for legacy sessions
						logger.WithFields(map[string]interface{}{
							"job_id":          job.ID,
							"transcript_path": metadata.TranscriptPath,
						}).Info("Fast path: using transcript path (no session ID)")
						logSpec = metadata.TranscriptPath
					}
				} else {
					logger.WithFields(map[string]interface{}{
						"job_id":     job.ID,
						"find_error": findErr,
					}).Info("Session not found in registry yet")
				}
			}

			// Try to read historical logs using aglogs read
			readCmd := exec.Command("grove", "aglogs", "read", logSpec)
			readCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
			readOutput, readErr := readCmd.Output()

			logger.WithFields(map[string]interface{}{
				"log_spec":   logSpec,
				"output_len": len(readOutput),
				"error":      readErr,
			}).Info("aglogs read completed for running job")

			if readErr != nil {
				// Session is still initializing, retry
				return LogContentLoadedMsg{
					Content:     "⏳ Waiting for agent session to start...\n(This may take a few seconds)\n",
					ShouldRetry: true,
					JobID:       job.ID,
				}
			}

			content := string(readOutput)
			if content != "" {
				separator := theme.DefaultTheme.Success.Render(strings.Repeat("─", 80))
				streamLabel := theme.DefaultTheme.Success.Render(fmt.Sprintf("  %s new  ", theme.IconSparkle))
				content = content + "\n\n" + separator + "\n" + streamLabel + "\n" + separator + "\n\n"
			} else {
				content = "⏳ Agent session found, waiting for logs...\n"
			}

			return LogContentLoadedMsg{
				Content:        content,
				ShouldRetry:    false,
				StartStreaming: true,
				LogFilePath:    logSpec,
				JobID:          job.ID,
			}
		}

		// Fallback for completed jobs: try aglogs read
		// This handles the case where the job completed but job.log doesn't have formatted transcript yet
		logger.WithFields(map[string]interface{}{
			"job_id":     job.ID,
			"job_spec":   jobSpec,
			"job_status": job.Status,
		}).Info("Trying aglogs read fallback for completed job")

		readCmd := exec.Command("grove", "aglogs", "read", jobSpec)
		readCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
		readOutput, readErr := readCmd.Output()

		logger.WithFields(map[string]interface{}{
			"job_id":     job.ID,
			"job_spec":   jobSpec,
			"output_len": len(readOutput),
			"error":      readErr,
		}).Info("aglogs read fallback result")

		if readErr == nil && len(readOutput) > 0 {
			logger.WithFields(map[string]interface{}{
				"job_spec":   jobSpec,
				"output_len": len(readOutput),
			}).Info("Loaded completed job logs via aglogs read fallback")

			return LogContentLoadedMsg{
				Content:     string(readOutput),
				ShouldRetry: false,
				JobID:       job.ID,
			}
		}

		// Completed job with no logs
		logger.WithFields(map[string]interface{}{
			"job_id":    job.ID,
			"job_spec":  jobSpec,
			"job_title": job.Title,
		}).Warn("No agent logs found for completed job")

		return LogContentLoadedMsg{
			Content:     theme.DefaultTheme.Muted.Render(fmt.Sprintf("No agent logs found for completed job '%s'.", job.Title)),
			ShouldRetry: false,
			JobID:       job.ID,
		}
	}
}

// streamAgentLogsCmd creates a background process to stream agent logs from a specific file.
func streamAgentLogsCmd(plan *orchestration.Plan, job *orchestration.Job, logFilePath string, program *tea.Program) tea.Cmd {
	return func() tea.Msg {
		logger := logging.NewLogger("flow-tui")

		// Get the log file path for this job
		jobLogPath, err := orchestration.GetJobLogPath(plan, job)
		if err != nil {
			return LogContentLoadedMsg{Err: fmt.Errorf("failed to get log path: %w", err)}
		}

		// Read the current file content to check if agent logs are already there
		currentContent, err := os.ReadFile(jobLogPath)
		alreadyHasAgentLogs := false
		if err == nil {
			// Check if the file already contains the agent session header
			alreadyHasAgentLogs = strings.Contains(string(currentContent), "=== Job:")
		}

		// Open the log file in append mode
		logFile, err := os.OpenFile(jobLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return LogContentLoadedMsg{Err: fmt.Errorf("failed to open log file: %w", err)}
		}

		// If we don't have agent logs yet, write the historical logs from aglogs read
		if !alreadyHasAgentLogs {
			// Read existing agent logs using 'aglogs read' to get historical content
			readCmd := exec.Command("grove", "aglogs", "read", logFilePath)
			readCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
			existingLogs, err := readCmd.Output()

			if err == nil && len(existingLogs) > 0 {
				logger.WithFields(map[string]interface{}{
					"log_path":    jobLogPath,
					"logs_length": len(existingLogs),
				}).Info("Writing existing agent logs to file before streaming")

				if _, err := logFile.Write(existingLogs); err != nil {
					logFile.Close()
					return LogContentLoadedMsg{Err: fmt.Errorf("failed to write existing logs: %w", err)}
				}
				logFile.Sync() // Ensure existing logs are written
			}
		} else {
			logger.WithFields(map[string]interface{}{
				"log_path": jobLogPath,
			}).Debug("Agent logs already in file, skipping duplicate write")
		}

		// Start streaming new content using the direct file path
		streamCmd := exec.Command("grove", "aglogs", "stream", logFilePath)
		streamCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")

		stdout, err := streamCmd.StdoutPipe()
		if err != nil {
			logFile.Close()
			return LogContentLoadedMsg{Err: fmt.Errorf("failed to create stdout pipe: %w", err)}
		}

		if err := streamCmd.Start(); err != nil {
			logFile.Close()
			return LogContentLoadedMsg{Err: fmt.Errorf("failed to start aglogs stream: %w", err)}
		}

		// Stream output line by line back to the TUI and write to log file
		go func() {
			logger := logging.NewLogger("flow-tui")
			defer logFile.Close()
			defer stdout.Close()

			// Recover from any panics in the streaming goroutine
			// This can happen if program.Send() is called after the TUI is shutting down
			defer func() {
				if r := recover(); r != nil {
					logger.WithFields(map[string]interface{}{
						"panic": r,
					}).Warn("Recovered from panic in agent log streaming goroutine")
				}
			}()

			scanner := bufio.NewScanner(stdout)
			lineCount := 0
			for scanner.Scan() {
				line := scanner.Text()
				lineCount++

				// Strip the [job-id] prefix from streamed lines for cleaner display and storage
				displayLine := line
				if strings.HasPrefix(line, "[") {
					if endIdx := strings.Index(line, "] "); endIdx > 0 {
						displayLine = line[endIdx+2:]
					}
				}

				// Write to log file (without job-id prefix)
				fmt.Fprintln(logFile, displayLine)
				logFile.Sync() // Ensure it's written immediately

				// Send to TUI as a LogLineMsg with job.ID to tag the source
				// NoPrefix=true prevents the logviewer from adding [job-id] prefix
				// Wrapped in a func to allow recover() to catch any panics from program.Send()
				func() {
					defer func() {
						if r := recover(); r != nil {
							logger.WithFields(map[string]interface{}{
								"panic":      r,
								"line_count": lineCount,
							}).Warn("Recovered from panic when sending log line to TUI")
						}
					}()
					program.Send(logviewer.LogLineMsg{
						Workspace: job.ID,
						Line:      displayLine,
						NoPrefix:  true,
					})
				}()
			}
			if err := scanner.Err(); err != nil {
				logger.WithFields(map[string]interface{}{
					"error":      err,
					"line_count": lineCount,
				}).Error("Scanner error in agent log stream")
			} else {
				logger.WithFields(map[string]interface{}{
					"line_count": lineCount,
				}).Info("Agent log stream ended normally")
			}
			if err := streamCmd.Wait(); err != nil {
				logger.WithFields(map[string]interface{}{
					"error": err,
				}).Error("aglogs stream command exited with error")
			} else {
				logger.Info("aglogs stream command exited successfully")
			}
		}()

		// This command manages its own lifecycle in the background.
		return nil
	}
}

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
// IMPORTANT: This function spawns a background goroutine and returns immediately to avoid
// blocking the bubbletea event loop. The goroutine sends JobRunFinishedMsg when done.
func runJobsWithOrchestrator(orchestrator *orchestration.Orchestrator, jobs []*orchestration.Job, program *tea.Program) tea.Cmd {
	return func() tea.Msg {
		logger := logging.NewLogger("flow-tui")
		logger.WithFields(map[string]interface{}{
			"num_jobs": len(jobs),
		}).Info("runJobsWithOrchestrator started - spawning background goroutine")

		// TUI mode is already set in newStatusTUIModel, but ensure it's set here too
		os.Setenv("GROVE_FLOW_TUI_MODE", "true")

		// Spawn background goroutine to run jobs - DO NOT block the tea.Cmd
		// This is critical to avoid deadlocks with bubbletea's event loop
		go func() {
			// Recover from any panics in the background goroutine
			defer func() {
				if r := recover(); r != nil {
					logger.WithFields(map[string]interface{}{
						"panic": r,
					}).Error("Panic recovered in runJobsWithOrchestrator background goroutine")
					program.Send(JobRunFinishedMsg{Jobs: jobs, Err: fmt.Errorf("panic during job execution: %v", r)})
				}
			}()

			ctx := context.Background()

			// Create a StreamWriter for live TUI updates with smart tagging.
			var writerTag string
			if len(jobs) == 1 {
				writerTag = jobs[0].ID
			} else {
				writerTag = "Job Output"
			}
			tuiWriter := logviewer.NewStreamWriter(program, writerTag)

			if len(jobs) == 1 {
				tuiWriter.NoWorkspacePrefix = true
			}

			// Run jobs concurrently
			var wg sync.WaitGroup
			errChan := make(chan error, len(jobs))

			for _, job := range jobs {
				wg.Add(1)
				go func(j *orchestration.Job) {
					defer wg.Done()
					// Recover from panics in individual job goroutines
					defer func() {
						if r := recover(); r != nil {
							logger.WithFields(map[string]interface{}{
								"job_id": j.ID,
								"panic":  r,
							}).Error("Panic recovered in job execution goroutine")
							errChan <- fmt.Errorf("job %s panicked: %v", j.ID, r)
						}
					}()

					logger.WithFields(map[string]interface{}{
						"job_id":       j.ID,
						"job_filename": j.Filename,
					}).Info("Executing job via orchestrator")

					// Get the log file path for this specific job
					logFilePath, err := orchestration.GetJobLogPath(orchestrator.Plan, j)
					if err != nil {
						errChan <- fmt.Errorf("failed to get log path for job %s: %w", j.ID, err)
						return
					}

					// Open the job's log file for appending
					logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
					if err != nil {
						errChan <- fmt.Errorf("failed to open log file %s: %w", logFilePath, err)
						return
					}
					defer logFile.Close()

					// Create a MultiWriter for this job - output goes to both log file and TUI
					// DO NOT redirect os.Stdout - that breaks bubbletea!
					multiWriter := io.MultiWriter(logFile, tuiWriter)

					// Execute the job
					if execErr := orchestrator.ExecuteJobWithWriter(ctx, j, multiWriter); execErr != nil {
						errChan <- fmt.Errorf("job %s failed: %w", j.ID, execErr)
					}
				}(job)
			}

			// Wait for all jobs to finish
			wg.Wait()

			close(errChan)
			var execErrors []error
			for err := range errChan {
				execErrors = append(execErrors, err)
			}

			// Send completion message to TUI
			if len(execErrors) > 0 {
				var errStrings []string
				for _, e := range execErrors {
					errStrings = append(errStrings, e.Error())
				}
				program.Send(JobRunFinishedMsg{Jobs: jobs, Err: fmt.Errorf(strings.Join(errStrings, "\n"))})
			} else {
				logger.Info("All jobs completed successfully")
				program.Send(JobRunFinishedMsg{Jobs: jobs, Err: nil})
			}
		}()

		// Return nil immediately - the goroutine will send JobRunFinishedMsg when done
		return nil
	}
}

// runJobsCmd creates a tea.Cmd that executes one or more jobs in the background,
// streaming their output to a log file for the TUI to display.
// DEPRECATED: This is the old implementation that spawns CLI commands.
// Use runJobsWithOrchestrator instead.
func runJobsCmd(logFile string, planDir string, jobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// This command runs in a goroutine managed by the Bubble Tea runtime.

		// Use the log file if provided, otherwise use a discard writer
		var f io.Writer
		var closer func()
		var sync func()
		if logFile != "" {
			file, err := os.Create(logFile)
			if err != nil {
				return JobRunFinishedMsg{Err: fmt.Errorf("failed to create log file: %w", err)}
			}
			f = file
			closer = func() { file.Close() }
			sync = func() { file.Sync() }
		} else {
			// No log file - use a no-op writer
			f = io.Discard
			closer = func() {}
			sync = func() {}
		}
		defer closer()

		// Write initial status to log
		fmt.Fprintf(f, "Starting job execution...\n")
		fmt.Fprintf(f, "Plan directory: %s\n", planDir)
		fmt.Fprintf(f, "Running %d job(s):\n", len(jobs))
		for _, job := range jobs {
			fmt.Fprintf(f, "  - %s (%s)\n", job.Title, job.Filename)
		}
		fmt.Fprintf(f, "\n")
		sync() // Ensure it's written immediately

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
		sync()

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
		sync()

		// After completion, return a message with the result.
		return JobRunFinishedMsg{Jobs: jobs, Err: runErr}
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
	return tea.ExecProcess(exec.Command("grove", "flow", "plan", "resume", job.FilePath),
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

// JobCompletedMsg is sent when a job completion attempt finishes
type JobCompletedMsg struct {
	Err error
}

func setJobCompleted(job *orchestration.Job, plan *orchestration.Plan, completeJobFunc func(*orchestration.Job, *orchestration.Plan, bool) error) tea.Cmd {
	return func() tea.Msg {
		// Use the shared completion function (silent mode for TUI)
		// Wrap in defer/recover to catch any panics from exec.Command calls
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic during job completion: %v", r)
				}
			}()
			err = completeJobFunc(job, plan, true)
		}()

		return JobCompletedMsg{Err: err}
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

func setMultipleJobStatus(jobs []*orchestration.Job, plan *orchestration.Plan, status orchestration.JobStatus) tea.Cmd {
	return func() tea.Msg {
		sp := orchestration.NewStatePersister()
		for _, job := range jobs {
			if err := sp.UpdateJobStatus(job, status); err != nil {
				return err
			}
		}
		return RefreshMsg{} // Refresh to show the status change
	}
}

func setJobType(job *orchestration.Job, plan *orchestration.Plan, jobType orchestration.JobType) tea.Cmd {
	return func() tea.Msg {
		sp := orchestration.NewStatePersister()
		if err := sp.UpdateJobType(job, jobType); err != nil {
			return err
		}
		return RefreshMsg{} // Refresh to show the type change
	}
}

func setMultipleJobType(jobs []*orchestration.Job, plan *orchestration.Plan, jobType orchestration.JobType) tea.Cmd {
	return func() tea.Msg {
		sp := orchestration.NewStatePersister()
		for _, job := range jobs {
			if err := sp.UpdateJobType(job, jobType); err != nil {
				return err
			}
		}
		return RefreshMsg{} // Refresh to show the type change
	}
}

func setJobTemplate(job *orchestration.Job, plan *orchestration.Plan, template string) tea.Cmd {
	return func() tea.Msg {
		sp := orchestration.NewStatePersister()
		if err := sp.UpdateJobTemplate(job, template); err != nil {
			return err
		}
		return RefreshMsg{} // Refresh to show the template change
	}
}

func setMultipleJobTemplate(jobs []*orchestration.Job, plan *orchestration.Plan, template string) tea.Cmd {
	return func() tea.Msg {
		sp := orchestration.NewStatePersister()
		for _, job := range jobs {
			if err := sp.UpdateJobTemplate(job, template); err != nil {
				return err
			}
		}
		return RefreshMsg{} // Refresh to show the template change
	}
}

func addJobWithDependencies(planDir string, dependencies []string) tea.Cmd {
	// Build the command
	args := []string{"plan", "add", planDir, "-i"}

	// Add dependencies if provided
	for _, dep := range dependencies {
		args = append(args, "-d", dep)
	}

	// Run flow through grove delegator
	cmdArgs := append([]string{"flow"}, args...)
	return tea.ExecProcess(exec.Command("grove", cmdArgs...), func(err error) tea.Msg {
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

// addJobsFromRecipeCmd creates a command to add jobs from a recipe to the plan
func addJobsFromRecipeCmd(plan *orchestration.Plan, recipeName string, externalDeps []string) tea.Cmd {
	return func() tea.Msg {
		recipe, err := orchestration.GetRecipe(recipeName, "")
		if err != nil {
			return recipeAddedMsg{err: err}
		}

		// For now, we will use empty template data in the TUI
		templateData := struct {
			PlanName string
			Vars     map[string]string
		}{
			PlanName: plan.Name,
			Vars:     make(map[string]string),
		}

		_, err = orchestration.AddJobsFromRecipe(plan, recipe, externalDeps, templateData)
		return recipeAddedMsg{err: err}
	}
}
