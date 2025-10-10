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
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	planListVerbose         bool
	planListIncludeFinished bool
	planListAllWorkspaces   bool
)

// PlanSummary represents a plan in the JSON output
type PlanSummary struct {
	ID            string               `json:"id"`
	Title         string               `json:"title"`
	Path          string               `json:"path"`
	Status        string               `json:"status"`
	JobCount      int                  `json:"job_count"`
	Jobs          []*orchestration.Job `json:"jobs,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
	Repository    string               `json:"repository,omitempty"`
	WorkspaceName string               `json:"workspace_name,omitempty"`
	WorkspacePath string               `json:"workspace_path,omitempty"`
}

// newPlanListCmd creates the `plan list` command.
func newPlanListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all plans in the configured plans directory or across all workspaces",
		Long: `Scans for and lists orchestration plans. By default, it scans the directory specified in
'flow.plans_directory'. With --all-workspaces, it discovers all projects and scans for plans within them.`,
		RunE: runPlanList,
	}

	cmd.Flags().BoolVarP(&planListVerbose, "verbose", "v", false, "Show detailed information including jobs in each plan")
	cmd.Flags().BoolVar(&planListIncludeFinished, "include-finished", false, "Include finished plans in the output")
	cmd.Flags().BoolVar(&planListAllWorkspaces, "all-workspaces", false, "List plans across all discovered workspaces")

	return cmd
}

func runPlanList(cmd *cobra.Command, args []string) error {
	var summaries []PlanSummary
	var err error

	if planListAllWorkspaces {
		summaries, err = listAllWorkspacePlans()
		if err != nil {
			return err
		}
	} else {
		summaries, err = listCurrentWorkspacePlans()
		if err != nil {
			return err
		}
	}

	if len(summaries) == 0 {
		fmt.Println("No plans found.")
		return nil
	}

	// Check if JSON output is requested
	opts := cli.GetOptions(cmd)
	if opts.JSONOutput {
		return outputPlansJSON(summaries)
	}

	// Simple tabular output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if planListAllWorkspaces {
		fmt.Fprintln(w, "WORKSPACE\tPLAN NAME\tJOBS\tSTATUS")
	} else {
		fmt.Fprintln(w, "NAME\tJOBS\tSTATUS")
	}
	for _, summary := range summaries {
		if planListAllWorkspaces {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", summary.WorkspaceName, summary.Title, summary.JobCount, summary.Status)
		} else {
			fmt.Fprintf(w, "%s\t%d\t%s\n", summary.Title, summary.JobCount, summary.Status)
		}
	}
	w.Flush()

	return nil
}

func listCurrentWorkspacePlans() ([]PlanSummary, error) {
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return nil, err
	}
	if flowCfg.PlansDirectory == "" {
		return nil, fmt.Errorf("'flow.plans_directory' is not set in your grove.yml configuration")
	}

	basePath, err := expandPath(flowCfg.PlansDirectory)
	if err != nil {
		return nil, fmt.Errorf("could not expand plans_directory path: %w", err)
	}

	// Get current workspace info
	cwd, _ := os.Getwd()
	workspaceName := filepath.Base(cwd)
	workspacePath := cwd

	return findPlansInDir(basePath, workspaceName, workspacePath)
}

func listAllWorkspacePlans() ([]PlanSummary, error) {
	var allSummaries []PlanSummary
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Suppress discoverer's debug output

	discoverer := workspace.NewDiscoveryService(logger)
	result, err := discoverer.DiscoverAll()
	if err != nil {
		return nil, fmt.Errorf("failed to discover workspaces: %w", err)
	}
	projectInfos := workspace.TransformToProjectInfo(result)

	for _, proj := range projectInfos {
		// Try to load the workspace's flow config
		originalDir, _ := os.Getwd()
		os.Chdir(proj.Path)
		flowCfg, err := loadFlowConfig()
		os.Chdir(originalDir)

		if err != nil || flowCfg.PlansDirectory == "" {
			// Fall back to convention: look for a 'plans' directory
			plansDir := filepath.Join(proj.Path, "plans")
			if _, err := os.Stat(plansDir); os.IsNotExist(err) {
				continue
			}
			summaries, err := findPlansInDir(plansDir, proj.Name, proj.Path)
			if err == nil {
				allSummaries = append(allSummaries, summaries...)
			}
			continue
		}

		// Use the configured plans directory
		basePath := flowCfg.PlansDirectory
		// Handle relative paths by making them relative to proj.Path
		if !filepath.IsAbs(basePath) {
			basePath = filepath.Join(proj.Path, basePath)
		}
		// Expand ~ and git variables relative to the workspace
		if strings.HasPrefix(basePath, "~/") {
			home, _ := os.UserHomeDir()
			basePath = filepath.Join(home, basePath[2:])
		}
		// Replace git variables
		if strings.Contains(basePath, "${REPO}") || strings.Contains(basePath, "{{REPO}}") ||
		   strings.Contains(basePath, "${BRANCH}") || strings.Contains(basePath, "{{BRANCH}}") {
			os.Chdir(proj.Path)
			expandedPath, err := expandPath(flowCfg.PlansDirectory)
			os.Chdir(originalDir)
			if err == nil {
				basePath = expandedPath
			}
		}

		if _, err := os.Stat(basePath); os.IsNotExist(err) {
			continue
		}

		summaries, err := findPlansInDir(basePath, proj.Name, proj.Path)
		if err == nil {
			allSummaries = append(allSummaries, summaries...)
		}
	}
	return allSummaries, nil
}

func findPlansInDir(basePath, workspaceName, workspacePath string) ([]PlanSummary, error) {
	var summaries []PlanSummary
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory %s: %w", basePath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			planPath := filepath.Join(basePath, entry.Name())
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
			mdFiles, _ := filepath.Glob(filepath.Join(planPath, "*.md"))

			if _, err := os.Stat(planConfigPath); err == nil || len(mdFiles) > 0 {
				plan, err := orchestration.LoadPlan(planPath)
				if err == nil {
					if !planListIncludeFinished && plan.Config != nil && plan.Config.Status == "finished" {
						continue
					}
					summary := createPlanSummary(plan, planPath)
					summary.WorkspaceName = workspaceName
					summary.WorkspacePath = workspacePath
					summaries = append(summaries, summary)
				}
			}
		}
	}
	return summaries, nil
}

func createPlanSummary(plan *orchestration.Plan, expandedPath string) PlanSummary {
	// Calculate status summary
	statusCounts := make(map[orchestration.JobStatus]int)
	for _, job := range plan.Jobs {
		statusCounts[job.Status]++
	}

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

	var createdAt, updatedAt time.Time
	if len(plan.Jobs) > 0 {
		createdAt = plan.Jobs[0].CreatedAt
		updatedAt = plan.Jobs[0].UpdatedAt
	}

	summary := PlanSummary{
		ID:        plan.Name,
		Title:     plan.Name,
		Path:      expandedPath,
		Status:    status,
		JobCount:  len(plan.Jobs),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}

	if planListVerbose {
		summary.Jobs = plan.Jobs
	}

	return summary
}

// expandPath expands ~ and git variables in a path string.
func expandPath(path string) (string, error) {
	return expandFlowPath(path)
}

// outputPlansJSON outputs the plans in JSON format
func outputPlansJSON(summaries []PlanSummary) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summaries)
}
