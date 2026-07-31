package orchestration

import (
	"fmt"
	"strings"
)

// Coordinator handoff: the frontmatter contract behind a coordinator agent that
// ends its own Flow job and continues as a fresh successor job in the same plan
// when its context window fills up.
//
// Flow owns the *record* (which job continues which, how deep the chain is, how
// deep it may go); the agent-side grove-pi `flow_handoff` tool owns the
// *mechanism* (spec authoring, `plan add` of the successor, background launch,
// completion of the predecessor by the successor at session start). Keeping the
// bound here rather than only in the extension means a runaway chain is stopped
// at the one gate every successor must pass through — `flow plan add` — even if
// the agent side is bypassed, patched, or replaced.
const (
	// CoordModeManual is the default: a handoff happens only when the operator
	// explicitly asks for one in the session (`/handoff` in Pi).
	CoordModeManual = "manual"
	// CoordModeAutonomous lets the agent hand off on its own once context usage
	// crosses the job's handoff threshold.
	CoordModeAutonomous = "autonomous"

	// DefaultHandoffMax bounds a handoff chain when neither the job nor
	// flow config specifies one. Deliberately small: an unattended chain is a
	// budget risk, and raising it is a one-line frontmatter edit.
	DefaultHandoffMax = 3
	// MaxHandoffMax is the ceiling accepted from config or frontmatter.
	MaxHandoffMax = 20
	// DefaultHandoffThreshold is the context-usage percent that arms an
	// autonomous handoff. 80% of the window leaves room for the successor spec
	// to be written before compaction or overflow would kick in.
	DefaultHandoffThreshold = 80
)

// ValidateCoordMode checks a coord_mode value. Empty means manual.
func ValidateCoordMode(mode string) error {
	switch strings.TrimSpace(mode) {
	case "", CoordModeManual, CoordModeAutonomous:
		return nil
	default:
		return fmt.Errorf("invalid coord_mode %q: must be %s or %s", mode, CoordModeManual, CoordModeAutonomous)
	}
}

// IsAutonomousCoordinator reports whether this job's agent may hand off without
// being asked at run time.
func (j *Job) IsAutonomousCoordinator() bool {
	return strings.TrimSpace(j.CoordMode) == CoordModeAutonomous
}

// EffectiveHandoffMax resolves the chain bound for this job: job frontmatter,
// then flow config, then the built-in default. cfg may be nil.
func EffectiveHandoffMax(job *Job, cfg *FlowConfig) int {
	if job != nil && job.HandoffMax > 0 {
		return job.HandoffMax
	}
	if cfg != nil && cfg.HandoffMax > 0 {
		return cfg.HandoffMax
	}
	return DefaultHandoffMax
}

// EffectiveHandoffThreshold resolves the arming threshold (percent of the
// context window) for this job: job frontmatter, then flow config, then the
// built-in default. cfg may be nil.
func EffectiveHandoffThreshold(job *Job, cfg *FlowConfig) int {
	if job != nil && job.HandoffThreshold > 0 {
		return job.HandoffThreshold
	}
	if cfg != nil && cfg.HandoffThreshold > 0 {
		return cfg.HandoffThreshold
	}
	return DefaultHandoffThreshold
}

// ValidateHandoffFields checks a job's handoff frontmatter for internal
// consistency. It is deliberately independent of the plan: plan-level checks
// (does handoff_from name a real job?) belong to the creation path, which has
// the plan in hand.
//
// The depth-vs-max check is the budget gate. A successor is created with
// handoff_depth = predecessor depth + 1, so refusing depth > max here is what
// makes an exhausted budget a hard stop rather than a suggestion.
func ValidateHandoffFields(job *Job, cfg *FlowConfig) error {
	if job == nil {
		return nil
	}
	if err := ValidateCoordMode(job.CoordMode); err != nil {
		return err
	}
	if job.HandoffDepth < 0 {
		return fmt.Errorf("invalid handoff_depth %d: must not be negative", job.HandoffDepth)
	}
	if job.HandoffMax < 0 || job.HandoffMax > MaxHandoffMax {
		return fmt.Errorf("invalid handoff_max %d: must be between 0 (use the default) and %d", job.HandoffMax, MaxHandoffMax)
	}
	if job.HandoffThreshold < 0 || job.HandoffThreshold > 99 {
		return fmt.Errorf("invalid handoff_threshold %d: must be between 0 (use the default) and 99 percent", job.HandoffThreshold)
	}
	if max := EffectiveHandoffMax(job, cfg); job.HandoffDepth > max {
		return fmt.Errorf("handoff budget exhausted: handoff_depth %d exceeds handoff_max %d; raise handoff_max on the chain or finish the work in this job", job.HandoffDepth, max)
	}
	if job.HandoffFrom != "" && job.HandoffDepth == 0 {
		return fmt.Errorf("handoff_from %q requires a handoff_depth of at least 1", job.HandoffFrom)
	}
	return nil
}

// HandoffBudgetRemaining reports how many further successors this job's chain
// may still create. cfg may be nil.
func HandoffBudgetRemaining(job *Job, cfg *FlowConfig) int {
	if job == nil {
		return 0
	}
	remaining := EffectiveHandoffMax(job, cfg) - job.HandoffDepth
	if remaining < 0 {
		return 0
	}
	return remaining
}
