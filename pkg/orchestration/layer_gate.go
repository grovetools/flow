package orchestration

import (
	"context"
	"fmt"
	"strings"
)

// validateFrozenContextCoverage is the fire-time empty-freeze gate (oracle-plays
// J1). It runs in executeChatJob immediately after PrepareContextLayers returns
// and BEFORE any prompt assembly / provider dispatch, so a trip aborts the turn
// before the API call is billed. It returns a plain error whose text IS the
// user-facing last_error: the runtime funnel (LocalRuntime.ExecuteJob /
// DaemonRuntime.handleTerminalStatus) stamps status: failed + last_error, and
// executeChatJob's deferred terminal-failure guard writes the ctx-writer line +
// structured ulog.Error. The gate therefore emits NO logging of its own — it
// mirrors the PinnedContextRemovedError idiom exactly (build the error, return
// it). The ctx parameter is retained for signature stability but the body
// performs no logging.
//
// Two conditions trip the gate (both anchored on the job's OWN rules
// resolution, never total context size — a lineage child with a small own layer
// is legitimate):
//
//   - P1: the job DECLARES a rules file (job.RulesFile != "") but its rules
//     resolve to 0 files. The turn would fire blind on inherited/lineage layers
//     only — the exact wasted-generation incident this gate exists to stop. A
//     job that wants no own context omits rules_file and never reaches P1.
//   - P2: some file the rules resolve to is not captured in ANY frozen layer of
//     the whole lineage (own base/diff OR inherited/dep-transcript). Coverage is
//     an engine invariant, so this is zero-tolerance (no ratio): any shortfall
//     is a bug or a corrupt store, and set-membership on canonical keys subsumes
//     "resolved N, froze 0" and "froze far fewer" with no magic number. A nil
//     manifest with a non-empty fileset trips defensively (nothing captured).
//
// The gate re-resolves the rules a third time in the turn (A2 forbids widening
// LayerEngineResult to carry the engine's fileset out). This assumes the
// worktree is stable between PrepareContextLayers returning and this call —
// microseconds apart in the same goroutine — so P2 compares like against like;
// no gate-side new-file carve-out is added (it would reopen the very hole P2
// closes).
func validateFrozenContextCoverage(ctx context.Context, planDir string, job *Job, contextDir, rulesPath string) error {
	fileset, err := resolveRulesFileset(contextDir, rulesPath)
	if err != nil {
		return fmt.Errorf("empty-freeze gate: re-resolving rules file %s: %w", rulesPath, err)
	}

	// P1 — declared rules, empty resolution.
	if job.RulesFile != "" && len(fileset) == 0 {
		return fmt.Errorf("empty-freeze gate: job declares rules_file %s but it resolved 0 files at freeze time — the turn would fire blind on inherited/lineage context only, wasting a full generation. Fix the rules file so it selects the intended files, then recover with: %s",
			rulesPath, emptyFreezeRecoveryHint(job))
	}

	// P2 — every resolved file must be captured somewhere in the frozen lineage.
	manifest, err := LoadLayerManifest(ContextLayersDir(planDir, job.ID))
	if err != nil {
		return fmt.Errorf("empty-freeze gate: loading frozen layer manifest: %w", err)
	}
	if manifest == nil {
		if len(fileset) == 0 {
			return nil
		}
		return fmt.Errorf("empty-freeze gate: rules file %s resolved %d file(s) but no layer manifest was frozen — nothing was captured for this turn. Recover with: %s",
			rulesPath, len(fileset), emptyFreezeRecoveryHint(job))
	}

	union := UnionFileRecords(manifest, contextDir)
	var missing []string
	for _, f := range fileset {
		if _, ok := union[f]; !ok {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("empty-freeze gate: %d of %d file(s) resolved by rules file %s were not captured in any frozen context layer (freeze gap): %s — the turn would fire with context missing files the rules demanded. Recover with: %s",
			len(missing), len(fileset), rulesPath, formatMissingPaths(missing), emptyFreezeRecoveryHint(job))
	}
	return nil
}

// emptyFreezeRecoveryHint builds the recovery verb named in every gate error:
// reset the failed job then re-run with a fresh context freeze.
func emptyFreezeRecoveryHint(job *Job) string {
	ref := job.Filename
	if ref == "" {
		ref = job.ID
	}
	return fmt.Sprintf("flow retry %s && flow plan run %s --rebase-context", ref, ref)
}

// formatMissingPaths renders up to the first few missing paths for the P2 error
// so the message stays actionable without dumping an unbounded list.
func formatMissingPaths(missing []string) string {
	const limit = 5
	if len(missing) <= limit {
		return strings.Join(missing, ", ")
	}
	return fmt.Sprintf("%s, … (+%d more)", strings.Join(missing[:limit], ", "), len(missing)-limit)
}
