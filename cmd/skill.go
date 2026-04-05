package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// NewSkillCmd returns the top-level skill command with subcommands.
func NewSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Skill management commands",
		Long:  `Commands for working with skills in flow plans.`,
	}

	cmd.AddCommand(newSkillSequenceCmd())
	return cmd
}

func newSkillSequenceCmd() *cobra.Command {
	var (
		dirFlag    string
		inlineFlag bool
	)

	cmd := &cobra.Command{
		Use:   "sequence <skill1> [skill2 ...]",
		Short: "Generate a skill sequence execution plan",
		Long: `Resolves the given skill names and outputs an XML execution plan
that an agent can follow to work through the skills in order.

Skills can be specified as space-separated arguments or comma-separated.
If the GROVE_FLOW_JOB_ID environment variable is set (i.e., running inside
a job session), the output includes flow artifact CLI protocols. Otherwise,
a plain TODO-list protocol is used.`,
		Example: `  # Generate sequence for three skills
  flow skill sequence prep sear plate

  # Comma-separated also works
  flow skill sequence prep,sear,plate

  # Include inline skill content
  flow skill sequence prep sear plate --inline

  # Specify a custom working directory
  flow skill sequence prep sear plate --dir /path/to/project`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Support both space-separated and comma-separated skill names
			var skillNames []string
			for _, arg := range args {
				for _, name := range strings.Split(arg, ",") {
					name = strings.TrimSpace(name)
					if name != "" {
						skillNames = append(skillNames, name)
					}
				}
			}

			if len(skillNames) == 0 {
				return fmt.Errorf("at least one skill name is required")
			}

			// Resolve working directory
			workDir := orchestration.GetProjectRootSafe(dirFlag)

			// Resolve skill sequence metadata
			nodes, err := orchestration.ResolveSkillSequenceMetadata(skillNames, workDir)
			if err != nil {
				return fmt.Errorf("resolving skill sequence: %w", err)
			}

			if len(nodes) == 0 {
				return fmt.Errorf("no skills resolved")
			}

			// Check if we're in a job session for artifact instrumentation
			var artifactDir string
			jobPath := os.Getenv("GROVE_FLOW_JOB_PATH")
			jobID := os.Getenv("GROVE_FLOW_JOB_ID")
			if jobPath != "" && jobID != "" {
				planDir := filepath.Dir(jobPath)
				artifactDir = filepath.Join(planDir, ".artifacts", jobID)
			}

			// Generate the XML
			output, err := orchestration.GenerateSkillSequenceXML(nodes, artifactDir, inlineFlag, workDir)
			if err != nil {
				return fmt.Errorf("generating sequence XML: %w", err)
			}

			fmt.Print(output)
			return nil
		},
	}

	cmd.Flags().StringVarP(&dirFlag, "dir", "d", ".", "Context directory for skill resolution")
	cmd.Flags().BoolVarP(&inlineFlag, "inline", "i", true, "Include inline SKILL.md content in output")

	return cmd
}
