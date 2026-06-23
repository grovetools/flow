package orchestration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"text/template"
)

// MarkPlanReview marks the plan in planDir as ready for review. It is the
// reusable core of the `flow review` CLI command (see flow/cmd/plan_review.go):
// it loads the plan, executes the optional on_review shell hook, flips the
// plan status to "review", and persists the change via SavePlan.
//
// The call is idempotent: if the plan is already "review" or "finished" it
// returns nil without re-running the hook. This lets non-CLI callers (e.g. the
// git-viewer review roll-up) invoke it safely without re-triggering side effects.
func MarkPlanReview(planDir string) error {
	plan, err := LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Idempotent no-op when already at or past review.
	if plan.Config != nil && (plan.Config.Status == "review" || plan.Config.Status == "finished") {
		return nil
	}

	// Find the first job with a note_ref to expose to the hook template.
	var noteRef string
	for _, job := range plan.Jobs {
		if job.NoteRef != "" {
			noteRef = job.NoteRef
			break
		}
	}

	// Execute on_review hook if it exists.
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
				return fmt.Errorf("failed to parse on_review hook template: %w", err)
			}
			var renderedCmd bytes.Buffer
			if err := tmpl.Execute(&renderedCmd, templateData); err != nil {
				return fmt.Errorf("failed to render on_review hook command: %w", err)
			}

			// Execute the command.
			hookCmd := exec.Command("sh", "-c", renderedCmd.String()) //nolint:gosec // on_review hook comes from trusted plan config
			hookCmd.Stdout = os.Stdout
			hookCmd.Stderr = os.Stderr
			if err := hookCmd.Run(); err != nil {
				return fmt.Errorf("on_review hook execution failed: %w", err)
			}
			fmt.Println("* on_review hook executed successfully.")
		}
	}

	// Update plan status to 'review' and persist.
	if plan.Config == nil {
		plan.Config = &PlanConfig{}
	}
	plan.Config.Status = "review"

	if err := SavePlan(planDir, plan); err != nil {
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
