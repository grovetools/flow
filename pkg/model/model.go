// Package model holds the canonical model->provider logic shared across the
// Grove ecosystem: which provider owns a given model ID/alias, whether that
// provider is authenticated on this host, and provider-specific request limits
// such as the Anthropic max_tokens cap.
//
// It is a narrow leaf package (no dependency on orchestration) so that callers
// outside flow (e.g. the grove CLI) can route by provider without pulling in
// the orchestration engine.
package model

import (
	"fmt"
	"strings"

	anthropicconfig "github.com/grovetools/grove-anthropic/pkg/config"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	geminiconfig "github.com/grovetools/grove-gemini/pkg/config"
	geminimodels "github.com/grovetools/grove-gemini/pkg/models"
	openrouterconfig "github.com/grovetools/grove-openrouter/pkg/config"
	openroutermodels "github.com/grovetools/grove-openrouter/pkg/models"
)

const (
	ProviderAnthropic  = "Anthropic"
	ProviderGoogle     = "Google"
	ProviderOpenRouter = "OpenRouter"
)

// LookupModelProvider returns the provider name for a known model ID or alias.
// Returns ("", false) if the model is not found in any registry.
func LookupModelProvider(model string) (provider string, found bool) {
	for _, m := range anthropicmodels.Models() {
		if m.ID == model || (m.Alias != "" && m.Alias == model) {
			return ProviderAnthropic, true
		}
	}
	resolved := anthropicmodels.ResolveAlias(model)
	if resolved != model {
		return ProviderAnthropic, true
	}

	for _, m := range geminimodels.Models() {
		if m.ID == model || (m.Alias != "" && m.Alias == model) {
			return ProviderGoogle, true
		}
	}

	// OpenRouter is matched by prefix only, not by registry membership: any
	// "openrouter/<vendor>/<model>" string routes to the provider (the curated
	// registry is just recommendations; OpenRouter passes uncatalogued models
	// through). Bare "<vendor>/<model>" aliases are deliberately NOT matched —
	// resolving them would let an opencode/pi agent model string (e.g.
	// "anthropic/claude-sonnet-4-5") silently bill through OpenRouter. Requiring
	// the prefix makes that string an unknown-model error instead.
	if openroutermodels.HasPrefix(model) {
		return ProviderOpenRouter, true
	}
	return "", false
}

// IsProviderAuthenticated checks whether a provider has credentials configured
// on this host. Returns (true, "") if authenticated, or (false, instructions)
// with an actionable message on how to configure credentials.
func IsProviderAuthenticated(provider string) (ok bool, instructions string) {
	switch provider {
	case ProviderAnthropic:
		if _, found := anthropicconfig.GetAPIKeySource(); found {
			return true, ""
		}
		return false, "Anthropic provider is not authenticated.\n" +
			"Configure credentials using one of:\n" +
			"  1. Set ANTHROPIC_API_KEY environment variable\n" +
			"  2. Add 'anthropic.api_key_command' to grove.yml\n" +
			"  3. Add 'anthropic.api_key' to grove.yml"
	case ProviderGoogle:
		if _, err := geminiconfig.ResolveAPIKey(); err == nil {
			return true, ""
		}
		return false, "Google (Gemini) provider is not authenticated.\n" +
			"Configure credentials using one of:\n" +
			"  1. Set GEMINI_API_KEY environment variable\n" +
			"  2. Add 'gemini.api_key_command' to grove.yml\n" +
			"  3. Add 'gemini.api_key' to grove.yml"
	case ProviderOpenRouter:
		if _, found := openrouterconfig.GetAPIKeySource(); found {
			return true, ""
		}
		return false, "OpenRouter provider is not authenticated.\n" +
			"Configure credentials using one of:\n" +
			"  1. Set OPENROUTER_API_KEY environment variable\n" +
			"  2. Add 'openrouter.api_key_command' to grove.yml\n" +
			"  3. Add 'openrouter.api_key' to grove.yml"
	default:
		return false, fmt.Sprintf("unknown provider %q", provider)
	}
}

// MaxTokens returns a safe max_tokens value for the given model. The Anthropic
// API rejects requests whose max_tokens exceeds the model's output limit, so
// the cap is looked up by model family instead of hardcoding a single value for
// every model.
//
// Works with both aliases (claude-sonnet-4-6) and dated IDs
// (claude-sonnet-4-6-20260115) since matching is prefix-based.
//
// TODO: source the cap from the grove-anthropic model registry once the Model
// struct carries an output/max-tokens field; today it only exposes pricing, so
// the family cascade below is retained.
func MaxTokens(model string) int64 {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	// Claude 4.x families support 64K output tokens. (Opus 4.x can go up to
	// 128K, but 64K is valid everywhere in the family and keeps prior behavior.)
	// Opus 5 and Sonnet 5 also support 128K output; 64K keeps them on the same
	// conservative budget as the rest of the cascade.
	case strings.HasPrefix(m, "claude-opus-4"),
		strings.HasPrefix(m, "claude-opus-5"),
		strings.HasPrefix(m, "claude-sonnet-4"),
		strings.HasPrefix(m, "claude-sonnet-5"),
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
		// Conservative default for unknown or future (incl. non-Anthropic) models.
		return 32000
	}
}
