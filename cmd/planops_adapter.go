package cmd

import (
	"context"
	"fmt"
	"os"
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
	printReceiptOutcome(result.Receipt)
	if result.Failed() {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

// printReceiptOutcome reports the landing receipt. A failed receipt is printed
// loudly on stderr: the land itself already succeeded, so the only thing left to
// do about it is make sure nobody believes the provenance was recorded.
func printReceiptOutcome(receipt *planops.ReceiptOutcome) {
	if receipt == nil {
		return
	}
	for _, warning := range receipt.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: landing receipt incomplete: %s\n", warning)
	}
	switch {
	case receipt.Error != "":
		fmt.Fprintf(os.Stderr, "WARNING: the land succeeded but its landing receipt was NOT written: %s\n", receipt.Error)
	case receipt.Path != "":
		fmt.Printf("[RECEIPT] %s\n", receipt.Path)
	}
}
