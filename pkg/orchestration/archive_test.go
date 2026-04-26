package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveContextRules_Success(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "plan")
	_ = os.MkdirAll(planDir, 0755)

	// Create source rules file
	rulesContent := "# My Rules\ninclude: **/*.go\nexclude: vendor/\n"
	rulesPath := filepath.Join(dir, "custom.rules")
	if err := os.WriteFile(rulesPath, []byte(rulesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create job file with frontmatter
	jobFilePath := filepath.Join(planDir, "01-test-job.md")
	jobFileContent := `---
id: test-job-abc123
title: Test Job
status: completed
type: oneshot
---
Some body content.
`
	if err := os.WriteFile(jobFilePath, []byte(jobFileContent), 0644); err != nil {
		t.Fatal(err)
	}

	job := &Job{
		ID:       "test-job-abc123",
		FilePath: jobFilePath,
	}
	plan := &Plan{
		Directory: planDir,
	}

	err := ArchiveContextRules(job, plan, rulesPath)
	if err != nil {
		t.Fatalf("ArchiveContextRules() error = %v", err)
	}

	// Verify artifact directory was created
	artifactDir := filepath.Join(planDir, ".artifacts", "test-job-abc123")
	if _, err := os.Stat(artifactDir); os.IsNotExist(err) {
		t.Error("artifact directory was not created")
	}

	// Verify context.rules was written with correct content
	archivedPath := filepath.Join(artifactDir, "context.rules")
	archived, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatalf("failed to read archived rules: %v", err)
	}
	if string(archived) != rulesContent {
		t.Errorf("archived content = %q, want %q", string(archived), rulesContent)
	}

	// Verify job struct was updated
	expectedRel := filepath.Join(".artifacts", "test-job-abc123", "context.rules")
	if job.UsedRulesFile != expectedRel {
		t.Errorf("job.UsedRulesFile = %q, want %q", job.UsedRulesFile, expectedRel)
	}

	// Verify job file frontmatter was updated
	updatedContent, err := os.ReadFile(jobFilePath)
	if err != nil {
		t.Fatalf("failed to read updated job file: %v", err)
	}
	if !strings.Contains(string(updatedContent), "used_rules_file: "+expectedRel) {
		t.Errorf("job file frontmatter missing used_rules_file, content:\n%s", string(updatedContent))
	}
}

func TestArchiveContextRules_EmptyPath(t *testing.T) {
	job := &Job{ID: "test-job"}
	plan := &Plan{Directory: t.TempDir()}

	err := ArchiveContextRules(job, plan, "")
	if err != nil {
		t.Errorf("ArchiveContextRules() with empty path should return nil, got %v", err)
	}

	// Verify no artifacts directory was created
	artifactDir := filepath.Join(plan.Directory, ".artifacts")
	if _, err := os.Stat(artifactDir); !os.IsNotExist(err) {
		t.Error("artifact directory should not have been created for empty path")
	}
}

func TestArchiveContextRulesForTurn_Success(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "plan")
	_ = os.MkdirAll(planDir, 0755)

	rulesContent := "# Turn Rules\ninclude: **/*.go\n"
	rulesPath := filepath.Join(dir, "active.rules")
	if err := os.WriteFile(rulesPath, []byte(rulesContent), 0644); err != nil {
		t.Fatal(err)
	}

	plan := &Plan{Directory: planDir}

	relPath, err := ArchiveContextRulesForTurn(plan, "chat-job-123", "abc123", rulesPath)
	if err != nil {
		t.Fatalf("ArchiveContextRulesForTurn() error = %v", err)
	}

	expectedRel := filepath.Join(".artifacts", "chat-job-123", "abc123-context.rules")
	if relPath != expectedRel {
		t.Errorf("relPath = %q, want %q", relPath, expectedRel)
	}

	// Verify the archived file exists and has correct content
	archivedPath := filepath.Join(planDir, ".artifacts", "chat-job-123", "abc123-context.rules")
	archived, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatalf("failed to read archived rules: %v", err)
	}
	if string(archived) != rulesContent {
		t.Errorf("archived content = %q, want %q", string(archived), rulesContent)
	}
}

func TestArchiveContextRulesForTurn_EmptyPath(t *testing.T) {
	plan := &Plan{Directory: t.TempDir()}
	relPath, err := ArchiveContextRulesForTurn(plan, "chat-job-123", "abc123", "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if relPath != "" {
		t.Errorf("relPath = %q, want empty", relPath)
	}
}

func TestArchiveContextRules_MissingFile(t *testing.T) {
	job := &Job{ID: "test-job"}
	plan := &Plan{Directory: t.TempDir()}

	err := ArchiveContextRules(job, plan, "/nonexistent/path/rules.file")
	if err == nil {
		t.Error("ArchiveContextRules() with missing file should return error")
	}
	if !strings.Contains(err.Error(), "failed to read used rules file") {
		t.Errorf("error = %q, want containing 'failed to read used rules file'", err.Error())
	}
}
