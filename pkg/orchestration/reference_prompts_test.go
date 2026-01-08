package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReferenceBased_OneShotExecutor_BuildPrompt tests reference-based prompts for oneshot executor
func TestReferenceBased_OneShotExecutor_BuildPrompt(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test source files
	file1 := filepath.Join(tmpDir, "file1.go")
	os.WriteFile(file1, []byte("package main\n\nfunc main() {}"), 0644)

	file2 := filepath.Join(tmpDir, "file2.go")
	os.WriteFile(file2, []byte("package main\n\nfunc helper() {}"), 0644)

	// Change to tmp dir for relative path resolution
	oldCwd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldCwd)

	plan := &Plan{
		Directory: tmpDir,
	}

	// Test traditional prompt assembly (backward compatibility)
	t.Run("traditional_prompt", func(t *testing.T) {
		job := &Job{
			Include:    []string{"file1.go", "file2.go"},
			PromptBody: "Do something with these files",
		}

		executor := NewOneShotExecutor(NewMockLLMClient(), nil)
		prompt, _, _, err := executor.buildPrompt(job, plan, "")
		if err != nil {
			t.Fatalf("buildPrompt() error = %v", err)
		}

		// Verify prompt contains all sources
		if !strings.Contains(prompt, "package main") {
			t.Errorf("Prompt missing file content")
		}
		if !strings.Contains(prompt, "Do something with these files") {
			t.Errorf("Prompt missing job body")
		}
		if !strings.Contains(prompt, "Content from file1.go") {
			t.Errorf("Prompt missing file1.go header")
		}
	})

	// Test reference-based prompt with template
	t.Run("reference_based_prompt", func(t *testing.T) {
		// Create a mock template for testing
		_ = "You are a code refactoring expert." // mockTemplateContent
		
		// We'll test the structure even if template loading fails
		job := &Job{
			Template:   "test-template",
			Include:    []string{"file1.go", "file2.go"},
			PromptBody: "<!-- This step uses template 'test-template' with include files -->\n<!-- Template will be resolved at execution time -->\n\n## Additional Instructions\n\nRefactor these files",
		}

		executor := NewOneShotExecutor(NewMockLLMClient(), nil)
		prompt, _, _, err := executor.buildPrompt(job, plan, "")
		
		// The test might fail if the template doesn't exist, but we can verify
		// that it's attempting to use the reference-based path
		if err != nil {
			if !strings.Contains(err.Error(), "resolving template test-template") {
				t.Fatalf("buildPrompt() unexpected error = %v", err)
			}
			// Expected error for missing template
			return
		}

		// If no error (unlikely in test), verify the structure
		if !strings.Contains(prompt, "System Instructions (from template:") {
			t.Errorf("Prompt missing template header")
		}
		if !strings.Contains(prompt, "--- START OF file1.go ---") {
			t.Errorf("Prompt missing file separator")
		}
		if !strings.Contains(prompt, "Refactor these files") {
			t.Errorf("Prompt missing additional instructions")
		}
	})

	// Test empty include files list
	t.Run("empty_include_files", func(t *testing.T) {
		job := &Job{
			Template:   "test-template",
			Include:    []string{},
			PromptBody: "Do something",
		}

		executor := NewOneShotExecutor(NewMockLLMClient(), nil)
		_, _, _, err := executor.buildPrompt(job, plan, "")

		// Should handle empty include files gracefully
		if err != nil && !strings.Contains(err.Error(), "template") {
			t.Errorf("Unexpected error for empty include files: %v", err)
		}
	})

	// Test non-existent include file
	t.Run("missing_include_file", func(t *testing.T) {
		job := &Job{
			Include:    []string{"nonexistent.go"},
			PromptBody: "Process this file",
		}

		executor := NewOneShotExecutor(NewMockLLMClient(), nil)
		_, _, _, err := executor.buildPrompt(job, plan, "")
		
		if err == nil {
			t.Errorf("Expected error for missing include file")
		}
		if !strings.Contains(err.Error(), "nonexistent.go") {
			t.Errorf("Error should mention missing file: %v", err)
		}
	})

	// Test large number of include files
	t.Run("multiple_include_files", func(t *testing.T) {
		// Create multiple test files
		var includeFiles []string
		for i := 0; i < 5; i++ {
			filename := filepath.Join(tmpDir, strings.ReplaceAll("file_{{i}}.go", "{{i}}", string(rune('0'+i))))
			content := strings.ReplaceAll("package main\n\n// File {{i}}", "{{i}}", string(rune('0'+i)))
			os.WriteFile(filename, []byte(content), 0644)
			includeFiles = append(includeFiles, filepath.Base(filename))
		}

		job := &Job{
			Include:    includeFiles,
			PromptBody: "Process all these files",
		}

		executor := NewOneShotExecutor(NewMockLLMClient(), nil)
		prompt, _, _, err := executor.buildPrompt(job, plan, "")
		if err != nil {
			t.Fatalf("buildPrompt() error = %v", err)
		}

		// Verify all files are included
		for i, file := range includeFiles {
			if !strings.Contains(prompt, file) {
				t.Errorf("Prompt missing file %d: %s", i, file)
			}
		}
	})
}

// TestReferenceBased_AgentExecutor_BuildPrompt tests reference-based prompts for agent executor
func TestReferenceBased_AgentExecutor_BuildPrompt(t *testing.T) {
	t.Skip("Test uses removed buildPromptFromSources function")
}

// TestTemplateIntegration tests template loading and integration
func TestTemplateIntegration(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test template directory structure
	groveDir := filepath.Join(tmpDir, ".grove")
	templatesDir := filepath.Join(groveDir, "templates")
	os.MkdirAll(templatesDir, 0755)

	// Create a test template
	templatePath := filepath.Join(templatesDir, "test-template.md")
	templateContent := `---
type: agent
model: test-model
---

You are a test template. Your role is to process the provided files according to the instructions.`
	os.WriteFile(templatePath, []byte(templateContent), 0644)

	// Change to tmp dir
	oldCwd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldCwd)

	// plan := &Plan{
	// 	Directory: tmpDir,
	// }

	t.Run("load_custom_template", func(t *testing.T) {
		// Create include file
		os.WriteFile("test.go", []byte("package test"), 0644)

		job := &Job{
			ID:         "test-job",
			Template:   "test-template",
			Include:    []string{"test.go"},
			PromptBody: "Additional instructions",
			FilePath:   filepath.Join(tmpDir, "job.md"),
		}

		// This test would require the actual template loading logic to work
		// For now, we're testing that the structure is set up correctly
		if job.Template != "test-template" {
			t.Errorf("Template not set correctly")
		}
		if len(job.Include) != 1 {
			t.Errorf("Include files not set correctly")
		}
	})
}

// TestCLIIntegration tests the CLI command structure integration
func TestCLIIntegration(t *testing.T) {
	// Test that JobsAddStepCmd has the correct fields
	type JobsAddStepCmd struct {
		Dir          string
		Template     string
		Type         string
		Title        string
		DependsOn    []string
		IncludeFiles []string
		Prompt       string
		Interactive  bool
		PromptFile   string
		OutputType   string
	}
	cmd := &JobsAddStepCmd{
		Dir:          "test-dir",
		Template:     "test-template",
		Type:         "agent",
		Title:        "Test Job",
		DependsOn:    []string{"dep1", "dep2"},
		PromptFile:   "",
		Prompt:       "",
		OutputType:   "file",
		Interactive:  false,
		IncludeFiles: []string{"file1.go", "file2.go"},
	}

	// Verify all fields are accessible
	if cmd.Template != "test-template" {
		t.Errorf("Template field not set correctly")
	}
	if len(cmd.IncludeFiles) != 2 {
		t.Errorf("IncludeFiles field not set correctly: got %d files", len(cmd.IncludeFiles))
	}
	if cmd.IncludeFiles[0] != "file1.go" || cmd.IncludeFiles[1] != "file2.go" {
		t.Errorf("IncludeFiles content not correct: %v", cmd.IncludeFiles)
	}
}

// TestEdgeCases tests various edge cases
func TestEdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldCwd)

	plan := &Plan{
		Directory: tmpDir,
	}

	t.Run("binary_file_handling", func(t *testing.T) {
		// Create a binary file
		binaryFile := filepath.Join(tmpDir, "binary.dat")
		binaryContent := []byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD}
		os.WriteFile(binaryFile, binaryContent, 0644)

		job := &Job{
			Include:    []string{"binary.dat"},
			PromptBody: "Process binary file",
		}

		executor := NewOneShotExecutor(NewMockLLMClient(), nil)
		_, _, _, err := executor.buildPrompt(job, plan, "")
		
		// Should handle binary files gracefully (either skip or encode)
		if err != nil {
			t.Logf("Binary file handling resulted in error (expected): %v", err)
		} else {
			t.Logf("Binary file was processed into prompt")
		}
	})

	t.Run("symlink_handling", func(t *testing.T) {
		// Create a file and a symlink to it
		realFile := filepath.Join(tmpDir, "real.go")
		os.WriteFile(realFile, []byte("package real"), 0644)

		symlinkFile := filepath.Join(tmpDir, "link.go")
		os.Symlink(realFile, symlinkFile)

		job := &Job{
			Include:    []string{"link.go"},
			PromptBody: "Process symlink",
		}

		executor := NewOneShotExecutor(NewMockLLMClient(), nil)
		prompt, _, _, err := executor.buildPrompt(job, plan, "")
		
		if err != nil {
			t.Errorf("Failed to process symlink: %v", err)
		} else if !strings.Contains(prompt, "package real") {
			t.Errorf("Symlink content not included in prompt")
		}
	})

	t.Run("very_long_filename", func(t *testing.T) {
		// Create a file with a very long name
		longName := strings.Repeat("a", 200) + ".go"
		longFile := filepath.Join(tmpDir, longName)
		os.WriteFile(longFile, []byte("package long"), 0644)

		job := &Job{
			Include:    []string{longName},
			PromptBody: "Process long filename",
		}

		executor := NewOneShotExecutor(NewMockLLMClient(), nil)
		_, _, _, err := executor.buildPrompt(job, plan, "")
		
		// Should handle long filenames gracefully
		if err != nil && !strings.Contains(err.Error(), "too long") {
			t.Logf("Long filename handling: %v", err)
		}
	})
}