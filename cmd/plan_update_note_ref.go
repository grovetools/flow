package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// planUpdateNoteRefCmd is a deprecated NO-OP shim. In the old note↔plan design,
// note lifecycle was driven by generated shell hooks in .grove-plan.yml that
// called `flow plan update-note-ref <plan> <new-note-path>` to rewrite each
// job's note_ref after a note moved. That design is gone: NOTE frontmatter
// (plan_ref/plan_job) is now the source of truth and flow resolves notes by
// querying nb. note_ref is a non-load-bearing provenance hint that is never
// rewritten on move.
//
// Existing plans still have this call frozen into their .grove-plan.yml hooks,
// so the command must keep accepting the same args and exit 0 — it just does
// nothing but print a deprecation warning to stderr.
var planUpdateNoteRefCmd = &cobra.Command{
	Use:    "update-note-ref [plan] [new-note-path]",
	Short:  "Deprecated no-op (note_ref is no longer rewritten on move)",
	Long:   `Deprecated no-op. Retained only so legacy .grove-plan.yml hooks that call it do not break. Note lifecycle is now driven by native Go code keyed on note frontmatter (plan_ref/plan_job); note_ref is provenance-only and is never rewritten.`,
	Args:   cobra.ExactArgs(2),
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(os.Stderr, "Warning: 'flow plan update-note-ref' is deprecated and does nothing. Note lifecycle is now handled natively via note frontmatter (plan_ref/plan_job).")
		return nil
	},
}

func init() {
	planCmd.AddCommand(planUpdateNoteRefCmd)
}
