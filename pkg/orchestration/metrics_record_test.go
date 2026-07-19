package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/agentlogs/pkg/usage"
	"github.com/grovetools/eval/pkg/record"
)

// writeJobFile writes a minimal job markdown file with the given frontmatter
// lines and returns its path.
func writeJobFile(t *testing.T, dir, id string, frontmatter string) string {
	t.Helper()
	path := filepath.Join(dir, id+".md")
	// status is a required field: LoadJob rejects a job file without one.
	content := fmt.Sprintf(
		"---\nid: %s\ntitle: test job\ntype: headless_agent\nstatus: completed\n%s---\n\nbody\n",
		id, frontmatter)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing job file: %v", err)
	}
	return path
}

// writeTokenUsage plants a token-usage.json artifact for the job.
func writeTokenUsage(t *testing.T, planDir, jobID string, s usage.Summary) {
	t.Helper()
	path := TokenUsageArtifactPath(planDir, jobID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write token usage: %v", err)
	}
}

func readRecord(t *testing.T, planDir, jobID string) record.RunRecord {
	t.Helper()
	b, err := os.ReadFile(MetricsRecordArtifactPath(planDir, jobID))
	if err != nil {
		t.Fatalf("reading metrics record: %v", err)
	}
	var r record.RunRecord
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("metrics.json does not parse as a record.RunRecord: %v", err)
	}
	return r
}

// P1-12: with a config vector on disk, the record's key and config come from it.
func TestWriteMetricsRecordWithConfigVector(t *testing.T) {
	dir := t.TempDir()
	plan := &Plan{Directory: dir}
	job := &Job{ID: "job-1", Type: JobTypeHeadlessAgent}

	v := record.ConfigVector{Model: "claude-opus-4-8", Provider: "claude",
		Components: map[string]string{"briefing": "h1"}}
	if err := WriteConfigVectorArtifact(dir, "job-1", v); err != nil {
		t.Fatalf("stamp vector: %v", err)
	}

	if err := WriteMetricsRecord(job, plan); err != nil {
		t.Fatalf("write record: %v", err)
	}

	r := readRecord(t, dir, "job-1")
	if r.Schema != record.SchemaVersion {
		t.Errorf("schema = %q, want %q", r.Schema, record.SchemaVersion)
	}
	if r.Key.ConfigHash != v.Hash() {
		t.Errorf("config hash = %q, want the stamped vector's %q", r.Key.ConfigHash, v.Hash())
	}
	if r.Key.TaskID != "job-1" || r.Key.Replicate != 0 {
		t.Errorf("unexpected key: %+v", r.Key)
	}
	if r.Config.Model != "claude-opus-4-8" {
		t.Errorf("config model = %q", r.Config.Model)
	}

	// Axes owned by other writers must be absent, not zeroed — D6 makes
	// double-writing an axis a hard join error.
	if r.Process != nil || r.Outcome != nil || r.AdherenceJudged != nil {
		t.Errorf("record carries an axis it does not own: %+v", r)
	}
	if r.ComponentMetrics != nil {
		t.Error("record carries ComponentMetrics, which P1 does not write")
	}
}

// A missing config vector is legitimate (a pre-feature job): the record is
// still written, with a synthesized model-only config.
func TestWriteMetricsRecordWithoutConfigVector(t *testing.T) {
	dir := t.TempDir()
	plan := &Plan{Directory: dir}
	job := &Job{ID: "job-2", Type: JobTypeHeadlessAgent, Model: "some-model"}

	if err := WriteMetricsRecord(job, plan); err != nil {
		t.Fatalf("write record: %v", err)
	}

	r := readRecord(t, dir, "job-2")
	if r.Config.Model != "some-model" {
		t.Errorf("synthesized config model = %q, want the job's model", r.Config.Model)
	}
	if r.Key.ConfigHash == "" {
		t.Error("config hash is empty; a record must always carry an envelope")
	}
}

// P1-16: the writer is a whole-file overwrite, so attaching it at more than one
// completion seam is safe.
func TestWriteMetricsRecordIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	plan := &Plan{Directory: dir}
	job := &Job{ID: "job-3", Type: JobTypeHeadlessAgent}

	if err := WriteMetricsRecord(job, plan); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, _ := os.ReadFile(MetricsRecordArtifactPath(dir, "job-3"))
	if err := WriteMetricsRecord(job, plan); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, _ := os.ReadFile(MetricsRecordArtifactPath(dir, "job-3"))

	if string(first) != string(second) {
		t.Fatal("two writes produced different bytes; the writer is not idempotent")
	}
}

// P1-13: the Cost mapping, including the two traps — ExtraTotal must not leak
// in, and a missing artifact must yield nil rather than a zeroed struct.
func TestCostMapping(t *testing.T) {
	t.Run("maps every field and excludes ExtraTotal", func(t *testing.T) {
		dir := t.TempDir()
		plan := &Plan{Directory: dir}
		job := &Job{ID: "job-c", Type: JobTypeHeadlessAgent}

		writeTokenUsage(t, dir, "job-c", usage.Summary{
			Usage: usage.Usage{
				Input: 100, Output: 200, CacheRead: 300,
				CacheWrite5m: 40, CacheWrite1h: 60,
				ExtraTotal: 9999, // must not appear anywhere
			},
			CostUSD: 1.25,
		})

		if err := WriteMetricsRecord(job, plan); err != nil {
			t.Fatalf("write: %v", err)
		}
		r := readRecord(t, dir, "job-c")
		if r.Cost == nil {
			t.Fatal("Cost is nil despite a present token-usage artifact")
		}
		if r.Cost.InputTokens != 100 || r.Cost.OutputTokens != 200 || r.Cost.CacheReadTokens != 300 {
			t.Errorf("token mapping wrong: %+v", r.Cost)
		}
		if r.Cost.CacheWriteTokens != 100 {
			t.Errorf("cache write = %d, want 40+60=100", r.Cost.CacheWriteTokens)
		}
		if r.Cost.EstimatedUSD != 1.25 {
			t.Errorf("usd = %v, want 1.25", r.Cost.EstimatedUSD)
		}
		// The ExtraTotal guard: no field may have absorbed it.
		total := r.Cost.InputTokens + r.Cost.OutputTokens +
			r.Cost.CacheReadTokens + r.Cost.CacheWriteTokens
		if total != 700 {
			t.Errorf("token total = %d, want 700; ExtraTotal leaked in", total)
		}
	})

	t.Run("missing artifact yields a nil Cost, never a zeroed one", func(t *testing.T) {
		dir := t.TempDir()
		plan := &Plan{Directory: dir}
		job := &Job{ID: "job-nc", Type: JobTypeHeadlessAgent}

		if err := WriteMetricsRecord(job, plan); err != nil {
			t.Fatalf("write: %v", err)
		}
		if r := readRecord(t, dir, "job-nc"); r.Cost != nil {
			t.Fatalf("Cost = %+v, want nil when no usage was archived", r.Cost)
		}
	})

	t.Run("MissingPricing does not suppress the record", func(t *testing.T) {
		dir := t.TempDir()
		plan := &Plan{Directory: dir}
		job := &Job{ID: "job-mp", Type: JobTypeHeadlessAgent}

		writeTokenUsage(t, dir, "job-mp", usage.Summary{
			Usage:          usage.Usage{Input: 10},
			CostUSD:        0,
			MissingPricing: true,
		})

		if err := WriteMetricsRecord(job, plan); err != nil {
			t.Fatalf("write: %v", err)
		}
		r := readRecord(t, dir, "job-mp")
		if r.Cost == nil {
			t.Fatal("MissingPricing suppressed the Cost axis; it should not")
		}
		if r.Cost.CostSource == "" {
			t.Error("cost_source not set under MissingPricing")
		}
		// D7 amendment: the caveat must reach the RECORD, not just the
		// co-located token-usage.json. Nothing here was priced, so a bare
		// estimated_usd of 0 would read as "this run was free".
		if r.Cost.PricingCompleteness != record.PricingCompletenessUnpriced {
			t.Errorf("pricing_completeness = %q, want %q — an unpriced run's "+
				"$0 must not read as a free run",
				r.Cost.PricingCompleteness, record.PricingCompletenessUnpriced)
		}
	})
}

// D7 amendment: the record must self-describe how far EstimatedUSD can be
// trusted. summary.MissingPricing is a sticky OR that says only "at least one
// call was unpriced", and the two capture paths then fail differently — the
// transcript path DROPS unpriced calls from the total (so a survivor is a
// lower bound), while the api path substitutes models.GetPricingOK's default
// fallback rate and keeps costing (so nothing is dropped but the figure is a
// guess). Each of those is a different instruction to the consumer.
func TestPricingCompletenessClassification(t *testing.T) {
	tests := []struct {
		name    string
		jobType JobType
		summary usage.Summary
		want    string
	}{
		{
			name:    "fully priced transcript run is complete",
			jobType: JobTypeHeadlessAgent,
			summary: usage.Summary{Usage: usage.Usage{Input: 10}, CostUSD: 2.50},
			want:    record.PricingCompletenessComplete,
		},
		{
			name:    "fully priced api run is complete",
			jobType: JobTypeOneshot,
			summary: usage.Summary{Usage: usage.Usage{Input: 10}, CostUSD: 2.50},
			want:    record.PricingCompletenessComplete,
		},
		{
			// THE case a bare pointer could not express: MissingPricing
			// co-occurring with a nonzero cost. Some calls priced, some
			// dropped => the figure is a floor, not a total.
			name:    "transcript run with some priced and some dropped is partial",
			jobType: JobTypeHeadlessAgent,
			summary: usage.Summary{Usage: usage.Usage{Input: 10}, CostUSD: 0.42, MissingPricing: true},
			want:    record.PricingCompletenessPartial,
		},
		{
			name:    "transcript run with nothing priced is unpriced",
			jobType: JobTypeHeadlessAgent,
			summary: usage.Summary{Usage: usage.Usage{Input: 10}, CostUSD: 0, MissingPricing: true},
			want:    record.PricingCompletenessUnpriced,
		},
		{
			// The api path never drops a call — GetPricingOK's tier-3 default
			// (3.00/15.00) costs it anyway — so this is NOT a lower bound and
			// must not be labelled partial.
			name:    "api run costed at the fallback rate is fallback_rate",
			jobType: JobTypeOneshot,
			summary: usage.Summary{Usage: usage.Usage{Input: 10}, CostUSD: 0.42, MissingPricing: true},
			want:    record.PricingCompletenessFallbackRate,
		},
		{
			name:    "chat run costed at the fallback rate is fallback_rate",
			jobType: JobTypeChat,
			summary: usage.Summary{Usage: usage.Usage{Input: 10}, CostUSD: 0.42, MissingPricing: true},
			want:    record.PricingCompletenessFallbackRate,
		},
		{
			// Fallback arithmetic that happens to land on zero is still a
			// fallback estimate, not a dropped call.
			name:    "api run with zero fallback cost is still fallback_rate",
			jobType: JobTypeOneshot,
			summary: usage.Summary{Usage: usage.Usage{Input: 10}, CostUSD: 0, MissingPricing: true},
			want:    record.PricingCompletenessFallbackRate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			plan := &Plan{Directory: dir}
			job := &Job{ID: "j", Type: tc.jobType}
			writeTokenUsage(t, dir, "j", tc.summary)

			if err := WriteMetricsRecord(job, plan); err != nil {
				t.Fatalf("write: %v", err)
			}
			r := readRecord(t, dir, "j")
			// Demand the axis before asserting on it: a nil Cost would make
			// every assertion below a silent no-op.
			if r.Cost == nil {
				t.Fatalf("Cost axis is nil despite a planted usage artifact; "+
					"the pricing_completeness assertion never ran (%s)", tc.name)
			}
			if r.Cost.PricingCompleteness != tc.want {
				t.Errorf("pricing_completeness = %q, want %q",
					r.Cost.PricingCompleteness, tc.want)
			}
			// The cost figure itself must be forwarded unchanged — the flag
			// qualifies EstimatedUSD, it does not replace or zero it. Without
			// this, a writer that suppressed the number would still pass.
			if r.Cost.EstimatedUSD != tc.summary.CostUSD {
				t.Errorf("estimated_usd = %v, want the summary's %v",
					r.Cost.EstimatedUSD, tc.summary.CostUSD)
			}
		})
	}
}

// Zero-value safety at the WRITER: whenever flow emits a Cost axis it must
// stamp the flag. An unstamped axis would reach consumers as the unknown zero
// value, and while that is safe by design (it is not "complete"), flow always
// knows the answer, so leaving it blank is a writer bug this test catches.
func TestEveryWrittenCostCarriesPricingCompleteness(t *testing.T) {
	for _, jt := range []JobType{
		JobTypeOneshot, JobTypeChat, JobTypeHeadlessAgent,
		JobTypeInteractiveAgent, JobTypeIsolatedAgent,
	} {
		t.Run(string(jt), func(t *testing.T) {
			dir := t.TempDir()
			plan := &Plan{Directory: dir}
			job := &Job{ID: "j", Type: jt}
			writeTokenUsage(t, dir, "j", usage.Summary{Usage: usage.Usage{Input: 1}})

			if err := WriteMetricsRecord(job, plan); err != nil {
				t.Fatalf("write: %v", err)
			}
			r := readRecord(t, dir, "j")
			if r.Cost == nil {
				t.Fatalf("%s: Cost axis is nil despite a planted usage artifact", jt)
			}
			if r.Cost.PricingCompleteness == record.PricingCompletenessUnknown {
				t.Errorf("%s: flow emitted a Cost axis with no pricing_completeness", jt)
			}
		})
	}
}

// D24's call-path boundary: oneshot/chat are api_usage, every agent family is
// transcript_usage. This is not provider identity.
func TestCostSourceByCallPath(t *testing.T) {
	tests := []struct {
		jobType JobType
		want    string
	}{
		{JobTypeOneshot, costSourceAPIUsage},
		{JobTypeChat, costSourceAPIUsage},
		{JobTypeHeadlessAgent, costSourceTranscriptUsage},
		{JobTypeInteractiveAgent, costSourceTranscriptUsage},
		{JobTypeIsolatedAgent, costSourceTranscriptUsage},
	}

	for _, tc := range tests {
		t.Run(string(tc.jobType), func(t *testing.T) {
			dir := t.TempDir()
			plan := &Plan{Directory: dir}
			job := &Job{ID: "j", Type: tc.jobType}
			writeTokenUsage(t, dir, "j", usage.Summary{Usage: usage.Usage{Input: 1}})

			if err := WriteMetricsRecord(job, plan); err != nil {
				t.Fatalf("write: %v", err)
			}
			r := readRecord(t, dir, "j")
			if r.Cost == nil || r.Cost.CostSource != tc.want {
				t.Fatalf("cost_source = %v, want %q", r.Cost, tc.want)
			}
		})
	}
}

// P1-17: APIResponseSeconds is nil for every family. Agent families have a
// structurally empty ledger; oneshot/chat cannot be attributed safely because
// the orchestrator dispatches API jobs concurrently through one shared
// executor, so the process-global request-id env var would cross-contaminate.
func TestAPIResponseSecondsAlwaysNil(t *testing.T) {
	for _, jt := range []JobType{
		JobTypeOneshot, JobTypeChat, JobTypeHeadlessAgent,
		JobTypeInteractiveAgent, JobTypeIsolatedAgent,
	} {
		dir := t.TempDir()
		plan := &Plan{Directory: dir}
		job := &Job{ID: "j", Type: jt}
		writeTokenUsage(t, dir, "j", usage.Summary{Usage: usage.Usage{Input: 1}})

		if err := WriteMetricsRecord(job, plan); err != nil {
			t.Fatalf("write: %v", err)
		}
		r := readRecord(t, dir, "j")
		// The fixture plants usage precisely so the Cost axis is populated. A
		// `r.Cost != nil && ...` guard would turn this into a silent no-op the
		// day resolveCost stops finding the artifact — the assertion would
		// skip and the test would still read green. Demand the axis first.
		if r.Cost == nil {
			t.Fatalf("%s: Cost axis is nil despite a planted usage artifact; "+
				"the api_response_seconds assertion never ran", jt)
		}
		if r.Cost.APIResponseSeconds != nil {
			t.Errorf("%s: api_response_seconds = %v, want nil",
				jt, *r.Cost.APIResponseSeconds)
		}
	}
}

// P1-14: wall-clock comes from the PERSISTED duration, never from subtraction.
func TestWallClockSeconds(t *testing.T) {
	t.Run("reads the persisted duration", func(t *testing.T) {
		dir := t.TempDir()
		path := writeJobFile(t, dir, "job-w", "duration: 1m30s\n")
		job := &Job{ID: "job-w", Type: JobTypeHeadlessAgent, FilePath: path}

		got, ok := resolveWallClockSeconds(job)
		if !ok || got != 90 {
			t.Fatalf("wall clock = %v (ok=%v), want 90", got, ok)
		}
	})

	t.Run("falls back to the in-process delta", func(t *testing.T) {
		dir := t.TempDir()
		path := writeJobFile(t, dir, "job-x", "")
		start := time.Now().Add(-42 * time.Second)
		job := &Job{
			ID: "job-x", Type: JobTypeHeadlessAgent, FilePath: path,
			StartTime: start, EndTime: start.Add(42 * time.Second),
		}

		got, ok := resolveWallClockSeconds(job)
		if !ok || got < 41.9 || got > 42.1 {
			t.Fatalf("wall clock = %v (ok=%v), want ~42", got, ok)
		}
	})

	t.Run("nil when neither source is available", func(t *testing.T) {
		dir := t.TempDir()
		path := writeJobFile(t, dir, "job-y", "")
		job := &Job{ID: "job-y", Type: JobTypeHeadlessAgent, FilePath: path}

		if got, ok := resolveWallClockSeconds(job); ok {
			t.Fatalf("wall clock = %v, want unmeasured", got)
		}
	})

	// Every degenerate input must yield "not measured" rather than a wrong
	// number. This is the D4 guarantee the whole mechanism exists to provide.
	t.Run("all failure modes yield nil, never a wrong number", func(t *testing.T) {
		dir := t.TempDir()
		cases := []struct {
			name        string
			frontmatter string
			mutate      func(*Job)
		}{
			{"zero duration", "duration: 0s\n", nil},
			{"absent duration", "", nil},
			{"unparseable duration", "duration: not-a-duration\n", nil},
			{"negative in-memory delta", "", func(j *Job) {
				now := time.Now()
				j.StartTime, j.EndTime = now, now.Add(-10*time.Second)
			}},
			{"equal in-memory times", "", func(j *Job) {
				now := time.Now()
				j.StartTime, j.EndTime = now, now
			}},
			{"LoadJob error (missing file)", "", func(j *Job) {
				j.FilePath = filepath.Join(dir, "does-not-exist.md")
			}},
			{"empty file path", "", func(j *Job) { j.FilePath = "" }},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				path := writeJobFile(t, t.TempDir(), "j", tc.frontmatter)
				job := &Job{ID: "j", Type: JobTypeHeadlessAgent, FilePath: path}
				if tc.mutate != nil {
					tc.mutate(job)
				}
				got, ok := resolveWallClockSeconds(job)
				if ok {
					t.Fatalf("got a measured %v; want unmeasured", got)
				}
				if got != 0 {
					t.Fatalf("unmeasured result carried a value: %v", got)
				}
			})
		}
	})

	// The regression assertion, at its mandated strength: no constructed input
	// may produce a NON-POSITIVE wall clock in the emitted record. An
	// unmeasured wall clock is nil; a present one must be strictly > 0. A
	// present 0 would be the D4 lie this whole mechanism exists to prevent,
	// and testing only for < 0 is what let it ship once already.
	t.Run("emitted wall clock is never non-positive", func(t *testing.T) {
		dir := t.TempDir()
		plan := &Plan{Directory: dir}
		for _, fm := range []string{"duration: 0s\n", "duration: -5s\n", "", "duration: 1m30s\n"} {
			path := writeJobFile(t, t.TempDir(), "j", fm)
			job := &Job{ID: "j", Type: JobTypeHeadlessAgent, FilePath: path}
			writeTokenUsage(t, dir, "j", usage.Summary{Usage: usage.Usage{Input: 1}})

			if err := WriteMetricsRecord(job, plan); err != nil {
				t.Fatalf("write: %v", err)
			}
			r := readRecord(t, dir, "j")
			// Usage is planted for every iteration, so a nil Cost means the
			// wall-clock assertion below never ran at all. Fail loudly rather
			// than skipping into a green result.
			if r.Cost == nil {
				t.Fatalf("frontmatter %q: Cost axis is nil despite a planted "+
					"usage artifact; the wall-clock assertion never ran", fm)
			}
			if r.Cost.WallClockSeconds != nil && *r.Cost.WallClockSeconds <= 0 {
				t.Fatalf("frontmatter %q produced a non-positive wall clock %v; "+
					"unmeasured must be nil, never <= 0",
					fm, *r.Cost.WallClockSeconds)
			}
		}
	})

	// An unmeasured wall clock must NEVER reach the wire as a 0 that a
	// downstream reader would score as "completed instantly" (D4). The field
	// carries omitempty, so unmeasured is ABSENT; a present value of any kind
	// other than null is a failure, and 0 specifically is the D4 lie.
	t.Run("unmeasured wall clock is absent, never 0", func(t *testing.T) {
		dir := t.TempDir()
		plan := &Plan{Directory: dir}
		// No duration in frontmatter and no in-memory StartTime/EndTime, so
		// the resolver returns ok=false and the pointer must stay nil.
		path := writeJobFile(t, t.TempDir(), "j", "")
		job := &Job{ID: "j", Type: JobTypeHeadlessAgent, FilePath: path}
		writeTokenUsage(t, dir, "j", usage.Summary{Usage: usage.Usage{Input: 1}})

		if err := WriteMetricsRecord(job, plan); err != nil {
			t.Fatalf("write: %v", err)
		}

		raw, err := os.ReadFile(MetricsRecordArtifactPath(dir, "j"))
		if err != nil {
			t.Fatalf("read record: %v", err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		cost, ok := wire["cost"].(map[string]any)
		if !ok {
			t.Fatalf("cost axis missing: %s", raw)
		}
		// Absent is the expected spelling under omitempty. If the key is
		// present at all it must be null; any number — 0 above all — is the D4
		// lie this assertion exists to catch. Asserted on the DECODED value so
		// it is independent of MarshalIndent's whitespace.
		if v, present := cost["wall_clock_seconds"]; present {
			if n, isNum := v.(float64); isNum {
				t.Fatalf("unmeasured wall_clock_seconds serialised as the "+
					"measured number %v; want absent: %s", n, raw)
			}
			if v != nil {
				t.Fatalf("unmeasured wall_clock_seconds = %v, want absent: %s", v, raw)
			}
		}

		r := readRecord(t, dir, "j")
		if r.Cost.WallClockSeconds != nil {
			t.Fatalf("unmeasured wall clock decoded as %v, want nil",
				*r.Cost.WallClockSeconds)
		}
	})

	// P1-14's second mandated assertion: no code path derives the wall clock by
	// subtracting completed_at from started_at. The resolver reads the
	// PERSISTED duration and falls back only to the in-memory
	// EndTime.Sub(StartTime) delta; timestamp arithmetic on the frontmatter
	// fields is forbidden outright. Asserted two ways — behaviourally (a job
	// carrying only those timestamps yields "not measured") and structurally
	// (the source names neither field).
	t.Run("no path subtracts completed_at from started_at", func(t *testing.T) {
		dir := t.TempDir()
		// Frontmatter with both timestamps a clean 60s apart and NO duration.
		// A subtraction-based implementation would happily report 60.
		fm := "started_at: 2026-07-19T10:00:00Z\ncompleted_at: 2026-07-19T10:01:00Z\n"
		path := writeJobFile(t, dir, "j", fm)
		job := &Job{ID: "j", Type: JobTypeHeadlessAgent, FilePath: path}

		if got, ok := resolveWallClockSeconds(job); ok {
			t.Fatalf("wall clock = %v from timestamps alone; subtraction is "+
				"forbidden and this must be unmeasured", got)
		}

		plan := &Plan{Directory: dir}
		writeTokenUsage(t, dir, "j", usage.Summary{Usage: usage.Usage{Input: 1}})
		if err := WriteMetricsRecord(job, plan); err != nil {
			t.Fatalf("write: %v", err)
		}
		r := readRecord(t, dir, "j")
		if r.Cost == nil {
			t.Fatal("Cost axis is nil despite a planted usage artifact; the " +
				"subtraction assertion never ran")
		}
		if r.Cost.WallClockSeconds != nil {
			t.Fatalf("emitted wall clock %v derived from timestamps",
				*r.Cost.WallClockSeconds)
		}

		// Structural: the writer's source must not reference either field.
		src, err := os.ReadFile("metrics_record.go")
		if err != nil {
			t.Fatalf("read source: %v", err)
		}
		body := string(src)
		// Strip comments, which legitimately discuss why subtraction is banned.
		var code strings.Builder
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			code.WriteString(line)
			code.WriteString("\n")
		}
		// The ban list is DERIVED from the Job struct, not hard-coded. A
		// hard-coded list mixes Go identifiers with YAML keys and goes stale
		// silently: rename Job.CompletedAt to Job.FinishedAt and the ban on
		// "CompletedAt" becomes vacuous, so a subtraction implementation using
		// the new name sails straight through a still-green test.
		//
		// Reflection over Job contributes the Go field name plus its yaml key
		// for every timestamp field the resolver must not touch; the raw
		// frontmatter keys are added on top because a key can exist in a job
		// file without a corresponding Go field (started_at is exactly that
		// case today — Job has no StartedAt).
		// NOTE the scope of the rule: only the PERSISTED frontmatter
		// timestamps are forbidden. The in-memory EndTime/StartTime delta is
		// the sanctioned fallback (both are yaml:"-", so they are not
		// frontmatter at all) and must NOT be banned.
		banned := map[string]string{}
		derivedFromJob := 0
		jobType := reflect.TypeOf(Job{})
		for i := 0; i < jobType.NumField(); i++ {
			f := jobType.Field(i)
			if f.Type != reflect.TypeOf(time.Time{}) {
				continue
			}
			yamlKey := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if yamlKey == "" || yamlKey == "-" {
				continue // not persisted; the in-memory fallback may use it
			}
			// created_at / updated_at have nothing to do with duration.
			if !strings.Contains(yamlKey, "completed") && !strings.Contains(yamlKey, "started") &&
				!strings.Contains(yamlKey, "ended") && !strings.Contains(yamlKey, "finished") {
				continue
			}
			banned[f.Name] = "Job." + f.Name + " (Go field)"
			banned[yamlKey] = "Job." + f.Name + " (yaml key)"
			derivedFromJob++
		}
		// Raw frontmatter keys, whether or not a Go field carries them today.
		// started_at is exactly this case: UpdateJobStatus writes it, but Job
		// has no StartedAt field to reflect over.
		for _, k := range []string{"completed_at", "started_at"} {
			if _, ok := banned[k]; !ok {
				banned[k] = "job frontmatter key (no Go field)"
			}
		}
		// The self-check that stops a rename from silently emptying the list:
		// Job must still expose at least one persisted completion/start
		// timestamp. If reflection finds none, the model changed underneath
		// this rule and it must be re-derived — that is a failure, not a pass.
		if derivedFromJob == 0 {
			t.Fatalf("no persisted completion/start timestamp found on Job; "+
				"the forbidden-subtraction ban list can no longer be derived "+
				"and may be vacuous (current list: %v)", banned)
		}
		for token, origin := range banned {
			if strings.Contains(code.String(), token) {
				t.Errorf("metrics_record.go references %q (%s) outside a comment; "+
					"wall clock must come from the persisted duration only",
					token, origin)
			}
		}
	})
}

// P1-15: the fidelity fold, including the non-error nil that must stay a nil
// axis rather than becoming a zeroed struct.
func TestAdherenceDetFold(t *testing.T) {
	t.Run("no skill artifacts yields a nil axis", func(t *testing.T) {
		dir := t.TempDir()
		plan := &Plan{Directory: dir}
		job := &Job{ID: "job-a", Type: JobTypeHeadlessAgent}

		if err := WriteMetricsRecord(job, plan); err != nil {
			t.Fatalf("write: %v", err)
		}
		if r := readRecord(t, dir, "job-a"); r.AdherenceDet != nil {
			t.Fatalf("AdherenceDet = %+v, want nil when no skills ran", r.AdherenceDet)
		}
	})

	t.Run("counts completed and failed, ignores other statuses", func(t *testing.T) {
		dir := t.TempDir()
		plan := &Plan{Directory: dir}
		job := &Job{ID: "job-b", Type: JobTypeHeadlessAgent}

		artifactDir := filepath.Join(dir, ".artifacts", "job-b")
		if err := os.MkdirAll(artifactDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		states := []SkillFidelityState{
			{Skill: "a", Status: "completed",
				ArtifactsExpected: []string{"x", "y"}, ArtifactsProduced: []string{"x"}},
			{Skill: "b", Status: "failed", ArtifactsExpected: []string{"z"}},
			{Skill: "c", Status: "skipped"},
			{Skill: "d", Status: "pending"},
			{Skill: "e", Status: "running"},
		}
		for _, s := range states {
			b, err := json.Marshal(s)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := os.WriteFile(
				filepath.Join(artifactDir, s.Skill+"-status.json"), b, 0o600); err != nil {
				t.Fatalf("write status: %v", err)
			}
		}

		if err := WriteMetricsRecord(job, plan); err != nil {
			t.Fatalf("write: %v", err)
		}
		r := readRecord(t, dir, "job-b")
		if r.AdherenceDet == nil {
			t.Fatal("AdherenceDet is nil despite present skill artifacts")
		}
		if r.AdherenceDet.SkillsCompleted != 1 {
			t.Errorf("completed = %d, want 1", r.AdherenceDet.SkillsCompleted)
		}
		if r.AdherenceDet.SkillsFailed != 1 {
			t.Errorf("failed = %d, want 1", r.AdherenceDet.SkillsFailed)
		}
		if r.AdherenceDet.ArtifactsExpected != 3 {
			t.Errorf("expected = %d, want 3", r.AdherenceDet.ArtifactsExpected)
		}
		if r.AdherenceDet.ArtifactsProduced != 1 {
			t.Errorf("produced = %d, want 1", r.AdherenceDet.ArtifactsProduced)
		}
	})
}
