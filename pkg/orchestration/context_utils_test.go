package orchestration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScopeToSubProject(t *testing.T) {
	// Create temp directory structure to simulate an ecosystem
	tmpDir := t.TempDir()
	ecoRoot := filepath.Join(tmpDir, "genohype-eco")
	subProject := filepath.Join(ecoRoot, "genohype")
	if err := os.MkdirAll(subProject, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		workDir  string
		job      *Job
		expected string
	}{
		{
			name:     "nil job returns workDir unchanged",
			workDir:  ecoRoot,
			job:      nil,
			expected: ecoRoot,
		},
		{
			name:     "empty repository returns workDir unchanged",
			workDir:  ecoRoot,
			job:      &Job{Repository: ""},
			expected: ecoRoot,
		},
		{
			name:     "scopes from ecosystem root to sub-project",
			workDir:  ecoRoot,
			job:      &Job{Repository: "genohype"},
			expected: subProject,
		},
		{
			name:     "does not double-scope when already at sub-project",
			workDir:  subProject,
			job:      &Job{Repository: "genohype"},
			expected: subProject,
		},
		{
			name:     "non-existent sub-project returns workDir unchanged",
			workDir:  ecoRoot,
			job:      &Job{Repository: "nonexistent"},
			expected: ecoRoot,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ScopeToSubProject(tt.workDir, tt.job)
			if result != tt.expected {
				t.Errorf("ScopeToSubProject(%q, %+v) = %q, want %q", tt.workDir, tt.job, result, tt.expected)
			}
		})
	}
}
