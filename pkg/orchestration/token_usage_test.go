package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/agentlogs/pkg/usage"
)

func TestFormatTokenCount(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1.0k"},
		{12345, "12.3k"},
		{179549, "179.5k"},
		{1234567, "1.2M"},
		{-1500, "-1.5k"},
	}
	for _, c := range cases {
		if got := formatTokenCount(c.in); got != c.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatCostUSD(t *testing.T) {
	if got := formatCostUSD(1.2345, false); got != "$1.23" {
		t.Errorf("formatCostUSD(1.2345,false) = %q, want $1.23", got)
	}
	if got := formatCostUSD(0, true); got != "⚠ unpriced" {
		t.Errorf("formatCostUSD(0,true) = %q, want ⚠ unpriced", got)
	}
}

func TestRenderTokenUsageSection(t *testing.T) {
	s := usage.Summary{
		Usage: usage.Usage{
			Input:        1000,
			Output:       200,
			CacheRead:    5000,
			CacheWrite5m: 300,
			CacheWrite1h: 50,
		},
		CostUSD:      0.42,
		Models:       []string{"claude-opus-4-5"},
		MessageCount: 7,
		ModelBreakdown: []usage.AgentUsage{
			{Model: "claude-opus-4-5", Usage: usage.Usage{Input: 1000, Output: 200}, CostUSD: 0.42},
		},
	}
	out := renderTokenUsageSection(s)

	for _, want := range []string{
		"Total tokens: 6.5k",
		"Cost: $0.42",
		"Models: claude-opus-4-5",
		"Messages: 7",
		"### Token breakdown",
		"Input: 1.0k",
		"Output: 200",
		"Cache read: 5.0k",
		"Cache write (5m): 300",
		"Cache write (1h): 50",
		"### Per-model",
		"`claude-opus-4-5`: 1.2k tokens, $0.42",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered section missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderTokenUsageSectionOmitsZeroCacheWrite1h(t *testing.T) {
	s := usage.Summary{
		Usage:   usage.Usage{Input: 10, Output: 5},
		CostUSD: 0.01,
	}
	out := renderTokenUsageSection(s)
	if strings.Contains(out, "Cache write (1h)") {
		t.Errorf("expected no 1h cache write line when zero\n%s", out)
	}
}

func TestRenderTokenUsageSectionUnpriced(t *testing.T) {
	s := usage.Summary{
		Usage:          usage.Usage{Input: 10, Output: 5},
		MissingPricing: true,
	}
	out := renderTokenUsageSection(s)
	if !strings.Contains(out, "⚠ unpriced") {
		t.Errorf("expected unpriced marker\n%s", out)
	}
}

func TestTokenUsageArtifactPath(t *testing.T) {
	got := TokenUsageArtifactPath("/plans/p", "job-1")
	want := filepath.Join("/plans/p", ".artifacts", "job-1", "token-usage.json")
	if got != want {
		t.Errorf("TokenUsageArtifactPath = %q, want %q", got, want)
	}
}

func TestReadTokenUsageArtifactRoundTrip(t *testing.T) {
	planDir := t.TempDir()
	jobID := "job-xyz"

	// Build a summary, write it via the artifact path, and read it back.
	want := usage.Summary{
		SessionID: "sess-1",
		Usage:     usage.Usage{Input: 111, Output: 22, CacheRead: 3},
		CostUSD:   0.07,
		Models:    []string{"claude-opus-4-5"},
	}

	artifactDir := filepath.Dir(TokenUsageArtifactPath(planDir, jobID))
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TokenUsageArtifactPath(planDir, jobID), data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := ReadTokenUsageArtifact(planDir, jobID)
	if !ok {
		t.Fatal("ReadTokenUsageArtifact returned ok=false")
	}
	if got.SessionID != want.SessionID || got.Usage.Total() != want.Usage.Total() || got.CostUSD != want.CostUSD {
		t.Errorf("roundtrip mismatch: got %+v want %+v", got, want)
	}
}

func TestReadTokenUsageArtifactMissing(t *testing.T) {
	planDir := t.TempDir()
	if _, ok := ReadTokenUsageArtifact(planDir, "nope"); ok {
		t.Error("expected ok=false for missing artifact")
	}
}
