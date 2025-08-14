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

	// Load grove config to get default model if no override specified
	modelOverride := planRunModel
	if modelOverride == "" && flowCfg.OneshotModel != "" {
		modelOverride = flowCfg.OneshotModel
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
		SkipInteractive:     planRunSkipInteractive,
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
	if targetJob != "" {
		// Run single job
		return runSingleJob(ctx, orch, plan, targetJob)
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
		return runAllJobs(ctx, orch, plan, cmd)
	} else if planRunNext {
		// Run next available jobs
		return runNextJobs(ctx, orch, plan, cmd)
	} else {
		// Default to running next if no flags specified
		planRunNext = true
		return runNextJobs(ctx, orch, plan, cmd)
	}
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
	planRunParallel int
	planRunWatch    bool
	planRunYes      bool
	planRunSkipInteractive bool
)