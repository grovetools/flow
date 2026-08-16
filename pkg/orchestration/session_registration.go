package orchestration

import (
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
)

// newAgentSessionIntent builds the common daemon registration payload used by
// every Flow agent provider. ParentJobID is ownership lineage only; the daemon
// exposes it on the session without treating it as a scheduling dependency.
//
// The Type it stamps is "interactive_agent": every caller here launches an
// agent into a mux pane. Headless launches must go through
// newHeadlessSessionIntent instead — the type is what tells a host whether the
// session has a terminal to attach or only a transcript to stream.
func newAgentSessionIntent(job *Job, plan *Plan, provider, workDir, muxType string) daemon.SessionIntent {
	return daemon.SessionIntent{
		JobID:        job.ID,
		AttemptID:    job.AttemptID,
		ParentJobID:  job.ParentJobID,
		Provider:     provider,
		JobFilePath:  job.FilePath,
		PlanName:     plan.Name,
		Title:        job.Title,
		WorkDir:      workDir,
		Channels:     job.Channels,
		SignalTarget: job.SignalTarget,
		Autonomous:   job.Autonomous,
		Mux:          muxType,
		Type:         models.SessionTypeInteractiveAgent,
	}
}

// newHeadlessSessionIntent is the headless counterpart: same payload, but the
// session is registered for what it is — an agent with no multiplexer and no
// terminal, whose only live view is its transcript stream. Registering one as
// an interactive agent is what made treemux open an empty shell on click
// instead of the agent's log stream.
func newHeadlessSessionIntent(job *Job, plan *Plan, provider, workDir string) daemon.SessionIntent {
	intent := newAgentSessionIntent(job, plan, provider, workDir, models.MuxNone)
	intent.Type = models.SessionTypeHeadlessAgent
	return intent
}
