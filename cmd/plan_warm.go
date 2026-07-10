package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
)

// NewPlanWarmCmd creates the `flow plan warm` command: refresh a chat's
// Anthropic prefix-cache TTL without adding a turn (oracle-plays J5). Positional
// resolution mirrors `flow plan retry` MINUS the --plan branch (addendum A5).
func NewPlanWarmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "warm [slug] <job-file>",
		Short: "Refresh a chat's Anthropic cache TTL without adding a turn",
		Long: `Ride a chat's cached prefix as a near-zero-output request so the Anthropic
prompt-cache TTL is refreshed, without appending a turn, a history block, or a
request manifest that would bust the prefix. The only thing written is a warm
receipt under the job's .artifacts dir.

Warm refuses (before any API call) when the last turn's cache identity does not
match what warm would reproduce: the chat never fired, ran with caching
disabled, ran on a non-Anthropic provider, resolves to a different model
(caches are model-scoped), or the prefix bytes have diverged since the last
turn.

Examples:
  # Warm the active plan's chat
  flow plan warm 03-design.md

  # Warm a chat in a specific plan (by slug)
  flow plan warm my-feature 03-design.md

  # Warm a chat in a plan targeted with --at
  flow plan warm --at my-feature 03-design.md

  # Emit the warm receipt as JSON
  flow plan warm 03-design.md --json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runPlanWarm,
	}
	cmd.Flags().Bool("json", false, "Output the warm receipt as JSON")
	return cmd
}

func runPlanWarm(cmd *cobra.Command, args []string) error {
	var planName, jobFile, planDir string
	jsonOut, _ := cmd.Flags().GetBool("json")

	if len(args) == 2 {
		// flow plan warm <slug> <job-file>
		planName = args[0]
		jobFile = args[1]
	} else {
		jobPath := args[0]
		if filepath.IsAbs(jobPath) || filepath.Dir(jobPath) != "." {
			// flow plan warm my-slug/my-job.md (path contains directory)
			planName = filepath.Dir(jobPath)
			jobFile = filepath.Base(jobPath)
		} else {
			// flow plan warm my-job.md (bare filename, use active plan)
			jobFile = jobPath
			if activePlan, err := getActivePlanWithMigration(); err == nil && activePlan != "" {
				planName = activePlan
			}
		}
	}

	if planName != "" {
		resolvedPath, err := resolvePlanPathCtx(cmd.Context(), planName, ".")
		if err != nil {
			// If resolution fails, try using planName directly as a path.
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
	// Resolve dependencies so lineage/context attachments are populated.
	if err := plan.ResolveDependencies(); err != nil {
		return fmt.Errorf("resolve dependencies: %w", err)
	}

	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	result, err := orchestration.WarmChatCache(cmd.Context(), job, plan)
	if err != nil {
		return err
	}

	if jsonOut {
		data, mErr := json.MarshalIndent(result.Receipt, "", "  ")
		if mErr != nil {
			return mErr
		}
		fmt.Println(string(data))
		return nil
	}

	if result.Mock {
		fmt.Printf("%s Warm (mock): parity OK — receipt %s\n",
			color.GreenString(theme.IconSuccess), result.ReceiptPath)
		return nil
	}

	r := result.Receipt
	fmt.Printf("%s Warmed %s: cache_read=%s cache_write(5m/1h)=%s/%s cost=$%.4f\n",
		color.GreenString(theme.IconSuccess), job.Filename,
		orchestration.FormatTokenCount(r.CacheRead),
		orchestration.FormatTokenCount(r.CacheWrite5m),
		orchestration.FormatTokenCount(r.CacheWrite1h),
		r.CostUSD)
	if r.CacheRead == 0 && (r.CacheWrite5m > 0 || r.CacheWrite1h > 0) {
		fmt.Printf("%s No cache read — the prefix was cold-written (its TTL had already lapsed)\n",
			color.YellowString(theme.IconWarning))
	}
	return nil
}
