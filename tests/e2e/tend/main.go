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
		scenarios.JobLogCaptureScenario,
		scenarios.PlanLifecycleScenario,
		scenarios.PlanFinishEcosystemScenario,
		scenarios.JobManagementScenario,
		scenarios.PlanListTUIScenario,
		scenarios.PlanReviewNoWorktreeScenario,
		scenarios.PlanReviewTUIActionScenario,
		scenarios.PlanStatusTUIScenario,
		scenarios.PlanStatusTUIAbandonedScenario,
		scenarios.PlanStatusTUIFocusSwitchingScenario,
		scenarios.PlanStatusTUILayoutToggleScenario,
		scenarios.PlanStatusTUILogViewToggleScenario,
		scenarios.PlanStatusTUIOnlyScenario,
		scenarios.PlanStatusTUIColumnToggleScenario,
		scenarios.PlanStatusTUIColumnPersistenceScenario,
		scenarios.PlanStatusTUIDAGScenario,
		scenarios.PlanStatusTUILayoutLongNamesScenario,
		scenarios.PlanStatusTUILayoutPersistenceScenario,
		scenarios.PlanStatusTUIJobExecutionScenario,
		scenarios.PlanFromNoteScenario,
		scenarios.PlanMergeUpdateWorktreeScenario,
		scenarios.StandardFeatureRecipeScenario,
		scenarios.PlanAddTemplateScenario,
		scenarios.PlanAddRecipeScenario,
		scenarios.PlanAddRecipeAliasScenario,
		scenarios.PlanAddRecipeWithVariablesScenario,
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
		scenarios.SessionArchivingScenario,
		scenarios.RecipeConceptUpdateScenario,
		scenarios.RecipeConceptUpdateWithPlansScenario,
		scenarios.ConceptGatheringScenario,
		scenarios.ConceptGatheringWithNotesScenario,
		scenarios.ZombieWorktreeLogRecreationScenario,
		scenarios.PlanRecipeInheritsDefaultsScenario,
		scenarios.RecipeTemplateOverridesDefaultsScenario,
		scenarios.HoistedAddRecipeInheritsDefaultsScenario,
		scenarios.RecipeInheritsAllPropertiesScenario,
		scenarios.PlanStatusTUIMultiSelectScenario,
		scenarios.PlanStatusTUIBatchArchiveScenario,
		scenarios.PlanStatusTUIBatchSetStatusScenario,
		scenarios.PlanStatusTUIBatchXMLDepsScenario,
		scenarios.PlanStatusTUIBatchImplementDepsScenario,
		scenarios.PlanStatusTUISingleJobArchiveScenario,
		scenarios.PlanStatusTUISingleJobSetStatusScenario,
		scenarios.PlanStatusTUIBatchChangeTypeScenario,
		scenarios.PlanStatusTUIBatchChangeTemplateScenario,

		// Provider tests (parameterized for claude, codex, opencode)
		scenarios.ClaudeProviderLifecycleScenario,
		scenarios.ClaudeProviderArgsScenario,
		scenarios.CodexProviderLifecycleScenario,
		scenarios.CodexProviderArgsScenario,
		scenarios.OpencodeProviderLifecycleScenario,
		scenarios.OpencodeProviderArgsScenario,

		// Session registration tests (verify synchronous registration for all providers)
		scenarios.ClaudeSessionRegistrationScenario,
		scenarios.CodexSessionRegistrationScenario,
		scenarios.OpencodeSessionRegistrationScenario,
	}

	if err := app.Execute(nil, allScenarios); err != nil {
		os.Exit(1)
	}
}
