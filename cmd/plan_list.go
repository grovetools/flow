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
	"github.com/mattsolo1/grove-core/config"
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
the notebooks configuration. With --all-workspaces, it discovers all projects and scans for plans within them.`,
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
	// Get current workspace node
	node, err := workspace.GetProjectByPath(".")
	if err != nil {
		return nil, fmt.Errorf("could not determine current workspace: %w", err)
	}

	// Load config and initialize NotebookLocator
	coreCfg, err := config.LoadDefault()
	if err != nil {
		coreCfg = &config.Config{}
	}
	locator := workspace.NewNotebookLocator(coreCfg)

	// Check for deprecated config and use it as fallback
	flowCfg, _ := loadFlowConfig()
	if flowCfg != nil && flowCfg.PlansDirectory != "" {
		fmt.Fprintln(os.Stderr, "⚠️  Warning: The 'flow.plans_directory' config is deprecated. Please configure 'notebook.root_dir' in your global grove.yml instead.")
		// Use deprecated config as fallback
		plansDir, err := expandFlowPath(flowCfg.PlansDirectory)
		if err != nil {
			return nil, fmt.Errorf("could not expand plans_directory path: %w", err)
		}
		return findPlansInDir(plansDir, node.Name, node.Path)
	}

	// Get plans directory for current workspace using NotebookLocator
	plansDir, err := locator.GetPlansDir(node)
	if err != nil {
		return nil, fmt.Errorf("could not resolve plans directory: %w", err)
	}

	return findPlansInDir(plansDir, node.Name, node.Path)
}

func listAllWorkspacePlans() ([]PlanSummary, error) {
	var allSummaries []PlanSummary
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Suppress discoverer's debug output

	// Discover all workspaces
	discoverer := workspace.NewDiscoveryService(logger)
	result, err := discoverer.DiscoverAll()
	if err != nil {
		return nil, fmt.Errorf("failed to discover workspaces: %w", err)
	}
	provider := workspace.NewProvider(result)

	// Load config and initialize NotebookLocator
	coreCfg, err := config.LoadDefault()
	if err != nil {
		coreCfg = &config.Config{}
	}
	locator := workspace.NewNotebookLocator(coreCfg)

	// Get all plan directories using NotebookLocator
	scannedDirs, err := locator.ScanForAllPlans(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to scan for plans: %w", err)
	}

	// Track seen plans to avoid duplicates
	seenPlans := make(map[string]bool)

	// For each plan directory, scan for plans
	for _, scannedDir := range scannedDirs {
		ownerNode := scannedDir.Owner
		planDir := scannedDir.Path

		summaries, err := findPlansInDir(planDir, ownerNode.Name, ownerNode.Path)
		if err == nil {
			for _, summary := range summaries {
				// Deduplicate by plan path
				if !seenPlans[summary.Path] {
					allSummaries = append(allSummaries, summary)
					seenPlans[summary.Path] = true
				}
			}
		}
	}

	return allSummaries, nil
}

func findPlansInDir(basePath, workspaceName, workspacePath string) ([]PlanSummary, error) {
	var summaries []PlanSummary

	// First check if basePath itself is a plan directory
	planConfigPath := filepath.Join(basePath, ".grove-plan.yml")
	if _, err := os.Stat(planConfigPath); err == nil {
		// basePath itself is a plan directory
		plan, err := orchestration.LoadPlan(basePath)
		if err != nil {
			// If loading fails, log the error for visibility
			logrus.Warnf("Could not load plan at %s: %v", basePath, err)
		} else {
			VerifyRunningJobStatus(plan)
			if !planListIncludeFinished && plan.Config != nil && plan.Config.Status == "finished" {
				// Skip finished plan
			} else {
				summary := createPlanSummary(plan, basePath)
				summary.WorkspaceName = workspaceName
				summary.WorkspacePath = workspacePath
				summaries = append(summaries, summary)
			}
		}
		// Don't scan subdirectories if basePath itself is a plan
		return summaries, nil
	}

	// basePath is not a plan itself, so scan for plan subdirectories
	entries, err := os.ReadDir(basePath)
	if err != nil {
		// If we can't read the directory, it's fine if it doesn't exist
		if os.IsNotExist(err) {
			return summaries, nil
		}
		return nil, fmt.Errorf("failed to read plans directory %s: %w", basePath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			planPath := filepath.Join(basePath, entry.Name())
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
			mdFiles, _ := filepath.Glob(filepath.Join(planPath, "*.md"))

			if _, err := os.Stat(planConfigPath); err == nil || len(mdFiles) > 0 {
				plan, err := orchestration.LoadPlan(planPath)
				if err != nil {
					// If loading fails, we should not proceed, but also not fail the whole command.
					// Log the error for visibility, especially during tests.
					logrus.Warnf("Could not load plan at %s: %v", planPath, err)
					continue
				}
				// Perform liveness check on running jobs to report accurate status
				VerifyRunningJobStatus(plan)

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
