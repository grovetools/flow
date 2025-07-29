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

	// Add the jobs command and all its subcommands
	rootCmd.AddCommand(cmd.GetJobsCommand())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}