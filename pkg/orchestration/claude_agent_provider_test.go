package orchestration

import (
	"strings"
	"testing"
)

func TestClaudeAgentProvider_buildAgentCommand(t *testing.T) {
	provider := NewClaudeAgentProvider()
	plan := &Plan{Directory: "/test/plan"}
	briefingPath := "/test/plan/.artifacts/briefing-test-job-123.xml"
	agentArgs := []string{"--model", "test-model"}

	// Test case 1: Standard launch
	job1 := &Job{ID: "test-job", Type: JobTypeInteractiveAgent}
	cmd1, err1 := provider.buildAgentCommand(job1, plan, briefingPath, agentArgs)
	if err1 != nil {
		t.Fatalf("Test 1 failed: %v", err1)
	}
	if !strings.Contains(cmd1, "claude --model test-model") {
		t.Errorf("Test 1: command should contain claude with args. Got: %s", cmd1)
	}
	if !strings.Contains(cmd1, "Read the briefing file at") {
		t.Errorf("Test 1: command should contain instruction to read briefing file. Got: %s", cmd1)
	}
	if !strings.Contains(cmd1, briefingPath) {
		t.Errorf("Test 1: command should reference briefing file path. Got: %s", cmd1)
	}
	if strings.Contains(cmd1, "--continue") {
		t.Errorf("Test 1: command should not contain --continue. Got: %s", cmd1)
	}

	// Test case 2: Path with special characters
	specialBriefingPath := "/test/plan/.artifacts/briefing' with spaces.xml"
	job2 := &Job{ID: "test-job-2", Type: JobTypeInteractiveAgent}
	cmd2, err2 := provider.buildAgentCommand(job2, plan, specialBriefingPath, agentArgs)
	if err2 != nil {
		t.Fatalf("Test 2 failed: %v", err2)
	}
	// Verify correct shell escaping: single quotes are escaped as '\''
	if !strings.Contains(cmd2, "'/test/plan/.artifacts/briefing'\\'' with spaces.xml'") {
		t.Errorf("Test 2: command did not correctly escape path. Got: %s", cmd2)
	}
}
