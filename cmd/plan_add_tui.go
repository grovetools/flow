package cmd

import (
	"fmt"

	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/embed"
	coreconfig "github.com/grovetools/core/config"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/wizards/add"
	skillservice "github.com/grovetools/skills/pkg/service"
)

// runAddJobWizard launches the embeddable add-job wizard via
// embed.RunStandalone and returns the constructed job (or nil if the
// user cancelled). The wizard itself is implemented in
// flow/pkg/tui/wizards/add; this function preserves the standalone
// CLI contract by feeding the wizard's DoneMsg result back into the
// existing plan_add_step code path that persists the job.
func runAddJobWizard(plan *orchestration.Plan, initialDeps []string) (*orchestration.Job, error) {
	cfg := add.Config{
		Plan:        plan,
		InitialDeps: initialDeps,
	}

	// Best-effort: resolve workspace node + skill service up front so
	// the skills picker is populated. The wizard falls back to its own
	// derivation if these are nil, but the CLI path historically
	// computed them here so keep the behavior identical.
	if node, err := workspace.GetProjectByPath(plan.Directory); err == nil && node != nil {
		cfg.WorkspaceNode = node
	}
	if coreCfg, _ := coreconfig.LoadDefault(); coreCfg != nil {
		if svc, _ := skillservice.New(nil, coreCfg, nil); svc != nil {
			cfg.SkillService = svc
		}
	}

	model := add.New(cfg)
	result, err := embed.RunStandalone(model)
	if err != nil {
		return nil, fmt.Errorf("error running TUI for job creation: %w", err)
	}

	if result == nil {
		return nil, fmt.Errorf("job creation cancelled")
	}
	job, ok := result.(*orchestration.Job)
	if !ok || job == nil {
		return nil, fmt.Errorf("job creation cancelled")
	}
	if job.Title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}
	return job, nil
}
