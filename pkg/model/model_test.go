package model

import (
	"strings"
	"testing"
)

func TestLookupModelProvider(t *testing.T) {
	tests := []struct {
		model    string
		wantProv string
		wantOK   bool
	}{
		{"claude-opus-4-8", "Anthropic", true},
		// Dated snapshot ID: the registry ships claude-opus-4-6-20260115 (there is
		// no dated snapshot for opus-4-8, which is aliased to itself).
		{"claude-opus-4-6-20260115", "Anthropic", true},
		{"claude-sonnet-4-6", "Anthropic", true},
		{"claude-sonnet-4-6-20260115", "Anthropic", true},
		{"claude-haiku-4-5", "Anthropic", true},
		{"gemini-2.5-pro", "Google", true},
		{"gemini-3.1-pro-preview", "Google", true},
		{"gpt-4o", "", false},
		{"nonexistent-model", "", false},
		// OpenRouter: prefix-only resolution.
		{"openrouter/openai/gpt-5.2", "OpenRouter", true},
		// Uncatalogued but prefixed → still OpenRouter (prefix escape hatch).
		{"openrouter/mistralai/mistral-small", "OpenRouter", true},
		// Bare alias (no prefix) → NOT found: the conflation guard. A user
		// copying an opencode/pi agent model string must get an unknown-model
		// error, not silent OpenRouter routing.
		{"openai/gpt-5.2", "", false},
		// An OpenRouter ID whose vendor segment is "anthropic" must still route
		// to OpenRouter via the prefix, NOT be captured as a claude-family model
		// (Correction 6 — prefix precedence).
		{"openrouter/anthropic/claude-sonnet-4.5", "OpenRouter", true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			prov, ok := LookupModelProvider(tt.model)
			if ok != tt.wantOK {
				t.Errorf("LookupModelProvider(%q) found=%v, want %v", tt.model, ok, tt.wantOK)
			}
			if prov != tt.wantProv {
				t.Errorf("LookupModelProvider(%q) provider=%q, want %q", tt.model, prov, tt.wantProv)
			}
		})
	}
}

func TestIsProviderAuthenticated_OpenRouter(t *testing.T) {
	t.Run("env key present", func(t *testing.T) {
		t.Setenv("OPENROUTER_API_KEY", "x")
		ok, instructions := IsProviderAuthenticated(ProviderOpenRouter)
		if !ok {
			t.Errorf("expected authenticated with OPENROUTER_API_KEY set, got false (%s)", instructions)
		}
		if instructions != "" {
			t.Errorf("expected empty instructions when authenticated, got: %s", instructions)
		}
	})

	t.Run("no key", func(t *testing.T) {
		t.Setenv("OPENROUTER_API_KEY", "")
		ok, instructions := IsProviderAuthenticated(ProviderOpenRouter)
		if ok {
			t.Skip("host has OpenRouter configured via grove.yml; cannot assert unauthenticated")
		}
		for _, want := range []string{
			"OPENROUTER_API_KEY",
			"openrouter.api_key_command",
			"openrouter.api_key",
		} {
			if !strings.Contains(instructions, want) {
				t.Errorf("expected instructions to mention %q, got: %s", want, instructions)
			}
		}
	})
}

func TestMaxTokens(t *testing.T) {
	cases := []struct {
		model string
		want  int64
	}{
		{"claude-opus-4-8", 64000},
		{"claude-sonnet-4-6", 64000},
		{"claude-sonnet-4-6-20260115", 64000},
		{"claude-sonnet-5", 64000},
		{"claude-sonnet-5-20991231", 64000},
		{"claude-opus-4-6-20260115", 64000},
		{"claude-haiku-4-5", 64000},
		{"claude-haiku-4-5-20251001", 64000},
		{"claude-3-7-sonnet-20250219", 64000},
		{"claude-3-5-sonnet-20241022", 8192},
		{"claude-3-haiku-20240307", 4096},
		{"claude-future-model", 32000},
		{"gemini-2.5-pro", 32000},
		{"", 32000},
	}

	for _, tc := range cases {
		if got := MaxTokens(tc.model); got != tc.want {
			t.Errorf("MaxTokens(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}
