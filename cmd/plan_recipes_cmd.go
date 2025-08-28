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

var planRecipesCmd = &cobra.Command{
	Use:   "recipes",
	Short: "Manage plan recipes",
}

var planRecipesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available plan recipes",
	RunE: func(cmd *cobra.Command, args []string) error {
		// For now, only built-in recipes are supported.
		// In the future, a RecipeManager would handle discovery.
		recipes, err := orchestration.ListBuiltinRecipes()
		if err != nil {
			return err
		}

		if len(recipes) == 0 {
			fmt.Println("No plan recipes found.")
			return nil
		}

		opts := cli.GetOptions(cmd)
		if opts.JSONOutput {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(recipes)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSOURCE\tDESCRIPTION")
		for _, r := range recipes {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Name, "builtin", r.Description)
		}
		w.Flush()
		return nil
	},
}

func init() {
	planRecipesCmd.AddCommand(planRecipesListCmd)
}