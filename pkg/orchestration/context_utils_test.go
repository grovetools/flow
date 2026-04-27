package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopeToSubProject(t *testing.T) {
	// Create temp directory structure to simulate an ecosystem
	tmpDir := t.TempDir()
	ecoRoot := filepath.Join(tmpDir, "genohype-eco")
	subProject := filepath.Join(ecoRoot, "genohype")
	if err := os.MkdirAll(subProject, 0o755); err != nil {
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

func TestResolveJobSkillContent(t *testing.T) {
	t.Run("empty skill returns empty string and no error", func(t *testing.T) {
		job := &Job{Skill: ""}
		result, err := ResolveJobSkillContent(job, t.TempDir())
		if err != nil {
			t.Errorf("expected no error for empty skill, got %v", err)
		}
		if result != "" {
			t.Errorf("expected empty string for empty skill, got %q", result)
		}
	})

	t.Run("nonexistent skill returns error", func(t *testing.T) {
		job := &Job{Skill: "nonexistent-skill-that-does-not-exist-xyz"}
		result, err := ResolveJobSkillContent(job, t.TempDir())
		if err == nil {
			t.Error("expected error for nonexistent skill, got nil")
		}
		if result != "" {
			t.Errorf("expected empty string for nonexistent skill, got %q", result)
		}
	})

	t.Run("builtin skill resolves content", func(t *testing.T) {
		job := &Job{Skill: "grove-skill-guide"}

		result, err := ResolveJobSkillContent(job, t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == "" {
			t.Fatal("expected non-empty content for builtin skill")
		}

		// Content should NOT start with frontmatter (stripped)
		if strings.HasPrefix(result, "---") {
			t.Error("expected skill content to have frontmatter stripped")
		}
		// Content should contain meaningful skill instructions
		if len(result) < 50 {
			t.Errorf("expected substantial skill content, got %d chars", len(result))
		}
	})
}
