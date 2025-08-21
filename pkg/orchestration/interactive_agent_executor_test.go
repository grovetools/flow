package orchestration

import (
	"strings"
	"testing"
)

func TestBuildAgentCommandWithContinue(t *testing.T) {
	executor := &InteractiveAgentExecutor{}
	
	plan := &Plan{
		Directory: "/test/plan",
	}
	
	// Test with AgentContinue = true
	job := &Job{
		ID:            "test-job",
		Title:         "Test Job",
		Type:          JobTypeInteractiveAgent,
		AgentContinue: true,
		FilePath:      "/test/plan/01-test-job.md",
	}
	
	agentArgs := []string{"--model", "claude-3-5-sonnet"}
	
	cmd, err := executor.buildAgentCommand(job, plan, "/test/worktree", agentArgs)
	if err != nil {
		t.Fatalf("Failed to build agent command: %v", err)
	}
	
	// Check that the command is in the echo | claude format
	if !strings.HasPrefix(cmd, "echo '") {
		t.Errorf("Command should start with echo, got: %s", cmd)
	}
	if !strings.Contains(cmd, " | claude --continue ") {
		t.Errorf("Command should pipe to claude with --continue, got: %s", cmd)
	}
	
	// Test with AgentContinue = false
	job2 := &Job{
		ID:            "test-job-2",
		Title:         "Test Job 2",
		Type:          JobTypeInteractiveAgent,
		AgentContinue: false,
		FilePath:      "/test/plan/02-test-job-2.md",
	}
	
	cmd2, err := executor.buildAgentCommand(job2, plan, "/test/worktree", agentArgs)
	if err != nil {
		t.Fatalf("Failed to build agent command: %v", err)
	}
	
	// Check that the command does NOT use echo | ...
	if strings.HasPrefix(cmd2, "echo '") {
		t.Errorf("Command should not start with echo, got: %s", cmd2)
	}
	if strings.Contains(cmd2, "--continue") {
		t.Errorf("Command should NOT contain --continue flag, got: %s", cmd2)
	}
}