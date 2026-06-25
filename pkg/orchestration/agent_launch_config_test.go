package orchestration

import (
	"context"
	"strings"
	"testing"
)

// TestResolveProviderArgs asserts that NO provider receives an implicit default
// — claude in particular is never auto-given --dangerously-skip-permissions; the
// bypass is opt-in via grove.toml only.
func TestResolveProviderArgs(t *testing.T) {
	t.Run("claude gets no bypass default when unconfigured", func(t *testing.T) {
		args := resolveProviderArgs(FlowConfig{}, "claude")
		if len(args) != 0 {
			t.Errorf("expected no args (no implicit bypass), got %v", args)
		}
		for _, a := range args {
			if a == "--dangerously-skip-permissions" {
				t.Errorf("bypass must never be injected by default, got %v", args)
			}
		}
	})

	t.Run("claude opts into bypass via explicit args", func(t *testing.T) {
		// The bypass is available, but only when an operator configures it.
		cfg := FlowConfig{Providers: map[string]ProviderConfig{
			"claude": {Args: []string{"--dangerously-skip-permissions"}},
		}}
		args := resolveProviderArgs(cfg, "claude")
		if strings.Join(args, " ") != "--dangerously-skip-permissions" {
			t.Errorf("expected opt-in bypass, got %v", args)
		}
	})

	t.Run("claude uses configured args verbatim", func(t *testing.T) {
		cfg := FlowConfig{Providers: map[string]ProviderConfig{
			"claude": {Args: []string{"--model", "opus"}},
		}}
		args := resolveProviderArgs(cfg, "claude")
		if strings.Join(args, " ") != "--model opus" {
			t.Errorf("expected configured args, got %v", args)
		}
	})

	t.Run("non-claude provider gets no default either", func(t *testing.T) {
		args := resolveProviderArgs(FlowConfig{}, "opencode")
		if args != nil {
			t.Errorf("expected nil args for opencode, got %v", args)
		}
	})
}

// TestBuildHeadlessEnv_InjectsAgentEnv asserts flow.agent_env reaches the
// subprocess env and that GROVE_FLOW_* vars are still present (and ordered
// after agent_env so they win on collision).
func TestBuildHeadlessEnv_InjectsAgentEnv(t *testing.T) {
	job := &Job{ID: "j1", FilePath: "/p/j1.md", Title: "Title"}
	plan := &Plan{Name: "plan1"}
	agentEnv := map[string]string{"CLOUDSDK_CONFIG": "/tmp/x"}

	env := buildHeadlessEnv(job, plan, "claude", "/nonexistent-worktree", agentEnv)

	if !containsEnv(env, "CLOUDSDK_CONFIG=/tmp/x") {
		t.Errorf("expected CLOUDSDK_CONFIG injected, got %v", env)
	}
	if !containsEnv(env, "GROVE_FLOW_JOB_ID=j1") {
		t.Errorf("expected GROVE_FLOW_JOB_ID present, got %v", env)
	}

	// agent_env must precede GROVE_FLOW_* so grove internals win on collision.
	idxAgent := indexEnv(env, "CLOUDSDK_CONFIG=/tmp/x")
	idxGrove := indexEnv(env, "GROVE_FLOW_JOB_ID=j1")
	if idxAgent < 0 || idxGrove < 0 || idxAgent > idxGrove {
		t.Errorf("expected agent_env before GROVE_FLOW_* (agent=%d grove=%d)", idxAgent, idxGrove)
	}
}

// TestBuildHeadlessCommand_NoBypass asserts the hardcoded bypass is gone: with
// empty agentArgs the claude command carries no --dangerously-skip-permissions.
func TestBuildHeadlessCommand_NoBypass(t *testing.T) {
	cmd, err := buildHeadlessCommand(context.Background(), "claude", "do it", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range cmd.Args {
		if a == "--dangerously-skip-permissions" {
			t.Errorf("expected no bypass flag, got args %v", cmd.Args)
		}
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "claude" {
		t.Errorf("expected bare claude invocation, got %v", cmd.Args)
	}
}

// TestAgentEnvInline_Escaping asserts the inline tmux env fragment uses proper
// single-quote escaping for values containing single quotes.
func TestAgentEnvInline_Escaping(t *testing.T) {
	out := agentEnvInline(map[string]string{"CLOUDSDK_CONFIG": "/path/to/sa"})
	if !strings.Contains(out, "CLOUDSDK_CONFIG='/path/to/sa' ") {
		t.Errorf("expected quoted pair, got %q", out)
	}

	// A value with embedded single quotes must be escaped via the '\'' idiom.
	out2 := agentEnvInline(map[string]string{"DANGEROUS": "a'b"})
	if !strings.Contains(out2, `DANGEROUS='a'\''b' `) {
		t.Errorf("expected escaped single quotes, got %q", out2)
	}

	if agentEnvInline(nil) != "" {
		t.Errorf("expected empty string for nil map")
	}
}

func containsEnv(env []string, want string) bool {
	return indexEnv(env, want) >= 0
}

func indexEnv(env []string, want string) int {
	for i, e := range env {
		if e == want {
			return i
		}
	}
	return -1
}
