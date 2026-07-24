package orchestration

import (
	"math"
	"testing"

	"github.com/grovetools/agentlogs/pkg/usage"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
)

// Grove prices a job through one of two tables depending on how the tokens were
// captured (see metrics_record.go):
//
//   - API path — a grove-<provider> module made the call and read the usage off
//     the response. Rates come from that module's registry
//     (anthropicmodels.GetPricingOK, then logging.EstimateCostWithCacheSplitOK).
//   - Transcript path — a third-party CLI ran the model and wrote a JSONL.
//     Rates come from agentlogs' embedded models.dev snapshot (usage.EntryCost).
//
// Both are hand-maintained, in different repos, and their Anthropic rows
// overlap. Nothing forced them to agree, so a model launch that updated one and
// missed the other drifted silently — and did, twice: claude-fable-5 was once
// missing from the API-path cascade and under-billed ~3.3x as Sonnet, and
// claude-opus-5 was later missing from the transcript table, which made every
// Claude 5 agent job report "⚠ unpriced".
//
// flow is the only repo that depends on both, so the check lives here. These
// tests fail on the next model launch that updates one table and not the other.
//
// When one fails, the fix is a data edit, not a test edit:
//   - missing from the transcript table -> add the model to
//     agentlogs/pkg/usage/models-dev-pricing.json.
//   - missing from the registry -> add it to grove-anthropic/pkg/models.
//   - rates disagree -> find which one matches Anthropic's published price and
//     correct the other.

// registryPriceEpsilon tolerates float noise in per-million rates; real pricing
// differences are cents-per-million or larger, far above this.
const registryPriceEpsilon = 1e-6

// perMillion converts a per-token rate back to the per-million figure both
// tables are authored in.
func perMillion(perToken float64) float64 { return perToken * 1_000_000 }

// TestAnthropicPricingTablesAgree pins that every current Anthropic model is
// priced by the transcript table too, at the same base rates as the registry.
//
// "Priced" means Find resolves it, not that a particular key exists: the fuzzy
// matcher reaches a bare model string through any gateway-prefixed spelling
// (anthropic/…, us.anthropic.…), so dropping one key still passes while the
// others remain. That is the intended resilience — what this catches is a model
// with no entry in any spelling.
func TestAnthropicPricingTablesAgree(t *testing.T) {
	pm := usage.DefaultPricing()

	for _, m := range anthropicmodels.CurrentModels() {
		// Both the full ID and the alias reach transcripts: the API reports
		// dated IDs, while Claude Code writes the bare alias.
		lookups := []string{m.ID}
		if m.Alias != "" && m.Alias != m.ID {
			lookups = append(lookups, m.Alias)
		}
		for _, lookup := range lookups {
			t.Run(lookup, func(t *testing.T) {
				p, ok := pm.Find(lookup)
				if !ok {
					t.Fatalf("%q is in the grove-anthropic registry but has no entry in "+
						"agentlogs' pricing table — agent jobs on it will summarize as "+
						"\"⚠ unpriced\". Add it to models-dev-pricing.json.", lookup)
				}
				if got := perMillion(p.Input); math.Abs(got-m.Input) > registryPriceEpsilon {
					t.Errorf("input rate for %q disagrees: registry %.4f/M, transcript table %.4f/M",
						lookup, m.Input, got)
				}
				if got := perMillion(p.Output); math.Abs(got-m.Output) > registryPriceEpsilon {
					t.Errorf("output rate for %q disagrees: registry %.4f/M, transcript table %.4f/M",
						lookup, m.Output, got)
				}
			})
		}
	}
}

// TestAnthropicCacheRatesMatchDerivedMultipliers pins the cache half of the same
// agreement. The registry stores only input/output, so the API path *derives*
// cache rates from Anthropic's standard multipliers (0.10x read, 1.25x 5m
// write); the transcript table stores them explicitly. Equal base rates alone
// don't make the two paths agree on a dollar figure — a cached-heavy agent job
// is mostly cache reads — so the explicit values have to match what the API
// path would derive.
//
// A future model that genuinely prices cache off-multiple is a real finding,
// not test noise: it means EstimateCostWithCacheSplitOK needs a per-model
// override rather than a flat multiplier.
func TestAnthropicCacheRatesMatchDerivedMultipliers(t *testing.T) {
	const (
		cacheReadMultiplier    = 0.10
		cacheWrite5mMultiplier = 1.25
	)
	pm := usage.DefaultPricing()

	for _, m := range anthropicmodels.CurrentModels() {
		t.Run(m.ID, func(t *testing.T) {
			p, ok := pm.Find(m.ID)
			if !ok {
				t.Skip("no transcript-table entry; TestAnthropicPricingTablesAgree reports it")
			}
			if want := m.Input * cacheReadMultiplier; math.Abs(perMillion(p.CacheRead)-want) > registryPriceEpsilon {
				t.Errorf("cache-read rate for %q disagrees: transcript table %.4f/M, "+
					"API path derives %.4f/M (input %.4f x %.2f)",
					m.ID, perMillion(p.CacheRead), want, m.Input, cacheReadMultiplier)
			}
			if want := m.Input * cacheWrite5mMultiplier; math.Abs(perMillion(p.CacheCreate)-want) > registryPriceEpsilon {
				t.Errorf("cache-write rate for %q disagrees: transcript table %.4f/M, "+
					"API path derives %.4f/M (input %.4f x %.2f)",
					m.ID, perMillion(p.CacheCreate), want, m.Input, cacheWrite5mMultiplier)
			}
		})
	}
}
