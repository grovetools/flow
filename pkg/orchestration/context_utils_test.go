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

func TestResolveJobSkill(t *testing.T) {
	t.Run("empty skill returns empty string", func(t *testing.T) {
		job := &Job{Skill: ""}
		result := ResolveJobSkill(job, t.TempDir())
		if result != "" {
			t.Errorf("expected empty string for empty skill, got %q", result)
		}
	})

	t.Run("nonexistent skill returns empty string", func(t *testing.T) {
		job := &Job{Skill: "nonexistent-skill-that-does-not-exist-xyz"}
		result := ResolveJobSkill(job, t.TempDir())
		if result != "" {
			t.Errorf("expected empty string for nonexistent skill, got %q", result)
		}
	})

	t.Run("builtin skill resolves and writes file", func(t *testing.T) {
		workDir := t.TempDir()
		job := &Job{Skill: "grove-skill-guide"}

		result := ResolveJobSkill(job, workDir)
		if result == "" {
			t.Fatal("expected non-empty path for builtin skill")
		}

		expectedPath := filepath.Join(workDir, ".grove", "skills", "skill-grove-skill-guide.md")
		if result != expectedPath {
			t.Errorf("expected path %q, got %q", expectedPath, result)
		}

		// Verify file exists and has content
		content, err := os.ReadFile(result)
		if err != nil {
			t.Fatalf("failed to read resolved skill file: %v", err)
		}
		if len(content) == 0 {
			t.Error("expected skill file to have content")
		}
		// SKILL.md should contain frontmatter
		if !strings.HasPrefix(string(content), "---") {
			t.Error("expected skill content to start with frontmatter delimiter '---'")
		}
		if !strings.Contains(string(content), "grove-skill-guide") {
			t.Error("expected skill content to contain the skill name")
		}
	})

	t.Run("creates .grove/skills directory", func(t *testing.T) {
		workDir := t.TempDir()
		job := &Job{Skill: "grove-skill-guide"}

		ResolveJobSkill(job, workDir)

		skillDir := filepath.Join(workDir, ".grove", "skills")
		info, err := os.Stat(skillDir)
		if err != nil {
			t.Fatalf("expected .grove/skills directory to exist: %v", err)
		}
		if !info.IsDir() {
			t.Error("expected .grove/skills to be a directory")
		}
	})
}
