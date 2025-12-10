package main

import (
	"os"

	"github.com/mattsolo1/grove-core/cli"
	"github.com/mattsolo1/grove-flow/cmd"
	"github.com/mattsolo1/grove-flow/pkg/docs"
)

func main() {
	rootCmd := cli.NewStandardCommand(
		"flow",
		"Job orchestration and workflows",
	)

	// Add the plan (formerly jobs) and chat commands
	rootCmd.AddCommand(cmd.NewPlanCmd())
	rootCmd.AddCommand(cmd.GetChatCommand())
	rootCmd.AddCommand(cmd.NewVersionCmd())
	rootCmd.AddCommand(cmd.NewModelsCmd())
	rootCmd.AddCommand(cmd.NewStarshipCmd())
	rootCmd.AddCommand(cmd.GetRegisterCodexSessionCmd())
	rootCmd.AddCommand(cmd.NewTmuxCmd())
	rootCmd.AddCommand(cli.NewDocsCommand(docs.DocsJSON))

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}