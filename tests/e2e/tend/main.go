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
		scenarios.OneshotStripCommentsScenario,
		scenarios.ChatStripCommentsScenario,
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
		scenarios.AnchorRegistryScenario,
		scenarios.AnchorRegistryMultiScenario,
		scenarios.AnchorRegistryReapStubScenario,
		scenarios.ATTargetScenario,
		scenarios.CoordinatorWorkflowScenario,
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

		// Provider tests (parameterized for claude, codex, opencode, pi)
		scenarios.ClaudeProviderLifecycleScenario,
		scenarios.ClaudeProviderArgsScenario,
		scenarios.CodexProviderLifecycleScenario,
		scenarios.CodexProviderArgsScenario,
		scenarios.OpencodeProviderLifecycleScenario,
		scenarios.OpencodeProviderArgsScenario,
		scenarios.PiProviderLifecycleScenario,
		scenarios.PiProviderArgsScenario,
		scenarios.PerJobProviderOverrideScenario,
		scenarios.MixedProviderPlanScenario,

		// Provider lifecycle beyond launch: registered -> `flow agent list` ->
		// idle outcome (simulated at the hooks/registry boundary) ->
		// completion teardown + archival.
		scenarios.CodexOutcomeLifecycleScenario,
		scenarios.PiOutcomeLifecycleScenario,
		scenarios.OpencodeOutcomeLifecycleScenario,

		// Codex nested-session-layout discovery regression guard (P2).
		scenarios.CodexNestedDiscoveryScenario,

		// Headless execution mode coverage (pi supports it; codex must
		// fail fast with an actionable error).
		scenarios.PiHeadlessLaunchScenario,
		scenarios.CodexHeadlessUnsupportedScenario,

		// Session registration tests (verify synchronous registration for all providers)
		scenarios.OpencodeSessionRegistrationScenario,
		scenarios.PiSessionRegistrationScenario,

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
		scenarios.PiInteractiveNonTTYScenario,
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

		// Claude folder-trust pre-seeding (~/.claude.json hasTrustDialogAccepted)
		scenarios.ClaudeTrustSeedScenario,

		// Claude settings sync ([claude] grove.toml -> .claude/settings.local.json)
		scenarios.ClaudeSettingsSyncScenario,

		// Claude settings LAYERING — the three merge axes made observable:
		scenarios.ClaudeSettingsGroveCascadeScenario, // Axis A: grove-config cascade (override)
		scenarios.ClaudeSettingsMemberUnionScenario,  // Axis B: member-repo union (root-wins bool)
		scenarios.ClaudeSettingsNoClobberScenario,    // Axis C: additive seed + dry-run + malformed safety

		// Claude config self-protection: the protectConfig toggle that denies
		// writes to grove config files (sandbox denyWrite + permissions.deny),
		// strips on false, honors GROVE_UNLOCK_CONFIG, and seeds on ShouldSeed.
		scenarios.ClaudeSettingsSelfProtectionScenario,

		// oracle-cache-lineage (spec 19 §5, scenarios 1-23): the chat cache
		// rework's e2e surface — layer store mechanics, refresh verbs,
		// cross-job lineage, and the guards/edges — all against the mock LLM,
		// asserting on disk artifacts + request manifests + job.log.
		scenarios.OracleCacheLayer0CreationScenario,        // 1
		scenarios.OracleCacheByteImmutabilityScenario,      // 2
		scenarios.OracleCacheRulesWideningScenario,         // 3
		scenarios.OracleCacheWideningDedupScenario,         // 4
		scenarios.OracleCacheWorktreeEditFrozenScenario,    // 5
		scenarios.OracleCacheAppendDeltaScenario,           // 6
		scenarios.OracleCacheRebaseScenario,                // 7
		scenarios.OracleCacheRulesRemovalScenario,          // 8
		scenarios.OracleCacheRebaseAdvisoryScenario,        // 9
		scenarios.OracleCacheLineageInheritScenario,        // 10
		scenarios.OracleCacheDepTranscriptScenario,         // 11
		scenarios.OracleCacheGitDeltaOnLineageScenario,     // 12
		scenarios.OracleCacheLineageModelMismatchScenario,  // 13
		scenarios.OracleCachePinnedContextRejectedScenario, // 14
		scenarios.OracleCacheTTLFrontmatterScenario,        // 15
		scenarios.OracleCacheNoCacheScenario,               // 16
		scenarios.OracleCacheTranscriptStabilityScenario,   // 17
		scenarios.OracleCacheChatReopenScenario,            // 18
		scenarios.OracleCacheSnapshotOptOutScenario,        // 19
		scenarios.OracleCacheGeminiPassthroughScenario,     // 20
		scenarios.OracleCacheConcurrentChatsScenario,       // 21
		scenarios.OracleCacheUnreadableGlobScenario,        // 22
		scenarios.OracleCacheLegacyUntouchedScenario,       // 23
	}

	if err := app.Execute(context.TODO(), allScenarios); err != nil {
		os.Exit(1)
	}
}
