package status_tui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// tuiState holds persistent TUI settings.
type tuiState struct {
	ColumnVisibility map[string]bool `json:"column_visibility"`
}

// getStateFilePath returns the path to the TUI state file.
func getStateFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	stateDir := filepath.Join(home, ".grove", "flow")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "status-tui-state.json"), nil
}

// defaultColumnVisibility defines the initial state for columns.
func defaultColumnVisibility() map[string]bool {
	return map[string]bool{
		"JOB":        true,  // Job filename visible by default
		"TITLE":      false,
		"TYPE":       true,  // Job type visible by default
		"STATUS":     false,
		"TEMPLATE":   true,  // Template name visible by default
		"MODEL":      false,
		"WORKTREE":   false,
		"PREPEND":    false,
		"UPDATED":    false,
		"COMPLETED":  false,
		"DURATION":   false,
	}
}

// loadState loads the TUI state from disk or returns defaults.
func loadState() (*tuiState, error) {
	path, err := getStateFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default state if file doesn't exist
			return &tuiState{
				ColumnVisibility: defaultColumnVisibility(),
			}, nil
		}
		return nil, err
	}

	var state tuiState
	if err := json.Unmarshal(data, &state); err != nil {
		// On parse error, return default state
		return &tuiState{
			ColumnVisibility: defaultColumnVisibility(),
		}, nil
	}

	return &state, nil
}

// saveState saves the TUI state to disk.
func saveState(visibility map[string]bool) error {
	path, err := getStateFilePath()
	if err != nil {
		return err
	}

	state := tuiState{
		ColumnVisibility: visibility,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
