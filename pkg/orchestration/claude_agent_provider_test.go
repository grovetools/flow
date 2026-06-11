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

	// Test case 3: per-job model+effort flow through appendClaudeJobArgs into the command
	job3 := &Job{ID: "test-job-3", Type: JobTypeInteractiveAgent, Model: "opus", Effort: "high"}
	args3, err3 := appendClaudeJobArgs(nil, job3, plan)
	if err3 != nil {
		t.Fatalf("Test 3: appendClaudeJobArgs failed: %v", err3)
	}
	cmd3, err3 := provider.buildAgentCommand(job3, plan, briefingPath, args3)
	if err3 != nil {
		t.Fatalf("Test 3 failed: %v", err3)
	}
	if !strings.Contains(cmd3, "claude --model opus --effort high") {
		t.Errorf("Test 3: command should contain per-job model and effort flags. Got: %s", cmd3)
	}
}

func TestAppendClaudeJobArgs(t *testing.T) {
	baseArgs := []string{"--dangerously-skip-permissions"}

	t.Run("no model or effort leaves args untouched", func(t *testing.T) {
		args, err := appendClaudeJobArgs(baseArgs, &Job{ID: "j"}, &Plan{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(args) != 1 || args[0] != "--dangerously-skip-permissions" {
			t.Errorf("expected args unchanged, got %v", args)
		}
	})

	t.Run("job model appended", func(t *testing.T) {
		args, err := appendClaudeJobArgs(baseArgs, &Job{ID: "j", Model: "opus"}, &Plan{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"--dangerously-skip-permissions", "--model", "opus"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Errorf("expected %v, got %v", want, args)
		}
	})

	t.Run("model falls back to plan config", func(t *testing.T) {
		plan := &Plan{Config: &PlanConfig{Model: "plan-default"}}
		args, err := appendClaudeJobArgs(nil, &Job{ID: "j"}, plan)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"--model", "plan-default"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Errorf("expected %v, got %v", want, args)
		}
	})

	t.Run("job model wins over plan config", func(t *testing.T) {
		plan := &Plan{Config: &PlanConfig{Model: "plan-default"}}
		args, err := appendClaudeJobArgs(nil, &Job{ID: "j", Model: "opus"}, plan)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"--model", "opus"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Errorf("expected %v, got %v", want, args)
		}
	})

	t.Run("effort appended after model", func(t *testing.T) {
		args, err := appendClaudeJobArgs(nil, &Job{ID: "j", Model: "opus", Effort: "low"}, &Plan{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"--model", "opus", "--effort", "low"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Errorf("expected %v, got %v", want, args)
		}
	})

	t.Run("effort alone appended without model flag", func(t *testing.T) {
		args, err := appendClaudeJobArgs(nil, &Job{ID: "j", Effort: "high"}, &Plan{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"--effort", "high"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Errorf("expected %v, got %v", want, args)
		}
	})

	t.Run("shell-unsafe values rejected", func(t *testing.T) {
		if _, err := appendClaudeJobArgs(nil, &Job{ID: "j", Model: "opus; rm -rf /"}, &Plan{}); err == nil {
			t.Error("expected error for shell-unsafe model")
		}
		if _, err := appendClaudeJobArgs(nil, &Job{ID: "j", Effort: "high$(whoami)"}, &Plan{}); err == nil {
			t.Error("expected error for shell-unsafe effort")
		}
		if _, err := appendClaudeJobArgs(nil, &Job{ID: "j", Model: "opus[1m]"}, &Plan{}); err == nil {
			t.Error("expected error for glob characters in model")
		}
	})

	t.Run("does not mutate the shared provider args slice", func(t *testing.T) {
		shared := make([]string, 1, 4)
		shared[0] = "--dangerously-skip-permissions"
		args, err := appendClaudeJobArgs(shared, &Job{ID: "j", Model: "opus"}, &Plan{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if &shared[0] == &args[0] {
			t.Error("expected a copied slice when appending per-job flags")
		}
	})
}
