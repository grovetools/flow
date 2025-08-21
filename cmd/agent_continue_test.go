package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

func TestPlanAddAgentContinueFlag(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir, err := os.MkdirTemp("", "flow-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test plan
	plan := &orchestration.Plan{
		Directory: tmpDir,
		Jobs:      []*orchestration.Job{},
		JobsByID:  make(map[string]*orchestration.Job),
	}

	// Test adding a job with AgentContinue = true
	cmd := &PlanAddStepCmd{
		Dir:           tmpDir,
		Title:         "Test Job with Continue",
		Type:          "interactive_agent",
		AgentContinue: true,
		Prompt:        "Test prompt",
	}

	job, err := collectJobDetails(cmd, plan, "")
	if err != nil {
		t.Fatalf("Failed to collect job details: %v", err)
	}

	if !job.AgentContinue {
		t.Error("Job should have AgentContinue = true")
	}

	// Test adding a job without AgentContinue flag
	cmd2 := &PlanAddStepCmd{
		Dir:           tmpDir,
		Title:         "Test Job without Continue",
		Type:          "interactive_agent",
		AgentContinue: false,
		Prompt:        "Test prompt",
	}

	job2, err := collectJobDetails(cmd2, plan, "")
	if err != nil {
		t.Fatalf("Failed to collect job details: %v", err)
	}

	if job2.AgentContinue {
		t.Error("Job should have AgentContinue = false")
	}
}

func TestAgentJobTemplateWithContinue(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "flow-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a plan
	plan := &orchestration.Plan{
		Directory: tmpDir,
		Jobs:      []*orchestration.Job{},
		JobsByID:  make(map[string]*orchestration.Job),
	}

	// Create a job with AgentContinue = true
	job := &orchestration.Job{
		ID:            "test-continue",
		Title:         "Test Continue Job",
		Type:          orchestration.JobTypeInteractiveAgent,
		Status:        orchestration.JobStatusPending,
		AgentContinue: true,
		PromptBody:    "Test prompt",
		Output: orchestration.OutputConfig{
			Type: "file",
		},
	}

	// Add the job to the plan
	filename, err := orchestration.AddJob(plan, job)
	if err != nil {
		t.Fatalf("Failed to add job: %v", err)
	}

	// Read the generated file
	content, err := os.ReadFile(filepath.Join(tmpDir, filename))
	if err != nil {
		t.Fatalf("Failed to read job file: %v", err)
	}

	// Check that agent_continue: true is in the file
	if !strings.Contains(string(content), "agent_continue: true") {
		t.Errorf("Job file should contain 'agent_continue: true', got:\n%s", string(content))
	}
}