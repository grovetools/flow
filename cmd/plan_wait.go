package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

var (
	waitTimeout time.Duration
	waitAny     bool
)

func init() {
	planCmd.AddCommand(planWaitCmd)
	planWaitCmd.Flags().DurationVar(&waitTimeout, "timeout", 0, "Maximum time to wait for job(s) to reach terminal state (e.g., 5m, 30s)")
	planWaitCmd.Flags().BoolVar(&waitAny, "any", false, "Exit when any of the jobs reaches a terminal state (multiple job IDs allowed)")
}

var planWaitCmd = &cobra.Command{
	Use:   "wait <job> [<job2> ...]",
	Short: "Wait for job(s) to reach a terminal state (completed/failed/abandoned)",
	Long: `Block until one or more jobs reach a terminal state (completed, failed, or abandoned).

By default, waits for the specified job to complete. Use --any to wait for the first
of multiple jobs to finish. Use --timeout to set a maximum wait duration.

Examples:
  flow plan wait my-job.md
  flow plan wait my-job.md --timeout 30s
  flow plan wait job1.md job2.md --any --timeout 5m`,
	RunE: runPlanWait,
}

func runPlanWait(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("at least one job must be specified")
	}

	ctx := context.Background()
	if waitTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, waitTimeout)
		defer cancel()
	}

	// Get the active plan directory. Early override: when --at resolved a
	// unified target, base relative job paths on its plan dir; otherwise the
	// existing planRunDir/cwd default is preserved untouched.
	var planDir string
	if unified, ok := TargetFromContext(cmd.Context()); ok && unified.PlanDir != "" {
		planDir = unified.PlanDir
	} else {
		planDir = planRunDir
		if planDir == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("could not determine current directory: %w", err)
			}
			planDir = cwd
		}
	}

	// Resolve job file paths
	var jobPaths []string
	for _, arg := range args {
		jobPath := arg
		if !filepath.IsAbs(jobPath) {
			jobPath = filepath.Join(planDir, jobPath)
		}
		jobPaths = append(jobPaths, jobPath)
	}

	// Validate all job files exist
	for _, jobPath := range jobPaths {
		if _, err := os.Stat(jobPath); err != nil {
			return fmt.Errorf("job file not found: %s", jobPath)
		}
	}

	if waitAny && len(jobPaths) > 1 {
		return waitForAny(ctx, jobPaths)
	} else if len(jobPaths) == 1 {
		return waitForOne(ctx, jobPaths[0])
	}

	return fmt.Errorf("unexpected argument combination")
}

// waitForOne blocks until a single job reaches a terminal state.
func waitForOne(ctx context.Context, jobPath string) error {
	return waitForJobs(ctx, []string{jobPath}, true)
}

// waitForAny blocks until any of the jobs reaches a terminal state.
func waitForAny(ctx context.Context, jobPaths []string) error {
	return waitForJobs(ctx, jobPaths, false)
}

// waitForJobs implements the core waiting logic.
// If waitAll is true, waits for the single job (array of 1).
// If waitAll is false, returns when the first job reaches a terminal state.
func waitForJobs(ctx context.Context, jobPaths []string, waitAll bool) error {
	// Create a watcher for filesystem events
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watcher.Close()

	// Add all job files to the watcher
	for _, jobPath := range jobPaths {
		if err := watcher.Add(jobPath); err != nil {
			return fmt.Errorf("watch %s: %w", jobPath, err)
		}
	}

	// Helper to check if a job is in a terminal state
	getJobStatus := func(jobPath string) (orchestration.JobStatus, error) {
		job, err := orchestration.LoadJob(jobPath)
		if err != nil {
			return "", err
		}
		return job.Status, nil
	}

	// Helper to check if any/all jobs are terminal
	checkTerminalStates := func() (bool, orchestration.JobStatus, string) {
		if waitAll {
			// Single job case: check if it's terminal
			status, err := getJobStatus(jobPaths[0])
			if err != nil {
				return false, "", ""
			}
			isTerminal := status == orchestration.JobStatusCompleted ||
				status == orchestration.JobStatusFailed ||
				status == orchestration.JobStatusAbandoned
			return isTerminal, status, jobPaths[0]
		} else {
			// Multiple jobs: return on first terminal
			for _, jobPath := range jobPaths {
				status, err := getJobStatus(jobPath)
				if err != nil {
					continue
				}
				isTerminal := status == orchestration.JobStatusCompleted ||
					status == orchestration.JobStatusFailed ||
					status == orchestration.JobStatusAbandoned
				if isTerminal {
					return true, status, jobPath
				}
			}
			return false, "", ""
		}
	}

	// Initial check: job may already be terminal
	if isTerminal, status, jobPath := checkTerminalStates(); isTerminal {
		fmt.Printf("%s %s (status: %s)\n", status, filepath.Base(jobPath), status)
		return exitWithStatus(status)
	}

	// Polling fallback timer (in case fsnotify misses events)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("timeout waiting for job(s) to complete")
			}
			return ctx.Err()

		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("watcher closed unexpectedly")
			}
			// On file change, check if terminal state reached
			if event.Op&fsnotify.Write == fsnotify.Write {
				if isTerminal, status, jobPath := checkTerminalStates(); isTerminal {
					fmt.Printf("%s %s (status: %s)\n", status, filepath.Base(jobPath), status)
					return exitWithStatus(status)
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return fmt.Errorf("watcher error channel closed")
			}
			// Log but continue watching
			fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)

		case <-ticker.C:
			// Periodic polling fallback
			if isTerminal, status, jobPath := checkTerminalStates(); isTerminal {
				fmt.Printf("%s %s (status: %s)\n", status, filepath.Base(jobPath), status)
				return exitWithStatus(status)
			}
		}
	}
}

// exitWithStatus returns nil (exit 0) for completed, error (exit 1) for failed/abandoned.
func exitWithStatus(status orchestration.JobStatus) error {
	switch status {
	case orchestration.JobStatusCompleted:
		return nil
	case orchestration.JobStatusFailed, orchestration.JobStatusAbandoned:
		return fmt.Errorf("job %s", status)
	default:
		return fmt.Errorf("unexpected status: %s", status)
	}
}
