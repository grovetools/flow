package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mattsolo1/grove-core/cli"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// planJobsCmd is the parent command for job-related operations
var planJobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "Manage job types and definitions",
	Long:  `Manage job types and definitions for orchestration plans.`,
}

// planJobsListCmd lists available job types
var planJobsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available job types",
	Long:  `Display all available job types for orchestration plans.`,
	Args:  cobra.NoArgs,
	RunE:  runPlanJobsList,
}

type jobTypeInfo struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func NewPlanJobsCmd() *cobra.Command {
	// Add subcommands
	planJobsCmd.AddCommand(planJobsListCmd)
	return planJobsCmd
}

func runPlanJobsList(cmd *cobra.Command, args []string) error {
	jobTypes := []jobTypeInfo{
		{
			Type:        string(orchestration.JobTypeOneshot),
			Description: "Single execution job that runs once",
		},
		{
			Type:        string(orchestration.JobTypeAgent),
			Description: "Autonomous agent job for complex tasks",
		},
		{
			Type:        string(orchestration.JobTypeHeadlessAgent),
			Description: "Headless agent job without user interaction",
		},
		{
			Type:        string(orchestration.JobTypeShell),
			Description: "Shell command execution job",
		},
		{
			Type:        string(orchestration.JobTypeChat),
			Description: "Interactive chat session job",
		},
		{
			Type:        string(orchestration.JobTypeInteractiveAgent),
			Description: "Interactive agent job with user input",
		},
		{
			Type:        string(orchestration.JobTypeGenerateRecipe),
			Description: "Recipe generation job for automation",
		},
	}

	// Check if JSON output is requested
	opts := cli.GetOptions(cmd)
	if opts.JSONOutput {
		return outputJSON(jobTypes)
	}

	// Default to table output
	return outputTable(jobTypes)
}

func outputJSON(jobTypes []jobTypeInfo) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(jobTypes); err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}
	return nil
}

func outputTable(jobTypes []jobTypeInfo) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	// Print header
	fmt.Fprintln(w, "TYPE\tDESCRIPTION")
	fmt.Fprintln(w, "----\t-----------")

	// Print job types
	for _, jt := range jobTypes {
		fmt.Fprintf(w, "%s\t%s\n", jt.Type, jt.Description)
	}

	return nil
}