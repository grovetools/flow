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
	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-core/cli"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// Command flags
var (
	statusVerbose bool
	statusGraph   bool
	statusFormat  string
	statusTUI     bool
)

// InitPlanStatusFlags initializes the flags for the status command
func InitPlanStatusFlags() {
	planStatusCmd.Flags().BoolVarP(&statusVerbose, "verbose", "v", false, "Show detailed job information")
	planStatusCmd.Flags().BoolVarP(&statusGraph, "graph", "g", false, "Show dependency graph")
	planStatusCmd.Flags().StringVarP(&statusFormat, "format", "f", "tree", "Output format: tree, list, json")
	planStatusCmd.Flags().BoolVarP(&statusTUI, "tui", "t", false, "Launch interactive TUI")
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

	// Launch TUI if requested
	if statusTUI {
		// Check if we're in a TTY before launching TUI
		if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
			return fmt.Errorf("could not open a new TTY: TUI mode requires an interactive terminal")
		}
		return runStatusTUI(plan, graph)
	}

	// Check if JSON output is requested via --json flag
	opts := cli.GetOptions(cmd)
	isJSONRequested := opts.JSONOutput || statusFormat == "json"

	// If --json flag is used, override the format to json
	if opts.JSONOutput && statusFormat != "json" {
		statusFormat = "json"
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

	// Only display human-readable output if not in JSON mode
	if !isJSONRequested {
		// Display summary statistics first
		fmt.Print(formatStatusSummary(plan))
		fmt.Println()
	}

	// Display the main output
	fmt.Print(output)

	// Display dependency graph if requested (but not in JSON mode)
	if statusGraph && !isJSONRequested {
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
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusCompleted),
			color.GreenString("Completed"),
			statusCounts[orchestration.JobStatusCompleted])
	}

	if statusCounts[orchestration.JobStatusRunning] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusRunning),
			color.YellowString("Running"),
			statusCounts[orchestration.JobStatusRunning])
	}

	if statusCounts[orchestration.JobStatusPending] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusPending),
			color.HiBlackString("Pending"),
			statusCounts[orchestration.JobStatusPending])
	}

	if statusCounts[orchestration.JobStatusFailed] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusFailed),
			color.RedString("Failed"),
			statusCounts[orchestration.JobStatusFailed])
	}

	if statusCounts[orchestration.JobStatusBlocked] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusBlocked),
			color.RedString("Blocked"),
			statusCounts[orchestration.JobStatusBlocked])
	}

	if statusCounts[orchestration.JobStatusPendingUser] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusPendingUser),
			color.CyanString("Pending User"),
			statusCounts[orchestration.JobStatusPendingUser])
	}

	if statusCounts[orchestration.JobStatusPendingLLM] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusPendingLLM),
			color.YellowString("Pending LLM"),
			statusCounts[orchestration.JobStatusPendingLLM])
	}

	if statusCounts[orchestration.JobStatusNeedsReview] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusNeedsReview),
			color.CyanString("Needs Review"),
			statusCounts[orchestration.JobStatusNeedsReview])
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
		printJobTree(writer, root, "", isLast, plan, printed, nil)
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
func printJobTree(w io.Writer, job *orchestration.Job, prefix string, isLast bool, plan *orchestration.Plan, printed map[string]bool, parent *orchestration.Job) {
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

	// Check if this job has multiple dependencies and format inline
	if len(job.Dependencies) > 1 && parent != nil {
		// Find dependencies that are NOT the parent we came from
		var otherDeps []string
		for _, dep := range job.Dependencies {
			if dep != nil && dep.ID != parent.ID {
				otherDeps = append(otherDeps, dep.Filename)
			}
		}
		if len(otherDeps) > 0 {
			jobInfo += fmt.Sprintf(" ⚠️  Also: %s", strings.Join(otherDeps, ", "))
		}
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
		printJobTree(w, dep, newPrefix, i == len(dependents)-1, plan, printed, job)
	}
}

// findDependents returns jobs that depend on the given job, but only if this job
// is the dependency closest to a leaf (has the fewest downstream dependents).
func findDependents(job *orchestration.Job, plan *orchestration.Plan) []*orchestration.Job {
	var dependents []*orchestration.Job
	for _, candidate := range plan.Jobs {
		// Check if candidate depends on the given job
		dependsOnThis := false
		var closestToLeafDep *orchestration.Job
		minDistance := int(^uint(0) >> 1) // Max int
		
		for _, dep := range candidate.Dependencies {
			if dep != nil {
				if dep.ID == job.ID {
					dependsOnThis = true
				}
				// Find the dependency closest to a leaf (minimum distance to leaf)
				distance := getDistanceToLeaf(dep, plan)
				if closestToLeafDep == nil || distance < minDistance {
					closestToLeafDep = dep
					minDistance = distance
				}
			}
		}
		
		// Only include this dependent if the current job is its dependency closest to a leaf
		if dependsOnThis && closestToLeafDep != nil && closestToLeafDep.ID == job.ID {
			dependents = append(dependents, candidate)
		}
	}
	return dependents
}

// getDistanceToLeaf returns the minimum distance from this job to any leaf node
// (a job with no dependents). Distance 0 means this job is a leaf.
func getDistanceToLeaf(job *orchestration.Job, plan *orchestration.Plan) int {
	// Memoization to avoid recalculation
	distanceCache := make(map[string]int)
	return getDistanceToLeafCached(job, plan, distanceCache)
}

func getDistanceToLeafCached(job *orchestration.Job, plan *orchestration.Plan, cache map[string]int) int {
	if distance, ok := cache[job.ID]; ok {
		return distance
	}
	
	dependents := findAllDependents(job, plan)
	if len(dependents) == 0 {
		// This is a leaf node
		cache[job.ID] = 0
		return 0
	}
	
	minDistance := int(^uint(0) >> 1) // Max int
	for _, dep := range dependents {
		distance := getDistanceToLeafCached(dep, plan, cache) + 1
		if distance < minDistance {
			minDistance = distance
		}
	}
	
	cache[job.ID] = minDistance
	return minDistance
}

// findAllDependents returns ALL jobs that depend on the given job (not filtered).
func findAllDependents(job *orchestration.Job, plan *orchestration.Plan) []*orchestration.Job {
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
		Plan  string               `json:"plan"`
		Jobs  []*orchestration.Job `json:"jobs"`
		Stats map[string]int       `json:"statistics"`
	}{
		Plan:  plan.Name,
		Jobs:  plan.Jobs,
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
