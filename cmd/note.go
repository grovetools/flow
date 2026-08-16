package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/flow/pkg/orchestration"
)

const semanticPlanFlagAnnotation = "grove.flow/semantic-plan-flag"

var noteActivePlanForPath = coreplan.ActivePlanForPath

// NewNoteCmd exposes flow's note↔plan orchestration seam where agents already
// look for plan operations. nb remains the owner of note creation, storage and
// frontmatter; flow only resolves plan context and delegates.
func NewNoteCmd() *cobra.Command {
	var jobFile string

	noteCmd := &cobra.Command{
		Use:   "note [title]",
		Short: "Create and manage notes linked to Flow plans",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("note title is required")
			}
			planName, planDir, err := resolveNotePlan(cmd, "")
			if err != nil {
				return err
			}
			jobFile, err = validateNoteJob(planDir, jobFile)
			if err != nil {
				return err
			}
			note, err := orchestration.CreatePlanNote(args[0], planName, jobFile)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), note.Path)
			return nil
		},
	}
	noteCmd.Flags().StringVar(&jobFile, "job", "", "Link the note to a specific job filename")

	var listPlan string
	listCmd := &cobra.Command{
		Use:         "list",
		Short:       "List notes linked to a plan",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{semanticPlanFlagAnnotation: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			planName, _, err := resolveNotePlan(cmd, listPlan)
			if err != nil {
				return err
			}
			notes, err := orchestration.PlanNotes(planName)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(notes)
		},
	}
	listCmd.Flags().StringVar(&listPlan, "plan", "", "Plan name (default: plan associated with the current worktree)")

	var linkJob string
	linkCmd := &cobra.Command{
		Use:   "link <note> <plan>",
		Short: "Link an existing note to a plan",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			planName, planDir, err := resolveNotePlan(cmd, args[1])
			if err != nil {
				return err
			}
			linkJob, err = validateNoteJob(planDir, linkJob)
			if err != nil {
				return err
			}
			return orchestration.SetNoteLink(args[0], planName, linkJob)
		},
	}
	linkCmd.Flags().StringVar(&linkJob, "job", "", "Link the note to a specific job filename")

	unlinkCmd := &cobra.Command{
		Use:   "unlink <note>",
		Short: "Remove a note's plan and job link",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return orchestration.ClearNoteLink(args[0])
		},
	}

	noteCmd.AddCommand(listCmd, linkCmd, unlinkCmd)
	return noteCmd
}

func resolveNotePlan(cmd *cobra.Command, ref string) (name, dir string, err error) {
	if ref == "" {
		if target, ok := TargetFromContext(cmd.Context()); ok && target.PlanDir != "" {
			dir = target.PlanDir
		} else {
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				return "", "", fmt.Errorf("resolve current directory: %w", cwdErr)
			}
			ref = noteActivePlanForPath(cwd)
			if ref == "" {
				return "", "", fmt.Errorf("no plan is associated with the current directory; run from a plan worktree or pass --at")
			}
		}
	}
	if dir == "" {
		dir, err = resolvePlanPathCtx(cmd.Context(), ref, ".")
		if err != nil {
			return "", "", err
		}
	}
	return filepath.Base(filepath.Clean(dir)), dir, nil
}

func validateNoteJob(planDir, job string) (string, error) {
	if job == "" {
		return "", nil
	}
	job = filepath.Base(job)
	if filepath.Ext(job) != ".md" {
		return "", fmt.Errorf("job must be a .md filename, got %q", job)
	}
	if info, err := os.Stat(filepath.Join(planDir, job)); err != nil || info.IsDir() {
		if err == nil {
			err = fmt.Errorf("is a directory")
		}
		return "", fmt.Errorf("job %s is not in plan %s: %w", job, filepath.Base(planDir), err)
	}
	return job, nil
}
