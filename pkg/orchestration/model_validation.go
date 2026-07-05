package orchestration

import (
	"fmt"
	"strings"

	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	geminimodels "github.com/grovetools/grove-gemini/pkg/models"
	openroutermodels "github.com/grovetools/grove-openrouter/pkg/models"

	modelpkg "github.com/grovetools/flow/pkg/model"
)

// ValidateModelForJob checks that a model is valid for the given job type.
// For agent job types (interactive_agent, headless_agent) the check is
// per-provider: providerName is the job's EFFECTIVE agent provider (job
// `provider:` frontmatter > flow.interactive_provider; "" defaults to claude)
// and the provider's registry spec decides which models are acceptable —
// claude enforces Claude-family models, codex/opencode accept any model string
// (their CLIs own validation). Returns nil if valid, or an actionable error
// distinguishing:
//   - unknown agent provider
//   - unknown model / wrong model family for the provider (agent jobs)
//   - known model but API provider not authenticated (non-agent jobs)
func ValidateModelForJob(model string, jobType JobType, providerName string) error {
	if model == "" {
		return nil
	}

	isAgentJob := jobType == JobTypeInteractiveAgent || jobType == JobTypeHeadlessAgent
	if isAgentJob {
		if providerName == "" {
			providerName = defaultAgentProviderName
		}
		spec, ok := LookupAgentProvider(providerName)
		if !ok {
			return unknownAgentProviderError(providerName)
		}
		// Agent jobs delegate to the provider's CLI which handles its own
		// auth — no API-key check here.
		if spec.ValidateJobModel != nil {
			return spec.ValidateJobModel(model)
		}
		return nil
	}

	provider, known := modelpkg.LookupModelProvider(model)
	if !known {
		if isClaudeFamilyModel(model) {
			return nil
		}
		return fmt.Errorf("unknown model %q: not found in any provider registry.\n"+
			"Run 'flow models' to see available models", model)
	}

	// Non-agent jobs (oneshot, chat) call the API directly; verify the API
	// provider is configured on this host.
	if ok, instructions := modelpkg.IsProviderAuthenticated(provider); !ok {
		return fmt.Errorf("model %q requires the %s provider, which is not configured on this host.\n"+
			"The job will fail to run until credentials are set up.\n\n%s",
			model, provider, instructions)
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
	if _, known := modelpkg.LookupModelProvider(model); known {
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
	// OpenRouter: IDs only, no bare aliases — flow never accepts the bare
	// "<vendor>/<model>" spelling (see LookupModelProvider), so the did-you-mean
	// suggestion should only ever propose prefixed "openrouter/..." IDs.
	for _, m := range openroutermodels.Models() {
		names = append(names, m.ID)
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
