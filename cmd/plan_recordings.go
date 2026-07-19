package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

var planRecordingsCmd = &cobra.Command{
	Use:   "recordings [slug] <job-file>",
	Short: "Show the session recordings linked to a job",
	Long: `Show the session recordings (.cast files) linked to a job (recording.json sidecar).

A QA/pilot run can record its session with 'grove record' and link the cast to
its job with 'flow plan recordings add'. This command lists the linked casts
and prints the playback command for each — the recording is additive evidence
for a human reviewer, alongside the job's machine-readable artifacts.

Exits non-zero when the job has no linked recordings.

Examples:
  # Show recordings for a job in the active plan
  flow plan recordings 12-recording-sidecar.md

  # Show recordings for a job in a specific plan
  flow plan recordings --at my-feature 12-recording-sidecar.md

  # Raw sidecar JSON
  flow plan recordings 12-recording-sidecar.md --json`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runPlanRecordings,
}

var planRecordingsAddCmd = &cobra.Command{
	Use:   "add <job-file> <cast-file>",
	Short: "Link a .cast recording to a job",
	Long: `Link an existing asciicast (.cast) file to a job's recording.json sidecar.

The file is validated (asciicast v2/v3 header) and stored by reference; when it
lives under the job's artifacts dir (the recommended layout,
.artifacts/<job-id>/recordings/<name>.cast) the link is relative, so the
sidecar stays valid wherever the plan directory travels. Re-adding the same
path updates its entry instead of duplicating it.

Examples:
  # Record a QA session server-side, then link the cast to the pilot job
  grove record run --detach -o <plan>/.artifacts/<job-id>/recordings/
  # ... drive the session ... then: grove record stop
  flow plan recordings add --at <plan> 07-pilot.md <plan>/.artifacts/<job-id>/recordings/run.cast`,
	Args: cobra.ExactArgs(2),
	RunE: runPlanRecordingsAdd,
}

func init() {
	planRecordingsCmd.Flags().StringP("plan", "p", "", "Specify the plan slug or directory")
	planRecordingsCmd.Flags().Bool("json", false, "Print the raw recording.json record")

	planRecordingsAddCmd.Flags().StringP("plan", "p", "", "Specify the plan slug or directory")
	planRecordingsAddCmd.Flags().String("name", "", "Recording name (default: cast filename without .cast)")
	planRecordingsAddCmd.Flags().String("title", "", "Human title (default: the cast header's title, if any)")
	planRecordingsAddCmd.Flags().String("note", "", "Freeform note, e.g. the finding id the recording backs")

	planRecordingsCmd.AddCommand(planRecordingsAddCmd)
}

// resolvePlanJobForRecordings resolves the plan directory + job for the
// recordings commands, with the same slug/flag/active-plan fallbacks as
// 'flow plan commits'.
func resolvePlanJobForRecordings(cmd *cobra.Command, planName, jobFile string) (*orchestration.Plan, *orchestration.Job, error) {
	if planName == "" {
		if filepath.IsAbs(jobFile) || filepath.Dir(jobFile) != "." {
			planName = filepath.Dir(jobFile)
			jobFile = filepath.Base(jobFile)
		} else if activePlan, err := getActivePlanWithMigration(); err == nil && activePlan != "" {
			planName = activePlan
		}
	}

	var planDir string
	if planName != "" {
		resolvedPath, err := resolvePlanPathCtx(cmd.Context(), planName, ".")
		if err != nil {
			planDir = planName
		} else {
			planDir = resolvedPath
		}
	} else if unified, ok := TargetFromContext(cmd.Context()); ok && unified.PlanDir != "" {
		planDir = unified.PlanDir
	} else {
		planDir = "."
	}

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load plan: %w", err)
	}
	job, found := plan.GetJobByFilename(filepath.Base(jobFile))
	if !found {
		return nil, nil, fmt.Errorf("job not found: %s", jobFile)
	}
	return plan, job, nil
}

func runPlanRecordings(cmd *cobra.Command, args []string) error {
	planFlag, _ := cmd.Flags().GetString("plan")
	asJSON, _ := cmd.Flags().GetBool("json")

	planName := planFlag
	jobFile := args[0]
	if len(args) == 2 {
		planName = args[0]
		jobFile = args[1]
		if planFlag != "" {
			fmt.Fprintf(os.Stderr, "Warning: --plan flag ignored when two positional arguments are provided\n")
		}
	}

	plan, job, err := resolvePlanJobForRecordings(cmd, planName, jobFile)
	if err != nil {
		return err
	}

	rec, err := orchestration.ReadJobRecordings(plan, job)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no recordings linked to job %s (expected %s) — link one with 'flow plan recordings add'", job.Filename, orchestration.JobRecordingsPath(plan, job))
		}
		return fmt.Errorf("read recording record: %w", err)
	}

	if asJSON {
		data, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Job: %s (%s)\n\n", rec.JobFile, rec.JobID)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tFORMAT\tSIZE\tCREATED\tTITLE")
	for _, r := range rec.Recordings {
		title := r.Title
		if title == "" {
			title = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", r.Name, r.Format, r.Bytes, r.CreatedAt, title)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "\nPlay:")
	for _, r := range rec.Recordings {
		fmt.Fprintf(out, "  asciinema play %q\n", orchestration.ResolveJobRecordingPath(plan, job, r))
	}
	return nil
}

func runPlanRecordingsAdd(cmd *cobra.Command, args []string) error {
	planFlag, _ := cmd.Flags().GetString("plan")
	name, _ := cmd.Flags().GetString("name")
	title, _ := cmd.Flags().GetString("title")
	note, _ := cmd.Flags().GetString("note")

	plan, job, err := resolvePlanJobForRecordings(cmd, planFlag, args[0])
	if err != nil {
		return err
	}

	entry, err := orchestration.AddJobRecording(plan, job, args[1], name, title, note)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Linked %s (%s, %d bytes) to job %s\n", entry.Name, entry.Format, entry.Bytes, job.Filename)
	fmt.Fprintf(out, "Sidecar: %s\n", orchestration.JobRecordingsPath(plan, job))
	fmt.Fprintf(out, "Play:    asciinema play %q\n", orchestration.ResolveJobRecordingPath(plan, job, *entry))
	return nil
}
