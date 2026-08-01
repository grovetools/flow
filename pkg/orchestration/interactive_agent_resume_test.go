package orchestration

import (
	"context"
	"strings"
	"testing"
	"time"
)

type recordingPreparedProvider struct {
	job              *Job
	plan             *Plan
	workDir          string
	command          string
	expectedNativeID string

	// confirmation models a real provider: LaunchPrepared kicks off session
	// discovery in the background and returns before it finishes.
	confirmation *sessionConfirmation
	confirmed    bool
}

func (p *recordingPreparedProvider) LaunchPrepared(_ context.Context, job *Job, plan *Plan, workDir, command, expectedNativeID string) error {
	p.job = job
	p.plan = plan
	p.workDir = workDir
	p.command = command
	p.expectedNativeID = expectedNativeID
	p.confirmation = startSessionConfirmation(func() error {
		time.Sleep(20 * time.Millisecond)
		p.confirmed = true
		return nil
	})
	return nil
}

func (p *recordingPreparedProvider) awaitSessionConfirmation(ctx context.Context) error {
	return p.confirmation.wait(ctx)
}

func TestPreparedInteractiveAgentResumeLaunchForwardsPreparedLifecycleInputs(t *testing.T) {
	provider := &recordingPreparedProvider{}
	job := &Job{ID: "job-1"}
	plan := &Plan{Name: "plan-1"}
	prepared := &PreparedInteractiveAgentResume{
		provider: provider, job: job, plan: plan, workDir: "/work",
		shellCommand: "claude --resume native-1", expectedNativeID: "native-1",
	}
	if err := prepared.Launch(context.Background()); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if provider.job != job || provider.plan != plan || provider.workDir != "/work" {
		t.Fatalf("Launch() did not forward job/plan/workdir: %#v", provider)
	}
	if provider.command != "claude --resume native-1" || provider.expectedNativeID != "native-1" {
		t.Fatalf("Launch() did not forward prepared command/native ID: %#v", provider)
	}
}

// TestPreparedInteractiveAgentResumeAwaitsBackgroundConfirmation covers the
// seam itself: Launch is allowed to return with discovery still in flight, but
// AwaitSessionConfirmation must not return until it has finished. `flow plan
// resume` exits between those two calls' worth of time, so a wait that does not
// actually wait leaves the daemon holding a pending session forever.
func TestPreparedInteractiveAgentResumeAwaitsBackgroundConfirmation(t *testing.T) {
	provider := &recordingPreparedProvider{}
	prepared := &PreparedInteractiveAgentResume{
		provider: provider, job: &Job{ID: "job-1"}, plan: &Plan{Name: "plan-1"}, workDir: "/work",
	}
	if err := prepared.Launch(context.Background()); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if err := prepared.AwaitSessionConfirmation(context.Background()); err != nil {
		t.Fatalf("AwaitSessionConfirmation() error = %v", err)
	}
	if !provider.confirmed {
		t.Fatal("AwaitSessionConfirmation() returned before the provider's background confirmation ran")
	}
}

func TestPrepareInteractiveAgentResumeUsesArchivedProviderAndPlanTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")

	job := &Job{ID: "job-1", Provider: "claude", Model: "gpt-5.2"}
	plan := &Plan{Name: "plan-1", Orchestration: &Config{AgentTarget: "tmux"}}
	prepared, err := PrepareInteractiveAgentResume(job, plan, home, "codex", "native-123")
	if err != nil {
		t.Fatalf("PrepareInteractiveAgentResume() error = %v", err)
	}
	if _, ok := prepared.provider.(*CodexAgentProvider); !ok {
		t.Fatalf("provider = %T, want *CodexAgentProvider", prepared.provider)
	}
	if prepared.shellCommand != "codex --model gpt-5.2 resume native-123" {
		t.Fatalf("shellCommand = %q", prepared.shellCommand)
	}

	plan.Orchestration.AgentTarget = "native"
	prepared, err = PrepareInteractiveAgentResume(job, plan, home, "codex", "native-123")
	if err != nil {
		t.Fatalf("native PrepareInteractiveAgentResume() error = %v", err)
	}
	gp, ok := prepared.provider.(*GrovetermAgentProvider)
	if !ok || gp.agentTarget != "native" || gp.spec.Name != "codex" {
		t.Fatalf("native provider = %#v, want codex groveterm provider", prepared.provider)
	}
}

func TestPrepareInteractiveAgentResumePiUsesFlowOwnedSessionDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")

	job := &Job{ID: "job-1", Status: JobStatusCompleted}
	plan := &Plan{Name: "plan-1", Directory: home, Orchestration: &Config{AgentTarget: "native"}}
	prepared, err := PrepareInteractiveAgentResume(job, plan, home, "pi", "019c6073-cf17-7492")
	if err != nil {
		t.Fatalf("PrepareInteractiveAgentResume() error = %v", err)
	}
	gp, ok := prepared.provider.(*GrovetermAgentProvider)
	if !ok || gp.agentTarget != "native" || gp.spec.Name != "pi" {
		t.Fatalf("provider = %#v, want pi groveterm provider", prepared.provider)
	}
	want := "pi --session-dir " + piJobSessionDir(home, "job-1") + " " + piOfflineStartupArg + " --session 019c6073-cf17-7492"
	if prepared.shellCommand != want {
		t.Fatalf("shellCommand = %q, want %q", prepared.shellCommand, want)
	}
}

func TestPrepareInteractiveAgentResumeDefaultsMissingTargetToTmux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	job := &Job{ID: "job-1", Status: JobStatusCompleted}

	prepared, err := PrepareInteractiveAgentResume(job, &Plan{Name: "plan-1"}, home, "claude", "native-1")
	if err != nil {
		t.Fatalf("PrepareInteractiveAgentResume() error = %v", err)
	}
	if _, ok := prepared.provider.(*ClaudeAgentProvider); !ok {
		t.Fatalf("provider = %T, want tmux *ClaudeAgentProvider", prepared.provider)
	}
}

func TestPrepareInteractiveAgentResumeRejectsBeforeLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	job := &Job{Status: JobStatusCompleted}

	for _, tc := range []struct {
		name, provider, target, planDir, want string
	}{
		{name: "unsupported provider capability", provider: "opencode", target: "tmux", planDir: home, want: "does not support session resume"},
		{name: "pi requires plan directory", provider: "pi", target: "native", planDir: "", want: "plan directory"},
		{name: "pi has no prepared tmux launch", provider: "pi", target: "tmux", planDir: home, want: "does not support prepared resume launch on tmux"},
		{name: "unsupported target", provider: "claude", target: "mystery", planDir: home, want: "agent_target not set or unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &Plan{Directory: tc.planDir, Orchestration: &Config{AgentTarget: tc.target}}
			_, err := PrepareInteractiveAgentResume(job, plan, home, tc.provider, "native-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
			if job.Status != JobStatusCompleted {
				t.Fatalf("preparation mutated job status to %q", job.Status)
			}
		})
	}
}
