package cmd

import (
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

func newAgentExitedCmd() *cobra.Command {
	var jobID, planDir, attemptID string
	var exitCode int
	cmd := &cobra.Command{
		Use:    "exited",
		Short:  "Report a supervised interactive provider exit",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return orchestration.ReportInteractiveAgentExit(cmd.Context(), planDir, jobID, attemptID, exitCode)
		},
	}
	cmd.Flags().StringVar(&jobID, "job", "", "Flow job id")
	cmd.Flags().StringVar(&planDir, "plan", "", "absolute Flow plan directory")
	cmd.Flags().StringVar(&attemptID, "attempt", "", "Flow execution attempt id (empty for legacy supervisors)")
	cmd.Flags().IntVar(&exitCode, "exit-code", 0, "provider exit code")
	_ = cmd.MarkFlagRequired("job")
	_ = cmd.MarkFlagRequired("plan")
	_ = cmd.MarkFlagRequired("exit-code")
	return cmd
}
