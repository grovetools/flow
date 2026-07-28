package browser

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/planops"
)

// Bulk fast-forward ("F") is the portfolio-wide form of the per-row "U"
// handoff: every listed plan whose worktree is clean and whose catch-up rebase
// onto local main is predicted conflict-free is updated in one confirmed
// operation. Plans that are dirty, conflicting, mid-rebase, unbound, or already
// current are reported as skipped and never touched.
//
// Eligibility is decided exclusively by planops.Preview — the same preflight
// the single-row path runs — so the bulk button can never mutate a repository
// the interactive path would have refused. Execution then re-runs Preview and
// compares fingerprints, so a plan that goes dirty between the confirmation
// prompt and the rebase is failed rather than forced.

// bulkPreviewTimeout bounds the whole preflight sweep; bulkExecuteTimeout
// bounds the confirmed rebase sequence. Both are generous: a cold portfolio
// pays one merge-tree per behind repository.
const (
	bulkPreviewTimeout  = 2 * time.Minute
	bulkExecuteTimeout  = 10 * time.Minute
	bulkPreviewParallel = 4
)

// bulkCandidate is one plan that passed preflight, carrying the exact preview
// that was shown to the user so Execute can freshness-check it.
type bulkCandidate struct {
	key     string
	name    string
	preview planops.OperationPreview
	repos   int
}

// bulkSkip records a plan the sweep deliberately left alone, with the reason
// rendered in the confirmation prompt.
type bulkSkip struct {
	name   string
	reason string
}

// bulkFailure is one plan whose confirmed execution did not complete.
type bulkFailure struct {
	name   string
	detail string
}

type bulkPreviewMsg struct {
	generation uint64
	candidates []bulkCandidate
	skipped    []bulkSkip
}

type bulkResultMsg struct {
	generation uint64
	updated    []string
	failed     []bulkFailure
}

// bulkTarget is the minimal, already-qualified input the preflight sweep needs
// from a row. Resolving these on the Update goroutine keeps the async command
// free of any dependency on mutable model state.
type bulkTarget struct {
	key    string
	name   string
	target coreplan.PlanActionTarget
}

// collectBulkTargets partitions the visible rows into plans worth preflighting
// and plans that are structurally ineligible (archived, unbound, no qualified
// repositories, or already mid-action). The skip reasons are the same
// vocabulary the single-row refusals use.
func (m Model) collectBulkTargets() ([]bulkTarget, []bulkSkip) {
	var targets []bulkTarget
	var skipped []bulkSkip
	for _, item := range m.plans {
		name := item.Name
		key := planItemKey(item)
		switch {
		case item.Archived:
			skipped = append(skipped, bulkSkip{name: name, reason: archivedReadOnlyMessage})
		case !item.Binding.Valid():
			health := string(item.Binding.Health)
			if health == "" {
				health = string(coreplan.BindingUnbound)
			}
			skipped = append(skipped, bulkSkip{name: name, reason: health})
		case key == "" || item.ActionTarget.PlanDir == "" || item.ActionTarget.ContainerPath == "" || len(item.ActionTarget.Repos) == 0:
			skipped = append(skipped, bulkSkip{name: name, reason: "no qualified repository target"})
		default:
			if pending, ok := m.actionPending[key]; ok {
				skipped = append(skipped, bulkSkip{name: name, reason: "action already in flight: " + string(pending)})
				continue
			}
			targets = append(targets, bulkTarget{key: key, name: name, target: item.ActionTarget})
		}
	}
	return targets, skipped
}

// previewBulkFastForwardCmd preflights every candidate plan concurrently.
// Preview is read-only, so parallelism here only shortens the wait; the
// mutating pass below stays strictly sequential.
func previewBulkFastForwardCmd(targets []bulkTarget, skipped []bulkSkip, generation uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bulkPreviewTimeout)
		defer cancel()

		outcomes := make([]bulkPreflight, len(targets))

		var wg sync.WaitGroup
		sem := make(chan struct{}, bulkPreviewParallel)
		for i, target := range targets {
			wg.Add(1)
			go func(i int, target bulkTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				outcomes[i] = previewBulkTarget(ctx, target)
			}(i, target)
		}
		wg.Wait()

		msg := bulkPreviewMsg{generation: generation, skipped: append([]bulkSkip(nil), skipped...)}
		for _, o := range outcomes {
			switch {
			case o.candidate != nil:
				msg.candidates = append(msg.candidates, *o.candidate)
			case o.skip != nil:
				msg.skipped = append(msg.skipped, *o.skip)
			}
		}
		sort.SliceStable(msg.candidates, func(i, j int) bool { return msg.candidates[i].name < msg.candidates[j].name })
		sort.SliceStable(msg.skipped, func(i, j int) bool { return msg.skipped[i].name < msg.skipped[j].name })
		return msg
	}
}

// bulkPreflight is one plan's preflight verdict: exactly one of the two
// fields is set.
type bulkPreflight struct {
	candidate *bulkCandidate
	skip      *bulkSkip
}

func previewBulkTarget(ctx context.Context, target bulkTarget) (out bulkPreflight) {
	preview, err := planops.Preview(ctx, target.target, planops.OperationUpdateOnly)
	if err != nil {
		out.skip = &bulkSkip{name: target.name, reason: err.Error()}
		return out
	}
	if blocked := preview.Blocked(); len(blocked) > 0 {
		out.skip = &bulkSkip{name: target.name, reason: summarizeBlocked(blocked)}
		return out
	}
	ready := preview.ReadyCount()
	if ready == 0 {
		out.skip = &bulkSkip{name: target.name, reason: "already up to date"}
		return out
	}
	out.candidate = &bulkCandidate{key: target.key, name: target.name, preview: preview, repos: ready}
	return out
}

// summarizeBlocked renders the blocking repositories compactly: identical
// reasons across an ecosystem collapse to one phrase with the repo names, so a
// 20-repo group does not produce 20 near-identical lines.
func summarizeBlocked(blocked []planops.RepoPreview) string {
	order := make([]string, 0, len(blocked))
	repos := make(map[string][]string, len(blocked))
	for _, repo := range blocked {
		reason := repo.Reason
		if reason == "" {
			reason = "blocked"
		}
		if _, seen := repos[reason]; !seen {
			order = append(order, reason)
		}
		repos[reason] = append(repos[reason], repo.Name)
	}
	parts := make([]string, 0, len(order))
	for _, reason := range order {
		parts = append(parts, fmt.Sprintf("%s (%s)", reason, strings.Join(repos[reason], ", ")))
	}
	return strings.Join(parts, "; ")
}

// executeBulkFastForwardCmd runs the confirmed updates one plan at a time.
// Plans can share an underlying object store, so rebases are never overlapped;
// a failure isolates to its plan and the sweep continues.
func executeBulkFastForwardCmd(candidates []bulkCandidate, generation uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bulkExecuteTimeout)
		defer cancel()

		msg := bulkResultMsg{generation: generation}
		for _, candidate := range candidates {
			result := planops.Execute(ctx, candidate.preview)
			if !result.Failed() {
				msg.updated = append(msg.updated, candidate.name)
				continue
			}
			msg.failed = append(msg.failed, bulkFailure{name: candidate.name, detail: bulkFailureDetail(result)})
		}
		return msg
	}
}

// bulkFailureDetail prefers the per-repository failure text over the generic
// "operation stopped" wrapper, which on its own never says which repo broke.
func bulkFailureDetail(result planops.OperationResult) string {
	for _, repo := range result.Results {
		if repo.Outcome == planops.OutcomeFailed {
			detail := repo.Detail
			if detail == "" {
				detail = "failed"
			}
			return repo.Name + ": " + detail
		}
	}
	if result.Error != "" {
		return result.Error
	}
	return "failed"
}

// bulkResultSummary renders the post-execution status line. It stays a single
// line — the footer reserves a fixed number of rows — so a portfolio-wide
// sweep cannot push the plan table off screen.
func bulkResultSummary(msg bulkResultMsg) string {
	var parts []string
	if len(msg.updated) > 0 {
		parts = append(parts, theme.DefaultTheme.Success.Render(fmt.Sprintf("%s Fast-forwarded %s: %s",
			theme.IconSuccess, pluralize(len(msg.updated), "plan"), strings.Join(msg.updated, ", "))))
	}
	if len(msg.failed) > 0 {
		details := make([]string, 0, len(msg.failed))
		for _, failure := range msg.failed {
			details = append(details, failure.name+" ("+failure.detail+")")
		}
		parts = append(parts, theme.DefaultTheme.Error.Render(fmt.Sprintf("%s %s failed: %s",
			theme.IconError, pluralize(len(msg.failed), "plan"), strings.Join(details, "; "))))
	}
	if len(parts) == 0 {
		return "Nothing to fast-forward"
	}
	return strings.Join(parts, "  ·  ")
}

// bulkCandidateRepoTotal is the number of repositories the confirmed operation
// will rebase, used for the confirmation headline.
func bulkCandidateRepoTotal(candidates []bulkCandidate) int {
	total := 0
	for _, candidate := range candidates {
		total += candidate.repos
	}
	return total
}

func pluralize(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}
