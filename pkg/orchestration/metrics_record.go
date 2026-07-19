package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/agentlogs/pkg/usage"
	"github.com/grovetools/eval/pkg/record"
	"github.com/sirupsen/logrus"
)

// Cost source values (D24). This is a call-path boundary, not provider
// identity: non-claude providers (codex/pi/opencode) all recover their usage
// from a transcript, so they are transcript_usage too.
const (
	costSourceAPIUsage        = "api_usage"
	costSourceTranscriptUsage = "transcript_usage"
)

// MetricsRecordArtifactPath returns the on-disk location of a job's partial
// run record.
func MetricsRecordArtifactPath(planDir, jobID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, "metrics.json")
}

// resolveRecordKey produces the record's identity and configuration by reading
// back the config vector this job was stamped with.
//
// TaskID defaults to the job id and Replicate to 0. Both are placeholders that
// the eval matrix verb overrides — keeping the read-back and the defaulting in
// this one function is deliberate, so that override can be added later without
// touching the writer.
//
// A missing config vector is a legitimate state (a job that predates this
// feature, or a retried old run): synthesize a Config carrying only the model
// and continue. Nil axes are legal; a missing envelope is not (D5).
func resolveRecordKey(job *Job, plan *Plan) (record.RunKey, record.ConfigVector) {
	key := record.RunKey{TaskID: job.ID, Replicate: 0}

	if v, hash, ok := ReadConfigVectorArtifact(plan.Directory, job.ID); ok {
		key.ConfigHash = hash
		return key, v
	}

	synthesized := record.ConfigVector{Model: job.Model}
	key.ConfigHash = synthesized.Hash()
	return key, synthesized
}

// resolveCost maps the job's archived token usage into the Cost axis.
//
// When no usage artifact exists the axis stays nil rather than becoming a
// zeroed struct: ArchiveTokenUsage silently skips when the session-registry
// binding is absent, and a zero Cost would be indistinguishable from a genuine
// free run (D4).
func resolveCost(job *Job, plan *Plan) *record.Cost {
	summary, ok := ReadTokenUsageArtifact(plan.Directory, job.ID)
	if !ok {
		return nil
	}

	c := &record.Cost{
		InputTokens:     summary.Usage.Input,
		OutputTokens:    summary.Usage.Output,
		CacheReadTokens: summary.Usage.CacheRead,
		// Summed individually rather than via Usage.Total(): that helper folds
		// in Usage.ExtraTotal, which has no counterpart in the record schema
		// and would silently inflate the figure.
		CacheWriteTokens: summary.Usage.CacheWrite5m + summary.Usage.CacheWrite1h,
		EstimatedUSD:     summary.CostUSD,
	}

	switch job.Type {
	case JobTypeOneshot, JobTypeChat:
		c.CostSource = costSourceAPIUsage
	default:
		c.CostSource = costSourceTranscriptUsage
	}

	// summary.MissingPricing does not suppress the record: EstimatedUSD is
	// left as-is, and the caveat is now carried IN the record rather than only
	// in the co-located token-usage.json (D7 amendment) — D5 makes
	// run-record.json the only complete record, so a consumer reading it alone
	// must be able to see that the figure is not trustworthy.
	c.PricingCompleteness = resolvePricingCompleteness(summary, c.CostSource)

	// APIResponseSeconds is deliberately left nil for every family. See
	// resolveAPIResponseSeconds for why.
	c.APIResponseSeconds = resolveAPIResponseSeconds(job)

	// Assign only when genuinely measured; otherwise the pointer stays nil and
	// marshals as null. Never assign an unmeasured zero (D4).
	if wc, ok := resolveWallClockSeconds(job); ok {
		c.WallClockSeconds = &wc
	}
	return c
}

// resolvePricingCompleteness classifies how far summary.CostUSD can be
// trusted, so the record self-describes instead of shipping a bare number that
// reads as a confident total (D7 amendment).
//
// summary.MissingPricing is a STICKY OR: it is set the first time any single
// turn/entry could not be priced and never cleared, so on its own it says
// "at least one call was unpriced" and nothing about how much of the total
// survived. That is why a bare bool could not be forwarded — it cannot
// distinguish "lost everything" from "lost some of it".
//
// The two capture paths then fail DIFFERENTLY, which is why costSource is the
// discriminator (it is derived from job.Type immediately above, so the two
// derivations stay in lockstep, and it is the D24-shaped truth: the call path
// determines the pricing failure mode):
//
//   - transcript path (agent families, aglogs usage.EntryCost): an unpriced
//     entry contributes cost 0 and flags missing. The unpriced calls are
//     DROPPED from the total, so a surviving nonzero total is a strict lower
//     bound and a zero total means nothing survived at all.
//   - api path (oneshot/chat, AccumulateAPITokenUsage): an unknown model falls
//     through to the default fallback rate in models.GetPricingOK, which
//     returns (3.00, 15.00, false). The call IS costed — at a guess. Nothing
//     is dropped, so the total is neither trustworthy nor a bound; it is an
//     estimate of unknown direction.
//
// Two edges both err toward LESS trust, which is the safe direction:
// a transcript run whose only priced entries happened to cost exactly 0 is
// reported "unpriced" rather than "partial", and an api run whose fallback
// arithmetic yields 0 is still "fallback_rate".
func resolvePricingCompleteness(summary usage.Summary, costSource string) string {
	if !summary.MissingPricing {
		return record.PricingCompletenessComplete
	}
	if costSource == costSourceAPIUsage {
		return record.PricingCompletenessFallbackRate
	}
	if summary.CostUSD > 0 {
		return record.PricingCompletenessPartial
	}
	return record.PricingCompletenessUnpriced
}

// resolveAPIResponseSeconds always returns nil.
//
// For agent families the anthropic query ledger is structurally empty: they
// exec an external CLI via NewHeadlessCommand, and buildHeadlessEnv sets no
// GROVE_REQUEST_ID, so no ledger rows are attributable to the job (D24).
//
// For oneshot/chat the ledger does carry rows, but attributing them requires a
// request id scoped to the call — and the only available mechanism,
// os.Setenv("GROVE_REQUEST_ID", ...), is process-global while the orchestrator
// dispatches jobs concurrently (runJobsConcurrently fans out goroutines under a
// semaphore of config.MaxParallelJobs, default 3) into a single shared
// OneShotExecutor instance registered for both JobTypeOneshot and JobTypeChat.
// Two concurrent API jobs would therefore cross-contaminate each other's
// attribution, and one job's deferred Unsetenv would clear the other's id.
//
// D4 and D25 both prefer nil over an approximation, so this stays nil until
// request-scoped plumbing replaces the env var. That is a larger change than
// this phase owns.
func resolveAPIResponseSeconds(job *Job) *float64 {
	return nil
}

// resolveWallClockSeconds reads the job's wall-clock duration.
//
// It reads the PERSISTED duration field and never recomputes it by
// subtraction. UpdateJobStatus writes completed_at/duration to frontmatter but
// never sets job.Duration/job.CompletedAt in memory, and StartTime/EndTime are
// yaml:"-" so LoadJob cannot repopulate them. started_at is written only under
// a job.StartTime.IsZero() gate that never fires, because every launcher sets
// StartTime before the running transition — empirically it is absent from 100%
// of completed headless jobs, so subtraction would yield nothing for exactly
// the family the eval path runs.
//
// Follows the precedent in notify.go: prefer the recorded duration, fall back
// to the in-memory delta, and only emit a positive value.
//
// Every failure mode — field absent, parse error, never started, retry-cleared
// — yields ok=false rather than a wrong number, which is D4-correct by
// construction. There is no clock arithmetic anywhere, so timezone and skew
// are structurally non-issues.
func resolveWallClockSeconds(job *Job) (float64, bool) {
	if job.FilePath != "" {
		if reloaded, err := LoadJob(job.FilePath); err == nil && reloaded != nil {
			if secs := reloaded.Duration.Seconds(); secs > 0 {
				return secs, true
			}
		}
	}

	// In-process fallback: the job completed in this process, so the launcher's
	// StartTime and the terminal EndTime are both still in memory.
	if !job.StartTime.IsZero() && !job.EndTime.IsZero() {
		if secs := job.EndTime.Sub(job.StartTime).Seconds(); secs > 0 {
			return secs, true
		}
	}

	return 0, false
}

// resolveAdherenceDet folds the skill-fidelity artifacts into the
// deterministic adherence axis.
//
// BuildFidelityReport returns (nil, nil) when the job ran no skills. That is a
// non-error nil and must yield a nil axis, not a zeroed struct — a job with no
// skill sequence has no adherence to measure (D4).
func resolveAdherenceDet(job *Job, plan *Plan) *record.AdherenceDet {
	report, err := BuildFidelityReport(plan.Directory, job.ID)
	if err != nil || report == nil || len(report.Skills) == 0 {
		return nil
	}

	det := &record.AdherenceDet{}
	for _, state := range report.Skills {
		switch state.Status {
		case "completed":
			det.SkillsCompleted++
		case "failed":
			det.SkillsFailed++
			// pending / running / skipped count toward neither.
		}
		det.ArtifactsExpected += len(state.ArtifactsExpected)
		det.ArtifactsProduced += len(state.ArtifactsProduced)
	}
	return det
}

// WriteMetricsRecord writes a partial run record to
// .artifacts/<job-id>/metrics.json (D5, D14).
//
// It populates only the axes flow owns at job completion — Cost and
// AdherenceDet — plus the envelope. Outcome, AdherenceJudged, Process and
// ComponentMetrics have other writers, and D6 makes two partials populating the
// same axis a hard join error, so they are deliberately absent rather than
// zeroed.
//
// The write is a whole-file overwrite and therefore idempotent, which makes it
// safe to attach at more than one completion seam.
func WriteMetricsRecord(job *Job, plan *Plan) error {
	if job == nil || plan == nil {
		return fmt.Errorf("metrics record: nil job or plan")
	}
	if plan.Directory == "" || job.ID == "" {
		return fmt.Errorf("metrics record: empty plan directory or job id")
	}

	key, cfg := resolveRecordKey(job, plan)

	rec := record.RunRecord{
		Schema:       record.SchemaVersion,
		Key:          key,
		Config:       cfg,
		Cost:         resolveCost(job, plan),
		AdherenceDet: resolveAdherenceDet(job, plan),
	}

	out, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling metrics record: %w", err)
	}
	out = append(out, '\n')

	path := MetricsRecordArtifactPath(plan.Directory, job.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating job artifact directory: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("writing metrics record: %w", err)
	}
	return nil
}

// writeMetricsRecordQuietly attaches the metrics writer at a completion seam.
// A capture failure is warned and swallowed: there must be no path from a
// metrics failure to a job failure.
func writeMetricsRecordQuietly(job *Job, plan *Plan) {
	if err := WriteMetricsRecord(job, plan); err != nil {
		logrus.Warnf("failed to write metrics record: %v", err)
	}
}
