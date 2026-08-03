package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
)

// PlanSayCmd is the testable core of `flow plan say`: it appends a user turn to
// a chat job body as the final bytes after the last marker, validating the
// marker grammar before and after the write (oracle-plays J2). The struct +
// RunPlanSay split mirrors PlanAddStepCmd/RunPlanAddStep so tests bypass cobra.
type PlanSayCmd struct {
	Slug    string // optional plan slug (first positional when two are given)
	JobFile string // chat job filename (or slug/path when a separator is present)
	File    string // --file: path to the turn text; stdin when empty
	Force   bool   // --force: extend an already-pending user turn instead of refusing
	Text    string // pre-supplied turn text; bypasses --file/stdin (used by tests)

	// Ctx carries the cobra command context so RunPlanSay can honor the unified
	// `--at` target. The kong-style path leaves this nil; resolution is nil-safe.
	Ctx context.Context
}

func (c *PlanSayCmd) Run() error {
	return RunPlanSay(c)
}

// RunPlanSay resolves the target chat job, reads the turn text, and appends it
// through the sanctioned persister writer. Status is left untouched: runPlanRun
// owns reopen/dispatch (and the append_delta stamp for completed chats).
func RunPlanSay(c *PlanSayCmd) error {
	text := c.Text
	if text == "" {
		var err error
		text, err = readSayText(c.File)
		if err != nil {
			return err
		}
	}

	planDir, jobFile := resolveSayTarget(c.Ctx, c.Slug, c.JobFile)

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	// The persister is the universal guard (mech contract A4): it validates the
	// job is a chat, the body parses, no orphan/pending precondition, refuses a
	// running/in-flight turn, writes atomically, and post-verifies the shape.
	if err := orchestration.NewStatePersister().AppendChatUserTurn(job, text, c.Force); err != nil {
		return err
	}

	fmt.Println(theme.DefaultTheme.Success.Render("*") + " Appended turn to " + jobFile)

	// A pi-session chat has a live process waiting on this file, so `say` is
	// the whole delivery verb rather than half of one: nudge the session's
	// watcher and hand the job back to `running`, since the turn is now in
	// flight rather than waiting on the user. Both steps are no-ops for every
	// other responder, so this stays one code path for all chats.
	if job.IsPiSessionResponded() {
		return deliverPiSessionTurn(plan, job, jobFile)
	}

	fmt.Println("\nNext step:")
	fmt.Printf("- Run with: flow plan run %s\n", jobFile)
	return nil
}

// deliverPiSessionTurn completes a `say` against a pi-session chat: wake the
// session, then move the job out of pending_user.
//
// Neither step may fail the command. The append already succeeded and IS the
// durable truth — the session reconciles from the chat file on its next wake or
// poll regardless — so turning a nudge failure into a non-zero exit would
// invite callers to retry an append that already landed.
func deliverPiSessionTurn(plan *orchestration.Plan, job *orchestration.Job, jobFile string) error {
	if err := orchestration.NudgePiSessionWake(plan.Directory, job, orchestration.WakeReasonSay); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not nudge the pi session's watcher: %v (the turn is in the chat file; the session will pick it up on its next reconcile)\n", err)
	}
	if job.Status == orchestration.JobStatusPendingUser || job.Status == orchestration.JobStatusCompleted {
		if err := orchestration.NewStatePersister().UpdateJobStatus(job, orchestration.JobStatusRunning); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not move %s back to running: %v\n", jobFile, err)
		}
	}

	fmt.Println("\nDelivered to the seeded pi session.")
	if alive, _ := orchestration.AgentProcessAlive(job.ID); !alive {
		fmt.Printf("%s The session for %s is not running — start it with: flow plan run %s\n",
			theme.DefaultTheme.Warning.Render("!"), jobFile, jobFile)
	}
	return nil
}

// readSayText reads the turn text from --file, or from stdin when no file is
// given. It refuses when neither is available so an empty invocation fails loud.
func readSayText(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading turn text file %s: %w", file, err)
		}
		return string(b), nil
	}
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return "", fmt.Errorf("no turn text: pass --file <path> or pipe the turn text on stdin")
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading turn text from stdin: %w", err)
	}
	return string(b), nil
}

// resolveSayTarget resolves the plan directory and job filename from the
// positional arguments. It is the runPlanRetry recipe pruned of the `--plan`
// flag branch (mech contract §4-am correction 2 / J2 addendum R5b): `say`
// registers no `--plan`; `--at` is free via SetupTargetFlag and legacyTargetRef
// bridges deprecated spellings.
func resolveSayTarget(ctx context.Context, slug, jobArg string) (planDir, jobFile string) {
	var planName string
	if slug != "" {
		// flow plan say <slug> <job-file>
		planName = slug
		jobFile = jobArg
	} else if filepath.IsAbs(jobArg) || filepath.Dir(jobArg) != "." {
		// flow plan say my-slug/my-job.md (path contains a directory)
		planName = filepath.Dir(jobArg)
		jobFile = filepath.Base(jobArg)
	} else {
		// flow plan say my-job.md (bare filename, use active plan)
		jobFile = jobArg
		if activePlan, err := getActivePlanWithMigration(); err == nil && activePlan != "" {
			planName = activePlan
		}
	}

	if planName != "" {
		if resolved, err := resolvePlanPathCtx(ctx, planName, "."); err == nil {
			planDir = resolved
		} else {
			// Resolution miss: fall back to treating planName directly as a path.
			planDir = planName
		}
	} else if unified, ok := TargetFromContext(ctx); ok && unified.PlanDir != "" {
		planDir = unified.PlanDir
	} else {
		planDir = "."
	}
	return planDir, jobFile
}

// NewPlanSayCmd builds the `flow plan say` cobra command.
func NewPlanSayCmd() *cobra.Command {
	var file string
	var force bool

	cmd := &cobra.Command{
		Use:   "say [slug] <job-file>",
		Short: "Append a user turn to a chat job body (safe programmatic edit)",
		Long: `Append a user turn to a chat job body as the final bytes after the last chat
marker, validating the marker grammar before and after the write.

The turn text comes from --file, or from stdin when no --file is given. The
job's status is left untouched: run the turn afterwards with 'flow plan run'.

For a chat already waiting on a pending user turn, say refuses unless --force is
given (which extends that same turn). A chat with a completed last response has
its marker minted automatically.

Examples:
  # Append a turn from stdin to a chat in the active plan
  echo "Please refine the design." | flow plan say my-chat.md

  # Append a turn from a file, targeting a plan by slug
  flow plan say my-project my-chat.md --file turn.md

  # Extend an already-pending user turn
  flow plan say --force my-chat.md --file more.md`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := &PlanSayCmd{
				File:  file,
				Force: force,
				Ctx:   cmd.Context(),
			}
			if len(args) == 2 {
				c.Slug = args[0]
				c.JobFile = args[1]
			} else {
				c.JobFile = args[0]
			}
			return RunPlanSay(c)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "File containing the turn text (default: stdin)")
	cmd.Flags().BoolVar(&force, "force", false, "Extend an already-pending user turn instead of refusing")
	return cmd
}
