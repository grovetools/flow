package main

import (
	"context"
	"os"

	"github.com/grovetools/tend/pkg/app"
	"github.com/grovetools/tend/pkg/harness"

	"github.com/grovetools/flow/tests/e2e/tend/scenarios"
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
		scenarios.ExplicitTargetStatusHandlingScenario,
		scenarios.StatusErrorDetailsScenario,
		scenarios.JobLogCaptureScenario,
		scenarios.TitleBasedRunScenario,
		scenarios.PlanLifecycleScenario,
		scenarios.PlanFinishEcosystemScenario,
		scenarios.SiblingWorkspacesLifecycleScenario,
		scenarios.JobManagementScenario,
		scenarios.PlanListTUIScenario,
		scenarios.PlanInitTUIScenario,
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
		scenarios.RecipeInitActionsNotebookScenario,
		scenarios.RecipeInitActionsEcosystemScenario,
		scenarios.RecipeInitActionsFailureHandlingScenario,
		scenarios.PlanDomainFilteringScenario,
		scenarios.RecipeInitFlagScenario,
		scenarios.RecipePlanActionCommandScenario,
		scenarios.HoistedCommandsScenario,
		scenarios.HoistedCommandsWithActiveJobScenario,
		scenarios.CompleteOneCmdScenario,
		scenarios.RollingPlanWorkflowScenario,
		scenarios.SessionArchivingScenario,
		scenarios.RecipeConceptUpdateScenario,
		scenarios.RecipeConceptUpdateWithPlansScenario,
		scenarios.ConceptGatheringScenario,
		scenarios.ConceptGatheringWithNotesScenario,
		// ZombieWorktreeLogRecreationScenario retired 2026-06-11 (XDG worktrees P5):
		// it asserted worktree-local logs (<worktree>/.grove/logs/*.log) that core
		// stopped writing on 2026-05-06 (e725c88 moved workspace logs to StateDir()).
		// The behavior it covered — deleted worktrees not resurrected by logging — is
		// core logging-redirect behavior, owned by the canonical, StateDir-contract
		// scenario at core/tests/e2e/scenarios_zombie_worktrees.go (plus its XDG
		// variant). Keeping a copy here would re-couple the flow suite to core's
		// log-path internals — the coupling that rotted this scenario in the first
		// place. Flow's XDG worktree coverage now lives in SiblingWorkspacesLifecycleScenario.
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

		// Rules archiving and inheritance tests
		scenarios.RulesArchivingWorkflowScenario,
		scenarios.XMLJobRulesInheritanceScenario,

		// Memory search integration into XML briefings
		scenarios.MemoryIntegrationScenario,

		// File job type tests
		scenarios.FileJobTypeScenario,
		scenarios.FileJobTypeTUIScenario,

		// Provider tests (parameterized for claude, codex, opencode)
		scenarios.ClaudeProviderLifecycleScenario,
		scenarios.ClaudeProviderArgsScenario,
		scenarios.CodexProviderLifecycleScenario,
		scenarios.CodexProviderArgsScenario,
		scenarios.OpencodeProviderLifecycleScenario,
		scenarios.OpencodeProviderArgsScenario,

		// Session registration tests (verify synchronous registration for all providers)
		scenarios.OpencodeSessionRegistrationScenario,

		// Environment provisioning lifecycle
		scenarios.EnvLifecycleScenario,
		scenarios.EnvNoConfigScenario,

		// Named environment profile tests
		scenarios.EnvNamedProfileScenario,
		scenarios.EnvInvalidProfileScenario,
		scenarios.EnvStickyDefaultScenario,

		// Graceful wait for interactive agents (verifies exit code 0 and proper messaging)
		scenarios.GracefulWaitRunNextScenario,
		scenarios.GracefulWaitSingleJobScenario,

		// Non-TTY interactive agent tests (verifies detached tmux session creation)
		scenarios.ClaudeInteractiveNonTTYScenario,
		scenarios.CodexInteractiveNonTTYScenario,
		scenarios.OpencodeInteractiveNonTTYScenario,
		scenarios.MultiJobNonTTYRegressionScenario,

		// Plan status from any directory (--dir flag and global resolution)
		scenarios.PlanStatusDirFlagScenario,
		scenarios.PlanStatusGlobalResolutionScenario,
		scenarios.PlanStatusNotFoundErrorScenario,
		scenarios.PlanStatusAmbiguousErrorScenario,
		scenarios.PlanStatusBackwardCompatibilityScenario,
		scenarios.PlanStatusDirFlagOverrideScenario,

		// Skill sequence briefing injection
		scenarios.SkillSequenceBriefingScenario,
		scenarios.NestedSkillSequenceScenario,

		// Skill flag and skill_sequence inheritance
		scenarios.SkillInheritFlagScenario,

		// Skill fidelity observability
		scenarios.SkillFidelityTrackingScenario,

		// Artifact CLI
		scenarios.ArtifactCLIScenario,

		// Playbook coverage
		scenarios.PlaybookShowScenario,
		scenarios.PlanInitPlaybookScenario,
		scenarios.PlaybookEnvInjectionScenario,
		scenarios.PlaybookOverviewXMLScenario,
		scenarios.ArtifactCompleteNonSeqScenario,
		scenarios.TemplateShimScenario,
		scenarios.PlaybookListScenario,
		scenarios.PlaybookRecipeResolutionScenario,

		// Demote job to note
		scenarios.DemoteToNoteScenario,
		scenarios.DemoteWithWorkspaceFlagScenario,
		scenarios.PromoteDemoteRoundTripScenario,
	}

	if err := app.Execute(context.TODO(), allScenarios); err != nil {
		os.Exit(1)
	}
}
