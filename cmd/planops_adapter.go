package cmd

import (
	"context"
	"fmt"
	"strings"

	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planops"
	"github.com/grovetools/flow/pkg/planutil"
)

func resolvePlanopsTarget(plan *orchestration.Plan) (coreplan.PlanActionTarget, error) {
	if plan == nil || plan.Config == nil || plan.Config.Worktree == "" {
		return coreplan.PlanActionTarget{}, fmt.Errorf("plan has no associated worktree")
	}
	binding := coreplan.ResolvePlanBinding(coreplan.NewPlanKey(plan.Directory), plan.Config.Worktree, false)
	provider, err := planutil.DiscoverWorkspaceProvider()
	if err != nil {
		return coreplan.PlanActionTarget{}, fmt.Errorf("discover workspaces: %w", err)
	}
	target, err := coreplan.ResolvePlanActionTarget(binding, plan.Config.Repos, provider)
	if err != nil {
		return coreplan.PlanActionTarget{}, fmt.Errorf("resolve plan action target: %w", err)
	}
	return target, nil
}

func executePlanOperation(ctx context.Context, plan *orchestration.Plan, operation planops.Operation) error {
	target, err := resolvePlanopsTarget(plan)
	if err != nil {
		return err
	}
	preview, err := planops.Preview(ctx, target, operation)
	if err != nil {
		return err
	}
	result := planops.Execute(ctx, preview)
	for _, repo := range result.Results {
		fmt.Printf("[%s] %s: %s\n", strings.ToUpper(string(repo.Outcome)), repo.Name, repo.Detail)
	}
	if result.Failed() {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}
