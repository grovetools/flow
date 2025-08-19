package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// NewModelsCmd creates the flow models command.
func NewModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List available LLM models for use in jobs and chats",
		Long: `Lists recommended LLM models that can be used in job and chat frontmatter.
		
While other models supported by the 'llm' tool may work, these are the primary models tested with Grove Flow.`,
		RunE: runModelsList,
	}
	return cmd
}

func runModelsList(cmd *cobra.Command, args []string) error {
	models := []struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Note     string `json:"note"`
	}{
		{"gemini-2.5-pro", "Google", "Latest Gemini Pro model"},
		{"gemini-2.5-flash", "Google", "Fast, efficient model"},
		{"gemini-2.0-flash", "Google", "Previous generation flash model"},
		{"claude-4-sonnet", "Anthropic", "Claude 4 Sonnet"},
		{"claude-4-opus", "Anthropic", "Claude 4 Opus - most capable"},
		{"claude-3-haiku", "Anthropic", "Fast, lightweight model"},
	}

	// Check if JSON output is requested via global flag
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")
	
	if jsonOutput {
		// JSON output
		output := struct {
			Models []struct {
				ID       string `json:"id"`
				Provider string `json:"provider"`
				Note     string `json:"note"`
			} `json:"models"`
		}{
			Models: models,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}

	// Table output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "MODEL ID\tPROVIDER\tNOTE")
	fmt.Fprintln(w, "--------\t--------\t----")
	for _, model := range models {
		fmt.Fprintf(w, "%s\t%s\t%s\n", model.ID, model.Provider, model.Note)
	}
	w.Flush()
	
	fmt.Println("\nUsage: Specify the model in your job or chat frontmatter:")
	fmt.Println("  model: gemini-2.5-pro")
	
	return nil
}