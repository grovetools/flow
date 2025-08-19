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
		PlanStepCommandScenario(), // Tests interactive plan step command
		PlanConfigScenario(), // Tests plan-level configuration with .grove-plan.yml

		// Chat Scenarios
		BasicChatWorkflowScenario(),
		ChatLaunchScenario(), // Fixed with docker check skip
		ChatRunFilteringScenario(),
		ChatPipelineScenario(),
		
		// Phase 3: Complex Orchestration
		SimpleOrchestrationScenario(),
		// ComplexOrchestrationScenario(), // Tests generate_jobs - needs investigation on how flow processes job output
		ReferencePromptScenario(), // Fixed using flow plan add
		
		// Interactive Agent Scenarios
		InteractiveAgentBasicScenario(),
		InteractiveAgentSkipScenario(),
		InteractiveAgentWorkflowScenario(),
		
		// Worktree Context Isolation Scenarios
		SimpleWorktreeContextTestScenario(),
		
		// Debug Scenarios (optional - can be run individually)
		LaunchDebugScenario(),
		LaunchErrorHandlingScenario(),
		LaunchDockerExecFailureScenario(),
		LaunchContainerNotRunningScenario(),
		LaunchSilentFailureScenario(),
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