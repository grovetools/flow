package main

import (
	"os"

	"github.com/mattsolo1/grove-tend/pkg/app"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-flow/tests/e2e/tend/scenarios"
)

func main() {
	allScenarios := []*harness.Scenario{
		scenarios.AbandonedJobsScenario,
		scenarios.AgentFromChatScenario,
		scenarios.AgentLogViewerScenario,
		scenarios.BriefingFilesScenario,
		scenarios.CoreOrchestrationScenario,
		scenarios.DependencyWorkflowScenario,
		scenarios.OneshotWithContextScenario,
		scenarios.AgentWorktreeLifecycleScenario,
		scenarios.ChatAndExtractWorkflowScenario,
		scenarios.JobFailureAndRecoveryScenario,
		scenarios.FailedJobRerunnableScenario,
		scenarios.PlanLifecycleScenario,
		scenarios.PlanFinishEcosystemScenario,
		scenarios.JobManagementScenario,
		scenarios.PlanListTUIScenario,
		scenarios.PlanStatusTUIScenario,
		scenarios.PlanStatusTUIAbandonedScenario,
		scenarios.PlanStatusTUIFocusSwitchingScenario,
		scenarios.PlanStatusTUILayoutToggleScenario,
		scenarios.PlanStatusTUILogViewToggleScenario,
		scenarios.PlanStatusTUIOnlyScenario,
		scenarios.PlanStatusTUIColumnToggleScenario,
		scenarios.PlanStatusTUIColumnPersistenceScenario,
		scenarios.PlanStatusTUILayoutPersistenceScenario,
		scenarios.PlanFromNoteScenario,
		scenarios.PlanMergeUpdateWorktreeScenario,
		scenarios.StandardFeatureRecipeScenario,
		scenarios.PlanAddTemplateScenario,
		scenarios.RecipeInitActionsShellScenario,
		scenarios.RecipeInitActionsDockerComposeScenario,
		scenarios.RecipeInitActionsNotebookScenario,
		// scenarios.RecipeInitActionsEcosystemScenario,
		scenarios.RecipeInitActionsFailureHandlingScenario,
		scenarios.PlanDomainFilteringScenario,
		scenarios.RecipeInitFlagScenario,
		scenarios.RecipePlanActionCommandScenario,
		scenarios.RecipeDockerComposePortRemovalScenario,
		scenarios.HoistedCommandsScenario,
		scenarios.HoistedCommandsWithActiveJobScenario,
	}

	if err := app.Execute(nil, allScenarios); err != nil {
		os.Exit(1)
	}
}
