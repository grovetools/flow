package orchestration

import (
	"strings"
	"testing"
)

func TestValidateModelForJob_UnknownModel(t *testing.T) {
	err := ValidateModelForJob("totally-fake-model", JobTypeHeadlessAgent, "")
	if err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("expected 'unknown model' in error, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "flow models") {
		t.Errorf("expected 'flow models' suggestion in error, got: %s", err.Error())
	}
}

func TestValidateModelForJob_GeminiOnAgentJob(t *testing.T) {
	for _, jt := range []JobType{JobTypeInteractiveAgent, JobTypeHeadlessAgent} {
		t.Run(string(jt), func(t *testing.T) {
			err := ValidateModelForJob("gemini-2.5-pro", jt, "")
			if err == nil {
				t.Fatal("expected error for gemini model on agent job, got nil")
			}
			if !strings.Contains(err.Error(), "Google model") {
				t.Errorf("expected 'Google model' in error, got: %s", err.Error())
			}
			if !strings.Contains(err.Error(), "Claude CLI") {
				t.Errorf("expected 'Claude CLI' in error, got: %s", err.Error())
			}
		})
	}
}

func TestValidateModelForJob_ClaudeOnAgentJob(t *testing.T) {
	// Claude models should be accepted for agent jobs (provider auth check may
	// fail in CI where no API key is configured, so we just check it doesn't
	// fail with "unknown" or "wrong provider").
	err := ValidateModelForJob("claude-opus-4-8", JobTypeHeadlessAgent, "")
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "unknown model") || strings.Contains(errStr, "Google model") {
			t.Errorf("claude model should not be rejected as unknown/wrong-provider: %s", errStr)
		}
	}
}

func TestValidateModelForJob_EmptyModel(t *testing.T) {
	err := ValidateModelForJob("", JobTypeHeadlessAgent, "")
	if err != nil {
		t.Errorf("empty model should be valid, got: %s", err.Error())
	}
}

func TestValidateModelForJob_GeminiOnChatJob(t *testing.T) {
	// Gemini models on chat/oneshot jobs should NOT trigger the provider
	// mismatch error — only agent jobs enforce claude-only.
	err := ValidateModelForJob("gemini-2.5-pro", JobTypeChat, "")
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "Claude CLI") {
			t.Errorf("gemini on chat job should not get agent-only error: %s", errStr)
		}
	}
}

func TestValidateModelKnown(t *testing.T) {
	tests := []struct {
		model   string
		wantErr bool
	}{
		{"claude-opus-4-8", false},
		{"claude-sonnet-4-6", false},
		{"gemini-2.5-pro", false},
		{"totally-fake-model", true},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			err := ValidateModelKnown(tt.model)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateModelKnown(%q) error=%v, wantErr=%v", tt.model, err, tt.wantErr)
			}
		})
	}
}

func TestValidateModelKnown_DidYouMean(t *testing.T) {
	// Use a non-claude typo so isClaudeFamilyModel doesn't short-circuit
	err := ValidateModelKnown("gemini-2.5-por")
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
	if !strings.Contains(err.Error(), "Did you mean") {
		t.Errorf("expected 'Did you mean' suggestion, got: %s", err.Error())
	}
}

func TestValidateModelKnown_OpenRouterBareAliasSuggestsPrefixed(t *testing.T) {
	// The bare OpenRouter alias is not accepted flow-side (conflation guard).
	// It errors, and the did-you-mean should steer the user to the prefixed ID.
	err := ValidateModelKnown("openai/gpt-5.2")
	if err == nil {
		t.Fatal("expected error for bare openrouter alias, got nil")
	}
	if !strings.Contains(err.Error(), "openrouter/openai/gpt-5.2") {
		t.Errorf("expected did-you-mean to suggest %q, got: %s", "openrouter/openai/gpt-5.2", err.Error())
	}
}

func TestValidateModelKnown_OpenRouterPrefixedAnthropicVendor(t *testing.T) {
	// An OpenRouter ID whose vendor segment is "anthropic" must resolve via the
	// provider lookup (prefix precedence, Correction 6) — not be rejected — even
	// though it contains "claude". ValidateModelKnown returns nil.
	if err := ValidateModelKnown("openrouter/anthropic/claude-sonnet-4.5"); err != nil {
		t.Errorf("expected nil for prefixed openrouter/anthropic model, got: %s", err.Error())
	}
}

func TestValidateModelForJob_OpencodeAgentSlashModel(t *testing.T) {
	// opencode agent jobs carry bare slash model strings; adding OpenRouter to
	// the provider lookup must not break agent-job validation (the agent branch
	// early-returns before the provider lookup).
	if err := ValidateModelForJob("anthropic/claude-sonnet-4-5", JobTypeInteractiveAgent, "opencode"); err != nil {
		t.Errorf("expected opencode agent job with slash model to pass, got: %s", err.Error())
	}
}
