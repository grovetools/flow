package orchestration

import (
	"fmt"
	"strings"

	anthropicconfig "github.com/grovetools/grove-anthropic/pkg/config"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	geminiconfig "github.com/grovetools/grove-gemini/pkg/config"
	geminimodels "github.com/grovetools/grove-gemini/pkg/models"
)

const (
	ProviderAnthropic = "Anthropic"
	ProviderGoogle    = "Google"
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
	default:
		return false, fmt.Sprintf("unknown provider %q", provider)
	}
}

// ValidateModelForJob checks that a model is valid for the given job type.
// For agent job types (interactive_agent, headless_agent), it enforces that the
// model is known and compatible with the agent provider. Returns nil if valid,
// or an actionable error distinguishing:
//   - unknown model (not in any registry)
//   - known model but wrong provider for agent jobs
//   - known model but provider not authenticated
func ValidateModelForJob(model string, jobType JobType) error {
	if model == "" {
		return nil
	}

	provider, known := LookupModelProvider(model)
	if !known {
		if isClaudeFamilyModel(model) {
			return nil
		}
		return fmt.Errorf("unknown model %q: not found in any provider registry.\n"+
			"Run 'flow models' to see available models", model)
	}

	isAgentJob := jobType == JobTypeInteractiveAgent || jobType == JobTypeHeadlessAgent
	if isAgentJob && provider == ProviderGoogle {
		return fmt.Errorf("model %q is a %s model, but agent jobs run on the Claude CLI.\n"+
			"Use a Claude-family model (e.g. claude-opus-4-8, claude-sonnet-4-6) or\n"+
			"set flow.interactive_provider in grove.toml to route to a different provider",
			model, provider)
	}

	if isAgentJob && !isClaudeFamilyModel(model) {
		return fmt.Errorf("model %q is not recognized as a Claude-family model.\n"+
			"Agent jobs require a Claude model. Use a Claude-family model or\n"+
			"set flow.interactive_provider in grove.toml to route to a different provider", model)
	}

	if ok, instructions := IsProviderAuthenticated(provider); !ok {
		verb := "run"
		if isAgentJob {
			verb = "launch"
		}
		return fmt.Errorf("model %q requires the %s provider, which is not configured on this host.\n"+
			"The job will fail to %s until credentials are set up.\n\n%s",
			model, provider, verb, instructions)
	}

	return nil
}

// ValidateModelKnown is a lighter check that only verifies the model exists in
// a registry (or is a recognized claude family alias). Used by flow plan run
// --model to validate an override without coupling to a specific job type.
func ValidateModelKnown(model string) error {
	if model == "" {
		return nil
	}
	if _, known := LookupModelProvider(model); known {
		return nil
	}
	if isClaudeFamilyModel(model) {
		return nil
	}

	known := allModelNames()
	suggestion := closestMatch(model, known)
	msg := fmt.Sprintf("unknown model %q: not found in any provider registry", model)
	if suggestion != "" {
		msg += fmt.Sprintf("\n  Did you mean %q?", suggestion)
	}
	msg += "\n  Run 'flow models' to see available models"
	return fmt.Errorf("%s", msg)
}

func allModelNames() []string {
	var names []string
	for _, m := range anthropicmodels.Models() {
		names = append(names, m.ID)
		if m.Alias != "" {
			names = append(names, m.Alias)
		}
	}
	for _, m := range geminimodels.Models() {
		names = append(names, m.ID)
		if m.Alias != "" {
			names = append(names, m.Alias)
		}
	}
	return names
}

func closestMatch(input string, candidates []string) string {
	input = strings.ToLower(input)
	best := ""
	bestScore := 0
	for _, c := range candidates {
		cl := strings.ToLower(c)
		score := longestCommonSubstring(input, cl)
		if score > bestScore && score >= 4 {
			bestScore = score
			best = c
		}
	}
	return best
}

func longestCommonSubstring(a, b string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	maxLen := 0
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1] + 1
				if curr[j] > maxLen {
					maxLen = curr[j]
				}
			} else {
				curr[j] = 0
			}
		}
		prev, curr = curr, prev
		for k := range curr {
			curr[k] = 0
		}
	}
	return maxLen
}
