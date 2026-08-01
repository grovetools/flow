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
	// Pi-family sessions live in the Flow-owned job artifact directory; the
	// resumed process needs the same --session-dir the original launch used or
	// the native id will not resolve. For a completed job the directory already
	// exists, so the helper's MkdirAll is a no-op and preparing stays
	// effectively side-effect free.
	if spec.PiRuntime != nil {
		if plan == nil || plan.Directory == "" {
			return nil, fmt.Errorf("cannot resume %s job without its plan directory (Flow-owned session dir)", spec.Name)
		}
		agentArgs, err = appendPiJobSessionArgs(spec, plan.Directory, job.ID, agentArgs)
		if err != nil {
			return nil, err
		}
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
// It returns as soon as the agent pane exists; the session is not yet confirmed
// with the daemon. Callers must follow a successful Launch with
// AwaitSessionConfirmation before exiting.
func (p *PreparedInteractiveAgentResume) Launch(ctx context.Context) error {
	return p.provider.LaunchPrepared(ctx, p.job, p.plan, p.workDir, p.shellCommand, p.expectedNativeID)
}

// AwaitSessionConfirmation blocks until the post-launch work Launch started in
// the background has settled: PID capture, transcript discovery, Pi
// startup-failure diagnosis, and the daemon ConfirmSession that turns the
// pending intent into an attachable session.
//
// It is separate from Launch, not folded into it, for two reasons. The wait can
// legitimately run for tens of seconds, so the bound and the progress reporting
// belong to the caller that owns the terminal. And a confirmation failure must
// NOT be treated like a launch failure: the agent process is already live by
// then, so rolling the job back to `completed` (which is what
// runResumeWithRollback does with a Launch error) would lie about a running
// agent and, worse, undo the `failed` status handlePiStartupFailure just wrote.
//
// Every caller of this package that resumes and then exits must call this. The
// process exiting first is exactly how job steward-66dd4eb3 ended up wedged at
// status=pending in the daemon store on 2026-08-01, with a dead PTY treemux
// could not attach to and a pi startup failure nobody was left to record.
func (p *PreparedInteractiveAgentResume) AwaitSessionConfirmation(ctx context.Context) error {
	return p.provider.awaitSessionConfirmation(ctx)
}

// ResumeInteractiveAgent is the orchestration-level convenience entry point. It
// blocks through confirmation, so it is safe for a caller that exits right
// after; use PrepareInteractiveAgentResume directly to bound the wait or report
// progress while it runs.
func ResumeInteractiveAgent(ctx context.Context, job *Job, plan *Plan, workDir, providerName, nativeSessionID string) error {
	prepared, err := PrepareInteractiveAgentResume(job, plan, workDir, providerName, nativeSessionID)
	if err != nil {
		return err
	}
	if err := prepared.Launch(ctx); err != nil {
		return err
	}
	return prepared.AwaitSessionConfirmation(ctx)
}
