package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/tui/theme"
	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

var ulog = grovelogging.NewUnifiedLogger("grove-flow")

// runPlanRun implements the run command.
func runPlanRun(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Load flow config
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return err
	}

	// Determine target - either job files or plan directory
	var planDir string
	var targetJobs []string

	// Early override: if --at resolved a unified target, use its plan dir and
	// treat positional args as job filenames within it. All existing
	// resolution below (plan-name, filename, title, cwd) stays intact as the
	// fallback when --at is absent.
	if unified, ok := TargetFromContext(cmd.Context()); ok && unified.PlanDir != "" {
		planDir = unified.PlanDir
		for _, arg := range args {
			targetJobs = append(targetJobs, filepath.Base(arg))
		}
	} else if len(args) > 0 {
		target := args[0]

		// Resolution order:
		// 1. If it's an absolute path or contains /, use as-is
		// 2. If it's a filename (ends with .md, no /), look in active plan directory
		// 3. Otherwise treat as title and do title-based lookup
		if !strings.Contains(target, "/") {
			if strings.HasSuffix(target, ".md") {
				// It's a filename - try to find in active plan directory
				activePlan, _ := state.GetString(planRunDir, plan.StateKey)
				if activePlan != "" {
					if planPath, err := resolvePlanPath(activePlan, planRunDir); err == nil {
						candidatePath := filepath.Join(planPath, target)
						if _, err := os.Stat(candidatePath); err == nil {
							target = candidatePath
						}
					}
				}
			} else {
				// Try plan name first, then fall back to title-based lookup
				if planPath, err := resolvePlanPath(target, planRunDir); err == nil {
					if info, statErr := os.Stat(planPath); statErr == nil && info.IsDir() {
						target = planPath
					} else {
						// Not a valid plan directory, try title-based lookup
						resolvedPath, err := resolveJobByTitle(target)
						if err != nil {
							return fmt.Errorf("could not find job by title %q: %w", target, err)
						}
						target = resolvedPath
					}
				} else {
					// resolvePlanPath failed, try title-based lookup
					resolvedPath, err := resolveJobByTitle(target)
					if err != nil {
						return fmt.Errorf("could not find job by title %q: %w", target, err)
					}
					target = resolvedPath
				}
			}
		}

		// Check if target is a directory or file
		info, err := os.Stat(target)
		if err != nil {
			// It might be a plan name in a configured plans_directory, try resolving it.
			resolvedPath, resolveErr := resolvePlanPath(target, planRunDir)
			if resolveErr != nil {
				return fmt.Errorf("target not found: %s", target)
			}
			info, err = os.Stat(resolvedPath)
			if err != nil {
				return fmt.Errorf("target not found: %s", resolvedPath)
			}
			target = resolvedPath // Use the resolved path from now on
		}

		if info.IsDir() {
			planDir = target
		} else {
			// It's one or more job files
			planDir = filepath.Dir(target)
			// First arg was resolved above; add remaining args as-is (they should be filenames)
			targetJobs = append(targetJobs, filepath.Base(target))
			for _, arg := range args[1:] {
				targetJobs = append(targetJobs, filepath.Base(arg))
			}
		}
	} else {
		// No target specified, try to use active job
		activeJob, err := state.GetString(planRunDir, plan.StateKey)
		if err != nil {
			return fmt.Errorf("get active job: %w", err)
		}
		if activeJob != "" {
			// Use active job
			resolvedPath, err := resolvePlanPath(activeJob, planRunDir)
			if err != nil {
				return fmt.Errorf("could not resolve active job path: %w", err)
			}
			planDir = resolvedPath
		} else {
			// Fall back to current directory
			planDir = "."
		}
	}

	// Load the plan
	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	// Prevent running jobs in a held plan
	if plan.Config != nil && plan.Config.Status == "hold" {
		return fmt.Errorf("cannot run jobs: plan is on hold. Use 'flow plan unhold' to resume")
	}

	// Check for multiple worktrees
	worktrees := make(map[string]bool)
	hasMainRepo := false
	for _, job := range plan.Jobs {
		if job.Worktree == "" {
			hasMainRepo = true
		} else {
			worktrees[job.Worktree] = true
		}
	}

	// Warn if multiple different worktrees or a mix of worktree and non-worktree jobs
	if (len(worktrees) > 1) || (len(worktrees) > 0 && hasMainRepo) {
		fmt.Printf("%s Warning: This plan uses multiple worktrees and/or the main repository:\n", color.YellowString(theme.IconWarning))
		if hasMainRepo {
			fmt.Println("  - <main-repo>")
		}
		for wt := range worktrees {
			fmt.Printf("  - %s\n", wt)
		}
		fmt.Println()
	}

	// Determine which jobs will actually be run
	var jobsToRun []*orchestration.Job
	if len(targetJobs) > 0 {
		// Specific jobs were requested
		var validTargetJobs []string
		persister := orchestration.NewStatePersister()

		for _, jobFile := range targetJobs {
			job, found := plan.GetJobByFilename(jobFile)
			if found {
				if job.Status == orchestration.JobStatusFailed || job.Status == orchestration.JobStatusAbandoned {
					fmt.Printf("%s Auto-resetting job '%s' from %s to %s\n",
						color.CyanString("↺"),
						job.Title,
						job.Status,
						orchestration.JobStatusPending)

					// Sync the reset to disk immediately so the daemon can evaluate it properly
					if err := job.UpdateStatus(persister, orchestration.JobStatusPending); err != nil {
						return fmt.Errorf("auto-resetting job %s: %w", jobFile, err)
					}

					jobsToRun = append(jobsToRun, job)
					validTargetJobs = append(validTargetJobs, jobFile)
				} else if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusPendingUser {
					// Check if chat job has new user content that warrants reopening
					jobContent, err := os.ReadFile(job.FilePath)
					if err == nil && orchestration.HasNewUserContent(jobContent) {
						fmt.Printf("%s Auto-reopening job '%s' (new user content detected)\n",
							color.CyanString("↺"),
							job.Title)

						// Transition back to pending
						if err := job.UpdateStatus(persister, orchestration.JobStatusPending); err != nil {
							fmt.Printf("%s Warning: could not reopen job '%s': %v\n",
								color.YellowString(theme.IconWarning),
								job.Title, err)
							continue
						}

						jobsToRun = append(jobsToRun, job)
						validTargetJobs = append(validTargetJobs, jobFile)
					} else {
						fmt.Printf("%s Skipping job '%s' (already completed)\n",
							color.YellowString(theme.IconWarning),
							job.Title)
						continue
					}
				} else {
					jobsToRun = append(jobsToRun, job)
					validTargetJobs = append(validTargetJobs, jobFile)
				}
			} else {
				// Check if the file exists but wasn't loaded (e.g. missing frontmatter)
				// If so, try to initialize it as a chat job automatically
				fullPath := filepath.Join(planDir, jobFile)
				if _, err := os.Stat(fullPath); err == nil {
					// File exists, try to ensure it's a chat job
					if initializedJob, err := ensureChatJob(fullPath); err == nil {
						// Set derived fields that LoadJob doesn't set
						initializedJob.Filename = jobFile
						initializedJob.FilePath = fullPath

						// Add to plan and runnable list
						plan.Jobs = append(plan.Jobs, initializedJob)
						if initializedJob.ID != "" {
							plan.JobsByID[initializedJob.ID] = initializedJob
						}
						jobsToRun = append(jobsToRun, initializedJob)
						validTargetJobs = append(validTargetJobs, jobFile)
					}
				}
			}
		}

		// Update target jobs to reflect only the validated and potentially reset jobs
		targetJobs = validTargetJobs

		if len(targetJobs) == 0 {
			fmt.Println("\nNo valid jobs to run.")
			return nil
		}
	} else if !planRunAll {
		// Running next jobs - get runnable jobs
		graph, _ := orchestration.BuildDependencyGraph(plan)
		jobsToRun = graph.GetRunnableJobs()
	}
	// Note: if planRunAll is true, we don't check because we want to avoid the prompt for batch runs

	// Resolve agent_target from the caller's environment. The executor
	// requires a concrete value ("native" or "tmux") — "auto" must be
	// resolved here at the CLI perimeter, not inside the executor.
	agentTarget := "tmux" // safe default
	if mux.ActiveMux() == mux.MuxTuimux {
		agentTarget = "tuimux"
	} else if os.Getenv("GROVE_TERMINAL") != "" {
		agentTarget = "native"
	}

	ulog.Info("Resolved agent target for CLI run").
		Field("agent_target", agentTarget).
		Field("active_mux", string(mux.ActiveMux())).
		Field("tuimux_pty", os.Getenv("TUIMUX_PTY")).
		Field("grove_terminal", os.Getenv("GROVE_TERMINAL")).
		StructuredOnly().Log(ctx)

	// Inject the loaded configuration into the plan object
	plan.Orchestration = &orchestration.Config{
		OneshotModel:        flowCfg.OneshotModel,
		MaxConsecutiveSteps: flowCfg.MaxConsecutiveSteps,
		AgentTarget:         agentTarget,
	}

	// Check if any oneshot jobs need to be run
	hasOneShot := false
	for _, job := range plan.Jobs {
		if job.Type == orchestration.JobTypeOneshot && job.Status == orchestration.JobStatusPending {
			hasOneShot = true
			break
		}
	}

	// Check for the grove command (which provides `grove llm`) if oneshot jobs are present
	if hasOneShot {
		if _, err := exec.LookPath("grove"); err != nil {
			return fmt.Errorf("dependency 'grove' not found in PATH (oneshot jobs invoke 'grove llm')")
		}
	}

	// Only set model override if explicitly provided via CLI flag
	modelOverride := planRunModel

	// Validate the model override early so the user gets a clear error
	// before any jobs are dispatched.
	if modelOverride != "" {
		if err := orchestration.ValidateModelKnown(modelOverride); err != nil {
			return fmt.Errorf("invalid --model override: %w", err)
		}
	}

	// Create orchestrator config
	maxSteps := 20 // Default
	if flowCfg.MaxConsecutiveSteps > 0 {
		maxSteps = flowCfg.MaxConsecutiveSteps
	}
	orchConfig := &orchestration.OrchestratorConfig{
		MaxParallelJobs:     planRunParallel,
		CheckInterval:       5 * time.Second,
		ModelOverride:       modelOverride,
		MaxConsecutiveSteps: maxSteps,
		SkipInteractive:     planRunSkipInteractive || planRunYes, // --yes implies skip interactive
	}

	// Configure runtime: DaemonRuntime by default, LocalRuntime with --local
	var daemonClient daemon.Client
	if !planRunLocal {
		daemonClient = daemon.NewWithAutoStart()
		if daemonClient.IsRunning() {
			// Daemon is available — create orchestrator first, then set DaemonRuntime
			// (DaemonRuntime needs the orchestrator as StatusUpdater)
		} else {
			// Daemon not available, fall back to local
			ulog.Info("Daemon not available, falling back to local execution").Log(ctx)
			daemonClient = nil
		}
	}

	// In the daemon-less LocalRuntime path no daemon owns the spawned agents,
	// so have the headless executor persist a daemon JobInfo (with the agent
	// PID) for each launch. A later daemon then adopts and reconciles these
	// detached agents from their JobInfo + .status files. When a daemon IS
	// running, plan_run delegates via DaemonRuntime and the daemon's own
	// persister manages jobs/<id>.json, so we leave this false to avoid a
	// double-writer clobbering the daemon's lifecycle-managed record.
	if daemonClient == nil {
		orchConfig.DaemonJobPersist = true
	}

	// Create orchestrator (uses LocalRuntime by default)
	orch, err := orchestration.NewOrchestrator(plan, orchConfig)
	if err != nil {
		return fmt.Errorf("create orchestrator: %w", err)
	}

	// If daemon is available, override with DaemonRuntime
	if daemonClient != nil {
		orchConfig.Runtime = orchestration.NewDaemonRuntime(daemonClient, orch, orch.Logger())
	}

	// Handle --background mode: submit jobs to daemon and exit
	if planRunBackground {
		if daemonClient == nil {
			return fmt.Errorf("--background requires the daemon; start it with 'grove daemon start' or remove --local flag")
		}
		return submitJobsBackground(ctx, daemonClient, plan, targetJobs, jobsToRun, agentTarget)
	}

	// Handle different run modes
	var runErr error
	if len(targetJobs) > 0 {
		// Run one or more specific jobs - build a valid sub-plan with dependencies
		subPlan := &orchestration.Plan{
			Name:          plan.Name,
			Directory:     plan.Directory,
			Jobs:          []*orchestration.Job{},
			JobsByID:      make(map[string]*orchestration.Job),
			Config:        plan.Config,
			Orchestration: plan.Orchestration,
		}

		// Collect selected jobs and all their transitive dependencies
		jobMap := make(map[string]*orchestration.Job)
		var collectDeps func(job *orchestration.Job)
		collectDeps = func(job *orchestration.Job) {
			if _, exists := jobMap[job.ID]; exists {
				return // Already added
			}
			jobMap[job.ID] = job

			// Recursively add dependencies
			for _, depID := range job.DependsOn {
				if depJob, found := plan.GetJobByID(depID); found {
					collectDeps(depJob)
				} else if depJob, found := plan.GetJobByFilename(depID); found {
					collectDeps(depJob)
				}
			}
		}

		// Start with the selected jobs
		for _, jobFile := range targetJobs {
			job, found := plan.GetJobByFilename(jobFile)
			if found {
				collectDeps(job)
			}
		}

		// Build the jobs list from the map
		for _, job := range jobMap {
			subPlan.Jobs = append(subPlan.Jobs, job)
		}
		subPlan.JobsByID = jobMap

		if err := subPlan.ResolveDependencies(); err != nil {
			return fmt.Errorf("resolving dependencies for job subset: %w", err)
		}

		// Create a new orchestrator for the sub-plan
		subOrch, err := orchestration.NewOrchestrator(subPlan, orchConfig)
		if err != nil {
			return fmt.Errorf("create orchestrator for subset: %w", err)
		}

		if len(targetJobs) == 1 {
			// For single job execution, create a single-job sub-plan
			// This ensures chat jobs execute directly without confirmation dialogs
			runErr = runSingleJob(ctx, subOrch, subPlan, targetJobs[0], true)
		} else {
			// Count how many jobs were originally selected vs dependencies
			selectedCount := len(targetJobs)
			depCount := len(subPlan.Jobs) - selectedCount

			// Run all jobs in the sub-plan
			if depCount > 0 {
				fmt.Printf("\n%s Running %d selected jobs (+%d dependencies) respecting dependencies...\n",
					color.YellowString(theme.IconRunning), selectedCount, depCount)
			} else {
				fmt.Printf("\n%s Running %d selected jobs respecting dependencies...\n",
					color.YellowString(theme.IconRunning), selectedCount)
			}

			runErr = subOrch.RunAll(ctx)
			if runErr != nil {
				fmt.Printf("\n%s Some selected jobs failed.\n", color.RedString(theme.IconError))
			} else {
				fmt.Printf("\n%s All selected jobs completed successfully.\n", color.GreenString(theme.IconSuccess))
			}
		}
	} else if planRunAll {
		// Check if this is a chat-style plan
		planMDPath := filepath.Join(plan.Directory, "plan.md")
		if _, err := os.Stat(planMDPath); err == nil {
			// plan.md exists, check if it's a chat job
			for _, job := range plan.Jobs {
				if job.FilePath == planMDPath && job.Type == orchestration.JobTypeChat {
					return fmt.Errorf("flow plan run --all is disabled for chat-style plans to prevent infinite loops. Please run chat turns one by one")
				}
			}
		}
		// Run all jobs
		runErr = runAllJobs(ctx, orch, plan, cmd)
	} else if planRunNext {
		// Run next available jobs
		runErr = runNextJobs(ctx, orch, plan, cmd)
	} else {
		// Default to running next if no flags specified
		planRunNext = true
		runErr = runNextJobs(ctx, orch, plan, cmd)
	}

	return runErr
}

// runSingleJob executes a specific job.
func runSingleJob(ctx context.Context, orch *orchestration.Orchestrator, plan *orchestration.Plan, jobFile string, skipConfirm bool) error {
	// Find the job
	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	// Check if runnable
	if job.Status == orchestration.JobStatusCompleted {
		return fmt.Errorf("job already completed: %s", jobFile)
	}

	if job.Status == orchestration.JobStatusRunning {
		return fmt.Errorf("job already running: %s", jobFile)
	}

	// Check dependencies
	unmetDeps := getUnmetDependencies(job, plan)
	if len(unmetDeps) > 0 {
		return fmt.Errorf("dependencies not satisfied for job %s: %s",
			jobFile, strings.Join(unmetDeps, ", "))
	}

	if !skipConfirm {
		// Show job details for plan-based execution
		fmt.Printf("Job: %s\n", color.CyanString(job.Title))
		fmt.Printf("Status: %s → %s\n", job.Status, orchestration.JobStatusRunning)
		fmt.Printf("Dependencies: %s All satisfied\n", color.GreenString(theme.IconSuccess))

		// Confirm execution unless --yes
		if !planRunYes {
			fmt.Print("\nExecute this job? [Y/n]: ")
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			response = strings.TrimSpace(response)
			if response != "" && response != "y" && response != "Y" {
				fmt.Println("Aborted.")
				return nil
			}
		}
	}

	// Execute the job
	ulog.Progress("Running job").
		Field("job", jobFile).
		Pretty(fmt.Sprintf("%s Running job %s...", color.YellowString(theme.IconRunning), jobFile)).
		Log(ctx)

	jobPath := filepath.Join(plan.Directory, jobFile)
	err := orch.RunJob(ctx, jobPath)
	if err != nil {
		ulog.Error("Job failed").
			Field("job", job.Title).
			Err(err).
			Log(ctx)
		return err
	}

	// Check final status — interactive jobs stay "running" after the executor returns
	if job.Status == orchestration.JobStatusRunning {
		ulog.Info("Job launched").
			Field("job", job.Title).
			Pretty(fmt.Sprintf("%s Job running: %s", color.BlueString(theme.IconInfo), job.Title)).
			Log(ctx)
		fmt.Println("Use 'flow plan status' to monitor progress, and 'flow plan complete' when done.")
	} else {
		ulog.Success("Job completed").
			Field("job", job.Title).
			Pretty(fmt.Sprintf("%s Job completed: %s", color.GreenString(theme.IconSuccess), job.Title)).
			Log(ctx)
	}
	return nil
}

// runNextJobs executes all currently runnable jobs.
func runNextJobs(ctx context.Context, orch *orchestration.Orchestrator, plan *orchestration.Plan, cmd *cobra.Command) error {
	// Get current status
	status := orch.GetStatus()

	// Get runnable jobs first to determine if there's anything to do
	graph, _ := orchestration.BuildDependencyGraph(plan)
	runnable := graph.GetRunnableJobs()

	// Check if we're truly done (no pending, no running, no runnable jobs)
	if status.Pending == 0 && status.Running == 0 && len(runnable) == 0 {
		if status.Failed > 0 {
			return fmt.Errorf("no runnable jobs - %d jobs failed", status.Failed)
		}
		fmt.Println("All jobs completed!")
		return nil
	}

	if len(runnable) == 0 {
		if status.Running > 0 {
			fmt.Printf("\n%s All runnable jobs submitted. %d job(s) still running (e.g. interactive agents).\n",
				color.BlueString(theme.IconInfo), status.Running)
			fmt.Println("Use 'flow plan status' to monitor progress.")
			return nil
		}
		return fmt.Errorf("no runnable jobs - check for failed dependencies")
	}

	// Show what will run
	fmt.Println("Ready to run:")
	for _, job := range runnable {
		fmt.Printf("- %s (%s)\n", job.Filename, job.Title)
	}

	// Confirm unless --yes
	if !planRunYes {
		fmt.Printf("\nRun %d job(s)? [Y/n]: ", len(runnable))
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(response)
		if response != "" && response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Execute jobs
	fmt.Printf("\n%s Running %d job(s)...\n",
		color.YellowString(theme.IconRunning), len(runnable))

	err := orch.RunNext(ctx)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	// Check if any jobs are still running (e.g. interactive agents)
	postStatus := orch.GetStatus()
	if postStatus.Running > 0 {
		fmt.Printf("\n%s %d job(s) launched and still running (e.g. interactive agents).\n",
			color.BlueString(theme.IconInfo), postStatus.Running)
		fmt.Println("Use 'flow plan status' to monitor progress.")
	} else {
		fmt.Printf("%s All jobs completed\n", color.GreenString(theme.IconSuccess))
	}
	return nil
}

// runAllJobs executes all remaining jobs in the plan.
func runAllJobs(ctx context.Context, orch *orchestration.Orchestrator, plan *orchestration.Plan, cmd *cobra.Command) error {
	// Get initial status
	status := orch.GetStatus()

	remaining := status.Pending + status.Running
	if remaining == 0 {
		if status.Failed > 0 {
			return fmt.Errorf("no jobs to run - %d jobs failed", status.Failed)
		}
		fmt.Println("All jobs already completed!")
		return nil
	}

	// Show plan overview
	fmt.Printf("Plan: %s\n", color.CyanString(plan.Name))
	fmt.Printf("Total jobs: %d (%d completed, %d remaining)\n",
		status.Total, status.Completed, remaining)

	// Confirm unless --yes
	if !planRunYes {
		fmt.Print("\nThis will run all remaining jobs. Continue? [Y/n]: ")
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(response)
		if response != "" && response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Run all jobs
	fmt.Println("\nStarting orchestration...")

	// Set up progress monitoring if --watch
	if planRunWatch {
		go monitorProgress(ctx, orch)
	}

	err := orch.RunAll(ctx)
	if err != nil {
		return fmt.Errorf("orchestration failed: %w", err)
	}

	// Final status
	finalStatus := orch.GetStatus()
	fmt.Printf("\n%s Orchestration complete!\n", color.GreenString(theme.IconSuccess))
	fmt.Printf("Completed: %d, Failed: %d\n",
		finalStatus.Completed, finalStatus.Failed)

	return nil
}

// getUnmetDependencies returns the IDs of unmet dependencies.
func getUnmetDependencies(job *orchestration.Job, plan *orchestration.Plan) []string {
	var unmet []string

	for _, depRef := range job.DependsOn {
		// Try to find by ID first
		dep, found := plan.GetJobByID(depRef)
		if !found {
			// Try to find by filename
			dep, found = plan.GetJobByFilename(depRef)
			if !found {
				unmet = append(unmet, depRef+" (not found)")
				continue
			}
		}

		dependencyMet := false
		if dep.Status == orchestration.JobStatusCompleted || dep.Status == orchestration.JobStatusAbandoned {
			dependencyMet = true
		} else if (job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeAgent) && dep.Type == orchestration.JobTypeChat && dep.Status == orchestration.JobStatusPendingUser {
			// Special case: an interactive agent can run if its chat dependency is pending user input.
			dependencyMet = true
		}

		if !dependencyMet {
			unmet = append(unmet, fmt.Sprintf("%s (%s)", depRef, dep.Status))
		}
	}

	return unmet
}

// monitorProgress displays real-time progress updates.
func monitorProgress(ctx context.Context, orch *orchestration.Orchestrator) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status := orch.GetStatus()
			fmt.Printf("\r%s Progress: %d/%d completed, %d running",
				spinner[i%len(spinner)],
				status.Completed,
				status.Total,
				status.Running)
			i++
		}
	}
}

// Command flags specific to run (defined in jobs.go)
var (
	planRunParallel        int
	planRunWatch           bool
	planRunYes             bool
	planRunSkipInteractive bool
)

// submitJobsBackground submits jobs to the daemon and exits without waiting.
func submitJobsBackground(ctx context.Context, client daemon.Client, plan *orchestration.Plan, targetJobs []string, jobsToRun []*orchestration.Job, agentTarget string) error {
	jobs := jobsToRun
	if len(jobs) == 0 && len(targetJobs) > 0 {
		for _, jf := range targetJobs {
			if job, found := plan.GetJobByFilename(jf); found {
				jobs = append(jobs, job)
			}
		}
	}

	if len(jobs) == 0 {
		// Submit next runnable jobs
		graph, _ := orchestration.BuildDependencyGraph(plan)
		jobs = graph.GetRunnableJobs()
	}

	if len(jobs) == 0 {
		return fmt.Errorf("no jobs to submit")
	}

	fmt.Printf("Submitting %d job(s) to daemon in background...\n", len(jobs))
	for _, job := range jobs {
		info, err := client.SubmitJob(ctx, models.JobSubmitRequest{
			PlanDir:     plan.Directory,
			JobFile:     job.Filename,
			AgentTarget: agentTarget,
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", color.RedString(theme.IconError), job.Filename, err)
			continue
		}
		fmt.Printf("  %s %s → %s (id: %s)\n", color.GreenString(theme.IconSuccess), job.Filename, info.Status, info.ID)
	}

	fmt.Println("\nJobs submitted. Use 'flow plan status' to monitor progress.")
	return nil
}

// looksLikeFilePath returns true if the string appears to be a file path rather than a title.
// A string is considered a file path if it contains "/" or ends with ".md".
func looksLikeFilePath(s string) bool {
	return strings.Contains(s, "/") || strings.HasSuffix(s, ".md")
}

// resolveJobByTitle attempts to find a job by its title.
// It searches in the following order:
// 1. Active plan's jobs
// 2. Notebook directories (inbox, issues, in_progress, quick, etc.)
func resolveJobByTitle(title string) (string, error) {
	// 1. Check active plan first
	if path, err := findJobInActivePlan(title); err == nil && path != "" {
		return path, nil
	}

	// 2. Fall back to notebook directories
	if path, err := findJobInNotebook(title); err == nil && path != "" {
		return path, nil
	}

	return "", fmt.Errorf("no job found with title %q in active plan or notebook directories", title)
}

// findJobInActivePlan searches for a job with the given title in the active plan.
func findJobInActivePlan(title string) (string, error) {
	// Get active plan directory
	activeJob, err := state.GetString(planRunDir, plan.StateKey)
	if err != nil || activeJob == "" {
		return "", nil // No active plan, not an error
	}

	planDir, err := resolvePlanPath(activeJob, ".")
	if err != nil {
		return "", nil // Can't resolve, not an error
	}

	// Load the plan
	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return "", nil // Can't load plan, not an error
	}

	// Search for job by title
	for _, job := range plan.Jobs {
		if job.Title == title {
			return job.FilePath, nil
		}
	}

	return "", nil // Not found in active plan
}

// nbContext represents the structure returned by `nb context --json`
type nbContext struct {
	Paths map[string]string `json:"paths"`
}

// findJobInNotebook searches for a job with the given title in notebook directories.
func findJobInNotebook(title string) (string, error) {
	// Run nb context --json to get notebook paths
	cmd := exec.Command("nb", "context", "--json")
	output, err := cmd.Output()
	if err != nil {
		return "", nil // nb command failed, not an error for us
	}

	var ctx nbContext
	if err := json.Unmarshal(output, &ctx); err != nil {
		return "", nil // Parse error, not an error for us
	}

	// Priority order for searching notebook directories
	searchOrder := []string{
		"inbox",       // likely location for quick chats
		"issues",      // common for issue-related chats
		"in_progress", // active work
		"quick",       // quick notes
		"current",     // current workspace items
		"llm",         // LLM-related items
		"plans",       // plans directory
	}

	// Search in priority order first
	for _, dir := range searchOrder {
		if path, ok := ctx.Paths[dir]; ok {
			if found := searchDirForJobByTitle(path, title); found != "" {
				return found, nil
			}
		}
	}

	// Search remaining directories
	for dirName, path := range ctx.Paths {
		// Skip already searched directories
		alreadySearched := false
		for _, searched := range searchOrder {
			if dirName == searched {
				alreadySearched = true
				break
			}
		}
		if alreadySearched {
			continue
		}

		if found := searchDirForJobByTitle(path, title); found != "" {
			return found, nil
		}
	}

	return "", nil // Not found
}

// searchDirForJobByTitle walks a directory looking for a markdown file with matching title.
func searchDirForJobByTitle(dir, title string) string {
	if dir == "" {
		return ""
	}

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return ""
	}

	var found string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		// Try to load as a job
		job, err := orchestration.LoadJob(path)
		if err != nil {
			// Not a job file, check if filename matches title
			baseName := strings.TrimSuffix(info.Name(), ".md")
			if baseName == title {
				found = path
				return filepath.SkipAll
			}
			return nil
		}

		// Check if job title matches
		if job.Title == title {
			found = path
			return filepath.SkipAll
		}

		return nil
	})

	return found
}
