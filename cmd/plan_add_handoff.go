package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"github.com/grovetools/flow/pkg/orchestration"
)

// registerHandoffAddFlags wires the coordinator-handoff flags onto every
// surface that creates a job (`flow plan add`, `flow add`, `flow job add`).
// They share one variable set because they share one RunE.
func registerHandoffAddFlags(flags *pflag.FlagSet) {
	flags.StringVar(&planAddCoordMode, "coord-mode", "", "Coordinator autonomy for agent jobs: manual (default) or autonomous (the agent hands off to a successor job when its context window nears the handoff threshold)")
	flags.StringVar(&planAddHandoffFrom, "handoff-from", "", "Predecessor Flow job ID this job continues from (coordinator handoff lineage)")
	flags.IntVar(&planAddHandoffDepth, "handoff-depth", 0, "Position of this job in its handoff chain (0 = original coordinator)")
	flags.IntVar(&planAddHandoffMax, "handoff-max", 0, "Upper bound on chained coordinator handoffs (0 = flow.handoff_max, default 3)")
	flags.IntVar(&planAddHandoffThreshold, "handoff-threshold", 0, "Context-window usage percent that arms an autonomous handoff (0 = flow.handoff_threshold, default 80)")
}

// applyHandoffFields stamps and validates the coordinator-handoff frontmatter
// of a job being added (see pkg/orchestration/job_handoff.go).
//
// `flow plan add` is the single gate every successor in a handoff chain must
// pass through — the grove-pi `flow_handoff` tool creates its successor by
// shelling out to it — so this is where the chain bound is enforced. An agent
// that miscounts, or a hand-edited job file that claims a deeper chain than the
// budget allows, is refused here rather than at launch.
func applyHandoffFields(cmd *PlanAddStepCmd, plan *orchestration.Plan, job *orchestration.Job) error {
	if mode := strings.TrimSpace(cmd.CoordMode); mode != "" {
		if err := orchestration.ValidateCoordMode(mode); err != nil {
			return fmt.Errorf("invalid --coord-mode: %w", err)
		}
		job.CoordMode = mode
	}
	if from := strings.TrimSpace(cmd.HandoffFrom); from != "" {
		if _, found := plan.GetJobByID(from); !found {
			return fmt.Errorf("handoff predecessor job ID not found in this plan: %s", from)
		}
		job.HandoffFrom = from
	}
	if cmd.HandoffDepth != 0 {
		job.HandoffDepth = cmd.HandoffDepth
	}
	if cmd.HandoffMax != 0 {
		job.HandoffMax = cmd.HandoffMax
	}
	if cmd.HandoffThreshold != 0 {
		job.HandoffThreshold = cmd.HandoffThreshold
	}

	// Nothing handoff-shaped anywhere: leave the frontmatter clean.
	if job.CoordMode == "" && job.HandoffFrom == "" && job.HandoffDepth == 0 && job.HandoffMax == 0 && job.HandoffThreshold == 0 {
		return nil
	}

	// A malformed or missing flow section must not silently disable the bound;
	// EffectiveHandoffMax falls back to the built-in default for a nil config.
	cfg, err := orchestration.LoadFlowConfig()
	if err != nil {
		cfg = nil
	}

	// Materialize the effective bound onto an autonomous coordinator. The chain
	// then carries its own budget: it stays auditable in the job file and does
	// not shift under a running chain when someone edits flow config.
	if job.IsAutonomousCoordinator() && job.HandoffMax == 0 {
		job.HandoffMax = orchestration.EffectiveHandoffMax(job, cfg)
	}

	return orchestration.ValidateHandoffFields(job, cfg)
}
