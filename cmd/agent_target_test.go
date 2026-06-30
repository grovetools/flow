package cmd

import (
	"context"
	"testing"

	"github.com/grovetools/core/pkg/plan"
)

// TestAgentSlugJob verifies how positional args are split into (slug, job)
// depending on whether a unified --at target is present on the context. With
// --at the plan is pinned, so only <job> is required; without it the canonical
// <slug> <job> pair is required.
func TestAgentSlugJob(t *testing.T) {
	withAt := context.WithValue(context.Background(), TargetContextKey, &plan.ResolvedTarget{PlanDir: "/some/plan"})
	bare := context.Background()

	t.Run("--at + single job arg", func(t *testing.T) {
		slug, job, err := agentSlugJob(withAt, []string{"my-job"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if slug != "" || job != "my-job" {
			t.Fatalf("got slug=%q job=%q, want slug=\"\" job=\"my-job\"", slug, job)
		}
	})

	t.Run("--at with zero args errors", func(t *testing.T) {
		if _, _, err := agentSlugJob(withAt, nil); err == nil {
			t.Fatal("expected error for missing job")
		}
	})

	t.Run("bare slug+job", func(t *testing.T) {
		slug, job, err := agentSlugJob(bare, []string{"my-plan", "my-job"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if slug != "my-plan" || job != "my-job" {
			t.Fatalf("got slug=%q job=%q, want slug=\"my-plan\" job=\"my-job\"", slug, job)
		}
	})

	t.Run("bare with one arg errors", func(t *testing.T) {
		if _, _, err := agentSlugJob(bare, []string{"only-one"}); err == nil {
			t.Fatal("expected error for missing job without --at")
		}
	})
}
