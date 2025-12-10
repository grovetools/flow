package orchestration

import (
	"strings"
	"testing"
)

func TestCodexAgentProvider_buildAgentCommand(t *testing.T) {
	provider := NewCodexAgentProvider()
	plan := &Plan{Directory: "/test/plan"}
	briefingPath := "/test/plan/.artifacts/briefing-test-job-123.xml"
	agentArgs := []string{"--arg1", "value1"}

	// Test case 1: Standard launch
	job1 := &Job{ID: "test-job", Type: JobTypeInteractiveAgent}
	cmd1, err1 := provider.buildAgentCommand(job1, plan, briefingPath, agentArgs)
	if err1 != nil {
		t.Fatalf("Test 1 failed: %v", err1)
	}
	if !strings.Contains(cmd1, "codex --arg1 value1") {
		t.Errorf("Test 1: command should contain codex with args. Got: %s", cmd1)
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
}
