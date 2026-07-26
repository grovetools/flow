package orchestration

import (
	"context"
	"fmt"

	"github.com/grovetools/core/config"
)

// PreparedInteractiveAgentResume is a fully validated resume launch. Preparing
// is side-effect free, allowing command callers to reject unsupported
// providers and routing targets before changing durable job state.
type PreparedInteractiveAgentResume struct {
	provider         preparedInteractiveAgentProvider
	job              *Job
	plan             *Plan
	workDir          string
	shellCommand     string
	expectedNativeID string
}

// PrepareInteractiveAgentResume resolves the archived provider explicitly and
// prepares its command for the plan's normal interactive launch target.
func PrepareInteractiveAgentResume(job *Job, plan *Plan, workDir, providerName, nativeSessionID string) (*PreparedInteractiveAgentResume, error) {
	coreCfg, err := config.LoadFrom(workDir)
	if err != nil {
		coreCfg = &config.Config{}
	}
	parsedFlowCfg, err := FlowConfigFrom(coreCfg)
	if err != nil {
		return nil, fmt.Errorf("load flow configuration: %w", err)
	}
	flowCfg := *parsedFlowCfg

	spec, ok := LookupAgentProvider(providerName)
	if !ok {
		return nil, unknownAgentProviderError(providerName)
	}
	if spec.BuildResumeShellCommand == nil {
		return nil, fmt.Errorf("agent provider %q does not support session resume", providerName)
	}
	if spec.SessionRegistration != SessionRegistrationDaemon {
		return nil, fmt.Errorf("agent provider %q cannot resume through the daemon session lifecycle", providerName)
	}

	agentArgs := resolveProviderArgs(flowCfg, spec.Name)
	agentArgs, err = appendProviderJobArgs(spec, agentArgs, job)
	if err != nil {
		return nil, err
	}
	shellCommand, err := spec.BuildResumeShellCommand(agentArgs, nativeSessionID)
	if err != nil {
		return nil, fmt.Errorf("build %s resume command: %w", providerName, err)
	}

	// Loaded plans do not persist the submission-time target. Preserve the
	// resume command's historical tmux behavior when it is absent, while
	// honoring an explicit runtime target when one is available.
	target := "tmux"
	if plan != nil && plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		target = plan.Orchestration.AgentTarget
	}
	var provider preparedInteractiveAgentProvider
	switch target {
	case "tmux":
		candidate := spec.newTmuxProvider(flowCfg.AgentEnv)
		var ok bool
		provider, ok = candidate.(preparedInteractiveAgentProvider)
		if !ok {
			return nil, fmt.Errorf("agent provider %q does not support prepared resume launch on tmux", providerName)
		}
	case "native", "tuimux":
		gp := NewGrovetermAgentProvider(spec, false, target)
		gp.agentEnv = flowCfg.AgentEnv
		provider = gp
	default:
		return nil, fmt.Errorf("agent_target not set or unsupported (%q): resume requires tmux, native, or tuimux routing", target)
	}

	return &PreparedInteractiveAgentResume{
		provider:         provider,
		job:              job,
		plan:             plan,
		workDir:          workDir,
		shellCommand:     shellCommand,
		expectedNativeID: nativeSessionID,
	}, nil
}

// Launch starts the prepared resume through the provider's normal lifecycle.
func (p *PreparedInteractiveAgentResume) Launch(ctx context.Context) error {
	return p.provider.LaunchPrepared(ctx, p.job, p.plan, p.workDir, p.shellCommand, p.expectedNativeID)
}

// ResumeInteractiveAgent is the orchestration-level convenience entry point.
func ResumeInteractiveAgent(ctx context.Context, job *Job, plan *Plan, workDir, providerName, nativeSessionID string) error {
	prepared, err := PrepareInteractiveAgentResume(job, plan, workDir, providerName, nativeSessionID)
	if err != nil {
		return err
	}
	return prepared.Launch(ctx)
}
