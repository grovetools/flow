package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/grovetools/flow/pkg/tui/view"
	"github.com/spf13/cobra"
)

// NewKeysCmd prints the key contract used when Flow is hosted inside another
// terminal application.
func NewKeysCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Show hosted-app key bindings",
		Long: `Show Flow's hosted-app key bindings.

The output identifies keys swallowed by Flow's view host and advisory
collision hints for outer hosts. Use --json for the stable machine-readable
contract.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ref := view.HostedKeys()
			if jsonOutput {
				return writeHostedKeysJSON(cmd.OutOrStdout(), ref)
			}
			return writeHostedKeysHuman(cmd.OutOrStdout(), ref)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the hosted-app key contract as JSON")
	return cmd
}

func writeHostedKeysJSON(w io.Writer, ref view.HostedKeyReference) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(ref)
}

func writeHostedKeysHuman(w io.Writer, ref view.HostedKeyReference) error {
	if _, err := fmt.Fprintf(w, "%s hosted keys (schema %d)\n", ref.App, ref.SchemaVersion); err != nil {
		return err
	}
	for _, binding := range ref.Bindings {
		collisionHints := "-"
		if len(binding.CollisionHints) > 0 {
			collisionHints = strings.Join(binding.CollisionHints, ", ")
		}
		configKey := "-"
		if binding.ConfigKey != "" {
			configKey = binding.ConfigKey
		}
		if _, err := fmt.Fprintf(w,
			"- %s/%s\n  keys: %s\n  description: %s\n  config-key: %s\n  host-swallowed: %t\n  collision-hints: %s\n",
			binding.Scope, binding.Action, strings.Join(binding.Keys, ", "), binding.Description,
			configKey, binding.HostSwallowed, collisionHints,
		); err != nil {
			return err
		}
	}
	return nil
}
