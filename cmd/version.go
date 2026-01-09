package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/version"
	"github.com/spf13/cobra"
)

var versionUlog = grovelogging.NewUnifiedLogger("grove-flow.version")

func NewVersionCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version information for this binary",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			info := version.GetInfo()

			if jsonOutput {
				jsonData, err := json.MarshalIndent(info, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal version info to JSON: %w", err)
				}
				versionUlog.Info("Version information").
					Field("version", info.Version).
					Field("commit", info.Commit).
					Field("build_date", info.BuildDate).
					Pretty(string(jsonData)).
					PrettyOnly().
					Log(ctx)
			} else {
				versionUlog.Info("Version information").
					Field("version", info.Version).
					Field("commit", info.Commit).
					Field("build_date", info.BuildDate).
					Pretty(info.String()).
					PrettyOnly().
					Log(ctx)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output version information in JSON format")

	return cmd
}
