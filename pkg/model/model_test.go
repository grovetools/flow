package model

import "testing"

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

func TestMaxTokens(t *testing.T) {
	cases := []struct {
		model string
		want  int64
	}{
		{"claude-opus-4-8", 64000},
		{"claude-sonnet-4-6", 64000},
		{"claude-sonnet-4-6-20260115", 64000},
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
