package browser

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/grovetools/core/pkg/paths"
)

// plansTUIState holds the persistent Plans-view settings, alongside the Jobs
// view's status-tui-state.json in the same state directory.
type plansTUIState struct {
	ColumnVisibility map[string]bool `json:"column_visibility"`
}

func plansStateFilePath() (string, error) {
	stateDir := filepath.Join(paths.StateDir(), "flow")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "plans-tui-state.json"), nil
}

// loadColumnVisibility returns the user's saved column choices, falling back
// to the defaults for anything missing, unknown, or unreadable. Only columns
// the picker still offers are honoured, so a stale state file cannot hide a
// column that has since been renamed or pin a new one to the wrong default.
func loadColumnVisibility() map[string]bool {
	visibility := defaultBrowserColumnVisibility()
	path, err := plansStateFilePath()
	if err != nil {
		return visibility
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return visibility
	}
	var state plansTUIState
	if err := json.Unmarshal(data, &state); err != nil {
		return visibility
	}
	for name, visible := range state.ColumnVisibility {
		if _, known := visibility[name]; known {
			visibility[name] = visible
		}
	}
	return visibility
}

// saveColumnVisibility persists the picker's choices. Only the picker may call
// it: columns dropped to fit a narrow pane are a rendering decision and must
// not survive the resize.
func saveColumnVisibility(visibility map[string]bool) error {
	path, err := plansStateFilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(plansTUIState{ColumnVisibility: visibility}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
