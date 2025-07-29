package state

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// State represents the local Grove state.
type State struct {
	ActiveJob string `yaml:"active_job,omitempty"`
}

// stateFilePath returns the path to the state file.
func stateFilePath() (string, error) {
	// Find the git root directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}

	// Walk up the directory tree looking for .git
	dir := cwd
	for {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			// Found .git directory
			statePath := filepath.Join(dir, ".grove", "state.yml")
			return statePath, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root without finding .git
			// Use current directory as fallback
			statePath := filepath.Join(cwd, ".grove", "state.yml")
			return statePath, nil
		}
		dir = parent
	}
}

// LoadState loads the state from the state file.
func LoadState() (*State, error) {
	path, err := stateFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty state if file doesn't exist
			return &State{}, nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var state State
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}

	return &state, nil
}

// SaveState saves the state to the state file.
func SaveState(state *State) error {
	path, err := stateFilePath()
	if err != nil {
		return err
	}

	// Ensure .grove directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}

	return nil
}

// GetActiveJob returns the active job from the state.
func GetActiveJob() (string, error) {
	state, err := LoadState()
	if err != nil {
		return "", err
	}
	return state.ActiveJob, nil
}

// SetActiveJob sets the active job in the state.
func SetActiveJob(jobID string) error {
	state, err := LoadState()
	if err != nil {
		return err
	}

	state.ActiveJob = jobID
	return SaveState(state)
}

// ClearActiveJob clears the active job from the state.
func ClearActiveJob() error {
	state, err := LoadState()
	if err != nil {
		return err
	}

	state.ActiveJob = ""
	return SaveState(state)
}