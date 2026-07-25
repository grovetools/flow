package orchestration

import (
	"encoding/json"
	"os"
)

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
