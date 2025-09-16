package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

func TestAutoEnableAgentContinue(t *testing.T) {
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

	// Test 1: First interactive_agent job should NOT have agent_continue
	cmd1 := &PlanAddStepCmd{
		Dir:           tmpDir,
		Title:         "First Interactive Agent",
		Type:          "interactive_agent",
		AgentContinue: false, // Not explicitly set by user
		Prompt:        "First job",
	}

	job1, err := collectJobDetails(cmd1, plan, "")
	if err != nil {
		t.Fatalf("Failed to collect job details for first job: %v", err)
	}

	if job1.AgentContinue {
		t.Error("First interactive_agent job should NOT have AgentContinue = true")
	}

	// Add the first job to the plan
	plan.Jobs = append(plan.Jobs, job1)

	// Test 2: Second interactive_agent job should automatically have agent_continue = true
	cmd2 := &PlanAddStepCmd{
		Dir:           tmpDir,
		Title:         "Second Interactive Agent",
		Type:          "interactive_agent",
		AgentContinue: false, // Not explicitly set by user
		Prompt:        "Second job",
	}

	job2, err := collectJobDetails(cmd2, plan, "")
	if err != nil {
		t.Fatalf("Failed to collect job details for second job: %v", err)
	}

	if !job2.AgentContinue {
		t.Error("Second interactive_agent job should automatically have AgentContinue = true")
	}

	// Test 3: User can still explicitly set agent_continue = true on first job
	plan2 := &orchestration.Plan{
		Directory: tmpDir,
		Jobs:      []*orchestration.Job{},
		JobsByID:  make(map[string]*orchestration.Job),
	}

	cmd3 := &PlanAddStepCmd{
		Dir:           tmpDir,
		Title:         "First Job with Explicit Continue",
		Type:          "interactive_agent",
		AgentContinue: true, // Explicitly set by user
		Prompt:        "First job with continue",
	}

	job3, err := collectJobDetails(cmd3, plan2, "")
	if err != nil {
		t.Fatalf("Failed to collect job details for job with explicit continue: %v", err)
	}

	if !job3.AgentContinue {
		t.Error("Job with explicit AgentContinue = true should keep that value")
	}

	// Test 4 (removed): The distinction between 'agent' and 'interactive_agent'
	// for auto-continue logic is no longer relevant as they are now aliases.
}

func TestAutoEnableAgentContinueIntegration(t *testing.T) {
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

	// Add first interactive_agent job
	job1 := &orchestration.Job{
		ID:            "first-agent",
		Title:         "First Agent",
		Type:          orchestration.JobTypeInteractiveAgent,
		Status:        orchestration.JobStatusPending,
		AgentContinue: false,
		PromptBody:    "First job",
		Output: orchestration.OutputConfig{
			Type: "file",
		},
	}

	filename1, err := orchestration.AddJob(plan, job1)
	if err != nil {
		t.Fatalf("Failed to add first job: %v", err)
	}

	// Read the first job file
	content1, err := os.ReadFile(filepath.Join(tmpDir, filename1))
	if err != nil {
		t.Fatalf("Failed to read first job file: %v", err)
	}

	// First job should NOT have agent_continue
	if strings.Contains(string(content1), "agent_continue") {
		t.Errorf("First job file should NOT contain agent_continue, got:\n%s", string(content1))
	}

	// Now add second interactive_agent job through the command
	cmd := &PlanAddStepCmd{
		Dir:    tmpDir,
		Title:  "Second Agent Auto Continue",
		Type:   "interactive_agent",
		Prompt: "Second job that should auto-continue",
	}

	job2, err := collectJobDetails(cmd, plan, "")
	if err != nil {
		t.Fatalf("Failed to collect details for second job: %v", err)
	}

	// Second job should have agent_continue = true
	if !job2.AgentContinue {
		t.Error("Second interactive_agent job should have AgentContinue = true")
	}

	// Add it to the plan
	filename2, err := orchestration.AddJob(plan, job2)
	if err != nil {
		t.Fatalf("Failed to add second job: %v", err)
	}

	// Read the second job file
	content2, err := os.ReadFile(filepath.Join(tmpDir, filename2))
	if err != nil {
		t.Fatalf("Failed to read second job file: %v", err)
	}

	// Second job SHOULD have agent_continue: true
	if !strings.Contains(string(content2), "agent_continue: true") {
		t.Errorf("Second job file should contain 'agent_continue: true', got:\n%s", string(content2))
	}
}