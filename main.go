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

	// Add hoisted plan commands at the top level
	rootCmd.AddCommand(cmd.NewStatusCmd())
	rootCmd.AddCommand(cmd.NewListCmd())
	rootCmd.AddCommand(cmd.NewRunCmd())
	rootCmd.AddCommand(cmd.NewAddCmd())
	rootCmd.AddCommand(cmd.NewCompleteCmd())
	rootCmd.AddCommand(cmd.NewGraphCmd())
	rootCmd.AddCommand(cmd.NewStepCmd())
	rootCmd.AddCommand(cmd.NewOpenCmd())
	rootCmd.AddCommand(cmd.NewReviewCmd())
	rootCmd.AddCommand(cmd.NewFinishCmd())
	rootCmd.AddCommand(cmd.NewActionCmd())

	// Add the plan command (with all subcommands for backward compatibility)
	rootCmd.AddCommand(cmd.NewPlanCmd())

	// Add plan configuration commands at the top level
	rootCmd.AddCommand(cmd.NewSetCmd())
	rootCmd.AddCommand(cmd.NewCurrentCmd())
	rootCmd.AddCommand(cmd.NewUnsetCmd())
	rootCmd.AddCommand(cmd.NewConfigCmd())
	rootCmd.AddCommand(cmd.NewHoldCmd())
	rootCmd.AddCommand(cmd.NewUnholdCmd())
	rootCmd.AddCommand(cmd.NewResumeCmd())

	// Add other top-level commands
	rootCmd.AddCommand(cmd.GetChatCommand())
	rootCmd.AddCommand(cmd.NewVersionCmd())
	rootCmd.AddCommand(cmd.NewModelsCmd())
	rootCmd.AddCommand(cmd.NewStarshipCmd())
	rootCmd.AddCommand(cmd.GetRegisterCodexSessionCmd())
	rootCmd.AddCommand(cmd.GetRegisterOpencodeSessionCmd())
	rootCmd.AddCommand(cmd.NewTmuxCmd())
	rootCmd.AddCommand(cli.NewDocsCommand(docs.DocsJSON))

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}