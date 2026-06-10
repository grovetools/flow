package orchestration

import "strings"

// anthropicMaxTokens returns a safe max_tokens value for the given Anthropic
// model. The Anthropic API rejects requests whose max_tokens exceeds the
// model's output limit, so the cap is looked up by model family instead of
// hardcoding a single value for every model.
//
// Works with both aliases (claude-sonnet-4-6) and dated IDs
// (claude-sonnet-4-6-20260115) since matching is prefix-based.
func anthropicMaxTokens(model string) int64 {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	// Claude 4.x families support 64K output tokens. (Opus 4.x can go up to
	// 128K, but 64K is valid everywhere in the family and keeps prior behavior.)
	case strings.HasPrefix(m, "claude-opus-4"),
		strings.HasPrefix(m, "claude-sonnet-4"),
		strings.HasPrefix(m, "claude-haiku-4"):
		return 64000
	// Claude 3.7 Sonnet supports 64K output tokens.
	case strings.HasPrefix(m, "claude-3-7"):
		return 64000
	// Claude 3.5 generation caps at 8192 output tokens.
	case strings.HasPrefix(m, "claude-3-5"):
		return 8192
	// Remaining Claude 3 generation (e.g. claude-3-haiku) caps at 4096.
	case strings.HasPrefix(m, "claude-3"):
		return 4096
	default:
		// Conservative default for unknown or future models.
		return 32000
	}
}
