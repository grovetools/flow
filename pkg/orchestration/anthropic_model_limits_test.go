package orchestration

import "testing"

func TestAnthropicMaxTokens(t *testing.T) {
	cases := []struct {
		model string
		want  int64
	}{
		{"claude-sonnet-4-6", 64000},
		{"claude-sonnet-4-6-20260115", 64000},
		{"claude-opus-4-6-20260115", 64000},
		{"claude-haiku-4-5", 64000},
		{"claude-haiku-4-5-20251001", 64000},
		{"claude-3-7-sonnet-20250219", 64000},
		{"claude-3-5-sonnet-20241022", 8192},
		{"claude-3-haiku-20240307", 4096},
		{"claude-future-model", 32000},
		{"", 32000},
	}

	for _, tc := range cases {
		if got := anthropicMaxTokens(tc.model); got != tc.want {
			t.Errorf("anthropicMaxTokens(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}
