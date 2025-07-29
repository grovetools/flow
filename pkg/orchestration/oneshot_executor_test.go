package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOneShotExecutor_Execute(t *testing.T) {
	// Create temporary directory for test
	tmpDir := t.TempDir()

	// Create test plan
	plan := &Plan{
		Directory: tmpDir,
		Jobs:      make(map[string]*Job),
		JobsByID:  make(map[string]*Job),
	}

	// Create spec file
	specPath := filepath.Join(tmpDir, "spec.md")
	specContent := "# Test Specification\n\nImplement feature X."
	if err := os.WriteFile(specPath, []byte(specContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create job file
	jobContent := `---
id: test-job-123
title: Test Job
status: pending
type: oneshot
prompt_source:
  - spec.md
output:
  type: file
---
Create a plan based on the spec.`

	jobPath := filepath.Join(tmpDir, "01-test-job.md")
	if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Load the job
	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = "01-test-job.md"
	job.FilePath = jobPath

	// Create executor
	config := &Config{
		MaxPromptLength: 10000,
		Timeout:         1 * time.Minute,
	}
	executor := NewOneShotExecutor(config)

	// Execute the job
	ctx := context.Background()
	err = executor.Execute(ctx, job, plan)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Verify job status was updated
	if job.Status != JobStatusCompleted {
		t.Errorf("Job status = %v, want completed", job.Status)
	}

	// Verify job file was updated
	updatedContent, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedContent), "status: completed") {
		t.Errorf("Job file not updated with completed status")
	}
}

func TestOneShotExecutor_BuildPrompt(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	spec1 := filepath.Join(tmpDir, "spec1.md")
	os.WriteFile(spec1, []byte("Spec 1 content"), 0644)

	spec2 := filepath.Join(tmpDir, "spec2.md")
	os.WriteFile(spec2, []byte("Spec 2 content"), 0644)

	plan := &Plan{
		Directory: tmpDir,
	}

	job := &Job{
		PromptSource: []string{"spec1.md", "spec2.md"},
		PromptBody:   "Do something with these specs",
	}

	executor := NewOneShotExecutor(nil)
	prompt, err := executor.buildPrompt(job, plan)
	if err != nil {
		t.Fatalf("buildPrompt() error = %v", err)
	}

	// Verify prompt contains all sources
	if !strings.Contains(prompt, "Spec 1 content") {
		t.Errorf("Prompt missing spec1 content")
	}
	if !strings.Contains(prompt, "Spec 2 content") {
		t.Errorf("Prompt missing spec2 content")
	}
	if !strings.Contains(prompt, "Do something with these specs") {
		t.Errorf("Prompt missing job body")
	}
}

func TestOneShotExecutor_BuildPrompt_ReferenceBasedPrompts(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test source files
	file1 := filepath.Join(tmpDir, "file1.go")
	os.WriteFile(file1, []byte("package main\n\nfunc main() {}"), 0644)

	file2 := filepath.Join(tmpDir, "file2.go")
	os.WriteFile(file2, []byte("package main\n\nfunc helper() {}"), 0644)

	plan := &Plan{
		Directory: tmpDir,
	}

	// Test reference-based job with template
	job := &Job{
		Template:     "agent-run",
		PromptSource: []string{"file1.go", "file2.go"},
		PromptBody:   "<!-- This step uses template 'agent-run' with source files -->\n<!-- Template will be resolved at execution time -->\n\n## Additional Instructions\n\nRefactor these files",
	}

	executor := NewOneShotExecutor(nil)
	prompt, err := executor.buildPrompt(job, plan)
	if err != nil {
		// The test might fail if the template doesn't exist, but we can check
		// if it's trying to use the reference-based path
		if !strings.Contains(err.Error(), "resolving template agent-run") {
			t.Fatalf("buildPrompt() unexpected error = %v", err)
		}
		return
	}

	// Verify prompt structure for reference-based prompts
	if !strings.Contains(prompt, "System Instructions (from template: agent-run)") {
		t.Errorf("Prompt missing template header")
	}
	if !strings.Contains(prompt, "--- START OF file1.go ---") {
		t.Errorf("Prompt missing file1.go separator")
	}
	if !strings.Contains(prompt, "--- END OF file1.go ---") {
		t.Errorf("Prompt missing file1.go end separator")
	}
	if !strings.Contains(prompt, "--- START OF file2.go ---") {
		t.Errorf("Prompt missing file2.go separator")
	}
	if !strings.Contains(prompt, "package main") {
		t.Errorf("Prompt missing file content")
	}
	if !strings.Contains(prompt, "Refactor these files") {
		t.Errorf("Prompt missing additional instructions")
	}
}

func TestOneShotExecutor_ProcessOutput(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name       string
		outputType string
		outputPath string
		output     string
		verify     func(t *testing.T, job *Job, plan *Plan)
	}{
		{
			name:       "file output with path",
			outputType: "file",
			outputPath: "output/result.md",
			output:     "# Result\n\nThis is the output",
			verify: func(t *testing.T, job *Job, plan *Plan) {
				outputFile := filepath.Join(plan.Directory, "output/result.md")
				content, err := os.ReadFile(outputFile)
				if err != nil {
					t.Errorf("Output file not created: %v", err)
					return
				}
				if string(content) != "# Result\n\nThis is the output" {
					t.Errorf("Output file content mismatch")
				}
			},
		},
		{
			name:       "file output no path",
			outputType: "file",
			outputPath: "",
			output:     "Appended output",
			verify: func(t *testing.T, job *Job, plan *Plan) {
				content, err := os.ReadFile(job.FilePath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(content), "## Output") {
					t.Errorf("Output not appended to job file")
				}
				if !strings.Contains(string(content), "Appended output") {
					t.Errorf("Output content not found in job file")
				}
			},
		},
		{
			name:       "none output",
			outputType: "none",
			output:     "This should be ignored",
			verify: func(t *testing.T, job *Job, plan *Plan) {
				content, err := os.ReadFile(job.FilePath)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(content), "This should be ignored") {
					t.Errorf("Output was processed when it should be ignored")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir := filepath.Join(tmpDir, tt.name)
			os.MkdirAll(testDir, 0755)

			plan := &Plan{Directory: testDir}

			jobPath := filepath.Join(testDir, "job.md")
			jobContent := `---
id: test
title: Test
status: running
type: oneshot
---
Body`
			os.WriteFile(jobPath, []byte(jobContent), 0644)

			job := &Job{
				FilePath: jobPath,
				Output: OutputConfig{
					Type: tt.outputType,
					Path: tt.outputPath,
				},
			}

			executor := NewOneShotExecutor(nil)
			err := executor.processOutput(tt.output, job, plan)
			if err != nil {
				t.Fatalf("processOutput() error = %v", err)
			}

			tt.verify(t, job, plan)
		})
	}
}

func TestMockLLMClient(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mock response file
	mockFile := filepath.Join(tmpDir, "mock_response.txt")
	mockContent := "This is a mock response"
	os.WriteFile(mockFile, []byte(mockContent), 0644)

	// Set environment variable
	os.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockFile)
	defer os.Unsetenv("GROVE_MOCK_LLM_RESPONSE_FILE")

	client := NewMockLLMClient()
	response, err := client.Complete(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	if response != mockContent {
		t.Errorf("Complete() = %v, want %v", response, mockContent)
	}
}

func TestMockLLMClient_SplitByFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mock response file with job definitions
	mockFile := filepath.Join(tmpDir, "mock_plan.md")
	mockContent := `Based on the spec, here's the plan:

## Overview
We'll implement this in two steps.

---
id: step-1
title: First Step
status: pending
type: agent
---
Implement the first part.

---
id: step-2
title: Second Step
status: pending
type: agent
depends_on:
  - 02-generated-job.md
---
Implement the second part.`

	os.WriteFile(mockFile, []byte(mockContent), 0644)

	// Set environment variables
	os.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockFile)
	os.Setenv("GROVE_MOCK_LLM_OUTPUT_MODE", "split_by_frontmatter")
	os.Setenv("GROVE_CURRENT_JOB_PATH", filepath.Join(tmpDir, "01-initial.md"))
	defer func() {
		os.Unsetenv("GROVE_MOCK_LLM_RESPONSE_FILE")
		os.Unsetenv("GROVE_MOCK_LLM_OUTPUT_MODE")
		os.Unsetenv("GROVE_CURRENT_JOB_PATH")
	}()

	client := NewMockLLMClient()
	response, err := client.Complete(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	// Verify main response
	if !strings.Contains(response, "Based on the spec") {
		t.Errorf("Main response missing expected content")
	}

	// Verify job files were created
	job1Path := filepath.Join(tmpDir, "02-generated-job.md")
	if _, err := os.Stat(job1Path); err != nil {
		t.Errorf("First job file not created: %v", err)
	}

	job2Path := filepath.Join(tmpDir, "03-generated-job.md")
	if _, err := os.Stat(job2Path); err != nil {
		t.Errorf("Second job file not created: %v", err)
	}

	// Verify job content
	job1Content, _ := os.ReadFile(job1Path)
	if !strings.Contains(string(job1Content), "id: step-1") {
		t.Errorf("First job missing expected ID")
	}
}

func TestOneShotExecutor_ErrorHandling(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		setup     func() (*Job, *Plan)
		wantErr   string
		wantStatus JobStatus
	}{
		{
			name: "missing prompt source",
			setup: func() (*Job, *Plan) {
				jobPath := filepath.Join(tmpDir, "job1.md")
				jobContent := `---
id: test1
title: Test
status: pending
type: oneshot
prompt_source:
  - missing.md
---
Body`
				os.WriteFile(jobPath, []byte(jobContent), 0644)
				job, _ := LoadJob(jobPath)
				job.FilePath = jobPath
				plan := &Plan{Directory: tmpDir}
				return job, plan
			},
			wantErr:    "reading prompt source",
			wantStatus: JobStatusFailed,
		},
		{
			name: "prompt too long",
			setup: func() (*Job, *Plan) {
				jobPath := filepath.Join(tmpDir, "job2.md")
				jobContent := `---
id: test2
title: Test
status: pending
type: oneshot
---
Body`
				os.WriteFile(jobPath, []byte(jobContent), 0644)
				job, _ := LoadJob(jobPath)
				job.FilePath = jobPath
				job.PromptBody = strings.Repeat("x", 200) // Exceeds test limit
				plan := &Plan{Directory: tmpDir}
				return job, plan
			},
			wantErr:    "prompt exceeds maximum length",
			wantStatus: JobStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, plan := tt.setup()

			config := &Config{
				MaxPromptLength: 100, // Small limit for testing
			}
			executor := NewOneShotExecutor(config)

			err := executor.Execute(context.Background(), job, plan)
			if err == nil {
				t.Errorf("Execute() expected error containing %q, got nil", tt.wantErr)
			} else if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Execute() error = %v, want error containing %q", err, tt.wantErr)
			}

			if job.Status != tt.wantStatus {
				t.Errorf("Job status = %v, want %v", job.Status, tt.wantStatus)
			}
		})
	}
}