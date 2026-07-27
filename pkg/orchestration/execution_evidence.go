package orchestration

import (
	"encoding/json"
	"os"

	coresessions "github.com/grovetools/core/pkg/sessions"
)

// writeSessionMetadataArtifact writes the archived metadata.json for a job.
// It copies the registry record when there is one (preserving unknown
// provider fields), and otherwise serializes the metadata reconstructed from
// the job and its artifact transcript. Both routes stamp the same authoritative
// job identity and status.
//
// The reconstructed route exists so a job whose registry record was reaped
// still ends up with the archived evidence pair (metadata.json +
// transcript.jsonl) that later completions and retry decisions read.
func writeSessionMetadataArtifact(source, destination string, metadata *coresessions.SessionMetadata, job *Job) error {
	if source != "" {
		return writeArchivedSessionMetadata(source, destination, job)
	}
	if metadata == nil {
		return os.ErrNotExist
	}
	record := *metadata
	record.JobID = job.ID
	record.Status = string(job.Status)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(destination, data, 0o600)
}

// writeArchivedSessionMetadata preserves unknown provider fields while making
// frontmatter status the authoritative job outcome mirrored by the archive.
// The session registry remains provider-owned evidence; archived status is not
// allowed to contradict the job frontmatter that accepted that evidence.
func writeArchivedSessionMetadata(source, destination string, job *Job) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	raw["job_id"] = job.ID
	raw["status"] = string(job.Status)
	data, err = json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(destination, data, 0o600)
}
