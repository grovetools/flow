package orchestration

import (
	"strings"
	"testing"
)

// TestResolvePiModelProfile pins the calibration table, including the two
// properties that make it safe rather than merely present: an unknown model
// gets NO discount (cx's Anthropic estimate is pessimistic for other
// tokenizers, which is the right bias when blind), and a provider-qualified
// spelling resolves to the same family as the bare id.
func TestResolvePiModelProfile(t *testing.T) {
	tests := []struct {
		model  string
		family string
		factor float64
		known  bool
	}{
		{"gpt-5.6-sol", "openai", piFactorOpenAI, true},
		{"openrouter/openai/gpt-5.6", "openai", piFactorOpenAI, true},
		{"codex-mini", "openai", piFactorOpenAI, true},
		{"claude-opus-5", "anthropic", piFactorAnthropic, true},
		{"anthropic/claude-sonnet-4-5", "anthropic", piFactorAnthropic, true},
		{"gemini-3-pro", "google", piFactorGoogle, true},
		{"some-new-model-9", "unknown", piFactorUnknown, false},
		{"", "unknown", piFactorUnknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := ResolvePiModelProfile(tt.model)
			if got.Family != tt.family || got.Factor != tt.factor || got.Known != tt.known {
				t.Errorf("ResolvePiModelProfile(%q) = {%s %.2f known=%v}, want {%s %.2f known=%v}",
					tt.model, got.Family, got.Factor, got.Known, tt.family, tt.factor, tt.known)
			}
		})
	}
}

// TestPiTokenizerFactorHasMarginOverMeasurement guards the calibration against
// a future edit that "optimizes" the OpenAI factor down to the raw
// measurement. The probe measured 86K real tokens against a 200K cx estimate
// (0.43); the shipped factor must sit ABOVE that, because an optimistic factor
// fails as silent mid-session compaction rather than as an error.
func TestPiTokenizerFactorHasMarginOverMeasurement(t *testing.T) {
	const measured = 86_000.0 / 200_000.0 // 0.43, gpt-5.6-sol seeding probe
	if piFactorOpenAI <= measured {
		t.Errorf("piFactorOpenAI = %.2f, must exceed the measured %.2f so the gate keeps a safety margin", piFactorOpenAI, measured)
	}
	if piFactorOpenAI > 1.0 {
		t.Errorf("piFactorOpenAI = %.2f, must not exceed cx's own estimate (1.0)", piFactorOpenAI)
	}
	if piFactorUnknown != 1.0 {
		t.Errorf("piFactorUnknown = %.2f, must be 1.0: an unidentified tokenizer gets no discount", piFactorUnknown)
	}
}

// TestGatePiSeedWindow_PassAndFail: the gate admits a bundle that leaves room
// for a dialogue and refuses one that does not, with an actionable error.
func TestGatePiSeedWindow_PassAndFail(t *testing.T) {
	// 200K cx tokens on gpt-5.6-sol → 110K after the 0.55 factor, against a
	// 264K budget (66% of 400K). This is the probe's real bundle, and it must
	// pass — a gate that rejects it would reject the feature's whole use case.
	result, err := GatePiSeedWindow("gpt-5.6-sol", 200_000, "04-design.md")
	if err != nil {
		t.Fatalf("GatePiSeedWindow(200K cx, gpt-5.6-sol) = %v, want it to pass", err)
	}
	if result.ModelTokens != 110_000 {
		t.Errorf("ModelTokens = %d, want 110000 (200K x 0.55)", result.ModelTokens)
	}
	if result.Budget != 264_000 {
		t.Errorf("Budget = %d, want 264000 (66%% of 400K)", result.Budget)
	}

	// The same bundle against a 200K-window Claude: 200K estimated tokens
	// against a 132K budget — refused.
	_, err = GatePiSeedWindow("claude-sonnet-4-5", 200_000, "04-design.md")
	if err == nil {
		t.Fatal("GatePiSeedWindow(200K cx, 200K window) succeeded, want a refusal")
	}
	for _, want := range []string{"seed window gate", "Narrow the rules file", "04-design.md", piWindowOverrideEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q; the gate must name the problem AND every lever that fixes it:\n%v", want, err)
		}
	}
}

// TestGatePiSeedWindow_UnknownModelAdvises: an unrecognized model must not
// fail — it must gate conservatively and say so.
func TestGatePiSeedWindow_UnknownModelAdvises(t *testing.T) {
	result, err := GatePiSeedWindow("some-new-model-9", 50_000, "04-design.md")
	if err != nil {
		t.Fatalf("GatePiSeedWindow() error = %v, want a pass with an advisory", err)
	}
	if result.Advisory == "" || !strings.Contains(result.Advisory, piWindowOverrideEnv) {
		t.Errorf("Advisory = %q, want it to name the override escape hatch", result.Advisory)
	}
	if result.ModelTokens != 50_000 {
		t.Errorf("ModelTokens = %d, want the undiscounted 50000", result.ModelTokens)
	}
}

// TestGatePiSeedWindow_Override: an operator can state the real window for a
// model the table does not know, and it takes effect.
func TestGatePiSeedWindow_Override(t *testing.T) {
	t.Setenv(piWindowOverrideEnv, "1000000")
	result, err := GatePiSeedWindow("some-new-model-9", 300_000, "04-design.md")
	if err != nil {
		t.Fatalf("GatePiSeedWindow() with an override error = %v", err)
	}
	if result.Profile.Window != 1_000_000 {
		t.Errorf("Window = %d, want the overridden 1000000", result.Profile.Window)
	}

	t.Setenv(piWindowOverrideEnv, "not-a-number")
	result, err = GatePiSeedWindow("claude-opus-5", 10_000, "04-design.md")
	if err != nil {
		t.Fatalf("a malformed override must not fail the gate: %v", err)
	}
	// A typo must be reported, not silently ignored — otherwise the operator
	// believes an override is in force that is not.
	if !strings.Contains(result.Advisory, piWindowOverrideEnv) {
		t.Errorf("Advisory = %q, want it to report the malformed override", result.Advisory)
	}
	if result.Profile.Window != piWindow400K {
		t.Errorf("Window = %d, want the table value %d after ignoring a malformed override", result.Profile.Window, piWindow400K)
	}
}
