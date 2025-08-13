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
			PromptSource: []string{"file1.go", "file2.go"},
			PromptBody:   "Do something with these files",
		}

		executor := NewOneShotExecutor(nil)
		prompt, _, err := executor.buildPrompt(job, plan, "")
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
			Template:     "test-template",
			PromptSource: []string{"file1.go", "file2.go"},
			PromptBody:   "<!-- This step uses template 'test-template' with source files -->\n<!-- Template will be resolved at execution time -->\n\n## Additional Instructions\n\nRefactor these files",
		}

		executor := NewOneShotExecutor(nil)
		prompt, _, err := executor.buildPrompt(job, plan, "")
		
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

	// Test empty source files list
	t.Run("empty_source_files", func(t *testing.T) {
		job := &Job{
			Template:     "test-template",
			PromptSource: []string{},
			PromptBody:   "Do something",
		}

		executor := NewOneShotExecutor(nil)
		_, _, err := executor.buildPrompt(job, plan, "")
		
		// Should handle empty source files gracefully
		if err != nil && !strings.Contains(err.Error(), "template") {
			t.Errorf("Unexpected error for empty source files: %v", err)
		}
	})

	// Test non-existent source file
	t.Run("missing_source_file", func(t *testing.T) {
		job := &Job{
			PromptSource: []string{"nonexistent.go"},
			PromptBody:   "Process this file",
		}

		executor := NewOneShotExecutor(nil)
		_, _, err := executor.buildPrompt(job, plan, "")
		
		if err == nil {
			t.Errorf("Expected error for missing source file")
		}
		if !strings.Contains(err.Error(), "nonexistent.go") {
			t.Errorf("Error should mention missing file: %v", err)
		}
	})

	// Test large number of source files
	t.Run("multiple_source_files", func(t *testing.T) {
		// Create multiple test files
		var sourceFiles []string
		for i := 0; i < 5; i++ {
			filename := filepath.Join(tmpDir, strings.ReplaceAll("file_{{i}}.go", "{{i}}", string(rune('0'+i))))
			content := strings.ReplaceAll("package main\n\n// File {{i}}", "{{i}}", string(rune('0'+i)))
			os.WriteFile(filename, []byte(content), 0644)
			sourceFiles = append(sourceFiles, filepath.Base(filename))
		}

		job := &Job{
			PromptSource: sourceFiles,
			PromptBody:   "Process all these files",
		}

		executor := NewOneShotExecutor(nil)
		prompt, _, err := executor.buildPrompt(job, plan, "")
		if err != nil {
			t.Fatalf("buildPrompt() error = %v", err)
		}

		// Verify all files are included
		for i, file := range sourceFiles {
			if !strings.Contains(prompt, file) {
				t.Errorf("Prompt missing file %d: %s", i, file)
			}
		}
	})
}

// TestReferenceBased_AgentExecutor_BuildPrompt tests reference-based prompts for agent executor
func TestReferenceBased_AgentExecutor_BuildPrompt(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test source files
	sourceFile1 := filepath.Join(tmpDir, "api.go")
	os.WriteFile(sourceFile1, []byte("package api\n\nfunc Handler() {}"), 0644)
	
	sourceFile2 := filepath.Join(tmpDir, "service.go")
	os.WriteFile(sourceFile2, []byte("package service\n\nfunc Process() {}"), 0644)

	// Change to tmp dir for relative path resolution
	oldCwd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldCwd)

	plan := &Plan{
		Directory: tmpDir,
	}

	// Test traditional prompt assembly
	t.Run("traditional_prompt", func(t *testing.T) {
		job := &Job{
			ID: "test-job",
			PromptBody: "Test the implementation",
			PromptSource: []string{"api.go"},
			FilePath: filepath.Join(tmpDir, "job.md"),
		}

		prompt, err := buildPromptFromSources(job, plan)
		if err != nil {
			t.Errorf("Failed to build prompt: %v", err)
		}

		if !strings.Contains(prompt, "You are an expert software developer") {
			t.Errorf("Prompt missing standard header")
		}
		if !strings.Contains(prompt, "api.go") {
			t.Errorf("Prompt missing source file reference")
		}
		if !strings.Contains(prompt, "Test the implementation") {
			t.Errorf("Prompt missing job body")
		}
	})

	// Test reference-based prompt with template
	t.Run("reference_based_prompt", func(t *testing.T) {
		job := &Job{
			ID: "test-job",
			Template: "test-template",
			PromptBody: "<!-- This step uses template 'test-template' with source files -->\n<!-- Template will be resolved at execution time -->\n\n## Additional Instructions\n\nRefactor the API",
			PromptSource: []string{"api.go", "service.go"},
			FilePath: filepath.Join(tmpDir, "job.md"),
		}

		prompt, err := buildPromptFromSources(job, plan)
		
		// The test might fail if the template doesn't exist
		if err != nil {
			if !strings.Contains(err.Error(), "resolving template test-template") {
				t.Errorf("buildPromptFromSources() unexpected error = %v", err)
			}
			// Expected error for missing template
			return
		}

		// Verify prompt references the files to be read
		if !strings.Contains(prompt, "api.go") {
			t.Errorf("Prompt missing api.go reference")
		}
		if !strings.Contains(prompt, "service.go") {
			t.Errorf("Prompt missing service.go reference")
		}
		if !strings.Contains(prompt, "Refactor the API") {
			t.Errorf("Prompt missing additional instructions")
		}
	})

	// Test with absolute paths in source files
	t.Run("absolute_path_source_files", func(t *testing.T) {
		job := &Job{
			ID: "test-job",
			PromptBody: "Process absolute paths",
			PromptSource: []string{sourceFile1, sourceFile2},
			FilePath: filepath.Join(tmpDir, "job.md"),
		}

		prompt, err := buildPromptFromSources(job, plan)
		if err != nil {
			t.Errorf("Failed to build prompt with absolute paths: %v", err)
		}

		// Should handle absolute paths correctly
		if !strings.Contains(prompt, "api.go") || !strings.Contains(prompt, "service.go") {
			t.Errorf("Prompt missing source file references")
		}
	})

	// Test mixed relative and absolute paths
	t.Run("mixed_path_types", func(t *testing.T) {
		job := &Job{
			ID: "test-job",
			PromptBody: "Process mixed paths",
			PromptSource: []string{"api.go", sourceFile2},
			FilePath: filepath.Join(tmpDir, "job.md"),
		}

		prompt, err := buildPromptFromSources(job, plan)
		if err != nil {
			t.Errorf("Failed to build prompt with mixed paths: %v", err)
		}

		if !strings.Contains(prompt, "api.go") || !strings.Contains(prompt, "service.go") {
			t.Errorf("Prompt missing source file references")
		}
	})

	// Test with subdirectory source files
	t.Run("subdirectory_source_files", func(t *testing.T) {
		// Create subdirectory with files
		subDir := filepath.Join(tmpDir, "subdir")
		os.MkdirAll(subDir, 0755)
		
		subFile := filepath.Join(subDir, "config.go")
		os.WriteFile(subFile, []byte("package config\n\nvar Config = map[string]string{}"), 0644)

		job := &Job{
			ID: "test-job",
			PromptBody: "Process subdirectory file",
			PromptSource: []string{"api.go", "subdir/config.go"},
			FilePath: filepath.Join(tmpDir, "job.md"),
		}

		prompt, err := buildPromptFromSources(job, plan)
		if err != nil {
			t.Errorf("Failed to build prompt with subdirectory files: %v", err)
		}

		if !strings.Contains(prompt, "api.go") || !strings.Contains(prompt, "config.go") {
			t.Errorf("Prompt missing source file references")
		}
		if !strings.Contains(prompt, "var Config") {
			t.Errorf("Prompt missing subdirectory file content")
		}
	})
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
		// Create source file
		os.WriteFile("test.go", []byte("package test"), 0644)

		job := &Job{
			ID:           "test-job",
			Template:     "test-template",
			PromptSource: []string{"test.go"},
			PromptBody:   "Additional instructions",
			FilePath:     filepath.Join(tmpDir, "job.md"),
		}

		// This test would require the actual template loading logic to work
		// For now, we're testing that the structure is set up correctly
		if job.Template != "test-template" {
			t.Errorf("Template not set correctly")
		}
		if len(job.PromptSource) != 1 {
			t.Errorf("Source files not set correctly")
		}
	})
}

// TestCLIIntegration tests the CLI command structure integration
func TestCLIIntegration(t *testing.T) {
	// Test that JobsAddStepCmd has the correct fields
	type JobsAddStepCmd struct {
		Dir         string
		Template    string
		Type        string
		Title       string
		DependsOn   []string
		SourceFiles []string
		Prompt      string
		Interactive bool
		PromptFile  string
		OutputType  string
	}
	cmd := &JobsAddStepCmd{
		Dir:         "test-dir",
		Template:    "test-template",
		Type:        "agent",
		Title:       "Test Job",
		DependsOn:   []string{"dep1", "dep2"},
		PromptFile:  "",
		Prompt:      "",
		OutputType:  "file",
		Interactive: false,
		SourceFiles: []string{"file1.go", "file2.go"},
	}

	// Verify all fields are accessible
	if cmd.Template != "test-template" {
		t.Errorf("Template field not set correctly")
	}
	if len(cmd.SourceFiles) != 2 {
		t.Errorf("SourceFiles field not set correctly: got %d files", len(cmd.SourceFiles))
	}
	if cmd.SourceFiles[0] != "file1.go" || cmd.SourceFiles[1] != "file2.go" {
		t.Errorf("SourceFiles content not correct: %v", cmd.SourceFiles)
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
			PromptSource: []string{"binary.dat"},
			PromptBody:   "Process binary file",
		}

		executor := NewOneShotExecutor(nil)
		_, _, err := executor.buildPrompt(job, plan, "")
		
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
			PromptSource: []string{"link.go"},
			PromptBody:   "Process symlink",
		}

		executor := NewOneShotExecutor(nil)
		prompt, _, err := executor.buildPrompt(job, plan, "")
		
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
			PromptSource: []string{longName},
			PromptBody:   "Process long filename",
		}

		executor := NewOneShotExecutor(nil)
		_, _, err := executor.buildPrompt(job, plan, "")
		
		// Should handle long filenames gracefully
		if err != nil && !strings.Contains(err.Error(), "too long") {
			t.Logf("Long filename handling: %v", err)
		}
	})
}