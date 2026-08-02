// Package planops provides the CWD-independent, non-TUI git lifecycle service
// shared by Flow and Git Viewer. Callers resolve workspace/registry identity;
// this package only operates on the explicit repository checkout paths supplied
// in a PlanActionTarget.
package planops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/grovetools/core/git"
	coreplan "github.com/grovetools/core/pkg/plan"
)

// Operation is a plan-level git mutation.
type Operation string

const (
	OperationUpdateOnly Operation = "update-only"
	OperationLand       Operation = "land"
)

// Disposition is the preview decision for one repository.
type Disposition string

const (
	DispositionReady   Disposition = "ready"
	DispositionSkipped Disposition = "skipped"
	DispositionBlocked Disposition = "blocked"
)

// RepoPreview is the complete, immutable decision input for one checkout.
type RepoPreview struct {
	Name             string      `json:"name"`
	Path             string      `json:"path"`
	Branch           string      `json:"branch,omitempty"`
	Onto             string      `json:"onto,omitempty"`
	MainCheckoutPath string      `json:"mainCheckoutPath,omitempty"`
	Ahead            int         `json:"ahead"`
	Behind           int         `json:"behind"`
	Dirty            bool        `json:"dirty"`
	MainDirty        bool        `json:"mainDirty"`
	InProgress       []string    `json:"inProgress,omitempty"`
	MainInProgress   []string    `json:"mainInProgress,omitempty"`
	Conflict         bool        `json:"conflict"`
	Disposition      Disposition `json:"disposition"`
	Reason           string      `json:"reason,omitempty"`
}

// OperationPreview is the all-repository safety gate. Fingerprint binds the
// confirmation to the exact target and observed git state.
type OperationPreview struct {
	Target      coreplan.PlanActionTarget `json:"target"`
	Operation   Operation                 `json:"operation"`
	Repos       []RepoPreview             `json:"repos"`
	Fingerprint string                    `json:"fingerprint"`
}

// Blocked returns the repositories whose preflight must be resolved before
// Execute will mutate anything. Execute is all-or-nothing per target, so a
// single blocked repository disqualifies the whole preview; callers that
// choose between targets (for example a bulk fast-forward) gate on this.
func (p OperationPreview) Blocked() []RepoPreview {
	var blocked []RepoPreview
	for _, repo := range p.Repos {
		if repo.Disposition == DispositionBlocked {
			blocked = append(blocked, repo)
		}
	}
	return blocked
}

// ReadyCount reports how many repositories this preview would actually mutate.
// A preview with no blocked repos and a zero ready count is already in the
// requested state and needs no execution at all.
func (p OperationPreview) ReadyCount() int {
	count := 0
	for _, repo := range p.Repos {
		if repo.Disposition == DispositionReady {
			count++
		}
	}
	return count
}

// Outcome is an execution disposition.
type Outcome string

const (
	OutcomeSucceeded    Outcome = "succeeded"
	OutcomeFailed       Outcome = "failed"
	OutcomeNotAttempted Outcome = "not-attempted"
)

// RepoResult is the structured execution result for one repo.
type RepoResult struct {
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail,omitempty"`
}

// OperationResult always contains one result for every preview repo. Receipt
// reports the landing receipt for a land that actually advanced main; it is
// always nil for update-only, which lands nothing.
type OperationResult struct {
	Operation          Operation       `json:"operation"`
	PreviewFingerprint string          `json:"previewFingerprint"`
	FreshFingerprint   string          `json:"freshFingerprint,omitempty"`
	Stale              bool            `json:"stale"`
	Results            []RepoResult    `json:"results"`
	Error              string          `json:"error,omitempty"`
	Receipt            *ReceiptOutcome `json:"receipt,omitempty"`
}

func (r OperationResult) Failed() bool {
	if r.Error != "" {
		return true
	}
	for _, repo := range r.Results {
		if repo.Outcome == OutcomeFailed {
			return true
		}
	}
	return false
}

// Preview deterministically preflights every explicit repository before any
// mutation. It never discovers from CWD.
func Preview(ctx context.Context, target coreplan.PlanActionTarget, operation Operation) (OperationPreview, error) {
	if operation != OperationUpdateOnly && operation != OperationLand {
		return OperationPreview{}, fmt.Errorf("unsupported plan operation %q", operation)
	}
	if len(target.Repos) == 0 {
		return OperationPreview{}, errors.New("plan action target has no repositories")
	}

	repos := append([]coreplan.RepoTarget(nil), target.Repos...)
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].Name != repos[j].Name {
			return repos[i].Name < repos[j].Name
		}
		return repos[i].Path < repos[j].Path
	})
	preview := OperationPreview{Target: target, Operation: operation, Repos: make([]RepoPreview, 0, len(repos))}
	preview.Target.Repos = repos
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return OperationPreview{}, err
		}
		preview.Repos = append(preview.Repos, previewRepo(repo, operation))
	}
	preview.Fingerprint = fingerprint(preview)
	return preview, nil
}

func previewRepo(repo coreplan.RepoTarget, operation Operation) RepoPreview {
	p := RepoPreview{Name: repo.Name, Path: filepath.Clean(repo.Path), Disposition: DispositionBlocked}
	if p.Name == "" {
		p.Name = filepath.Base(p.Path)
	}
	if !git.IsGitRepo(p.Path) {
		p.Reason = "repository checkout unavailable"
		return p
	}
	p.Onto = git.LocalMainBranch(p.Path)
	ext, err := git.GetExtendedStatus(p.Path)
	if err != nil || ext == nil || ext.StatusInfo == nil {
		p.Reason = "status unavailable"
		return p
	}
	p.Branch = ext.Branch
	p.Dirty = !statusClean(ext.StatusInfo)
	p.InProgress = inProgress(p.Path)
	if p.Onto != "" {
		p.Ahead, p.Behind, _ = git.GetCommitsDivergence(p.Path, p.Onto, "HEAD")
	}

	switch {
	// Detached HEAD outranks every other verdict: porcelain v2 reports it as the
	// literal "(detached)" (see git.IsDetachedHead), and a detached checkout must
	// never be mutated regardless of what else preflight would conclude.
	case git.IsDetachedHead(p.Branch):
		p.Reason = "detached HEAD"
	case p.Onto == "":
		p.Reason = "no local main/master"
	case p.Dirty:
		p.Reason = "worktree is not clean"
	case len(p.InProgress) > 0:
		p.Reason = "git operation in progress: " + strings.Join(p.InProgress, ", ")
	case p.Branch == p.Onto:
		p.Disposition, p.Reason = DispositionSkipped, "already on "+p.Onto
	default:
		if p.Behind > 0 {
			p.Conflict, _ = git.WouldRebaseConflict(p.Path, p.Onto, "HEAD")
		}
		if p.Conflict {
			p.Reason = "predicted rebase conflict"
			break
		}
		if operation == OperationUpdateOnly && p.Behind == 0 {
			p.Disposition, p.Reason = DispositionSkipped, "already up to date with "+p.Onto
			break
		}
		if operation == OperationLand && p.Ahead == 0 {
			p.Disposition, p.Reason = DispositionSkipped, "no commits ahead of "+p.Onto
			break
		}
		p.Disposition = DispositionReady
		p.Reason = "ready"
	}

	if operation != OperationLand || p.Disposition != DispositionReady {
		return p
	}
	mainPath, err := git.MainCheckoutPath(p.Path)
	if err != nil {
		p.Disposition, p.Reason = DispositionBlocked, err.Error()
		return p
	}
	p.MainCheckoutPath = filepath.Clean(mainPath)
	mainStatus, err := git.GetExtendedStatus(mainPath)
	if err != nil || mainStatus == nil || mainStatus.StatusInfo == nil {
		p.Disposition, p.Reason = DispositionBlocked, "main checkout status unavailable"
		return p
	}
	p.MainDirty = !statusClean(mainStatus.StatusInfo)
	p.MainInProgress = inProgress(mainPath)
	switch {
	case mainStatus.Branch != p.Onto:
		p.Disposition, p.Reason = DispositionBlocked, "main checkout is on "+mainStatus.Branch+", expected "+p.Onto
	case p.MainDirty:
		p.Disposition, p.Reason = DispositionBlocked, "main checkout is not clean"
	case len(p.MainInProgress) > 0:
		p.Disposition, p.Reason = DispositionBlocked, "main checkout git operation in progress: "+strings.Join(p.MainInProgress, ", ")
	}
	return p
}

func statusClean(s *git.StatusInfo) bool {
	return s != nil && s.ModifiedCount == 0 && s.StagedCount == 0 && s.UntrackedCount == 0
}

func inProgress(repoPath string) []string {
	checks := []struct{ path, name string }{
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
		{"BISECT_LOG", "bisect"},
	}
	seen := map[string]bool{}
	var out []string
	for _, check := range checks {
		cmd := exec.Command("git", "rev-parse", "--git-path", check.path)
		cmd.Dir = repoPath
		b, err := cmd.Output()
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(b))
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoPath, path)
		}
		if _, err := os.Stat(path); err == nil && !seen[check.name] {
			seen[check.name] = true
			out = append(out, check.name)
		}
	}
	return out
}

func fingerprint(preview OperationPreview) string {
	copy := preview
	copy.Fingerprint = ""
	b, _ := json.Marshal(copy)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Execute freshness-checks the complete preview, then executes ready repos in
// deterministic order. A blocked preview mutates nothing. An unexpected runtime
// failure stops the sequence and marks all remaining repos not-attempted.
func Execute(ctx context.Context, preview OperationPreview) OperationResult {
	result := OperationResult{Operation: preview.Operation, PreviewFingerprint: preview.Fingerprint}
	fresh, err := Preview(ctx, preview.Target, preview.Operation)
	if err != nil {
		result.Error = err.Error()
		return notAttempted(result, preview.Repos, "fresh preflight failed")
	}
	result.FreshFingerprint = fresh.Fingerprint
	if preview.Fingerprint == "" || preview.Fingerprint != fresh.Fingerprint {
		result.Stale = true
		result.Error = "preview is stale; review and confirm a fresh preview"
		return notAttempted(result, fresh.Repos, "stale preview")
	}

	var blockers []string
	for _, repo := range fresh.Repos {
		if repo.Disposition == DispositionBlocked {
			blockers = append(blockers, repo.Name+": "+repo.Reason)
		}
	}
	if len(blockers) > 0 {
		result.Error = "preflight blocked: " + strings.Join(blockers, "; ")
		for _, repo := range fresh.Repos {
			outcome, detail := OutcomeNotAttempted, "another repository blocked the all-repo preflight"
			if repo.Disposition == DispositionBlocked {
				outcome, detail = OutcomeFailed, repo.Reason
			}
			result.Results = append(result.Results, RepoResult{Name: repo.Name, Path: repo.Path, Outcome: outcome, Detail: detail})
		}
		return result
	}

	failed := false
	var landings []RepoLanding
	var receiptWarnings []string
	for _, repo := range fresh.Repos {
		rr := RepoResult{Name: repo.Name, Path: repo.Path}
		switch {
		case failed:
			rr.Outcome, rr.Detail = OutcomeNotAttempted, "stopped after previous repository failed"
		case ctx.Err() != nil:
			failed = true
			rr.Outcome, rr.Detail = OutcomeFailed, ctx.Err().Error()
		case repo.Disposition == DispositionSkipped:
			rr.Outcome, rr.Detail = OutcomeSucceeded, repo.Reason
		default:
			landing, warnings, execErr := executeRepo(repo, preview.Operation)
			receiptWarnings = append(receiptWarnings, warnings...)
			if execErr != nil {
				failed = true
				rr.Outcome, rr.Detail = OutcomeFailed, execErr.Error()
			} else {
				rr.Outcome = OutcomeSucceeded
				if preview.Operation == OperationLand {
					rr.Detail = "landed " + repo.Branch + " onto " + repo.Onto
				} else {
					rr.Detail = "rebased " + repo.Branch + " onto " + repo.Onto
				}
				if landing != nil {
					landings = append(landings, *landing)
				}
			}
		}
		result.Results = append(result.Results, rr)
	}
	if failed {
		result.Error = "operation stopped after a repository failed"
	}
	// The receipt is written last, after every advance this operation is going
	// to make has already happened — including a run that failed partway, whose
	// earlier repos really did land and must not go unrecorded. Nothing here can
	// change the outcome above: recordLandingReceipt only reports.
	if preview.Operation == OperationLand {
		result.Receipt = recordLandingReceipt(fresh, landings, receiptWarnings)
	}
	return result
}

// recordLandingReceipt writes the receipt for a completed land. It never
// returns an error: main has already moved, so a receipt problem is something
// to report loudly, never something to fail or unwind the land over.
func recordLandingReceipt(preview OperationPreview, landings []RepoLanding, warnings []string) *ReceiptOutcome {
	outcome := &ReceiptOutcome{Warnings: warnings}
	if len(landings) == 0 {
		outcome.Skipped = "no repository landed"
		return outcome
	}
	planDir := strings.TrimSpace(preview.Target.PlanDir)
	if planDir == "" {
		// git-viewer lands an explicit directory scope that need not be a plan.
		// There is no plan to file a receipt under; that is a property of the
		// target, not a failure of this land.
		outcome.Skipped = "target is not plan-scoped"
		return outcome
	}
	receipt := LandingReceipt{
		SchemaVersion: ReceiptSchemaVersion,
		Plan:          filepath.Base(filepath.Clean(planDir)),
		PlanDir:       filepath.Clean(planDir),
		WorktreePath:  preview.Target.ContainerPath,
		RegistryID:    preview.Target.RegistryID,
		Operation:     preview.Operation,
		Fingerprint:   preview.Fingerprint,
		Timestamp:     time.Now().UTC(),
		Repos:         landings,
	}
	path, err := WriteReceipt(planDir, receipt)
	if err != nil {
		outcome.Error = err.Error()
		return outcome
	}
	outcome.Path = path
	return outcome
}

func notAttempted(result OperationResult, repos []RepoPreview, detail string) OperationResult {
	for _, repo := range repos {
		result.Results = append(result.Results, RepoResult{Name: repo.Name, Path: repo.Path, Outcome: OutcomeNotAttempted, Detail: detail})
	}
	return result
}

// executeRepo mutates one repository and, for a land, reports what it landed.
// The returned landing is nil for update-only (which lands nothing) and for any
// failure. Warnings describe receipt fields that could not be observed; they
// never affect the operation's outcome.
func executeRepo(repo RepoPreview, operation Operation) (*RepoLanding, []string, error) {
	var warnings []string
	observe := func(dir, ref, what string) string {
		// An empty dir would resolve the ref against process CWD — some other
		// repository entirely. planops never discovers from CWD, so an absent
		// checkout path is an unobservable field, not a silent wrong answer.
		if strings.TrimSpace(dir) == "" {
			warnings = append(warnings, fmt.Sprintf("%s: could not read %s: no checkout path", repo.Name, what))
			return ""
		}
		sha, err := git.ResolveRef(dir, ref)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: could not read %s: %v", repo.Name, what, err))
			return ""
		}
		return sha
	}

	// Observed before any mutation: the branch head as reviewed (the rebase
	// below rewrites it) and the main tip this land will move.
	var reviewedHead, mainBefore string
	if operation == OperationLand {
		reviewedHead = observe(repo.Path, "HEAD", "reviewed branch head")
		mainBefore = observe(repo.MainCheckoutPath, repo.Onto, "main before advance")
	}

	if repo.Behind > 0 {
		if err := git.Rebase(repo.Path, repo.Onto); err != nil {
			_ = git.AbortRebase(repo.Path)
			return nil, warnings, fmt.Errorf("catch-up rebase failed (aborted): %w", err)
		}
	}
	if operation == OperationUpdateOnly {
		return nil, warnings, nil
	}
	// AdvanceMainToBranch performs an ff-only update and resynchronizes the
	// linked worktree. MainCheckoutPath was freshness-checked in Preview.
	if err := git.AdvanceMainToBranch(repo.MainCheckoutPath, repo.Branch, repo.Onto, repo.Path); err != nil {
		return nil, warnings, err
	}
	return &RepoLanding{
		Repo:            repo.Name,
		Branch:          repo.Branch,
		Onto:            repo.Onto,
		Path:            repo.Path,
		ReviewedHeadSHA: reviewedHead,
		LandedRange: LandedRange{
			Start: mainBefore,
			End:   observe(repo.MainCheckoutPath, repo.Onto, "main after advance"),
		},
	}, warnings, nil
}
