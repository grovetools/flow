package main

import (
	"os"
	"github.com/mattsolo1/grove-core/cli"
	"github.com/grovepm/grove-flow/cmd"
)

func main() {
	rootCmd := cli.NewStandardCommand(
		"flow",
		"Job orchestration and workflows",
	)

	// Add the plan (formerly jobs) and chat commands
	rootCmd.AddCommand(cmd.GetPlanCommand())
	rootCmd.AddCommand(cmd.GetChatCommand())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}