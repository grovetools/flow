package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-core/cli"
	"github.com/mattsolo1/grove-core/pkg/process"
	"github.com/mattsolo1/grove-core/tui/theme"
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
		// Smart Redirect: If no active plan is set and the user wants the TUI,
		// redirect them to the plan list TUI instead of showing an error.
		isNoActiveJobError := strings.Contains(err.Error(), "no active job set")
		isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())

		if isNoActiveJobError && statusTUI && isTTY {
			fmt.Println("No active plan set. Launching plan browser...")
			// The runPlanTUI function handles the `flow plan tui` command.
			// By calling it here, we effectively redirect the user.
			return runPlanTUI(cmd, []string{}) // Pass empty args to tui command
		}
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// Load plan from the resolved directory
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	if len(plan.Jobs) == 0 {
		// Use plan.Name which is derived from the directory name for a clear message.
		fmt.Printf("No jobs found in plan '%s'\n", plan.Name)
		return nil
	}

	// Build dependency graph
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		return fmt.Errorf("build dependency graph: %w", err)
	}

	// Verify the status of running jobs using PID liveness checks
	// Skip verification if GROVE_SKIP_PID_CHECK is set (useful for tests)
	if os.Getenv("GROVE_SKIP_PID_CHECK") != "true" {
		VerifyRunningJobStatus(plan)
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

// VerifyRunningJobStatus checks the PID liveness for jobs marked as running.
// If a job's process is dead, its status is updated in-memory to "interrupted".
func VerifyRunningJobStatus(plan *orchestration.Plan) {
	// We use a custom "interrupted" status string for display purposes.
	// This is not persisted to disk - it's only updated in memory.
	const JobStatusInterrupted = orchestration.JobStatus("interrupted")

	for _, job := range plan.Jobs {
		if job.Status != orchestration.JobStatusRunning {
			continue
		}

		// Special handling for interactive agent jobs
		if job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeAgent {
			pid, _, err := findClaudeSessionInfo(job.ID)
			if err != nil {
				// Could not find session info (e.g., session directory deleted), mark as interrupted
				job.Status = JobStatusInterrupted
				continue
			}
			if !process.IsProcessAlive(pid) {
				// Associated Claude process is dead
				job.Status = JobStatusInterrupted
			}
		} else {
			// Original logic for other job types
			pid, err := orchestration.ReadLockFile(job.FilePath)
			if err != nil || !process.IsProcessAlive(pid) {
				// Lock file missing or process is dead, mark as interrupted.
				job.Status = JobStatusInterrupted
			}
		}
	}
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
	} else if statusCounts[orchestration.JobStatusPending] > 0 || statusCounts[orchestration.JobStatusTodo] > 0 ||
		statusCounts[orchestration.JobStatusHold] > 0 ||
		statusCounts[orchestration.JobStatusPendingUser] > 0 ||
		statusCounts[orchestration.JobStatusPendingLLM] > 0 {
		overallStatus = "Ready"
	} else if statusCounts[orchestration.JobStatusFailed] > 0 {
		overallStatus = "Failed"
	}

	fmt.Fprintf(writer, "Plan: %s\n", renderInfo(plan.Name))
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
			renderSuccess("Completed"),
			statusCounts[orchestration.JobStatusCompleted])
	}

	if statusCounts[orchestration.JobStatusRunning] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusRunning),
			renderWarning("Running"),
			statusCounts[orchestration.JobStatusRunning])
	}

	if statusCounts[orchestration.JobStatusTodo] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusTodo),
			renderMuted("Todo"),
			statusCounts[orchestration.JobStatusTodo])
	}

	if statusCounts[orchestration.JobStatusHold] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusHold),
			renderWarning("On Hold"),
			statusCounts[orchestration.JobStatusHold])
	}

	if statusCounts[orchestration.JobStatusPending] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusPending),
			renderMuted("Pending"),
			statusCounts[orchestration.JobStatusPending])
	}

	if statusCounts[orchestration.JobStatusFailed] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusFailed),
			renderError("Failed"),
			statusCounts[orchestration.JobStatusFailed])
	}

	if statusCounts[orchestration.JobStatusBlocked] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusBlocked),
			renderError("Blocked"),
			statusCounts[orchestration.JobStatusBlocked])
	}

	if statusCounts[orchestration.JobStatusPendingUser] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusPendingUser),
			renderInfo("Pending User"),
			statusCounts[orchestration.JobStatusPendingUser])
	}

	if statusCounts[orchestration.JobStatusPendingLLM] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusPendingLLM),
			renderWarning("Pending LLM"),
			statusCounts[orchestration.JobStatusPendingLLM])
	}

	if statusCounts[orchestration.JobStatusNeedsReview] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusNeedsReview),
			renderInfo("Needs Review"),
			statusCounts[orchestration.JobStatusNeedsReview])
	}

	if statusCounts[orchestration.JobStatus("interrupted")] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatus("interrupted")),
			renderError("Interrupted"),
			statusCounts[orchestration.JobStatus("interrupted")])
	}

	if statusCounts[orchestration.JobStatusAbandoned] > 0 {
		fmt.Fprintf(writer, "%s %s: %d\n",
			colorizeStatus(orchestration.JobStatusAbandoned),
			renderMuted("Abandoned"),
			statusCounts[orchestration.JobStatusAbandoned])
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
	fmt.Fprintf(writer, "%s %s\n", "📁", renderInfo(plan.Name))

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

	// Check for any missing dependencies
	var hasMissingDeps bool
	for _, dep := range job.Dependencies {
		if dep == nil {
			hasMissingDeps = true
			break
		}
	}
	if hasMissingDeps {
		jobInfo += " " + renderError("[? missing deps]")
	}

	// Check if this job has multiple dependencies and format inline
	if len(job.Dependencies) > 1 && parent != nil {
		// Find dependencies that are NOT the parent we came from
		var otherDeps []string
		for _, dep := range job.Dependencies {
			if dep != nil && dep.ID != parent.ID {
				otherDeps = append(otherDeps, dep.Filename)
			} else if dep == nil {
				otherDeps = append(otherDeps, renderError("? missing"))
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
	dependents := findAllDependents(job, plan)
	for i, dep := range dependents {
		printJobTree(w, dep, newPrefix, i == len(dependents)-1, plan, printed, job)
	}
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

// Helper functions for rendering styled text
func renderSuccess(text string) string {
	return theme.DefaultTheme.Success.Render(text)
}

func renderError(text string) string {
	return theme.DefaultTheme.Error.Render(text)
}

func renderWarning(text string) string {
	return theme.DefaultTheme.Warning.Render(text)
}

func renderInfo(text string) string {
	return theme.DefaultTheme.Info.Render(text)
}

func renderMuted(text string) string {
	return theme.DefaultTheme.Muted.Render(text)
}

// colorizeStatus returns a colored status icon.
func colorizeStatus(status orchestration.JobStatus) string {
	switch status {
	case orchestration.JobStatusCompleted:
		return theme.DefaultTheme.Success.Render(theme.IconStatusCompleted)
	case orchestration.JobStatusRunning:
		return theme.DefaultTheme.Warning.Render(theme.IconStatusRunning)
	case orchestration.JobStatusFailed:
		return theme.DefaultTheme.Error.Render(theme.IconStatusFailed)
	case orchestration.JobStatusBlocked:
		return theme.DefaultTheme.Error.Render(theme.IconStatusBlocked)
	case orchestration.JobStatusNeedsReview:
		return theme.DefaultTheme.Info.Render(theme.IconStatusNeedsReview)
	case orchestration.JobStatusPendingUser:
		return theme.DefaultTheme.Info.Render(theme.IconStatusPendingUser)
	case orchestration.JobStatusPendingLLM:
		return theme.DefaultTheme.Warning.Render(theme.IconHeadlessAgent)
	case "interrupted": // Jobs that were running but process is dead
		return theme.DefaultTheme.Error.Render(theme.IconStatusInterrupted)
	case orchestration.JobStatusTodo:
		return theme.DefaultTheme.Info.Render(theme.IconStatusTodo)
	case orchestration.JobStatusHold:
		return theme.DefaultTheme.Warning.Render(theme.IconStatusHold)
	case orchestration.JobStatusAbandoned:
		return theme.DefaultTheme.Muted.Render(theme.IconStatusAbandoned)
	default: // Pending
		return theme.IconPending
	}
}
