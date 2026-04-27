package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// getArtifactContext resolves the plan, job, and artifact directory from
// the GROVE_FLOW_JOB_PATH and GROVE_FLOW_JOB_ID environment variables
// that the daemon injects into agent tmux sessions.
func getArtifactContext() (plan *orchestration.Plan, job *orchestration.Job, artifactDir string, err error) {
	jobPath := os.Getenv("GROVE_FLOW_JOB_PATH")
	jobID := os.Getenv("GROVE_FLOW_JOB_ID")

	if jobPath == "" || jobID == "" {
		return nil, nil, "", fmt.Errorf("not in a job session (missing GROVE_FLOW_JOB_PATH or GROVE_FLOW_JOB_ID)")
	}

	planDir := filepath.Dir(jobPath)
	plan, err = orchestration.LoadPlan(planDir)
	if err != nil {
		return nil, nil, "", fmt.Errorf("loading plan: %w", err)
	}

	job, found := plan.GetJobByID(jobID)
	if !found {
		return nil, nil, "", fmt.Errorf("job %s not found in plan", jobID)
	}

	artifactDir = filepath.Join(planDir, ".artifacts", jobID)
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		return nil, nil, "", err
	}

	return plan, job, artifactDir, nil
}

// NewArtifactCmd returns the artifact command with write, read, list, and complete subcommands.
func NewArtifactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Manage artifacts for the current job",
		Long: `Manage artifacts for the current flow job session.

Resolves the artifact directory from GROVE_FLOW_JOB_PATH and GROVE_FLOW_JOB_ID
environment variables that are injected into agent tmux sessions.

Subcommands:
  write    - Write an artifact file
  read     - Read an artifact file
  list     - List all artifacts
  complete - Mark a skill as complete and write status`,
	}

	// WRITE
	var fileFlag, contentFlag string
	writeCmd := &cobra.Command{
		Use:   "write <filename>",
		Short: "Write an artifact to the job's artifact directory",
		Long: `Write an artifact file. Content can come from:
  --file <path>     Read content from a local file
  --content <str>   Inline content string
  stdin             Default if neither flag is provided`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, artifactDir, err := getArtifactContext()
			if err != nil {
				return err
			}

			targetPath := filepath.Join(artifactDir, args[0])

			var data []byte
			if fileFlag != "" {
				data, err = os.ReadFile(fileFlag)
				if err != nil {
					return fmt.Errorf("reading source file: %w", err)
				}
			} else if contentFlag != "" {
				data = []byte(contentFlag)
			} else {
				data, err = io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("reading stdin: %w", err)
				}
			}

			if err := os.WriteFile(targetPath, data, 0600); err != nil {
				return fmt.Errorf("writing artifact: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote artifact: %s\n", args[0])
			return nil
		},
	}
	writeCmd.Flags().StringVar(&fileFlag, "file", "", "Read content from a local file")
	writeCmd.Flags().StringVar(&contentFlag, "content", "", "Inline content string")

	// READ
	readCmd := &cobra.Command{
		Use:   "read <filename>",
		Short: "Read an artifact from the job's artifact directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, artifactDir, err := getArtifactContext()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(filepath.Join(artifactDir, args[0]))
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	}

	// LIST
	var jsonFlag bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all artifacts for the current job",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, artifactDir, err := getArtifactContext()
			if err != nil {
				return err
			}

			entries, err := os.ReadDir(artifactDir)
			if err != nil {
				return err
			}

			if jsonFlag {
				var files []string
				for _, e := range entries {
					if !e.IsDir() {
						files = append(files, e.Name())
					}
				}
				if files == nil {
					files = []string{}
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(files)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ARTIFACT\tSIZE")
			for _, e := range entries {
				if !e.IsDir() {
					info, _ := e.Info()
					fmt.Fprintf(w, "%s\t%d B\n", e.Name(), info.Size())
				}
			}
			w.Flush()
			return nil
		},
	}
	listCmd.Flags().BoolVar(&jsonFlag, "json", false, "Output as JSON array")

	// COMPLETE
	var statusFlag, errorFlag, diagFlag, feedbackFlag string
	completeCmd := &cobra.Command{
		Use:   "complete <skill-name>",
		Short: "Mark a skill as complete and write its fidelity status",
		Long: `Completes a skill in the current job's skill sequence.

Verifies that all expected artifacts (from the skill's 'produces' metadata)
exist in the artifact directory before allowing a 'completed' status.

Writes a <skill-name>-status.json file for TUI consumption.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, job, artifactDir, err := getArtifactContext()
			if err != nil {
				return err
			}
			skillName := args[0]

			if statusFlag != "completed" && statusFlag != "failed" && statusFlag != "skipped" {
				return fmt.Errorf("invalid status: %s (must be completed, failed, or skipped)", statusFlag)
			}

			// Resolve skill sequence to verify expectations, but only for
			// jobs that actually declare a skill_sequence. Ad-hoc
			// interactive_agent jobs (e.g. fixup implementers) don't have
			// a declared sequence and should be allowed to complete
			// arbitrary skill names as loose markers without sequence
			// validation. See 08-fixup-plan §4.
			var expectedArtifacts []string
			if len(job.SkillSequence) > 0 {
				workDir, _ := orchestration.DetermineWorkingDirectory(plan, job)
				sequenceNodes, err := orchestration.ResolveSkillSequenceMetadata(job.SkillSequence, workDir)
				if err != nil {
					return fmt.Errorf("resolving skills: %w", err)
				}

				targetNode := findSkillInSequence(sequenceNodes, skillName)
				if targetNode == nil {
					return fmt.Errorf("skill '%s' not found in job's skill sequence", skillName)
				}
				expectedArtifacts = targetNode.Metadata.Produces
			}

			// Verify produced artifacts if status is completed
			var produced []string
			if statusFlag == "completed" {
				for _, expected := range expectedArtifacts {
					if _, statErr := os.Stat(filepath.Join(artifactDir, expected)); os.IsNotExist(statErr) {
						return fmt.Errorf("verification failed: expected artifact '%s' was not written. Use `flow artifact write %s` first", expected, expected)
					}
					produced = append(produced, expected)
				}
			}

			// Build optional pointers for JSON output
			var errPtr, diagPtr, feedbackPtr *string
			if errorFlag != "" {
				errPtr = &errorFlag
			}
			if diagFlag != "" {
				diagPtr = &diagFlag
			}
			if feedbackFlag != "" {
				feedbackPtr = &feedbackFlag
			}

			state := orchestration.SkillFidelityState{
				Skill:             skillName,
				Status:            statusFlag,
				ArtifactsExpected: expectedArtifacts,
				ArtifactsProduced: produced,
				Error:             errPtr,
				DiagnosticPath:    diagPtr,
				Feedback:          feedbackPtr,
			}

			// Ensure empty arrays instead of null in JSON output
			if state.ArtifactsExpected == nil {
				state.ArtifactsExpected = []string{}
			}
			if state.ArtifactsProduced == nil {
				state.ArtifactsProduced = []string{}
			}

			statusPath := filepath.Join(artifactDir, fmt.Sprintf("%s-status.json", skillName))
			data, _ := json.MarshalIndent(state, "", "  ")
			if err := os.WriteFile(statusPath, data, 0600); err != nil {
				return fmt.Errorf("writing status file: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "marked skill '%s' as %s\n", skillName, statusFlag)
			return nil
		},
	}
	completeCmd.Flags().StringVar(&statusFlag, "status", "completed", "Status: completed, failed, or skipped")
	completeCmd.Flags().StringVar(&errorFlag, "error", "", "Error message (used with --status failed)")
	completeCmd.Flags().StringVar(&diagFlag, "diagnostic-file", "", "Diagnostic artifact filename (used with --status failed)")
	completeCmd.Flags().StringVar(&feedbackFlag, "feedback", "", "Brief feedback about the skill execution")

	cmd.AddCommand(writeCmd, readCmd, listCmd, completeCmd)
	return cmd
}

// findSkillInSequence recursively searches a skill sequence tree for a skill by name.
func findSkillInSequence(nodes []orchestration.SkillSequenceNode, name string) *orchestration.SkillSequenceNode {
	for i := range nodes {
		if nodes[i].Metadata.Name == name {
			return &nodes[i]
		}
		if found := findSkillInSequence(nodes[i].Children, name); found != nil {
			return found
		}
	}
	return nil
}
