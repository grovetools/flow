package main

import (
	"os"

	"github.com/mattsolo1/grove-tend/pkg/app"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-flow/tests/e2e/tend/scenarios"
)

func main() {
	allScenarios := []*harness.Scenario{
		scenarios.CoreOrchestrationScenario,
	}

	if err := app.Execute(nil, allScenarios); err != nil {
		os.Exit(1)
	}
}
