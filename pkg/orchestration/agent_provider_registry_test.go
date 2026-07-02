package orchestration

import (
	"strings"
	"testing"
)

func specForTest(t *testing.T, name string) *AgentProviderSpec {
	t.Helper()
	spec, ok := LookupAgentProvider(name)
	if !ok {
		t.Fatalf("provider %q missing from registry", name)
	}
	return spec
}

func TestAgentProviderRegistry_Names(t *testing.T) {
	names := AgentProviderNames()
	want := []string{"claude", "codex", "opencode"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("expected sorted provider names %v, got %v", want, names)
	}

	headless := headlessAgentProviderNames()
	wantHeadless := []string{"claude", "opencode"}
	if strings.Join(headless, ",") != strings.Join(wantHeadless, ",") {
		t.Errorf("expected headless providers %v, got %v", wantHeadless, headless)
	}
}

func TestValidateAgentProviderName(t *testing.T) {
	if err := ValidateAgentProviderName(""); err != nil {
		t.Errorf("empty provider name should be valid (fallback), got: %v", err)
	}
	for _, name := range []string{"claude", "codex", "opencode"} {
		if err := ValidateAgentProviderName(name); err != nil {
			t.Errorf("provider %q should be valid, got: %v", name, err)
		}
	}
	err := ValidateAgentProviderName("pi")
	if err == nil {
		t.Fatal("expected error for unregistered provider")
	}
	if !strings.Contains(err.Error(), "pi") || !strings.Contains(err.Error(), "claude, codex, opencode") {
		t.Errorf("error should name the provider and list available ones, got: %v", err)
	}
}

func TestResolveJobProviderName_Precedence(t *testing.T) {
	cfgCodex := FlowConfig{InteractiveProvider: "codex"}

	t.Run("job provider wins over config", func(t *testing.T) {
		got := ResolveJobProviderName(&Job{Provider: "opencode"}, cfgCodex)
		if got != "opencode" {
			t.Errorf("expected job frontmatter to win, got %q", got)
		}
	})

	t.Run("config wins when job unset", func(t *testing.T) {
		got := ResolveJobProviderName(&Job{}, cfgCodex)
		if got != "codex" {
			t.Errorf("expected flow.interactive_provider fallback, got %q", got)
		}
	})

	t.Run("claude default when nothing set", func(t *testing.T) {
		got := ResolveJobProviderName(&Job{}, FlowConfig{})
		if got != "claude" {
			t.Errorf("expected claude default, got %q", got)
		}
	})

	t.Run("nil job tolerated", func(t *testing.T) {
		got := ResolveJobProviderName(nil, FlowConfig{})
		if got != "claude" {
			t.Errorf("expected claude default for nil job, got %q", got)
		}
	})
}

func TestResolveJobProviderSpec_UnknownIsError(t *testing.T) {
	_, err := resolveJobProviderSpec(&Job{Provider: "nonexistent"}, FlowConfig{})
	if err == nil {
		t.Fatal("expected error for unknown job provider")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the provider, got: %v", err)
	}

	// Unknown GLOBAL provider is also a hard error (previously the tmux path
	// errored but groveterm silently launched a claude-shaped command).
	_, err = resolveJobProviderSpec(&Job{}, FlowConfig{InteractiveProvider: "gemini"})
	if err == nil {
		t.Fatal("expected error for unknown global provider")
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("error should name the provider, got: %v", err)
	}
}

func TestBuildShellCommand_Shapes(t *testing.T) {
	instruction := buildBriefingInstruction("/tmp/it's a test/briefing.xml")
	wantInstr := `Read the briefing file at '/tmp/it'\''s a test/briefing.xml' and execute the task.`
	if instruction != wantInstr {
		t.Errorf("instruction mismatch:\n got: %s\nwant: %s", instruction, wantInstr)
	}

	t.Run("claude positional", func(t *testing.T) {
		got := specForTest(t, "claude").BuildShellCommand([]string{"--model", "opus"}, "instr")
		if got != `claude --model opus "instr"` {
			t.Errorf("unexpected claude command: %s", got)
		}
	})

	t.Run("codex positional", func(t *testing.T) {
		got := specForTest(t, "codex").BuildShellCommand([]string{"--full-auto"}, "instr")
		if got != `codex --full-auto "instr"` {
			t.Errorf("unexpected codex command: %s", got)
		}
	})

	t.Run("opencode uses --prompt", func(t *testing.T) {
		got := specForTest(t, "opencode").BuildShellCommand([]string{"--verbose"}, "instr")
		if got != `opencode --verbose --prompt "instr"` {
			t.Errorf("unexpected opencode command: %s", got)
		}
	})
}

func TestAppendProviderJobArgs_NonClaudeProviders(t *testing.T) {
	t.Run("codex accepts any model via --model", func(t *testing.T) {
		args, err := appendProviderJobArgs(specForTest(t, "codex"), []string{"--full-auto"}, &Job{ID: "j", Model: "gpt-5.2"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "--full-auto --model gpt-5.2"
		if strings.Join(args, " ") != want {
			t.Errorf("expected %q, got %v", want, args)
		}
	})

	t.Run("opencode accepts provider/model form", func(t *testing.T) {
		args, err := appendProviderJobArgs(specForTest(t, "opencode"), nil, &Job{ID: "j", Model: "anthropic/claude-sonnet-4-5"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "--model anthropic/claude-sonnet-4-5"
		if strings.Join(args, " ") != want {
			t.Errorf("expected %q, got %v", want, args)
		}
	})

	t.Run("effort rejected for providers without an effort flag", func(t *testing.T) {
		for _, name := range []string{"codex", "opencode"} {
			_, err := appendProviderJobArgs(specForTest(t, name), nil, &Job{ID: "j", Effort: "high"})
			if err == nil {
				t.Errorf("%s: expected error for unsupported effort", name)
				continue
			}
			if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "effort") {
				t.Errorf("%s: error should name provider and effort, got: %v", name, err)
			}
		}
	})

	t.Run("empty model and effort leaves args untouched", func(t *testing.T) {
		base := []string{"--full-auto"}
		args, err := appendProviderJobArgs(specForTest(t, "codex"), base, &Job{ID: "j"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Join(args, " ") != "--full-auto" {
			t.Errorf("expected args unchanged, got %v", args)
		}
	})

	t.Run("shell-unsafe model rejected regardless of provider", func(t *testing.T) {
		if _, err := appendProviderJobArgs(specForTest(t, "codex"), nil, &Job{ID: "j", Model: "gpt; rm -rf /"}); err == nil {
			t.Error("expected error for shell-unsafe model on codex")
		}
	})
}

func TestValidateModelForJob_PerProvider(t *testing.T) {
	t.Run("non-claude model allowed on codex agent job", func(t *testing.T) {
		if err := ValidateModelForJob("gpt-5.2", JobTypeInteractiveAgent, "codex"); err != nil {
			t.Errorf("codex agent job should accept gpt models, got: %v", err)
		}
	})

	t.Run("non-claude model allowed on opencode agent job", func(t *testing.T) {
		if err := ValidateModelForJob("anthropic/claude-sonnet-4-5", JobTypeHeadlessAgent, "opencode"); err != nil {
			t.Errorf("opencode agent job should accept provider/model, got: %v", err)
		}
	})

	t.Run("gemini model still rejected on claude agent job", func(t *testing.T) {
		err := ValidateModelForJob("gemini-2.5-pro", JobTypeInteractiveAgent, "claude")
		if err == nil {
			t.Fatal("expected error for gemini model on claude agent job")
		}
		if !strings.Contains(err.Error(), "Claude CLI") {
			t.Errorf("expected Claude CLI mismatch error, got: %v", err)
		}
	})

	t.Run("unknown provider is an error", func(t *testing.T) {
		err := ValidateModelForJob("gpt-5.2", JobTypeInteractiveAgent, "pi")
		if err == nil {
			t.Fatal("expected error for unknown provider")
		}
		if !strings.Contains(err.Error(), "unknown agent provider") {
			t.Errorf("expected unknown-provider error, got: %v", err)
		}
	})
}

// TestBuildCommandParityAcrossPaths asserts the tmux provider buildAgentCommand
// output equals the registry spec's shell command for the same inputs — the
// guarantee that migrating the switch sites onto the registry did not change
// the bytes sent into panes/PTYs.
func TestBuildCommandParityAcrossPaths(t *testing.T) {
	briefing := "/tmp/plan/.artifacts/briefing-1.xml"
	args := []string{"--dangerously-skip-permissions"}
	job := &Job{ID: "j", Type: JobTypeInteractiveAgent}
	plan := &Plan{}

	t.Run("claude tmux vs spec", func(t *testing.T) {
		cmd, err := NewClaudeAgentProvider().buildAgentCommand(job, plan, briefing, args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := specForTest(t, "claude").BuildShellCommand(args, buildBriefingInstruction(briefing))
		if cmd != want {
			t.Errorf("claude parity broken:\n tmux: %s\n spec: %s", cmd, want)
		}
	})

	t.Run("codex tmux vs spec", func(t *testing.T) {
		cmd, err := NewCodexAgentProvider().buildAgentCommand(job, plan, briefing, args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := specForTest(t, "codex").BuildShellCommand(args, buildBriefingInstruction(briefing))
		if cmd != want {
			t.Errorf("codex parity broken:\n tmux: %s\n spec: %s", cmd, want)
		}
	})

	t.Run("opencode tmux vs spec (interactive)", func(t *testing.T) {
		cmd, err := (&OpencodeAgentProvider{}).buildAgentCommand(job, briefing, args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := specForTest(t, "opencode").BuildShellCommand(args, buildBriefingInstruction(briefing))
		if cmd != want {
			t.Errorf("opencode parity broken:\n tmux: %s\n spec: %s", cmd, want)
		}
	})
}
