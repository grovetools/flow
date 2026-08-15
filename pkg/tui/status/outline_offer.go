package status

import (
	"github.com/grovetools/core/tui/embed"

	"github.com/grovetools/flow/pkg/orchestration"
)

// OutlineOffer implements embed.OutlineOfferer for the hosted status TUI: it
// answers the host's pin-outline chord with the transcript behind the job the
// user is looking at — the cursor row, or the job the outline/log detail pane
// is showing when the cursor is elsewhere. Resolution rides the same ladder
// the toc.ansi artifact writer uses (registry binding → live artifact
// transcript → archived copy), which is what makes finished and archived jobs
// pinnable at all: the agent is gone, the transcript is not.
func (m *Model) OutlineOffer() (embed.OutlineOffer, bool) {
	job := m.CurrentJob()
	if job == nil {
		job = m.ActiveLogJob
	}
	if job == nil || m.Plan == nil || job.ID == "" {
		return embed.OutlineOffer{}, false
	}
	path, metadata, ok := orchestration.ResolveTranscriptForOutline(job, m.Plan)
	if !ok || path == "" {
		return embed.OutlineOffer{}, false
	}
	offer := embed.OutlineOffer{
		JobID:          job.ID,
		Title:          job.Title,
		Provider:       job.Provider,
		TranscriptPath: path,
		JobFilePath:    job.FilePath,
		PlanDirectory:  m.Plan.Directory,
	}
	if metadata != nil {
		if metadata.Provider != "" {
			offer.Provider = metadata.Provider
		}
		offer.WorkingDirectory = metadata.WorkingDirectory
	}
	if offer.Provider == "" {
		// Artifact-owned session files are written by the Pi runtime; that is
		// the only writer of .artifacts/<job-id>/sessions/*.jsonl (mirrors
		// resolveJobTranscript's reconstruction).
		offer.Provider = "pi"
	}
	return offer, true
}

var _ embed.OutlineOfferer = (*Model)(nil)
