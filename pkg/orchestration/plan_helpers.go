package orchestration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"text/template"
)

// PlanReviewOutcome reports what marking a plan for review did.
type PlanReviewOutcome struct {
	// Flipped is true when this call moved the plan into "review". False means
	// it was already at or past review and the hook did not re-run.
	Flipped bool
	// Packet reports the review packet write. Zero when no packet write was
	// attempted (see MarkPlanReview vs MarkPlanReviewWithPacket).
	Packet ReviewPacketResult
	// PacketErr is the packet failure, if any. The status flip is NEVER undone
	// because of it: the packet is a promotion of the plan's story, not a
	// precondition for reviewing it.
	PacketErr error
}

// MarkPlanReview marks the plan in planDir as ready for review. It is the
// reusable core of the `flow review` CLI command (see flow/cmd/plan_review.go):
// it loads the plan, executes the optional on_review shell hook, flips the
// plan status to "review", persists the change via SavePlan, and — when it
// actually flipped — writes the durable review packet note.
//
// The call is idempotent: if the plan is already "review" or "finished" it
// returns nil without re-running the hook. This lets non-CLI callers (e.g. the
// git-viewer review roll-up, which re-fires on every changes refresh once every
// file is reviewed) invoke it safely without re-triggering side effects — the
// packet write included, so a fully-reviewed plan does not shell out to nb on
// every refresh. The CLI verb wants the opposite (an explicit `flow plan review`
// should always refresh the packet) and calls MarkPlanReviewWithPacket.
//
// A packet failure is reported through the returned error but leaves the flip
// in place: the plan IS in review, and the caller is told the packet did not
// get written.
func MarkPlanReview(planDir string) error {
	out, err := markPlanReview(planDir, false)
	if err != nil {
		return err
	}
	return out.PacketErr
}

// MarkPlanReviewWithPacket is MarkPlanReview with the review packet ALWAYS
// refreshed — including when the plan is already in review — and the packet
// result reported so a CLI can print where it landed. This is what makes
// re-running `flow plan review` after toggling more review marks re-checkpoint
// the snapshot instead of doing nothing.
func MarkPlanReviewWithPacket(planDir string) (PlanReviewOutcome, error) {
	return markPlanReview(planDir, true)
}

func markPlanReview(planDir string, alwaysWritePacket bool) (PlanReviewOutcome, error) {
	var outcome PlanReviewOutcome

	plan, err := LoadPlan(planDir)
	if err != nil {
		return outcome, fmt.Errorf("failed to load plan: %w", err)
	}

	// Idempotent no-op when already at or past review.
	if plan.Config != nil && (plan.Config.Status == "review" || plan.Config.Status == "finished") {
		if alwaysWritePacket {
			outcome.Packet, outcome.PacketErr = writeReviewPacketForPlan(plan, planDir)
		}
		return outcome, nil
	}

	// Find the first job with a note_ref to expose to the hook template. This is
	// back-compat only: note lifecycle no longer runs through review/ (native
	// finish handling moves notes straight to completed), but legacy frozen
	// on_review hooks still reference {{.NoteRef}}, so the template var stays
	// populated. MarkPlanReview itself moves no notes.
	var noteRef string
	for _, job := range plan.Jobs {
		if job.NoteRef != "" {
			noteRef = job.NoteRef
			break
		}
	}

	// Execute on_review hook if it exists. Hook failures are non-fatal warnings:
	// a broken/stale user hook must not block the plan from entering review.
	if plan.Config != nil && plan.Config.Hooks != nil {
		if hookCmdStr, ok := plan.Config.Hooks["on_review"]; ok && hookCmdStr != "" {
			fmt.Println("▶️  Executing on_review hook...")

			// Prepare template data.
			templateData := struct {
				PlanName string
				NoteRef  string
			}{
				PlanName: plan.Name,
				NoteRef:  noteRef,
			}

			// Render the hook command.
			tmpl, err := template.New("hook").Parse(hookCmdStr)
			if err != nil {
				fmt.Printf("Warning: failed to parse on_review hook template: %v\n", err)
			} else {
				var renderedCmd bytes.Buffer
				if err := tmpl.Execute(&renderedCmd, templateData); err != nil {
					fmt.Printf("Warning: failed to render on_review hook command: %v\n", err)
				} else {
					// Execute the command.
					hookCmd := exec.Command("sh", "-c", renderedCmd.String()) //nolint:gosec // on_review hook comes from trusted plan config
					hookCmd.Stdout = os.Stdout
					hookCmd.Stderr = os.Stderr
					if err := hookCmd.Run(); err != nil {
						fmt.Printf("Warning: on_review hook execution failed: %v\n", err)
					} else {
						fmt.Println("* on_review hook executed successfully.")
					}
				}
			}
		}
	}

	// Update plan status to 'review' and persist.
	if plan.Config == nil {
		plan.Config = &PlanConfig{}
	}
	plan.Config.Status = "review"

	if err := SavePlan(planDir, plan); err != nil {
		return outcome, fmt.Errorf("failed to save plan: %w", err)
	}
	outcome.Flipped = true

	// The packet runs AFTER the flip and never gates it. Its whole purpose is
	// durability of what review found; a notebook that is unreachable must not
	// keep a reviewed plan out of review.
	outcome.Packet, outcome.PacketErr = writeReviewPacketForPlan(plan, planDir)

	return outcome, nil
}

// writeReviewPacketForPlan writes the plan's review packet, wrapping a failure
// so the message says plainly that the flip stood and the packet did not.
func writeReviewPacketForPlan(plan *Plan, planDir string) (ReviewPacketResult, error) {
	result, err := WriteReviewPacket(ReviewPacketRequest{
		PlanDir: planDir,
		Plan:    plan,
		Trigger: ReviewTriggerReview,
	})
	if err != nil {
		return result, fmt.Errorf("plan is marked for review, but its review packet could not be written: %w", err)
	}
	return result, nil
}

// SetHold sets or clears the plan-level hold flag (Config.Status == "hold"
// in .grove-plan.yml) for the plan at planPath, persisting via SavePlan. It
// is the shared write path for hold toggling (CLI `flow plan hold`/`unhold`
// semantics and the plans-browser `h` action).
//
// Semantics: hold prevents NEW runs only. The CLI run guard
// (flow/cmd/plan_run.go) and the daemon jobrunner refuse to start jobs of a
// held plan, but jobs and agents that are already running continue
// unaffected.
//
// Clearing the hold resets Status to "" only when it is currently "hold";
// any other status (e.g. "review", "finished") is left untouched.
func SetHold(planPath string, hold bool) error {
	plan, err := LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	if hold {
		if plan.Config == nil {
			plan.Config = &PlanConfig{}
		}
		plan.Config.Status = "hold"
	} else {
		if plan.Config == nil || plan.Config.Status != "hold" {
			return nil // not on hold — nothing to clear
		}
		plan.Config.Status = ""
	}

	if err := SavePlan(planPath, plan); err != nil {
		return fmt.Errorf("failed to save plan: %w", err)
	}
	return nil
}

// FindRootJobs returns jobs with no dependencies.
func FindRootJobs(plan *Plan) []*Job {
	var roots []*Job
	for _, job := range plan.Jobs {
		if len(job.Dependencies) == 0 {
			roots = append(roots, job)
		}
	}
	return roots
}

// FindAllDependents returns ALL jobs that depend on the given job (not filtered).
func FindAllDependents(job *Job, plan *Plan) []*Job {
	var dependents []*Job
	for _, candidate := range plan.Jobs {
		for _, dep := range candidate.Dependencies {
			if dep != nil && dep.ID == job.ID {
				dependents = append(dependents, candidate)
				break
			}
		}
	}
	return dependents
}
