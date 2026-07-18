package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

var planCommitsCmd = &cobra.Command{
	Use:   "commits [slug] <job-file>",
	Short: "Show the commits a job produced, per repo",
	Long: `Show the per-repo commit ranges recorded for a job (commits.json sidecar).

Flow records each worktree repo's HEAD when an agent job starts and again when
it finishes, so review tooling can diff exactly that job's work. This command
prints the recorded ranges: a table by default, the raw sidecar with --json.

Exits non-zero when the job has no commit record (job never ran, predates the
feature, or ran without a worktree).

Examples:
  # Show commits for a job in the active plan
  flow plan commits 09-impl-job-commits.md

  # Show commits for a job in a specific plan
  flow plan commits my-plan 09-impl-job-commits.md
  flow plan commits --at my-feature 09-impl-job-commits.md

  # Raw sidecar JSON
  flow plan commits 09-impl-job-commits.md --json`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runPlanCommits,
}

func init() {
	planCommitsCmd.Flags().StringP("plan", "p", "", "Specify the plan slug or directory")
	planCommitsCmd.Flags().Bool("json", false, "Print the raw commits.json record")
}

func runPlanCommits(cmd *cobra.Command, args []string) error {
	planFlag, _ := cmd.Flags().GetString("plan")
	asJSON, _ := cmd.Flags().GetBool("json")

	var planName, jobFile string
	if len(args) == 2 {
		planName = args[0]
		jobFile = args[1]
		if planFlag != "" {
			fmt.Fprintf(os.Stderr, "Warning: --plan flag ignored when two positional arguments are provided\n")
		}
	} else {
		if planFlag != "" {
			planName = planFlag
			jobFile = args[0]
		} else {
			jobPath := args[0]
			if filepath.IsAbs(jobPath) || filepath.Dir(jobPath) != "." {
				planName = filepath.Dir(jobPath)
				jobFile = filepath.Base(jobPath)
			} else {
				jobFile = jobPath
				if activePlan, err := getActivePlanWithMigration(); err == nil && activePlan != "" {
					planName = activePlan
				}
			}
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
		return fmt.Errorf("load plan: %w", err)
	}

	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	rec, err := orchestration.ReadJobCommits(plan, job)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no commit record for job %s (expected %s) — the job has not run with commit capture, or ran without a worktree", job.Filename, orchestration.JobCommitsPath(plan, job))
		}
		return fmt.Errorf("read commit record: %w", err)
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
	fmt.Fprintf(out, "Job:      %s (%s)\n", rec.JobFile, rec.JobID)
	fmt.Fprintf(out, "Worktree: %s\n", rec.Worktree)
	fmt.Fprintf(out, "Started:  %s\n", rec.StartedAt)
	if rec.FinishedAt != "" {
		fmt.Fprintf(out, "Finished: %s\n", rec.FinishedAt)
	} else {
		fmt.Fprintf(out, "Finished: (not finalized — job may still be running)\n")
	}
	fmt.Fprintln(out)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPO\tBRANCH\tCOMMITS\tSHAS\tDIRTY")
	for _, repo := range rec.Repos {
		count := "-"
		shas := "-"
		if repo.Commits != nil {
			count = fmt.Sprintf("%d", len(repo.Commits))
			if len(repo.Commits) > 0 {
				abbrev := make([]string, len(repo.Commits))
				for i, sha := range repo.Commits {
					if len(sha) > 7 {
						sha = sha[:7]
					}
					abbrev[i] = sha
				}
				shas = strings.Join(abbrev, ",")
			}
		} else if repo.Note != "" {
			shas = "(" + repo.Note + ")"
		}
		dirty := "clean"
		if repo.DirtyAtEnd {
			dirty = "dirty"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", repo.Name, repo.Branch, count, shas, dirty)
	}
	return w.Flush()
}
