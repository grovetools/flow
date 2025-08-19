package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// runPlanStep implements the step command for guided plan execution.
func runPlanStep(cmd *cobra.Command, args []string) error {
	// Determine plan directory
	var planDir string
	if len(args) > 0 {
		planDir = args[0]
	} else {
		// Try to use current directory
		planDir = "."
	}

	// Resolve the plan path
	resolvedPath, err := resolvePlanPath(planDir)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}
	planDir = resolvedPath

	// Create a reader for user input
	reader := bufio.NewReader(os.Stdin)

	// Main execution loop
	for {
		// Load the plan fresh each iteration to get updated status
		plan, err := orchestration.LoadPlan(planDir)
		if err != nil {
			return fmt.Errorf("load plan: %w", err)
		}

		// Build dependency graph
		graph, err := orchestration.BuildDependencyGraph(plan)
		if err != nil {
			return fmt.Errorf("build dependency graph: %w", err)
		}

		// Get runnable jobs
		runnableJobs := graph.GetRunnableJobs()

		// If no runnable jobs, check if plan is complete or blocked
		if len(runnableJobs) == 0 {
			// Check if all jobs are completed
			allCompleted := true
			blockedJobs := 0
			failedJobs := 0

			for _, job := range plan.Jobs {
				switch job.Status {
				case orchestration.JobStatusPending:
					// Check if this job has unmet dependencies
					unmetDeps := getUnmetDependencies(job, plan)
					if len(unmetDeps) > 0 {
						blockedJobs++
					}
				case orchestration.JobStatusFailed:
					failedJobs++
					allCompleted = false
				case orchestration.JobStatusRunning:
					allCompleted = false
				case orchestration.JobStatusCompleted:
					// Already completed
				default:
					allCompleted = false
				}
			}

			if allCompleted {
				fmt.Println(color.GreenString("✓") + " All jobs in the plan have been completed!")
			} else if failedJobs > 0 {
				fmt.Printf("%s %d job(s) failed. Fix and re-run failed jobs or skip them to continue.\n",
					color.RedString("✗"), failedJobs)
			} else if blockedJobs > 0 {
				fmt.Printf("%s %d job(s) are blocked by dependencies.\n",
					color.YellowString("⚠"), blockedJobs)
			} else {
				fmt.Println("No runnable jobs found.")
			}
			break
		}

		// Display runnable jobs
		fmt.Println("\n" + color.CyanString("Next runnable job(s):"))
		for i, job := range runnableJobs {
			fmt.Printf("%d. %s (%s)\n", i+1, job.Title, job.Filename)
			if job.Type == orchestration.JobTypeAgent {
				fmt.Printf("   Type: %s (interactive)\n", color.YellowString(string(job.Type)))
			} else {
				fmt.Printf("   Type: %s\n", string(job.Type))
			}
			if len(job.DependsOn) > 0 {
				fmt.Printf("   Dependencies: %s\n", strings.Join(job.DependsOn, ", "))
			}
		}

		// Prompt user for action
		fmt.Print("\nWhat would you like to do? [R]un, [L]aunch, [S]kip, [Q]uit: ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read user input: %w", err)
		}

		choice := strings.TrimSpace(strings.ToLower(input))

		switch choice {
		case "r", "run":
			// Run the first job non-interactively
			if len(runnableJobs) > 0 {
				jobToRun := runnableJobs[0]
				fmt.Printf("\nRunning job: %s\n", color.CyanString(jobToRun.Title))

				// Execute using flow plan run
				flowBinary := os.Args[0]
				runCmd := exec.Command(flowBinary, "plan", "run", filepath.Join(planDir, jobToRun.Filename), "--yes")
				runCmd.Stdout = os.Stdout
				runCmd.Stderr = os.Stderr
				runCmd.Stdin = os.Stdin

				if err := runCmd.Run(); err != nil {
					fmt.Printf("%s Error running job: %v\n", color.RedString("✗"), err)
				}
			}

		case "l", "launch":
			// Launch the first job interactively (for agent jobs)
			if len(runnableJobs) > 0 {
				jobToLaunch := runnableJobs[0]
				if jobToLaunch.Type != orchestration.JobTypeAgent {
					fmt.Printf("%s Job '%s' is not an agent job. Use 'Run' instead.\n",
						color.YellowString("⚠"), jobToLaunch.Title)
					continue
				}

				fmt.Printf("\nLaunching interactive session for: %s\n", color.CyanString(jobToLaunch.Title))

				// Execute using flow plan launch
				flowBinary := os.Args[0]
				launchCmd := exec.Command(flowBinary, "plan", "launch", filepath.Join(planDir, jobToLaunch.Filename))
				launchCmd.Stdout = os.Stdout
				launchCmd.Stderr = os.Stderr
				launchCmd.Stdin = os.Stdin

				if err := launchCmd.Run(); err != nil {
					fmt.Printf("%s Error launching job: %v\n", color.RedString("✗"), err)
				}
			}

		case "s", "skip":
			// Skip the first job
			if len(runnableJobs) > 0 {
				jobToSkip := runnableJobs[0]
				fmt.Printf("\nSkipping job: %s\n", color.YellowString(jobToSkip.Title))

				// Update job status to skipped
				// Note: We might need to add a "skipped" status to the JobStatus enum
				jobToSkip.Status = orchestration.JobStatusCompleted // For now, mark as completed

				// Update the job file with the new status
				updates := map[string]interface{}{
					"status": string(jobToSkip.Status),
				}

				content, err := os.ReadFile(jobToSkip.FilePath)
				if err != nil {
					fmt.Printf("%s Error reading job file: %v\n", color.RedString("✗"), err)
					continue
				}

				newContent, err := orchestration.UpdateFrontmatter(content, updates)
				if err != nil {
					fmt.Printf("%s Error updating frontmatter: %v\n", color.RedString("✗"), err)
					continue
				}

				if err := os.WriteFile(jobToSkip.FilePath, newContent, 0644); err != nil {
					fmt.Printf("%s Error writing job file: %v\n", color.RedString("✗"), err)
					continue
				}
			}

		case "q", "quit":
			fmt.Println("\nExiting plan step mode.")
			return nil

		default:
			fmt.Printf("%s Invalid choice. Please enter R, L, S, or Q.\n", color.RedString("✗"))
			continue
		}

		// Add a small delay before next iteration to allow file system to sync
		time.Sleep(500 * time.Millisecond)
	}

	return nil
}
