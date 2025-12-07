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

var planTemplatesPrintWithFrontmatter bool

var planTemplatesPrintCmd = &cobra.Command{
	Use:   "print <template-name>",
	Short: "Print template contents for agent injection",
	Long: `Print the contents of a job template.

This is useful for injecting template instructions into a running agent session.
By default, only the template body is printed. Use --frontmatter to include the YAML frontmatter.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		templateName := args[0]

		manager := orchestration.NewTemplateManager()
		template, err := manager.FindTemplate(templateName)
		if err != nil {
			return fmt.Errorf("template '%s' not found", templateName)
		}

		// Print frontmatter if requested
		if planTemplatesPrintWithFrontmatter {
			fmt.Println("---")
			// Print frontmatter as YAML
			if len(template.Frontmatter) > 0 {
				for key, value := range template.Frontmatter {
					switch v := value.(type) {
					case string:
						fmt.Printf("%s: %q\n", key, v)
					case map[string]interface{}:
						fmt.Printf("%s:\n", key)
						for k2, v2 := range v {
							fmt.Printf("  %s: %v\n", k2, v2)
						}
					default:
						fmt.Printf("%s: %v\n", key, v)
					}
				}
			}
			fmt.Println("---")
			fmt.Println()
		}

		// Always print the template body
		fmt.Print(template.Prompt)

		return nil
	},
}

// This command is now registered in plan.go GetPlanCommand function
