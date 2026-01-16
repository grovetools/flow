package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	geminimodels "github.com/grovetools/grove-gemini/pkg/models"
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
	// Build combined model list from provider packages
	type displayModel struct {
		ID       string `json:"id"`
		Alias    string `json:"alias,omitempty"`
		Provider string `json:"provider"`
		Note     string `json:"note"`
	}

	var models []displayModel

	// Add Gemini models (no aliases - IDs are already short)
	for _, m := range geminimodels.Models() {
		models = append(models, displayModel{
			ID:       m.ID,
			Alias:    m.Alias,
			Provider: m.Provider,
			Note:     m.Note,
		})
	}

	// Add Anthropic models
	for _, m := range anthropicmodels.Models() {
		models = append(models, displayModel{
			ID:       m.ID,
			Alias:    m.Alias,
			Provider: m.Provider,
			Note:     m.Note,
		})
	}

	// Check if JSON output is requested via global flag
	jsonOutput, _ := cmd.Root().PersistentFlags().GetBool("json")

	if jsonOutput {
		// JSON output
		output := struct {
			Models []displayModel `json:"models"`
		}{
			Models: models,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}

	// Table output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ALIAS\tMODEL ID\tPROVIDER\tNOTE")
	fmt.Fprintln(w, "-----\t--------\t--------\t----")
	for _, model := range models {
		alias := model.Alias
		if alias == "" {
			alias = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", alias, model.ID, model.Provider, model.Note)
	}
	w.Flush()

	fmt.Println("\nUsage: Specify the model in your job or chat frontmatter:")
	fmt.Println("  model: claude-sonnet-4-5    # uses alias")
	fmt.Println("  model: gemini-2.5-pro       # full ID")

	return nil
}
