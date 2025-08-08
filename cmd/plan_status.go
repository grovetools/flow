package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// Command flags
var (
	statusVerbose bool
	statusGraph   bool
	statusFormat  string
)

// InitPlanStatusFlags initializes the flags for the status command
func InitPlanStatusFlags() {
	planStatusCmd.Flags().BoolVarP(&statusVerbose, "verbose", "v", false, "Show detailed job information")
	planStatusCmd.Flags().BoolVarP(&statusGraph, "graph", "g", false, "Show dependency graph")
	planStatusCmd.Flags().StringVarP(&statusFormat, "format", "f", "tree", "Output format: tree, list, json")
}

// RunPlanStatus implements the status command.
func RunPlanStatus(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	// Resolve the plan path with active job support
	planPath, err := resolvePlanPathWithActiveJob(dir)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// Load plan from the resolved directory
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	if len(plan.Jobs) == 0 {
		return fmt.Errorf("no jobs found in directory: %s", dir)
	}

	// Build dependency graph
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		return fmt.Errorf("build dependency graph: %w", err)
	}

	// Generate status display based on format
	var output string
	switch statusFormat {
	case "json":
		output, err = formatStatusJSON(plan)
	case "list":
		output = formatStatusList(plan)
	case "tree":
		output = formatStatusTree(plan, graph)
	default:
		return fmt.Errorf("unknown format: %s", statusFormat)
	}

	if err != nil {
		return fmt.Errorf("format output: %w", err)
	}

	// Display summary statistics first
	fmt.Print(formatStatusSummary(plan))
	fmt.Println()

	// Display the main output
	fmt.Print(output)

	// Display dependency graph if requested
	if statusGraph {
		fmt.Println("\nDependency Graph:")
		fmt.Println(graph.ToMermaid())
	}

	return nil
}

// formatStatusSummary creates the summary statistics.
func formatStatusSummary(plan *orchestration.Plan) string {
	var buf bytes.Buffer
	writer := &buf

	// Count jobs by status
	statusCounts := make(map[orchestration.JobStatus]int)
	for _, job := range plan.Jobs {
		statusCounts[job.Status]++
	}

	// Determine overall status
	overallStatus := "Complete"
	if statusCounts[orchestration.JobStatusRunning] > 0 {
		overallStatus = "In Progress"
	} else if statusCounts[orchestration.JobStatusPending] > 0 || 
		statusCounts[orchestration.JobStatusPendingUser] > 0 || 
		statusCounts[orchestration.JobStatusPendingLLM] > 0 {
		overallStatus = "Ready"
	} else if statusCounts[orchestration.JobStatusFailed] > 0 {
		overallStatus = "Failed"
	}

	fmt.Fprintf(writer, "Plan: %s\n", color.CyanString(plan.Name))
	fmt.Fprintf(writer, "Status: %s\n", overallStatus)
	
	// Check for Grove context files
	contextFiles := []string{
		filepath.Join(plan.Directory, ".grove", "context"),
		filepath.Join(plan.Directory, "CLAUDE.md"),
	}
	var foundContext []string
	for _, cf := range contextFiles {
		if _, err := os.Stat(cf); err == nil {
			foundContext = append(foundContext, filepath.Base(cf))
		}
	}
	if len(foundContext) > 0 {
		fmt.Fprintf(writer, "Context: %s\n", strings.Join(foundContext, ", "))
	}
	fmt.Fprintln(writer)
	
	fmt.Fprintf(writer, "Jobs: %d total\n", len(plan.Jobs))
	
	if statusCounts[orchestration.JobStatusCompleted] > 0 {
		fmt.Fprintf(writer, "%s Completed: %d\n", 
			colorizeStatus(orchestration.JobStatusCompleted), 
			statusCounts[orchestration.JobStatusCompleted])
	}
	
	if statusCounts[orchestration.JobStatusRunning] > 0 {
		fmt.Fprintf(writer, "%s Running: %d\n", 
			colorizeStatus(orchestration.JobStatusRunning), 
			statusCounts[orchestration.JobStatusRunning])
	}
	
	if statusCounts[orchestration.JobStatusPending] > 0 {
		fmt.Fprintf(writer, "%s Pending: %d\n", 
			colorizeStatus(orchestration.JobStatusPending), 
			statusCounts[orchestration.JobStatusPending])
	}
	
	if statusCounts[orchestration.JobStatusFailed] > 0 {
		fmt.Fprintf(writer, "%s Failed: %d\n", 
			colorizeStatus(orchestration.JobStatusFailed), 
			statusCounts[orchestration.JobStatusFailed])
	}
	
	if statusCounts[orchestration.JobStatusBlocked] > 0 {
		fmt.Fprintf(writer, "%s Blocked: %d\n", 
			colorizeStatus(orchestration.JobStatusBlocked), 
			statusCounts[orchestration.JobStatusBlocked])
	}
	
	if statusCounts[orchestration.JobStatusPendingUser] > 0 {
		fmt.Fprintf(writer, "%s Pending User: %d\n", 
			colorizeStatus(orchestration.JobStatusPendingUser), 
			statusCounts[orchestration.JobStatusPendingUser])
	}
	
	if statusCounts[orchestration.JobStatusPendingLLM] > 0 {
		fmt.Fprintf(writer, "%s Pending LLM: %d\n", 
			colorizeStatus(orchestration.JobStatusPendingLLM), 
			statusCounts[orchestration.JobStatusPendingLLM])
	}

	return buf.String()
}

// formatStatusTree creates a tree representation of the jobs.
func formatStatusTree(plan *orchestration.Plan, graph *orchestration.DependencyGraph) string {
	var buf bytes.Buffer
	writer := &buf

	// Find root jobs (no dependencies)
	roots := findRootJobs(plan)

	// Create a map to track which jobs have been printed
	printed := make(map[string]bool)

	// Print directory header
	fmt.Fprintf(writer, "%s %s\n", "📁", color.CyanString(plan.Name))

	// Print each root and its dependents
	for i, root := range roots {
		isLast := i == len(roots)-1
		printJobTree(writer, root, "", isLast, plan, printed)
	}

	// Print any jobs not yet printed (in case of disconnected components)
	for _, job := range plan.Jobs {
		if !printed[job.ID] {
			fmt.Fprintf(writer, "└── %s %s (orphaned)\n", 
				colorizeStatus(job.Status), job.Filename)
		}
	}

	return buf.String()
}

// findRootJobs returns jobs with no dependencies.
func findRootJobs(plan *orchestration.Plan) []*orchestration.Job {
	var roots []*orchestration.Job
	for _, job := range plan.Jobs {
		if len(job.Dependencies) == 0 {
			roots = append(roots, job)
		}
	}
	return roots
}

// printJobTree recursively prints a job and its dependents.
func printJobTree(w io.Writer, job *orchestration.Job, prefix string, isLast bool, plan *orchestration.Plan, printed map[string]bool) {
	// Skip if already printed
	if printed[job.ID] {
		return
	}
	printed[job.ID] = true

	// Print current job with tree formatting
	connector := "├── "
	if isLast {
		connector = "└── "
	}

	statusIcon := colorizeStatus(job.Status)
	jobInfo := job.Filename
	if statusVerbose && job.Title != "" {
		jobInfo = fmt.Sprintf("%s (%s)", job.Filename, job.Title)
	}
	
	fmt.Fprintf(w, "%s%s%s %s\n", prefix, connector, statusIcon, jobInfo)

	// Update prefix for children
	newPrefix := prefix
	if isLast {
		newPrefix += "    "
	} else {
		newPrefix += "│   "
	}

	// Find jobs that depend on this one
	dependents := findDependents(job, plan)
	for i, dep := range dependents {
		printJobTree(w, dep, newPrefix, i == len(dependents)-1, plan, printed)
	}
}

// findDependents returns jobs that depend on the given job.
func findDependents(job *orchestration.Job, plan *orchestration.Plan) []*orchestration.Job {
	var dependents []*orchestration.Job
	for _, candidate := range plan.Jobs {
		for _, dep := range candidate.Dependencies {
			if dep != nil && dep.ID == job.ID {
				dependents = append(dependents, candidate)
				break
			}
		}
	}
	return dependents
}

// formatStatusList creates a simple list format.
func formatStatusList(plan *orchestration.Plan) string {
	var buf bytes.Buffer
	writer := &buf

	// Sort jobs by filename
	jobs := plan.GetJobsSortedByFilename()

	for _, job := range jobs {
		statusIcon := colorizeStatus(job.Status)
		
		if statusVerbose {
			fmt.Fprintf(writer, "%s %-30s %-20s %s\n", 
				statusIcon, job.Filename, job.Status, job.Title)
			if job.ID != "" {
				fmt.Fprintf(writer, "    ID: %s\n", job.ID)
			}
			if len(job.DependsOn) > 0 {
				fmt.Fprintf(writer, "    Depends on: %s\n", strings.Join(job.DependsOn, ", "))
			}
			if job.Worktree != "" {
				fmt.Fprintf(writer, "    Worktree: %s\n", job.Worktree)
			}
			fmt.Fprintln(writer)
		} else {
			fmt.Fprintf(writer, "%s %s - %s\n", statusIcon, job.Filename, job.Title)
		}
	}

	return buf.String()
}

// formatStatusJSON creates JSON output.
func formatStatusJSON(plan *orchestration.Plan) (string, error) {
	// Create a structure for JSON output
	output := struct {
		Plan   string                   `json:"plan"`
		Jobs   []*orchestration.Job     `json:"jobs"`
		Stats  map[string]int           `json:"statistics"`
	}{
		Plan: plan.Name,
		Jobs: plan.Jobs,
		Stats: make(map[string]int),
	}

	// Calculate statistics
	for _, job := range plan.Jobs {
		output.Stats[string(job.Status)]++
	}
	output.Stats["total"] = len(plan.Jobs)

	// Marshal with indentation
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// colorizeStatus returns a colored status icon.
func colorizeStatus(status orchestration.JobStatus) string {
	switch status {
	case orchestration.JobStatusCompleted:
		return color.GreenString("✓")
	case orchestration.JobStatusRunning:
		return color.YellowString("⚡")
	case orchestration.JobStatusFailed:
		return color.RedString("✗")
	case orchestration.JobStatusBlocked:
		return color.RedString("🚫")
	case orchestration.JobStatusNeedsReview:
		return color.BlueString("👁")
	case orchestration.JobStatusPendingUser:
		return color.CyanString("💬")
	case orchestration.JobStatusPendingLLM:
		return color.YellowString("🤖")
	default: // Pending
		return "⏳"
	}
}