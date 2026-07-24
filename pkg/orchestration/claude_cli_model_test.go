package orchestration

import (
	"os"
	"path/filepath"
	"testing"
)

// writeClaudeSettings writes a settings file containing just a model value.
func writeClaudeSettings(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

// isolateClaudeConfig points CLAUDE_CONFIG_DIR at a scratch dir and clears
// ANTHROPIC_MODEL, so a developer's real settings can't leak into the test.
func isolateClaudeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_MODEL", "")
	return dir
}

func TestNormalizeClaudeModelLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Bare families resolve to whatever is current in the registry.
		{"opus", "claude-opus-5"},
		{"sonnet", "claude-sonnet-5"},
		{"haiku", "claude-haiku-4-5"},
		{"fable", "claude-fable-5"},
		{"opusplan", "claude-opus-5"}, // family variants match by prefix
		// The CLI's context-window marker names the same model.
		{"opus[1m]", "claude-opus-5"},
		{"claude-opus-5[1m]", "claude-opus-5"},
		// Aliases and full dated IDs collapse to the canonical alias.
		{"claude-opus-5", "claude-opus-5"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-sonnet-4-6-20260115", "claude-sonnet-4-6"},
		{"  claude-opus-4-8  ", "claude-opus-4-8"},
		// Unknown values yield no label rather than a wrong one.
		{"", ""},
		{"default", ""},
		{"gpt-5.2", ""},
		{"[1m]", ""},
	}
	for _, tc := range cases {
		if got := normalizeClaudeModelLabel(tc.in); got != tc.want {
			t.Errorf("normalizeClaudeModelLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveClaudeCLIModelPrecedence(t *testing.T) {
	t.Run("agent env outranks every file", func(t *testing.T) {
		userDir := isolateClaudeConfig(t)
		writeClaudeSettings(t, userDir, `{"model": "haiku"}`)
		workDir := t.TempDir()
		writeClaudeSettings(t, filepath.Join(workDir, ".claude"), `{"model": "sonnet"}`)

		got := resolveClaudeCLIModel(workDir, map[string]string{"ANTHROPIC_MODEL": "opus"})
		if got != "claude-opus-5" {
			t.Errorf("got %q, want claude-opus-5", got)
		}
	})

	t.Run("process env outranks files", func(t *testing.T) {
		userDir := isolateClaudeConfig(t)
		writeClaudeSettings(t, userDir, `{"model": "haiku"}`)
		t.Setenv("ANTHROPIC_MODEL", "claude-fable-5")

		if got := resolveClaudeCLIModel(t.TempDir(), nil); got != "claude-fable-5" {
			t.Errorf("got %q, want claude-fable-5", got)
		}
	})

	t.Run("project local outranks project shared and user", func(t *testing.T) {
		userDir := isolateClaudeConfig(t)
		writeClaudeSettings(t, userDir, `{"model": "haiku"}`)
		workDir := t.TempDir()
		claudeDir := filepath.Join(workDir, ".claude")
		writeClaudeSettings(t, claudeDir, `{"model": "sonnet"}`)
		local := filepath.Join(claudeDir, "settings.local.json")
		if err := os.WriteFile(local, []byte(`{"model": "opus"}`), 0o600); err != nil {
			t.Fatalf("write local settings: %v", err)
		}

		if got := resolveClaudeCLIModel(workDir, nil); got != "claude-opus-5" {
			t.Errorf("got %q, want claude-opus-5", got)
		}
	})

	t.Run("falls through to user settings", func(t *testing.T) {
		userDir := isolateClaudeConfig(t)
		writeClaudeSettings(t, userDir, `{"model": "opus[1m]", "permissions": {"defaultMode": "auto"}}`)

		if got := resolveClaudeCLIModel(t.TempDir(), nil); got != "claude-opus-5" {
			t.Errorf("got %q, want claude-opus-5", got)
		}
	})

	t.Run("settings without a model keep looking", func(t *testing.T) {
		userDir := isolateClaudeConfig(t)
		writeClaudeSettings(t, userDir, `{"model": "sonnet"}`)
		workDir := t.TempDir()
		// Present but silent on model: not an answer, so the user file wins.
		writeClaudeSettings(t, filepath.Join(workDir, ".claude"), `{"permissions": {}}`)

		if got := resolveClaudeCLIModel(workDir, nil); got != "claude-sonnet-5" {
			t.Errorf("got %q, want claude-sonnet-5", got)
		}
	})

	t.Run("malformed settings are skipped", func(t *testing.T) {
		userDir := isolateClaudeConfig(t)
		writeClaudeSettings(t, userDir, `{"model": "sonnet"}`)
		workDir := t.TempDir()
		writeClaudeSettings(t, filepath.Join(workDir, ".claude"), `{not json`)

		if got := resolveClaudeCLIModel(workDir, nil); got != "claude-sonnet-5" {
			t.Errorf("got %q, want claude-sonnet-5", got)
		}
	})

	t.Run("unresolvable value in the winning source yields no label", func(t *testing.T) {
		userDir := isolateClaudeConfig(t)
		// A lower-precedence source names a real model, but the winning one
		// names something unknown — answering "claude-opus-5" here would be a
		// confident lie about what the CLI will run.
		writeClaudeSettings(t, userDir, `{"model": "opus"}`)
		workDir := t.TempDir()
		writeClaudeSettings(t, filepath.Join(workDir, ".claude"), `{"model": "some-future-model"}`)

		if got := resolveClaudeCLIModel(workDir, nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("no configuration anywhere", func(t *testing.T) {
		isolateClaudeConfig(t) // scratch config dir, no settings file written

		if got := resolveClaudeCLIModel(t.TempDir(), nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// The fallback must be a concrete current model, never empty — an empty label
// would leave the frontmatter blank on an unconfigured host.
func TestClaudeCLIFallbackModel(t *testing.T) {
	if got := claudeCLIFallbackModel(); got != "claude-opus-5" {
		t.Errorf("claudeCLIFallbackModel() = %q, want claude-opus-5", got)
	}
}
