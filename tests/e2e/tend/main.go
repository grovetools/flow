// File: tests/e2e/tend/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mattsolo1/grove-tend/pkg/app"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

func main() {
	// A list of all E2E scenarios for grove-flow.
	scenarios := []*harness.Scenario{
		// Plan Scenarios
		BasicPlanLifecycleScenario(),
		PlanActiveJobScenario(),
		AgentJobLaunchScenario(), // Fixed with path resolution helpers
		PlanGraphScenario(),
		PlanWorktreeInheritanceScenario(), // Tests smart worktree inheritance
		PlanStepCommandScenario(),         // Tests interactive plan step command
		PlanConfigScenario(),              // Tests plan-level configuration with .grove-plan.yml
		PlanConfigPropagationScenario(),   // Tests plan config propagation to job frontmatter
		GoWorkspaceWorktreeScenario(),     // Tests go.work file creation in worktrees
		StatusTUIScenario(),                 // Tests enhanced status command with TUI flag
		PlanInitImprovementsScenario(),      // Tests plan init improvements: empty plan listing, --with-worktree, oneshot_model default
		PlanFinishScenario(),                 // Tests interactive plan cleanup workflow
		PlanFinishFlagsScenario(),            // Tests flag-based plan cleanup
		PlanFinishDevLinksScenario(),         // Tests dev links cleanup functionality
		PlanRecipesScenario(),                // Tests the new recipes feature
		PlanRecipeWithExtractScenario(),      // Tests combining recipe with content extraction
		GenerateRecipeScenario(),             // Tests generate-recipe job type functionality
		GenerateRecipeWithVariablesScenario(), // Tests generate-recipe with multiple template variables
		JobSummaryScenario(),                 // Tests job summarization on completion
		InteractiveAgentJobSummaryScenario(), // Tests interactive_agent job summarization with transcripts
		// OneshotJobSummaryScenario(),       // DISABLED: Tests oneshot job summarization via orchestrator - fails due to async goroutine not inheriting test PATH

		// Chat Scenarios
		// BasicChatWorkflowScenario(), // Temporarily disabled - hanging issue
		ChatLaunchScenario(), // Fixed with docker check skip
		ChatRunFilteringScenario(),
		ChatPipelineScenario(),
		ChatTemplateInjectionScenario(), // Tests automatic template injection
		ChatInteractivePromptScenario(), // Tests interactive prompt for chat jobs in plans
		ChatExtractBasicScenario(),      // Tests basic chat block extraction
		ChatExtractErrorScenario(),      // Tests extract command error handling
		ChatExtractAllScenario(),        // Tests extracting all content with 'all' argument
		ChatExtractListScenario(),       // Tests listing available blocks

		// Phase 3: Complex Orchestration
		SimpleOrchestrationScenario(),
		// ComplexOrchestrationScenario(), // Tests generate_jobs - needs investigation on how flow processes job output
		ReferencePromptScenario(), // Fixed using flow plan add

		// Interactive Agent Scenarios
		InteractiveAgentBasicScenario(),
		InteractiveAgentSkipScenario(),
		InteractiveAgentWorkflowScenario(),
		// InteractiveAgentPollingWorkflowScenario(), // Tests polling with flow plan complete

		// Agent Continue Scenarios
		AgentContinueScenario(),
		AgentContinueAutoEnableScenario(),
		AgentContinueFlagPropagationScenario(),

		// Worktree Context Isolation Scenarios
		SimpleWorktreeContextTestScenario(),
		WorktreeStateIsolationScenario(),
		WorktreeStateDirectNavigationScenario(),

		// Rules Prompt Scenarios
		RulesPromptProceedScenario(),
		RulesPromptCancelScenario(),
		RulesPromptEditScenario(),
		RulesInFrontmatterScenario(), // Tests job-specific rules_file in frontmatter

		// Template Scenarios
		// TemplateSymlinkWorktreeScenario(),
		// TemplateSymlinkFromMainScenario(), // Tests symlink creation when running from main directory

		// Debug Scenarios (optional - can be run individually)
		// LaunchDebugScenario(),
		// LaunchErrorHandlingScenario(),
		// LaunchDockerExecFailureScenario(),
		// LaunchContainerNotRunningScenario(),
		// LaunchSilentFailureScenario(),
	}

	// Setup signal handling for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nReceived interrupt signal, shutting down...")
		cancel()
	}()

	// Execute the custom tend application with our scenarios.
	if err := app.Execute(ctx, scenarios); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
