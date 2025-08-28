package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mattsolo1/grove-core/cli"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var (
	planListVerbose      bool
	planListIncludeFinished bool
)

// PlanSummary represents a plan in the JSON output
type PlanSummary struct {
	ID         string               `json:"id"`
	Title      string               `json:"title"`
	Path       string               `json:"path"`
	Status     string               `json:"status"`
	JobCount   int                  `json:"job_count"`
	Jobs       []*orchestration.Job `json:"jobs,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
	Repository string               `json:"repository,omitempty"`
}

// newPlanListCmd creates the `plan list` command.
func newPlanListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all plans in the configured plans directory",
		Long:  `Scans the directory specified in 'flow.plans_directory' from your grove.yml and lists all orchestration plans found.`,
		RunE:  runPlanList,
	}

	cmd.Flags().BoolVarP(&planListVerbose, "verbose", "v", false, "Show detailed information including jobs in each plan")
	cmd.Flags().BoolVar(&planListIncludeFinished, "include-finished", false, "Include finished plans in the output")

	return cmd
}

func runPlanList(cmd *cobra.Command, args []string) error {
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return err
	}
	if flowCfg.PlansDirectory == "" {
		return fmt.Errorf("'flow.plans_directory' is not set in your grove.yml configuration")
	}

	basePath, err := expandPath(flowCfg.PlansDirectory)
	if err != nil {
		return fmt.Errorf("could not expand plans_directory path: %w", err)
	}

	entries, err := os.ReadDir(basePath)
	if err != nil {
		return fmt.Errorf("failed to read plans directory %s: %w", basePath, err)
	}

	var plans []*orchestration.Plan
	for _, entry := range entries {
		if entry.IsDir() {
			planPath := filepath.Join(basePath, entry.Name())
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
			mdFiles, _ := filepath.Glob(filepath.Join(planPath, "*.md"))

			// A directory is considered a plan if it has a .grove-plan.yml file or contains .md files.
			if _, err := os.Stat(planConfigPath); err == nil || len(mdFiles) > 0 {
				plan, err := orchestration.LoadPlan(planPath)
				if err == nil {
					// Filter out finished plans unless explicitly included
					if !planListIncludeFinished && plan.Config != nil && plan.Config.Status == "finished" {
						continue
					}
					// Even if there are no jobs, the plan object itself is valid
					// and should be included in the list.
					plans = append(plans, plan)
				}
			}
		}
	}

	if len(plans) == 0 {
		fmt.Printf("No plans found in %s.\n", basePath)
		return nil
	}

	// Check if JSON output is requested
	opts := cli.GetOptions(cmd)
	if opts.JSONOutput {
		return outputPlansJSON(plans)
	}

	if planListVerbose {
		// Verbose output with job details
		for i, plan := range plans {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("Plan: %s\n", plan.Name)
			fmt.Printf("Path: %s\n", plan.Directory)
			fmt.Printf("Jobs: %d\n", len(plan.Jobs))

			if len(plan.Jobs) > 0 {
				fmt.Println("  Jobs:")
				for _, job := range plan.Jobs {
					deps := ""
					if len(job.Dependencies) > 0 {
						depNames := []string{}
						for _, dep := range job.Dependencies {
							if dep != nil {
								depNames = append(depNames, dep.Filename)
							}
						}
						deps = fmt.Sprintf(" (depends on: %s)", strings.Join(depNames, ", "))
					}
					fmt.Printf("    - %s [%s] %s%s\n", job.Filename, job.Status, job.Title, deps)
				}
			}
		}
	} else {
		// Simple tabular output
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tJOBS\tSTATUS")
		for _, plan := range plans {
			statusCounts := make(map[orchestration.JobStatus]int)
			for _, job := range plan.Jobs {
				statusCounts[job.Status]++
			}

			// Build a summary string showing job status counts
			var statusParts []string
			if c := statusCounts[orchestration.JobStatusCompleted]; c > 0 {
				statusParts = append(statusParts, fmt.Sprintf("%d completed", c))
			}
			if c := statusCounts[orchestration.JobStatusRunning]; c > 0 {
				statusParts = append(statusParts, fmt.Sprintf("%d running", c))
			}
			if c := statusCounts[orchestration.JobStatusPending] + statusCounts[orchestration.JobStatusPendingUser]; c > 0 {
				statusParts = append(statusParts, fmt.Sprintf("%d pending", c))
			}
			if c := statusCounts[orchestration.JobStatusFailed]; c > 0 {
				statusParts = append(statusParts, fmt.Sprintf("%d failed", c))
			}
			if c := statusCounts[orchestration.JobStatusBlocked]; c > 0 {
				statusParts = append(statusParts, fmt.Sprintf("%d blocked", c))
			}

			status := "no jobs"
			if len(statusParts) > 0 {
				status = strings.Join(statusParts, ", ")
			}

			fmt.Fprintf(w, "%s\t%d\t%s\n", plan.Name, len(plan.Jobs), status)
		}
		w.Flush()
	}

	return nil
}

// expandPath expands ~ and git variables in a path string.
func expandPath(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not get user home directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}

	repo, branch, err := git.GetRepoInfo(".")
	if err == nil {
		path = strings.ReplaceAll(path, "${REPO}", repo)
		path = strings.ReplaceAll(path, "${BRANCH}", branch)
		path = strings.ReplaceAll(path, "{{REPO}}", repo)
		path = strings.ReplaceAll(path, "{{BRANCH}}", branch)
	}

	return filepath.Abs(path)
}

// outputPlansJSON outputs the plans in JSON format
func outputPlansJSON(plans []*orchestration.Plan) error {
	summaries := make([]PlanSummary, 0, len(plans))

	for _, plan := range plans {
		// Calculate status summary
		statusCounts := make(map[orchestration.JobStatus]int)
		for _, job := range plan.Jobs {
			statusCounts[job.Status]++
		}

		// Build a summary string showing job status counts
		var statusParts []string
		if c := statusCounts[orchestration.JobStatusCompleted]; c > 0 {
			statusParts = append(statusParts, fmt.Sprintf("%d completed", c))
		}
		if c := statusCounts[orchestration.JobStatusRunning]; c > 0 {
			statusParts = append(statusParts, fmt.Sprintf("%d running", c))
		}
		if c := statusCounts[orchestration.JobStatusPending] + statusCounts[orchestration.JobStatusPendingUser]; c > 0 {
			statusParts = append(statusParts, fmt.Sprintf("%d pending", c))
		}
		if c := statusCounts[orchestration.JobStatusFailed]; c > 0 {
			statusParts = append(statusParts, fmt.Sprintf("%d failed", c))
		}
		if c := statusCounts[orchestration.JobStatusBlocked]; c > 0 {
			statusParts = append(statusParts, fmt.Sprintf("%d blocked", c))
		}

		status := "no jobs"
		if len(statusParts) > 0 {
			status = strings.Join(statusParts, ", ")
		}

		// Get creation and modification times from the first job if available
		var createdAt, updatedAt time.Time
		if len(plan.Jobs) > 0 {
			// For now, use the first job's metadata as a proxy for plan metadata
			createdAt = plan.Jobs[0].CreatedAt
			updatedAt = plan.Jobs[0].UpdatedAt
		}

		summary := PlanSummary{
			ID:        plan.Name,
			Title:     plan.Name,
			Path:      plan.Directory,
			Status:    status,
			JobCount:  len(plan.Jobs),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		}

		// Include job details if verbose
		if planListVerbose {
			summary.Jobs = plan.Jobs
		}

		summaries = append(summaries, summary)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summaries)
}
