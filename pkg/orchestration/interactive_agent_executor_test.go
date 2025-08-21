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
	
	// Check that the command contains --continue
	if !strings.Contains(cmd, " --continue ") {
		t.Errorf("Command should contain --continue flag, got: %s", cmd)
	}
	
	// Check that --continue comes before other args
	if !strings.Contains(cmd, "claude --continue --model") {
		t.Errorf("--continue should come right after claude, got: %s", cmd)
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
	
	// Check that the command does NOT contain --continue
	if strings.Contains(cmd2, "--continue") {
		t.Errorf("Command should NOT contain --continue flag, got: %s", cmd2)
	}
}