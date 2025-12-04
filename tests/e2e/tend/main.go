package main

import (
	"os"

	"github.com/mattsolo1/grove-tend/pkg/app"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-flow/tests/e2e/tend/scenarios"
)

func main() {
	allScenarios := []*harness.Scenario{
		scenarios.AgentFromChatScenario,
		scenarios.BriefingFilesScenario,
		scenarios.CoreOrchestrationScenario,
		scenarios.DependencyWorkflowScenario,
		scenarios.OneshotWithContextScenario,
		scenarios.AgentWorktreeLifecycleScenario,
		scenarios.ChatAndExtractWorkflowScenario,
		scenarios.JobFailureAndRecoveryScenario,
		scenarios.PlanLifecycleScenario,
		scenarios.JobManagementScenario,
		scenarios.PlanListTUIScenario,
		scenarios.PlanStatusTUIScenario,
	}

	if err := app.Execute(nil, allScenarios); err != nil {
		os.Exit(1)
	}
}
