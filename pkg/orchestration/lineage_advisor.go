package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"

	grovecontext "github.com/grovetools/cx/pkg/context"
)

// Lineage-overlap advisor (oracle-plays K2 / job 42). A follow-up chat that
// re-freezes files a completed sibling chat already froze pays a full cold base
// write (the measured $10.80 incident) when a simple `-d <chat>` would have
// inherited those layers WARM instead (spec 19 P5 cross-job cache lineage, see
// layer_lineage.go). The information to detect this sits on disk: every
// completed chat's layers.json records content-hashed file entries. This file
// computes the overlap and phrases an advisory — ADVISORY ONLY, never blocking,
// never noise-failing an add or a run: every error path degrades to no advice.
//
// Two emission points, two fidelities (see the two Advise* entry points):
//
//   - Add time (flow plan add --type chat): nothing is frozen yet and the
//     worktree may not exist, so the only affordable signal is which PATHS the
//     job's rules resolve to. The overlap is a path intersection against the
//     candidate's frozen file keys (a proxy — Exact=false). This is the cheapest
//     intervention: the user just edits `-d` before the job file exists.
//   - First fire (executeChatJob turn 1, before the base layer is frozen):
//     content hashes are authoritative. We do our OWN read+strip+sha256 pass
//     over the resolved fileset (the base render has NOT run yet at this point —
//     it happens inside PrepareContextLayers, so the hashes are not free) and
//     intersect by hash set (Exact=true), which sidesteps path canonicalization.
//     A parent whose strip_comments setting differs from this job's froze its
//     bytes under a different regime, so its hashes cannot match — for such a
//     parent we degrade to the path proxy.
//
// The same-model constraint is always surfaced: an inherited cache prefix only
// hits under the SAME model (the D8 guard in integrateLineage), so a model
// mismatch inverts the message from "would inherit warm" to "align model: to
// inherit warm" rather than suppressing the advice.

const (
	// lineageOverlapRatio is the fraction of the new job's fileset a candidate
	// must cover before we advise (mirrors rebaseAdvisoryRatio's role in
	// layer_engine.go: a tuned threshold beside the builder that consumes it).
	// Applied to file count for path-proxy candidates and to bytes for
	// hash-exact candidates (see LineageOverlapAdvice.passesThreshold) so a
	// pre-strip denominator is never divided into a post-strip numerator.
	lineageOverlapRatio = 0.60
	// lineageOverlapMinWarmTokens suppresses the advisory on trivially small
	// chats: even a 100% overlap on a few hundred bytes is not worth a note. The
	// measured incident was ~40k+ warm tokens.
	lineageOverlapMinWarmTokens = 10_000
	// lineageTokensPerByte is the bytes/token heuristic shared with
	// hashAndEstimate (request_manifest.go): warm tokens ≈ matched bytes / 4.
	lineageTokensPerByte = 4
)

// LineageOverlapAdvice is one completed sibling chat whose frozen layers overlap
// a new chat's rules resolution enough to advise `-d`-ing it. The best single
// candidate (highest MatchedBytes) is surfaced; a list of every partial overlap
// would be noise.
type LineageOverlapAdvice struct {
	// ParentJobID / ParentFilename identify the candidate chat to depend on.
	ParentJobID    string
	ParentFilename string
	// MatchedFiles / MatchedBytes are the overlap: files the candidate froze
	// that also appear in this job's fileset, and their frozen (post-strip)
	// byte total — the bytes that would ride warm.
	MatchedFiles int
	MatchedBytes int64
	// FilesetFiles / FilesetBytes size the new job's whole resolved fileset.
	// FilesetBytes is PRE-strip (os.Stat) at add time and POST-strip (rendered)
	// at fire time — the ratio gate keys off file count in the pre-strip case so
	// the two units never mix (see passesThreshold).
	FilesetFiles int
	FilesetBytes int64
	// ModelMatch reports whether this job's effective model equals the
	// candidate's; ParentModel / ChildModel carry the resolved ids for the
	// mismatch message. A cached prefix only hits under a matching model.
	ModelMatch  bool
	ParentModel string
	ChildModel  string
	// Exact is true when the overlap was verified by content hash (fire time,
	// matching strip settings) rather than by path (add time, or a fire-time
	// strip-setting mismatch degraded to the proxy).
	Exact bool
}

// warmTokens is the estimated warm-token count of the overlap (matched bytes /
// 4, the hashAndEstimate heuristic).
func (a *LineageOverlapAdvice) warmTokens() int64 {
	return a.MatchedBytes / lineageTokensPerByte
}

// passesThreshold gates the advisory: enough overlap AND a warm-token floor.
// Hash-exact overlaps compare bytes (both sides post-strip, one regime); path
// proxies compare file COUNT, because their matched bytes (post-strip, from the
// parent's records) and FilesetBytes (pre-strip os.Stat at add time, or a
// different strip regime at fire time) are in mismatched units and a byte ratio
// would spuriously dip on comment-heavy files (verifier finding 4).
func (a *LineageOverlapAdvice) passesThreshold() bool {
	if a.warmTokens() < lineageOverlapMinWarmTokens {
		return false
	}
	if a.Exact {
		if a.FilesetBytes <= 0 {
			return false
		}
		return float64(a.MatchedBytes)/float64(a.FilesetBytes) >= lineageOverlapRatio
	}
	if a.FilesetFiles <= 0 {
		return false
	}
	return float64(a.MatchedFiles)/float64(a.FilesetFiles) >= lineageOverlapRatio
}

// FormatAdvice renders the advisory body (the caller prefixes "Note: " at add
// time / "Lineage advisory: " at fire time). The estimate is phrased "(context
// layers alone)" so it stays truthful post-stream, where inheriting also reuses
// the parent's cached exchanges that layers.json does not record — the number
// becomes a floor, not a total (spec 27 §4). Post-stream also joins a template
// constraint alongside the model clause; that is a second field + one clause
// here, no structural change.
func (a *LineageOverlapAdvice) FormatAdvice() string {
	name := a.ParentFilename
	if name == "" {
		name = a.ParentJobID
	}
	proof := "path overlap"
	if a.Exact {
		proof = "content-hash match"
	}
	warm := fmt.Sprintf("~%s tokens warm (context layers alone)", formatTokenCountShort(a.warmTokens()))
	overlap := fmt.Sprintf("%d/%d files, %s", a.MatchedFiles, a.FilesetFiles, proof)
	if a.ModelMatch {
		return fmt.Sprintf("completed chat %s already froze layers this job's rules re-resolve — `-d %s` would inherit %s (%s)",
			name, name, warm, overlap)
	}
	return fmt.Sprintf("completed chat %s froze layers this job's rules re-resolve (%s; %s), but it ran on model %s and this chat resolves %s — lineage requires matching models; align `model:` to inherit warm",
		name, warm, overlap, a.ParentModel, a.ChildModel)
}

// formatTokenCountShort renders a token estimate as "41k" / "900".
func formatTokenCountShort(tokens int64) string {
	if tokens >= 1000 {
		return fmt.Sprintf("%dk", tokens/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

// AdviseLineageOverlapAtAdd computes the add-time (path-proxy) advisory for a
// newly built chat job before it is persisted. Returns nil advice (no error to
// the caller) whenever anything is unavailable or below threshold — the add
// must never block or fail on advisory computation.
func AdviseLineageOverlapAtAdd(plan *Plan, job *Job) (*LineageOverlapAdvice, error) {
	if plan == nil || job == nil || job.Type != JobTypeChat || job.IsAgentResponded() || job.RulesFile == "" {
		return nil, nil
	}
	candidates := lineageOverlapCandidates(plan, job)
	if len(candidates) == 0 {
		return nil, nil
	}
	// The worktree is unknown at add time; resolve the fileset against the
	// plan's git root (sub-project-scoped if the job pins a repository). If the
	// root can't be found, degrade to silence rather than guess a wrong root.
	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		return nil, nil //nolint:nilerr // advisory-only: never surface the error
	}
	contextDir := ScopeToSubProject(gitRoot, job)
	rulesPath, ok := resolveLineageRulesPath(plan, job, contextDir)
	if !ok {
		return nil, nil
	}
	fileset, err := resolveRulesFileset(contextDir, rulesPath)
	if err != nil || len(fileset) == 0 {
		return nil, nil //nolint:nilerr // advisory-only
	}
	// PRE-strip fileset bytes (os.Stat) — reported, but NOT the ratio gate's
	// denominator at add time (verifier finding 4).
	var filesetBytes int64
	for _, key := range fileset {
		if info, statErr := os.Stat(resolveFilesetPath(contextDir, key)); statErr == nil {
			filesetBytes += info.Size()
		}
	}
	childModel := addTimeEffectiveModel(job, plan)
	advice := bestLineageOverlap(candidates, plan, contextDir, childModel, job.IsStripCommentsEnabled(),
		fileset, nil, len(fileset), filesetBytes,
		func(dep *Job) string { return addTimeEffectiveModel(dep, plan) })
	return advice, nil
}

// AdviseLineageOverlapAtFire computes the fire-time (hash-exact) advisory just
// before the base layer is frozen. contextDir and rulesPath are the resolved
// values from regenerateContextInWorktree; childModel is the job's already
// resolved effective model. It does its OWN read+strip+sha256 pass over the
// fileset — the base render has not run yet at this call site, so the hashes are
// a genuine (cheap, one-time) duplicate pass, not free (verifier finding 1).
func AdviseLineageOverlapAtFire(plan *Plan, job *Job, contextDir, rulesPath, childModel string) (*LineageOverlapAdvice, error) {
	if plan == nil || job == nil || job.Type != JobTypeChat || job.IsAgentResponded() || rulesPath == "" {
		return nil, nil
	}
	// The run-scoped plan is pruned to the target job's dependency closure, so
	// it does NOT list completed siblings. Reload the full plan from disk to
	// enumerate candidates; keep the passed `plan` for model resolution (its
	// Orchestration rung is populated at run time, a reload's is not).
	candidatePlan := plan
	if full, rerr := LoadPlan(plan.Directory); rerr == nil && full != nil {
		candidatePlan = full
	}
	candidates := lineageOverlapCandidates(candidatePlan, job)
	if len(candidates) == 0 {
		return nil, nil
	}
	fileset, err := resolveRulesFileset(contextDir, rulesPath)
	if err != nil || len(fileset) == 0 {
		return nil, nil //nolint:nilerr // advisory-only
	}
	strip := job.IsStripCommentsEnabled()
	hashes := make(map[string]string, len(fileset))
	var filesetBytes int64
	for _, key := range fileset {
		rf, rerr := readRenderedFile(contextDir, key, strip)
		if rerr != nil {
			continue // an unreadable/vanished file — skip it, stay best-effort
		}
		hashes[key] = rf.Hash
		filesetBytes += int64(len(rf.Content))
	}
	if len(hashes) == 0 {
		return nil, nil
	}
	advice := bestLineageOverlap(candidates, candidatePlan, contextDir, childModel, strip,
		fileset, hashes, len(fileset), filesetBytes,
		func(dep *Job) string { return lineageEffectiveModel(dep, plan) })
	return advice, nil
}

// lineageOverlapCandidates returns the completed chat siblings eligible to be
// suggested as a `-d` target: same plan, chat type, completed, not this job,
// and not already a dependency. Agent-responded chats are skipped (they have no
// layer store, so bestLineageOverlap's nil-manifest guard would skip them
// anyway — the explicit skip is harmless and documents intent, verifier
// finding 3).
func lineageOverlapCandidates(plan *Plan, job *Job) []*Job {
	exclude := dependencyJobIDs(plan, job)
	var out []*Job
	for _, cand := range plan.Jobs {
		if cand == nil || cand.ID == "" || cand.ID == job.ID {
			continue
		}
		if cand.Type != JobTypeChat || cand.Status != JobStatusCompleted || cand.IsAgentResponded() {
			continue
		}
		if exclude[cand.ID] {
			continue
		}
		out = append(out, cand)
	}
	return out
}

// dependencyJobIDs collects the job ids this job already depends on, from both
// the resolved Dependencies (populated at fire time) and the raw DependsOn
// filenames mapped through plan.Jobs (the only form available at add time,
// before dependency resolution).
func dependencyJobIDs(plan *Plan, job *Job) map[string]bool {
	ids := make(map[string]bool)
	for _, dep := range job.Dependencies {
		if dep != nil && dep.ID != "" {
			ids[dep.ID] = true
		}
	}
	if len(job.DependsOn) > 0 {
		byFilename := make(map[string]string, len(plan.Jobs))
		for _, cand := range plan.Jobs {
			if cand != nil && cand.Filename != "" {
				byFilename[cand.Filename] = cand.ID
			}
		}
		for _, fn := range job.DependsOn {
			if id, ok := byFilename[fn]; ok {
				ids[id] = true
			}
		}
	}
	return ids
}

// bestLineageOverlap scans candidates and returns the single best passing
// advice (highest MatchedBytes), or nil. Each candidate's frozen files come
// from UnionFileRecords WITHOUT subtracting Removals: cross-job inheritance
// rides the whole recorded union (a file the parent removed from its rules but
// whose bytes stay frozen still rides warm for a child — the union is exactly
// what integrateLineage inherits). filesetHashes is nil for the path proxy;
// non-nil enables the hash-exact path for candidates whose strip setting matches
// childStrip. parentModelFn resolves each candidate's effective model (add time
// vs fire time differ on the Orchestration rung — verifier finding 2).
func bestLineageOverlap(
	candidates []*Job,
	plan *Plan,
	contextDir string,
	childModel string,
	childStrip bool,
	filesetKeys []string,
	filesetHashes map[string]string,
	filesetFiles int,
	filesetBytes int64,
	parentModelFn func(dep *Job) string,
) *LineageOverlapAdvice {
	var best *LineageOverlapAdvice
	for _, cand := range candidates {
		manifest, err := LoadLayerManifest(ContextLayersDir(plan.Directory, cand.ID))
		if err != nil || manifest == nil {
			continue
		}
		// Root the union against THIS job's resolution dir so the parent's keys
		// (worktree-relative in a healthy store) collapse onto the child's — the
		// same canonicalization resolveRulesFileset applied to filesetKeys.
		union := UnionFileRecords(manifest, contextDir)
		if len(union) == 0 {
			continue
		}
		// Hash-exact only when the parent froze under the SAME strip setting;
		// otherwise its recorded bytes were stripped in a different regime and
		// cannot hash-match, so fall back to the path proxy (verifier finding 1).
		exact := filesetHashes != nil && cand.IsStripCommentsEnabled() == childStrip
		var matchedFiles int
		var matchedBytes int64
		if exact {
			matchedFiles, matchedBytes = hashOverlap(filesetHashes, union)
		} else {
			matchedFiles, matchedBytes = pathOverlap(filesetKeys, union)
		}
		if matchedFiles == 0 {
			continue
		}
		parentModel := parentModelFn(cand)
		advice := &LineageOverlapAdvice{
			ParentJobID:    cand.ID,
			ParentFilename: cand.Filename,
			MatchedFiles:   matchedFiles,
			MatchedBytes:   matchedBytes,
			FilesetFiles:   filesetFiles,
			FilesetBytes:   filesetBytes,
			ModelMatch:     parentModel == childModel,
			ParentModel:    parentModel,
			ChildModel:     childModel,
			Exact:          exact,
		}
		if !advice.passesThreshold() {
			continue
		}
		if best == nil || advice.MatchedBytes > best.MatchedBytes {
			best = advice
		}
	}
	return best
}

// pathOverlap counts fileset keys present in the candidate's union and sums
// their frozen (post-strip) bytes — the add-time path proxy.
func pathOverlap(filesetKeys []string, union map[string]LayerFileRecord) (matchedFiles int, matchedBytes int64) {
	for _, key := range filesetKeys {
		if rec, ok := union[key]; ok {
			matchedFiles++
			matchedBytes += rec.Bytes
		}
	}
	return matchedFiles, matchedBytes
}

// hashOverlap counts fileset files whose rendered content hash matches a hash
// the candidate froze, summing the frozen bytes — the fire-time exact check.
// Keying by hash sidesteps path canonicalization entirely.
func hashOverlap(filesetHashes map[string]string, union map[string]LayerFileRecord) (matchedFiles int, matchedBytes int64) {
	unionByHash := make(map[string]LayerFileRecord, len(union))
	for _, rec := range union {
		unionByHash[rec.Hash] = rec
	}
	for _, h := range filesetHashes {
		if rec, ok := unionByHash[h]; ok {
			matchedFiles++
			matchedBytes += rec.Bytes
		}
	}
	return matchedFiles, matchedBytes
}

// addTimeEffectiveModel resolves a job's effective model for the ADD-time
// advisory. Unlike lineageEffectiveModel it drops the plan.Orchestration rung:
// plan.Orchestration is nil in the add path (LoadPlan never populates it — only
// plan_run does), so consulting it would produce a false mismatch (verifier
// finding 2). Applied symmetrically to child and candidate.
func addTimeEffectiveModel(job *Job, plan *Plan) string {
	switch {
	case job.Model != "":
		return resolveModelAlias(job.Model)
	case plan.Config != nil && plan.Config.Model != "":
		return resolveModelAlias(plan.Config.Model)
	default:
		return resolveModelAlias(anthropicmodels.DefaultModel)
	}
}

// resolveLineageRulesPath resolves a job's rules file the same way
// regenerateContextInWorktree does (plan dir → cwd → git root → absolute →
// named preset), for the add-time proxy where no worktree resolution has run.
// Read-only, best-effort: returns false rather than erroring.
func resolveLineageRulesPath(plan *Plan, job *Job, contextDir string) (string, bool) {
	if job.RulesFile == "" {
		return "", false
	}
	if p := filepath.Join(plan.Directory, job.RulesFile); fileExistsRegular(p) {
		return p, true
	}
	if cwd, err := os.Getwd(); err == nil {
		if p := filepath.Join(cwd, job.RulesFile); fileExistsRegular(p) {
			return p, true
		}
	}
	if gitRoot, err := GetProjectGitRoot(plan.Directory); err == nil {
		if p := filepath.Join(gitRoot, job.RulesFile); fileExistsRegular(p) {
			return p, true
		}
	}
	if filepath.IsAbs(job.RulesFile) && fileExistsRegular(job.RulesFile) {
		return job.RulesFile, true
	}
	presetName := strings.TrimSuffix(job.RulesFile, ".rules")
	if resolved, err := grovecontext.NewManager(contextDir).FindRulesetFile(contextDir, presetName); err == nil && resolved != "" {
		return resolved, true
	}
	return "", false
}

// resolveFilesetPath joins a canonical fileset key back to an absolute path
// against contextDir (mirroring readRenderedFile / cx's writeFileToXML).
func resolveFilesetPath(contextDir, key string) string {
	if filepath.IsAbs(key) {
		return key
	}
	return filepath.Join(contextDir, key)
}

// fileExistsRegular reports whether path names an existing file (not a dir).
func fileExistsRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
