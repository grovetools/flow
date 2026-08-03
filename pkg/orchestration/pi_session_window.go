package orchestration

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// pi_session_window.go is the context-window gate for seeded Pi sessions.
//
// Compaction is the existential risk for this whole feature. pi auto-compacts
// once the context exceeds (window − reserve), and a compacted oracle has had
// the curated bundle — the entire epistemic basis of its answers — replaced by
// a summary of itself. The gate's job is to make that impossible to reach by
// accident: refuse at launch, before a single token is spent, when the seed
// would not leave room for a real dialogue on top of it.
//
// # Why a calibration table exists at all
//
// cx's token estimate is DIVISOR-based and Anthropic-calibrated: code ≈ bytes/2,
// prose ≈ bytes/4, unknown ≈ bytes/3 (cx pkg/context/estimate.go). That is the
// right calibration for Claude's tokenizer and the wrong one for everyone else.
// The Phase 2 seeding probe measured the gap directly: a bundle cx estimated at
// 200K tokens billed 86K real input tokens on gpt-5.6-sol — ~3.9 bytes/token
// against cx's assumed ~2, i.e. cx over-estimates by ~2.3x on that family.
//
// Over-estimating is the safe direction (it refuses launches that would have
// fit), but by 2.3x it is safe to the point of uselessness: it would reject
// exactly the big-bundle sessions the feature exists to enable. So each family
// gets a conversion factor, and every factor carries a deliberate margin ABOVE
// the measurement, because the failure mode of an optimistic factor is silent
// mid-session compaction rather than an actionable error.

// piTokenizerFactor converts a cx token estimate into an estimate for a target
// model family. 1.0 means "cx's Anthropic calibration already applies".
//
// The OpenAI-family factor is 0.55 against a measured 0.43 (86K real / 200K
// estimated): ~28% of headroom above the observation, which absorbs both the
// per-file content-class mix (a bundle heavier in prose than the probe's tokenizes
// closer to cx's estimate) and ordinary tokenizer drift between model revisions.
const (
	piFactorAnthropic = 1.00
	piFactorOpenAI    = 0.55
	piFactorGoogle    = 1.00
	// piFactorUnknown deliberately does NOT discount. An unrecognized model
	// gets cx's raw estimate, which for a non-Anthropic tokenizer is
	// pessimistic — the correct bias when we cannot identify the tokenizer.
	piFactorUnknown = 1.00
)

// piModelWindow is the usable context window, in tokens, for a model family.
// These are the published windows for the families Pi is routed at today.
const (
	piWindowDefault = 200_000
	piWindow400K    = 400_000
	piWindow1M      = 1_000_000
)

// piSeedBudgetFraction is the share of the window the seed may occupy. The
// remainder is the dialogue budget: system prompt, tool definitions, tool
// results, and every turn of the actual conversation. Two thirds for the seed
// is the practical ceiling — beyond it, a long design chat compacts before it
// converges, which is the failure this gate exists to prevent.
const piSeedBudgetFraction = 0.66

// PiModelProfile is the resolved window/tokenizer profile for one model string.
type PiModelProfile struct {
	// Family is a short human label used in gate messages.
	Family string
	// Window is the model's total context window in tokens.
	Window int
	// Factor converts a cx token estimate to this family's tokenizer.
	Factor float64
	// Known is false when the model string matched nothing and the
	// conservative defaults were applied.
	Known bool
}

// SeedBudget is the number of tokens the seed may occupy under this profile.
func (p PiModelProfile) SeedBudget() int {
	return int(float64(p.Window) * piSeedBudgetFraction)
}

// ConvertEstimate converts a cx token estimate to this family's tokenizer,
// rounding up.
//
// The arithmetic goes through per-mille integers rather than float64 because
// binary floating point cannot represent 0.55: 200000 * 0.55 evaluates to
// 110000.00000000001, and a naive Ceil turns a round number into 110001. That
// is cosmetically embarrassing rather than dangerous, but a gate whose numbers
// do not reproduce by hand is a gate nobody trusts.
func (p PiModelProfile) ConvertEstimate(cxTokens int) int {
	permille := int64(math.Round(p.Factor * 1000))
	return int((int64(cxTokens)*permille + 999) / 1000)
}

// ResolvePiModelProfile maps a Pi `--model` string onto a window/tokenizer
// profile. Matching is prefix/substring based on the vendor families Pi routes
// at, because model strings reach us in several spellings (bare ids like
// `gpt-5.6-sol`, provider-qualified ids like `anthropic/claude-opus-5`, and
// OpenRouter-style triples).
//
// An empty model is NOT an error here: pi selects its own configured default,
// which we cannot see. It resolves to the unknown profile — smallest window,
// no discount — so the gate stays conservative exactly where it is blindest.
func ResolvePiModelProfile(model string) PiModelProfile {
	m := strings.ToLower(strings.TrimSpace(model))
	// Strip a provider qualifier ("openrouter/openai/gpt-5.6" → "gpt-5.6").
	if idx := strings.LastIndex(m, "/"); idx >= 0 && idx+1 < len(m) {
		m = m[idx+1:]
	}

	switch {
	case strings.HasPrefix(m, "claude-opus-5"), strings.HasPrefix(m, "claude-sonnet-5"):
		return PiModelProfile{Family: "anthropic", Window: piWindow400K, Factor: piFactorAnthropic, Known: true}
	case strings.HasPrefix(m, "claude-"):
		return PiModelProfile{Family: "anthropic", Window: piWindowDefault, Factor: piFactorAnthropic, Known: true}
	case strings.HasPrefix(m, "gemini-"):
		return PiModelProfile{Family: "google", Window: piWindow1M, Factor: piFactorGoogle, Known: true}
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"), strings.HasPrefix(m, "codex"):
		return PiModelProfile{Family: "openai", Window: piWindow400K, Factor: piFactorOpenAI, Known: true}
	default:
		return PiModelProfile{Family: "unknown", Window: piWindowDefault, Factor: piFactorUnknown, Known: false}
	}
}

// piWindowOverrideEnv lets an operator state the real window when the table
// does not know the model — the escape hatch named in the gate's own error, so
// a new model never becomes a hard block on shipping work.
const piWindowOverrideEnv = "GROVE_FLOW_PI_SESSION_WINDOW_TOKENS"

// applyWindowOverride replaces the profile's window when the override env var
// holds a positive integer. A malformed value is reported so a typo cannot
// silently reinstate the default the operator was trying to replace.
func applyWindowOverride(profile PiModelProfile) (PiModelProfile, error) {
	raw := strings.TrimSpace(os.Getenv(piWindowOverrideEnv))
	if raw == "" {
		return profile, nil
	}
	tokens, err := strconv.Atoi(raw)
	if err != nil || tokens <= 0 {
		return profile, fmt.Errorf("%s=%q is not a positive integer token count", piWindowOverrideEnv, raw)
	}
	profile.Window = tokens
	profile.Known = true
	return profile, nil
}

// PiWindowGateResult is the outcome of sizing a seed against a model window.
type PiWindowGateResult struct {
	Profile PiModelProfile
	// CXTokens is the estimate as cx computes it (Anthropic-calibrated).
	CXTokens int
	// ModelTokens is CXTokens converted to the target family's tokenizer.
	ModelTokens int
	// Budget is the token ceiling the seed had to fit under.
	Budget int
	// Advisory is a non-fatal note (e.g. an unknown model), or "".
	Advisory string
}

// FormatLine renders the one-line summary written to job.log at launch.
func (r PiWindowGateResult) FormatLine() string {
	return fmt.Sprintf("Seed window gate: %s tokens (cx estimate %s, %s tokenizer factor %.2f) against a %s-token budget of a %s-token %s window",
		piTokens(r.ModelTokens), piTokens(r.CXTokens), r.Profile.Family, r.Profile.Factor,
		piTokens(r.Budget), piTokens(r.Profile.Window), r.Profile.Family)
}

// GatePiSeedWindow sizes a seed against the target model's context window.
// jobRef is the job's filename, used only to make the error's recovery hint
// copy-pasteable.
//
// It returns an error — the launch-refusing kind — when the converted estimate
// exceeds the seed budget. The error names both the measured size and every
// lever that can fix it, because "your context is too big" without a next step
// is the least useful gate there is.
func GatePiSeedWindow(model string, cxTokens int, jobRef string) (*PiWindowGateResult, error) {
	profile := ResolvePiModelProfile(model)
	profile, overrideErr := applyWindowOverride(profile)

	result := &PiWindowGateResult{
		Profile:     profile,
		CXTokens:    cxTokens,
		ModelTokens: profile.ConvertEstimate(cxTokens),
		Budget:      profile.SeedBudget(),
	}
	switch {
	case overrideErr != nil:
		result.Advisory = overrideErr.Error() + " — ignoring the override and using the table window"
	case !profile.Known && model == "":
		result.Advisory = fmt.Sprintf("no model: in the job frontmatter — pi will pick its own default, so the gate assumed the conservative %s-token window and cx's Anthropic token calibration. Set model: (and %s if its window differs) for an accurate gate.",
			piTokens(profile.Window), piWindowOverrideEnv)
	case !profile.Known:
		result.Advisory = fmt.Sprintf("model %q is not in the window table — the gate assumed the conservative %s-token window and cx's Anthropic token calibration. Set %s to the model's real window if that is wrong.",
			model, piTokens(profile.Window), piWindowOverrideEnv)
	}

	if result.ModelTokens > result.Budget {
		return result, fmt.Errorf("seed window gate: the frozen context is ~%s tokens for %s (cx estimate %s tokens x %.2f %s tokenizer factor), over the %s-token seed budget (%.0f%% of a %s-token window). A seed this size leaves no room for the dialogue and would be auto-compacted mid-chat — which destroys the curated context the session exists to hold. Narrow the rules file (cx stats --job %s shows the largest consumers), pick a bigger-window model, or set %s if this model's real window is larger",
			piTokens(result.ModelTokens), profile.Family, piTokens(cxTokens), profile.Factor, profile.Family,
			piTokens(result.Budget), piSeedBudgetFraction*100, piTokens(profile.Window),
			jobRef, piWindowOverrideEnv)
	}
	return result, nil
}

// piTokens renders a token count through the shared humanizer, which is
// int64-typed because it also serves the usage ledger.
func piTokens(n int) string {
	return formatTokenCount(int64(n))
}
