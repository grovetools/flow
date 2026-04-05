package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// SkillFidelityState represents the JSON schema written by the agent
// and read by the TUI for skill execution tracking.
// The agent writes these to .artifacts/<job-id>/<skill-name>-status.json.
type SkillFidelityState struct {
	Skill             string   `json:"skill" yaml:"skill"`
	Status            string   `json:"status" yaml:"status"` // pending, running, completed, failed, skipped
	ArtifactsExpected []string `json:"artifacts_expected" yaml:"artifacts_expected"`
	ArtifactsProduced []string `json:"artifacts_produced" yaml:"artifacts_produced"`
	Error             *string  `json:"error" yaml:"error,omitempty"`
	DiagnosticPath    *string  `json:"diagnostic_path" yaml:"diagnostic_path,omitempty"`
}

// SkillFidelityReport aggregates fidelity states for a job's skill sequence.
type SkillFidelityReport struct {
	JobID  string               `json:"job_id"`
	Skills []SkillFidelityState `json:"skills"`
}

// ReadSkillFidelityStates reads all *-status.json files from a job's artifact directory
// and returns the parsed fidelity states.
func ReadSkillFidelityStates(artifactDir string) ([]SkillFidelityState, error) {
	files, err := os.ReadDir(artifactDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var states []SkillFidelityState
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), "-status.json") {
			continue
		}
		path := filepath.Join(artifactDir, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var state SkillFidelityState
		if json.Unmarshal(data, &state) == nil && state.Skill != "" {
			states = append(states, state)
		}
	}
	return states, nil
}

// BuildFidelityReport reads status files and builds a report for a job.
func BuildFidelityReport(planDir, jobID string) (*SkillFidelityReport, error) {
	artifactDir := filepath.Join(planDir, ".artifacts", jobID)
	states, err := ReadSkillFidelityStates(artifactDir)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, nil
	}
	return &SkillFidelityReport{
		JobID:  jobID,
		Skills: states,
	}, nil
}
