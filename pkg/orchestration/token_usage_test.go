package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/agentlogs/pkg/usage"
	"github.com/grovetools/grove-anthropic/pkg/anthropic"
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
	// Sub-half-cent real costs surface as "<$0.01" rather than a misleading
	// rounded "$0.00".
	if got := formatCostUSD(0.004, false); got != "<$0.01" {
		t.Errorf("formatCostUSD(0.004,false) = %q, want <$0.01", got)
	}
	// A genuine zero stays "$0.00" (distinct from nearly-free).
	if got := formatCostUSD(0, false); got != "$0.00" {
		t.Errorf("formatCostUSD(0,false) = %q, want $0.00", got)
	}
	// At/above a cent, normal formatting.
	if got := formatCostUSD(0.01, false); got != "$0.01" {
		t.Errorf("formatCostUSD(0.01,false) = %q, want $0.01", got)
	}
}

func TestAccumulateAPITokenUsage(t *testing.T) {
	planDir := t.TempDir()
	plan := &Plan{Directory: planDir}
	job := &Job{ID: "chat-1"}

	// First turn: fresh artifact.
	u1 := &anthropic.UsageResult{
		Model:               "claude-fable-5",
		InputTokens:         1000,
		OutputTokens:        200,
		CacheReadTokens:     500,
		CacheCreationTokens: 300,
		EstimatedCostUSD:    0.05,
		KnownPricing:        true,
	}
	if err := AccumulateAPITokenUsage(plan, job, u1); err != nil {
		t.Fatalf("first accumulate: %v", err)
	}
	s, ok := ReadTokenUsageArtifact(planDir, job.ID)
	if !ok {
		t.Fatal("artifact not written after first turn")
	}
	if s.SessionID != job.ID {
		t.Errorf("SessionID = %q, want %q", s.SessionID, job.ID)
	}
	if s.ProjectPath != planDir {
		t.Errorf("ProjectPath = %q, want %q", s.ProjectPath, planDir)
	}
	if s.MessageCount != 1 {
		t.Errorf("MessageCount = %d, want 1", s.MessageCount)
	}
	if s.Usage.Input != 1000 || s.Usage.Output != 200 || s.Usage.CacheRead != 500 || s.Usage.CacheWrite5m != 300 {
		t.Errorf("usage mismap: %+v", s.Usage)
	}
	if s.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v, want 0.05", s.CostUSD)
	}
	// Peak context = input + cache_read + cache_creation = 1800.
	if s.ContextSize != 1800 {
		t.Errorf("ContextSize = %d, want 1800", s.ContextSize)
	}
	if s.FirstActivity.IsZero() || s.LastActivity.IsZero() {
		t.Errorf("activity timestamps not set: %+v / %+v", s.FirstActivity, s.LastActivity)
	}

	// Second turn: SMALLER prompt (ContextSize must hold peak), same model.
	u2 := &anthropic.UsageResult{
		Model:            "claude-fable-5",
		InputTokens:      100,
		OutputTokens:     50,
		EstimatedCostUSD: 0.01,
		KnownPricing:     true,
	}
	if err := AccumulateAPITokenUsage(plan, job, u2); err != nil {
		t.Fatalf("second accumulate: %v", err)
	}
	s, _ = ReadTokenUsageArtifact(planDir, job.ID)
	if s.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", s.MessageCount)
	}
	if s.Usage.Input != 1100 || s.Usage.Output != 250 {
		t.Errorf("usage not summed: %+v", s.Usage)
	}
	if s.ContextSize != 1800 {
		t.Errorf("ContextSize dropped to %d; must hold peak 1800", s.ContextSize)
	}
	if len(s.Models) != 1 || len(s.ModelBreakdown) != 1 {
		t.Errorf("model dedupe failed: Models=%v Breakdown=%d", s.Models, len(s.ModelBreakdown))
	}
	if s.ModelBreakdown[0].Usage.Input != 1100 {
		t.Errorf("per-model breakdown not summed: %+v", s.ModelBreakdown[0])
	}

	// Third turn: DIFFERENT model, LARGER prompt, and unpriced.
	u3 := &anthropic.UsageResult{
		Model:            "claude-opus-4-8",
		InputTokens:      5000,
		CacheReadTokens:  1000,
		EstimatedCostUSD: 0.20,
		KnownPricing:     false,
	}
	if err := AccumulateAPITokenUsage(plan, job, u3); err != nil {
		t.Fatalf("third accumulate: %v", err)
	}
	s, _ = ReadTokenUsageArtifact(planDir, job.ID)
	if len(s.Models) != 2 || len(s.ModelBreakdown) != 2 {
		t.Errorf("second model not added: Models=%v Breakdown=%d", s.Models, len(s.ModelBreakdown))
	}
	if !s.MissingPricing {
		t.Errorf("MissingPricing should be sticky-true after an unpriced turn")
	}
	if s.ContextSize != 6000 {
		t.Errorf("ContextSize = %d, want 6000 (grown to larger turn)", s.ContextSize)
	}

	// No .tmp residue left behind by the atomic write.
	if _, err := os.Stat(TokenUsageArtifactPath(planDir, job.ID) + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: err=%v", err)
	}
}

func TestAccumulateAPITokenUsageNil(t *testing.T) {
	plan := &Plan{Directory: t.TempDir()}
	job := &Job{ID: "j"}
	if err := AccumulateAPITokenUsage(plan, job, nil); err != nil {
		t.Errorf("nil usage should be a no-op, got %v", err)
	}
	if _, ok := ReadTokenUsageArtifact(plan.Directory, job.ID); ok {
		t.Errorf("nil usage should not write an artifact")
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
