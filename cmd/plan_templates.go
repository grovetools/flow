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

var planTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "Manage job templates",
}

var planTemplatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available job templates",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager := orchestration.NewTemplateManager()
		templates, err := manager.ListTemplates()
		if err != nil {
			return err
		}

		if len(templates) == 0 {
			fmt.Println("No job templates found.")
			return nil
		}

		// Check if JSON output is requested
		opts := cli.GetOptions(cmd)
		if opts.JSONOutput {
			// Output templates as JSON
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(templates)
		}

		// Default tabular output
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSOURCE\tDESCRIPTION")
		for _, t := range templates {
			fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, t.Source, t.Description)
		}
		w.Flush()
		return nil
	},
}

// This command is now registered in plan.go GetPlanCommand function
