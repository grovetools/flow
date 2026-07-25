package orchestration

import "github.com/grovetools/core/pkg/daemon"

// newAgentSessionIntent builds the common daemon registration payload used by
// every Flow agent provider. ParentJobID is ownership lineage only; the daemon
// exposes it on the session without treating it as a scheduling dependency.
func newAgentSessionIntent(job *Job, plan *Plan, provider, workDir, muxType string) daemon.SessionIntent {
	return daemon.SessionIntent{
		JobID:        job.ID,
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
	}
}
