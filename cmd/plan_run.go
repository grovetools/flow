package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-core/docker"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-flow/pkg/state"
	"github.com/spf13/cobra"
)

// runPlanRun implements the run command.
func runPlanRun(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Load flow config
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return err
	}

	// Determine target - either a job file or plan directory
	var planDir string
	var targetJob string

	if len(args) > 0 {
		target := args[0]
		info, err := os.Stat(target)
		if err != nil {
			// It might be a plan name in a configured plans_directory, try resolving it.
			resolvedPath, resolveErr := resolvePlanPath(target)
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
			// It's a single file (like a chat job)
			planDir = filepath.Dir(target)
			targetJob = filepath.Base(target)
		}
	} else {
		// No target specified, try to use active job
		activeJob, err := state.GetActiveJob()
		if err != nil {
			return fmt.Errorf("get active job: %w", err)
		}
		if activeJob != "" {
			// Use active job
			resolvedPath, err := resolvePlanPath(activeJob)
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
		fmt.Printf("%s Warning: This plan uses multiple worktrees and/or the main repository:\n", color.YellowString("⚠️"))
		if hasMainRepo {
			fmt.Println("  - <main-repo>")
		}
		for wt := range worktrees {
			fmt.Printf("  - %s\n", wt)
		}
		fmt.Println()
	}

	// If plan uses a single worktree and we're not already in that session, create/switch to it
	if len(worktrees) == 1 && !hasMainRepo {
		worktreeName := ""
		for wt := range worktrees {
			worktreeName = wt
			break
		}

		// Get git root to check if we're already in the worktree
		gitRoot, err := orchestration.GetGitRootSafe(plan.Directory)
		if err != nil {
			gitRoot = ""
		}

		// Check if we're already in the worktree directory
		currentDir, _ := os.Getwd()
		expectedWorktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
		alreadyInWorktree := gitRoot != "" && currentDir != "" && strings.HasPrefix(currentDir, expectedWorktreePath)

		// Check if we're already in the correct tmux session
		currentTmuxSession := ""
		if os.Getenv("TMUX") != "" {
			// We're in tmux, get the current session name
			cmd := exec.Command("tmux", "display-message", "-p", "#S")
			output, err := cmd.Output()
			if err == nil {
				currentTmuxSession = strings.TrimSpace(string(output))
			}
		}
		
		expectedSessionName := SanitizeForTmuxSession(worktreeName)
		alreadyInCorrectSession := currentTmuxSession == expectedSessionName

		// Only prompt if we're not already in the worktree or the correct session
		if !alreadyInWorktree && !alreadyInCorrectSession {
			// Check if user wants to use tmux session for this worktree
			if !planRunYes && os.Getenv("TERM") != "" {
				// Check if we have a TTY before trying to prompt
				if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
					// No TTY available, skip the prompt and continue without tmux
					fmt.Println("No TTY available, continuing without tmux session...")
				} else {
					fmt.Printf("This plan uses worktree '%s'. Launch in dedicated tmux session? [Y/n]: ", color.CyanString(worktreeName))
					var response string
					fmt.Scanln(&response)
					response = strings.ToLower(strings.TrimSpace(response))
					
					// If user says no, continue with normal execution
					if response == "n" || response == "no" {
						fmt.Println("Continuing without tmux session...")
					} else {
						// User said yes or just pressed enter (default yes)
						reconstructedCmd := buildRunCommandForTmux(cmd, args)
						if err := CreateOrSwitchToWorktreeSessionAndRunCommand(ctx, plan, worktreeName, reconstructedCmd); err != nil {
							// If error, just continue with normal execution
							fmt.Printf("Note: Could not create/switch to tmux session: %v\n", err)
						} else {
							// Successfully launched in tmux session, exit
							return nil
						}
					}
				}
			} else if planRunYes {
				// Auto-yes mode, create/switch to session
				reconstructedCmd := buildRunCommandForTmux(cmd, args)
				if err := CreateOrSwitchToWorktreeSessionAndRunCommand(ctx, plan, worktreeName, reconstructedCmd); err != nil {
					// If error, just continue with normal execution
					fmt.Printf("Note: Could not create/switch to tmux session: %v\n", err)
				} else {
					// Successfully launched in tmux session, exit
					return nil
				}
			}
		}
	}

	// Inject the loaded configuration into the plan object
	plan.Orchestration = &orchestration.Config{
		OneshotModel:         flowCfg.OneshotModel,
		TargetAgentContainer: flowCfg.TargetAgentContainer,
		PlansDirectory:       flowCfg.PlansDirectory,
		MaxConsecutiveSteps:  flowCfg.MaxConsecutiveSteps,
	}

	// Check if any oneshot jobs need to be run
	hasOneShot := false
	for _, job := range plan.Jobs {
		if job.Type == orchestration.JobTypeOneshot && job.Status == orchestration.JobStatusPending {
			hasOneShot = true
			break
		}
	}

	// Check for llm command if oneshot jobs are present
	if hasOneShot {
		if _, err := exec.LookPath("llm"); err != nil {
			return fmt.Errorf("dependency 'llm' not found. Please install with 'pip install llm'")
		}
	}

	// Only set model override if explicitly provided via CLI flag
	modelOverride := planRunModel

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

	// Check if we need Docker (only for agent and interactive_agent jobs)
	var dockerClient docker.Client
	hasAgentJobs := false
	for _, job := range plan.Jobs {
		if job.Type == orchestration.JobTypeAgent || job.Type == orchestration.JobTypeInteractiveAgent {
			hasAgentJobs = true
			break
		}
	}

	if hasAgentJobs && !shouldSkipDockerCheck() {
		dockerClient, err = docker.NewSDKClient()
		if err != nil {
			return fmt.Errorf("failed to create Docker client: %w", err)
		}
	}

	// Create orchestrator
	orch, err := orchestration.NewOrchestrator(plan, orchConfig, dockerClient)
	if err != nil {
		return fmt.Errorf("create orchestrator: %w", err)
	}

	// Handle different run modes
	var runErr error
	if targetJob != "" {
		// Run single job
		runErr = runSingleJob(ctx, orch, plan, targetJob)
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

	// Wait for any pending hooks to complete. This is the crucial addition.
	orchestration.WaitForHooks()

	return runErr
}

// runSingleJob executes a specific job.
func runSingleJob(ctx context.Context, orch *orchestration.Orchestrator, plan *orchestration.Plan, jobFile string) error {
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

	if job.Status == orchestration.JobStatusFailed {
		// Allow re-running failed jobs by resetting status to pending
		job.Status = orchestration.JobStatusPending
		// Note: The orchestrator will handle updating the job file when it runs
	}

	// Check dependencies
	unmetDeps := getUnmetDependencies(job, plan)
	if len(unmetDeps) > 0 {
		return fmt.Errorf("dependencies not satisfied for job %s: %s",
			jobFile, strings.Join(unmetDeps, ", "))
	}

	// Show job details
	fmt.Printf("Job: %s\n", color.CyanString(job.Title))
	fmt.Printf("Status: %s → %s\n", job.Status, orchestration.JobStatusRunning)
	fmt.Printf("Dependencies: %s All satisfied\n", color.GreenString("✓"))

	// Confirm execution unless --yes
	if !planRunYes {
		fmt.Print("\nExecute this job? [Y/n]: ")
		var response string
		fmt.Scanln(&response)
		if response != "" && response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Execute the job
	fmt.Printf("\n%s Running job %s...\n",
		color.YellowString("⚡"), jobFile)

	jobPath := filepath.Join(plan.Directory, jobFile)
	err := orch.RunJob(ctx, jobPath)

	if err != nil {
		fmt.Printf("%s Job failed: %v\n", color.RedString("✗"), err)
		return err
	}

	fmt.Printf("%s Job completed successfully\n", color.GreenString("✓"))
	return nil
}

// runNextJobs executes all currently runnable jobs.
func runNextJobs(ctx context.Context, orch *orchestration.Orchestrator, plan *orchestration.Plan, cmd *cobra.Command) error {
	// Get current status
	status := orch.GetStatus()

	if status.Pending == 0 && status.Running == 0 {
		if status.Failed > 0 {
			return fmt.Errorf("no runnable jobs - %d jobs failed", status.Failed)
		}
		fmt.Println("All jobs completed!")
		return nil
	}

	// Get runnable jobs
	graph, _ := orchestration.BuildDependencyGraph(plan)
	runnable := graph.GetRunnableJobs()

	if len(runnable) == 0 {
		if status.Running > 0 {
			return fmt.Errorf("no runnable jobs - %d jobs are still running", status.Running)
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
		var response string
		fmt.Scanln(&response)
		if response != "" && response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Execute jobs
	fmt.Printf("\n%s Running %d job(s)...\n",
		color.YellowString("⚡"), len(runnable))

	err := orch.RunNext(ctx)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	fmt.Printf("%s All jobs completed\n", color.GreenString("✓"))
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
		var response string
		fmt.Scanln(&response)
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
	fmt.Printf("\n%s Orchestration complete!\n", color.GreenString("✓"))
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

		if dep.Status != orchestration.JobStatusCompleted {
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

// buildRunCommandForTmux reconstructs the flow plan run command with its flags for execution inside tmux.
func buildRunCommandForTmux(cmd *cobra.Command, args []string) []string {
	flowCmd := []string{"flow", "plan", "run"}

	// Add all the original flags only if they were explicitly set
	if cmd.Flags().Changed("all") && planRunAll {
		flowCmd = append(flowCmd, "--all")
	}
	if cmd.Flags().Changed("next") && planRunNext {
		flowCmd = append(flowCmd, "--next")
	}
	if cmd.Flags().Changed("yes") && planRunYes {
		flowCmd = append(flowCmd, "--yes")
	}
	if cmd.Flags().Changed("watch") && planRunWatch {
		flowCmd = append(flowCmd, "--watch")
	}
	if cmd.Flags().Changed("skip-interactive") && planRunSkipInteractive {
		flowCmd = append(flowCmd, "--skip-interactive")
	}
	if cmd.Flags().Changed("parallel") {
		flowCmd = append(flowCmd, "--parallel", fmt.Sprintf("%d", planRunParallel))
	}
	if cmd.Flags().Changed("model") && planRunModel != "" {
		flowCmd = append(flowCmd, "--model", planRunModel)
	}

	// Add the original arguments
	flowCmd = append(flowCmd, args...)
	return flowCmd
}

