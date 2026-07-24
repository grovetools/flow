package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/util/delegation"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planutil"
)

// refreshTickMsg is emitted only for the deliberately slow local fallback.
type refreshTickMsg time.Time

type planIndexConnectedMsg struct {
	snapshot   *models.PlanIndexSnapshot
	plans      []PlanListItem
	err        error
	updates    <-chan daemon.StateUpdate
	cancel     context.CancelFunc
	generation uint64
}
type planIndexConnectFailedMsg struct {
	err        error
	generation uint64
}
type planIndexStreamMsg struct {
	update        daemon.StateUpdate
	updates       <-chan daemon.StateUpdate
	generation    uint64
	firstRevision uint64 // first delta folded into update; detects real gaps after coalescing
}
type planIndexStreamClosedMsg struct{ generation uint64 }
type planIndexReconnectMsg struct{}

func fallbackRefreshTick() tea.Cmd {
	return tea.Tick(fallbackRefreshInterval, func(t time.Time) tea.Msg { return refreshTickMsg(t) })
}

func planIndexReconnectTick() tea.Cmd {
	return tea.Tick(daemonReconnectInterval, func(time.Time) tea.Msg { return planIndexReconnectMsg{} })
}

func connectPlanIndexCmd(factory DaemonClientFactory, generation uint64, showOnHold, showArchived bool) tea.Cmd {
	return func() tea.Msg {
		client := factory()
		if client == nil {
			return planIndexConnectFailedMsg{err: errors.New("daemon client unavailable"), generation: generation}
		}
		ctx, cancel := context.WithCancel(context.Background())
		updates, err := client.StreamState(ctx)
		if err != nil {
			cancel()
			return planIndexConnectFailedMsg{err: err, generation: generation}
		}
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 3*time.Second)
		snapshot, err := client.GetPlanIndex(fetchCtx)
		fetchCancel()
		if err != nil {
			cancel()
			return planIndexConnectFailedMsg{err: err, generation: generation}
		}

		// Hydrate the complete snapshot before reporting a live connection. This
		// keeps reconnect atomic: the retained stale rows remain visible until
		// one fresh, revision-qualified projection is ready to replace them.
		summaries := make(map[string]models.PlanSummary)
		if snapshot != nil {
			summaries = make(map[string]models.PlanSummary, len(snapshot.Plans))
			for _, summary := range snapshot.Plans {
				summaries[summary.PlanDir] = summary
			}
		}
		plans, projectionErr := loadPortfolio(summaries, showOnHold, showArchived)
		return planIndexConnectedMsg{snapshot: snapshot, plans: plans, err: projectionErr, updates: updates, cancel: cancel, generation: generation}
	}
}

func listenPlanIndexCmd(updates <-chan daemon.StateUpdate, generation uint64) tea.Cmd {
	return func() tea.Msg {
		first, ok := <-updates
		if !ok {
			return planIndexStreamClosedMsg{generation: generation}
		}
		if first.PlanIndex == nil {
			return planIndexStreamMsg{update: first, updates: updates, generation: generation}
		}
		batch := []daemon.StateUpdate{first}
		// SSE bursts commonly contain several filesystem revisions. Drain only
		// what is already queued and fold it into one projection/render turn;
		// never wait for a quiet period or delay interactive updates.
		for {
			select {
			case next, open := <-updates:
				if !open {
					return coalescePlanIndexUpdates(batch, updates, generation)
				}
				batch = append(batch, next)
			default:
				return coalescePlanIndexUpdates(batch, updates, generation)
			}
		}
	}
}

func coalescePlanIndexUpdates(batch []daemon.StateUpdate, updates <-chan daemon.StateUpdate, generation uint64) planIndexStreamMsg {
	if len(batch) == 1 {
		return planIndexStreamMsg{update: batch[len(batch)-1], updates: updates, generation: generation}
	}
	upserts := make(map[string]models.PlanSummary)
	removed := make(map[string]struct{})
	firstRevision := uint64(0)
	latest := batch[0]
	var scannedAt time.Time
	var revision uint64
	for _, update := range batch {
		if update.PlanIndex == nil {
			continue
		}
		delta := update.PlanIndex
		if firstRevision == 0 {
			firstRevision = delta.Revision
		}
		if delta.Revision > revision {
			revision, scannedAt, latest = delta.Revision, delta.ScannedAt, update
		}
		for _, dir := range delta.Removed {
			delete(upserts, dir)
			removed[dir] = struct{}{}
		}
		for _, summary := range delta.Upserts {
			delete(removed, summary.PlanDir)
			upserts[summary.PlanDir] = summary
		}
	}
	merged := &models.PlanIndexDelta{Revision: revision, ScannedAt: scannedAt}
	for _, summary := range upserts {
		merged.Upserts = append(merged.Upserts, summary)
	}
	for dir := range removed {
		merged.Removed = append(merged.Removed, dir)
	}
	sort.Slice(merged.Upserts, func(i, j int) bool { return merged.Upserts[i].PlanDir < merged.Upserts[j].PlanDir })
	sort.Strings(merged.Removed)
	latest.PlanIndex = merged
	return planIndexStreamMsg{update: latest, updates: updates, generation: generation, firstRevision: firstRevision}
}

// planListLoadCompleteMsg carries the result of an async plans list load.
type planListLoadCompleteMsg struct {
	plans               []PlanListItem
	error               error
	portfolio           bool
	planIndexRevision   uint64
	portfolioGeneration uint64
	loadGeneration      uint64
}

// gitLogMsg carries the result of fetching the top-level workspace git log.
type gitLogMsg struct {
	content string
	err     error
}

// repoGitLogMsg carries the result of fetching git log for one repo
// inside an ecosystem plan (when the user is navigating repos).
type repoGitLogMsg struct {
	content string
	err     error
}

// planDetailMsg is a deliberately bounded, user-triggered live hydration. The
// daemon projection never performs Git work for the whole portfolio; only the
// selected row is eligible, and generation/key guards reject stale results.
type planDetailMsg struct {
	key        string
	generation uint64
	item       PlanListItem
	err        error
}

func loadSelectedPlanDetailCmd(summary models.PlanSummary, generation uint64) tea.Cmd {
	return func() tea.Msg {
		items, err := loadPortfolio(map[string]models.PlanSummary{summary.PlanDir: summary}, true, true)
		if err != nil || len(items) != 1 {
			if err == nil {
				err = fmt.Errorf("selected plan detail disappeared: %s", summary.PlanDir)
			}
			return planDetailMsg{key: summary.PlanDir, generation: generation, err: err}
		}
		item := items[0]
		if !item.Binding.Valid() || summary.WorktreePath == "" {
			return planDetailMsg{key: summary.PlanDir, generation: generation, item: item}
		}
		if len(summary.Repositories) == 0 {
			status, statusErr := git.GetStatus(summary.WorktreePath)
			if statusErr != nil {
				return planDetailMsg{key: summary.PlanDir, generation: generation, err: statusErr}
			}
			status.AheadCount = planutil.CommitCount(summary.WorktreePath, "main..HEAD")
			status.BehindCount = planutil.CommitCount(summary.WorktreePath, "HEAD..main")
			item.GitStatus = status
			item.MergeStatus = planutil.MergeStatus(summary.WorkspaceRoot, summary.Worktree)
			item.MergeVerdict = item.MergeStatus
			return planDetailMsg{key: summary.PlanDir, generation: generation, item: item}
		}
		provider, discoveryErr := planutil.DiscoverWorkspaceProvider()
		if discoveryErr != nil {
			return planDetailMsg{key: summary.PlanDir, generation: generation, err: discoveryErr}
		}
		item.EcosystemRepoStatuses, item.MergeStatus, item.MergeVerdict = planutil.EcosystemRepoDetails(item.Plan, summary.Worktree, summary.WorktreePath, provider)
		return planDetailMsg{key: summary.PlanDir, generation: generation, item: item}
	}
}

// reviewCompleteMsg carries the result of the external `flow plan review`
// command.
type reviewCompleteMsg struct {
	output string
	err    error
}

type holdCompleteMsg struct {
	key  string
	hold bool
	err  error
}

func setHoldCmd(key, planDir string, hold bool) tea.Cmd {
	return func() tea.Msg {
		return holdCompleteMsg{key: key, hold: hold, err: orchestration.SetHold(planDir, hold)}
	}
}

func fetchGitLogCmd(workspacePath string) tea.Cmd {
	return func() tea.Msg {
		if workspacePath == "" {
			return gitLogMsg{err: fmt.Errorf("selected plan has no qualified workspace")}
		}
		gitRoot, err := git.GetGitRoot(workspacePath)
		if err != nil {
			return gitLogMsg{err: fmt.Errorf("selected workspace is not a git repository: %w", err)}
		}

		cmd := exec.Command("git", "log", "--oneline", "--decorate", "--color=always", "--graph", "--all", "--max-count=20")
		cmd.Dir = gitRoot

		output, err := cmd.CombinedOutput()
		if err != nil {
			return gitLogMsg{err: fmt.Errorf("git log failed: %w: %s", err, string(output))}
		}

		return gitLogMsg{content: string(output)}
	}
}

func fetchRepoGitLogCmd(repoPath string) tea.Cmd {
	return func() tea.Msg {
		if repoPath == "" {
			return repoGitLogMsg{err: fmt.Errorf("no repo path provided")}
		}

		cmd := exec.Command("git", "log", "--oneline", "--decorate", "--color=always", "--graph", "--all", "--max-count=20")
		cmd.Dir = repoPath

		output, err := cmd.CombinedOutput()
		if err != nil {
			return repoGitLogMsg{err: fmt.Errorf("git log failed: %w: %s", err, string(output))}
		}

		return repoGitLogMsg{content: string(output)}
	}
}

func loadPortfolioCmd(summaries map[string]models.PlanSummary, showOnHold, showArchived bool, revisionAndGeneration ...uint64) tea.Cmd {
	var rev, generation uint64
	if len(revisionAndGeneration) > 0 {
		rev = revisionAndGeneration[0]
	}
	if len(revisionAndGeneration) > 1 {
		generation = revisionAndGeneration[1]
	}
	return func() tea.Msg {
		plans, err := loadPortfolio(summaries, showOnHold, showArchived)
		return planListLoadCompleteMsg{plans: plans, error: err, portfolio: true, planIndexRevision: rev, portfolioGeneration: generation}
	}
}

func loadPortfolio(summaries map[string]models.PlanSummary, showOnHold, showArchived bool) ([]PlanListItem, error) {
	// Daemon mode is a pure in-memory projection. In particular, do not call
	// LoadPlan, resolve the registry, discover workspaces, stat plan dirs, or run
	// Git here: a 24-row cold snapshot must be usable in milliseconds rather
	// than paying one live hydration per row.
	all := make([]PlanListItem, 0, len(summaries))
	for _, summary := range summaries {
		if (summary.Archived && !showArchived) ||
			(!summary.Archived && summary.Lifecycle == "finished") ||
			(!showOnHold && summary.Lifecycle == "hold") {
			continue
		}
		key := coreplan.NewPlanKey(summary.PlanDir)
		config := &orchestration.PlanConfig{
			Worktree: summary.Worktree, Status: summary.Lifecycle,
			Repos: append([]string(nil), summary.Repositories...), Notes: summary.Notes,
		}
		if config.Status == "live" {
			config.Status = ""
		}
		plan := &orchestration.Plan{Name: summary.PlanName, Directory: summary.PlanDir, Config: config}
		bindingHealth := coreplan.BindingHealth(summary.BindingHealth)
		if bindingHealth == "" {
			switch {
			case summary.Archived:
				bindingHealth = coreplan.BindingArchived
			case summary.WorktreePath != "":
				bindingHealth = coreplan.BindingValid
			default:
				bindingHealth = coreplan.BindingUnbound
			}
		}
		binding := coreplan.PlanBinding{
			Key: key, Health: bindingHealth, Reason: summary.BindingReason,
			RegistryID: summary.RegistryID, ContainerPath: summary.WorktreePath,
			WorkspaceRoot: summary.WorkspaceRoot, PlanName: summary.PlanName,
			Repos: append([]string(nil), summary.Repositories...),
		}
		item := PlanListItem{
			Plan: plan, Name: summary.PlanName, Key: key, Binding: binding,
			Workspace: filepath.Base(summary.WorkspaceRoot), WorkspaceRoot: summary.WorkspaceRoot,
			Repositories: append([]string(nil), summary.Repositories...), Selected: summary.Selected,
			Worktree: summary.Worktree, JobCount: jobCount(summary.JobCounts),
			StatusParts: normalizedJobCounts(summary.JobCounts), LastUpdated: summary.UpdatedAt,
			ReviewStatus: formatSummaryLifecycle(summary.Lifecycle, summary.Archived),
			Notes:        summary.Notes, GitStatus: summary.GitStatus, MergeStatus: "-", Archived: summary.Archived,
		}
		item.Status = formatStatusParts(item.StatusParts)
		if binding.Valid() && binding.RegistryID != "" && binding.ContainerPath != "" {
			item.ActionTarget = coreplan.PlanActionTarget{
				PlanDir: summary.PlanDir, WorkspaceRoot: summary.WorkspaceRoot,
				RegistryID: binding.RegistryID, ContainerPath: binding.ContainerPath,
			}
			if len(summary.Repositories) == 0 {
				item.ActionTarget.Repos = []coreplan.RepoTarget{{Name: filepath.Base(binding.ContainerPath), Path: binding.ContainerPath}}
			} else {
				for _, repo := range summary.Repositories {
					item.ActionTarget.Repos = append(item.ActionTarget.Repos, coreplan.RepoTarget{Name: repo, Path: filepath.Join(binding.ContainerPath, repo)})
				}
			}
		}
		all = append(all, item)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].LastUpdated.After(all[j].LastUpdated) })
	return all, nil
}

func jobCount(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func normalizedJobCounts(counts map[string]int) map[string]int {
	out := make(map[string]int)
	for status, count := range counts {
		switch status {
		case "pending_user", "todo":
			out["pending"] += count
		default:
			out[status] += count
		}
	}
	return out
}

func formatStatusParts(parts map[string]int) string {
	labels := []string{"completed", "running", "pending", "failed", "blocked", "hold", "abandoned"}
	var out []string
	for _, label := range labels {
		if count := parts[label]; count > 0 {
			text := label
			if label == "hold" {
				text = "on hold"
			}
			out = append(out, fmt.Sprintf("%d %s", count, text))
		}
	}
	if len(out) == 0 {
		return "no jobs"
	}
	return strings.Join(out, ", ")
}

func formatSummaryLifecycle(lifecycle string, archived bool) string {
	if archived {
		return "Archived"
	}
	return formatConfigStatus(&orchestration.PlanConfig{Status: lifecycle})
}

func loadPlansListCmd(plansDirectory, cwdGitRoot string, showOnHold, showArchived bool, generation ...uint64) tea.Cmd {
	var gen uint64
	if len(generation) > 0 {
		gen = generation[0]
	}
	return func() tea.Msg {
		plans, err := loadPlansList(plansDirectory, cwdGitRoot, showOnHold, showArchived)
		return planListLoadCompleteMsg{plans: plans, error: err, loadGeneration: gen}
	}
}

// fetchPlans returns the parsed plan list for plansDirectory. It tries
// the daemon first (where the watcher keeps a pre-parsed snapshot) and
// falls back to a direct filesystem scan if the daemon is unreachable
// or has no cached data for this directory yet.
func fetchPlans(plansDirectory string) ([]*orchestration.Plan, error) {
	client := daemon.New()
	defer client.Close()

	if client.IsRunning() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		body, err := client.GetPlansRaw(ctx, plansDirectory)
		if err == nil && len(body) > 0 && string(body) != "[]" && string(body) != "null" {
			var plans []*orchestration.Plan
			if decodeErr := json.Unmarshal(body, &plans); decodeErr == nil {
				// If the daemon's cached snapshot is missing plan dirs that
				// already exist on disk (e.g. just-created plan that the
				// watcher hasn't picked up yet), fall through to a fresh
				// filesystem scan instead of returning a stale list.
				if !daemonPlansAreStale(plansDirectory, plans) {
					// `Job.Dependencies` is `json:"-"`, so the DAG pointer
					// graph is lost across the socket. Rebuild it locally.
					for _, p := range plans {
						_ = p.ResolveDependencies()
					}
					return plans, nil
				}
			}
		}
	}

	return loadPlansFromDisk(plansDirectory)
}

// daemonPlansAreStale returns true when the plans directory contains plan
// subdirectories that are not represented in the daemon's snapshot. A plan
// dir is identified by a `.grove-plan.yml` file or any `*.md` job file —
// matching loadPlansFromDisk's recognition rule.
func daemonPlansAreStale(plansDirectory string, plans []*orchestration.Plan) bool {
	entries, err := os.ReadDir(plansDirectory)
	if err != nil {
		return false
	}
	known := make(map[string]struct{}, len(plans))
	for _, p := range plans {
		known[p.Name] = struct{}{}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := known[entry.Name()]; ok {
			continue
		}
		planPath := filepath.Join(plansDirectory, entry.Name())
		if _, statErr := os.Stat(filepath.Join(planPath, ".grove-plan.yml")); statErr == nil {
			return true
		}
		mdFiles, _ := filepath.Glob(filepath.Join(planPath, "*.md"))
		if len(mdFiles) > 0 {
			return true
		}
	}
	return false
}

// loadPlansFromDisk is the daemon-less fallback: scan the plansDir and
// invoke orchestration.LoadPlan for each subdirectory.
func loadPlansFromDisk(plansDirectory string) ([]*orchestration.Plan, error) {
	entries, err := os.ReadDir(plansDirectory)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory %s: %w", plansDirectory, err)
	}
	plans := make([]*orchestration.Plan, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		planPath := filepath.Join(plansDirectory, entry.Name())
		planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
		mdFiles, _ := filepath.Glob(filepath.Join(planPath, "*.md"))
		if _, err := os.Stat(planConfigPath); err != nil && len(mdFiles) == 0 {
			continue
		}
		plan, err := orchestration.LoadPlan(planPath)
		if err != nil {
			continue
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// planListEntry pairs a loaded plan with its archived provenance so the
// item-building loop below can treat live and archived plans uniformly.
type planListEntry struct {
	plan     *orchestration.Plan
	archived bool
}

func loadPlansList(plansDirectory, cwdGitRoot string, showOnHold, showArchived bool) ([]PlanListItem, error) {
	plans, err := fetchPlans(plansDirectory)
	if err != nil {
		return nil, err
	}

	entries := make([]planListEntry, 0, len(plans))
	for _, plan := range plans {
		entries = append(entries, planListEntry{plan: plan})
	}

	if showArchived {
		// Archived plans live one level under <plansDir>/.archive (moved
		// there by `flow plan finish --archive`). The scan is disk-only —
		// the daemon snapshot never covers the archive dir — and must not
		// descend further (job-level archives live at <planDir>/.archive).
		archiveDir := filepath.Join(plansDirectory, ".archive")
		archivedPlans, archiveErr := loadPlansFromDisk(archiveDir)
		if archiveErr != nil && !errors.Is(archiveErr, fs.ErrNotExist) {
			return nil, archiveErr
		}
		for _, plan := range archivedPlans {
			entries = append(entries, planListEntry{plan: plan, archived: true})
		}
	}

	bindingRequests := make([]coreplan.BindingRequest, 0, len(entries))
	for _, entry := range entries {
		worktree := ""
		if entry.plan.Config != nil {
			worktree = entry.plan.Config.Worktree
		}
		bindingRequests = append(bindingRequests, coreplan.BindingRequest{
			PlanDir: entry.plan.Directory, WorkspaceRoot: cwdGitRoot,
			ConfiguredWorktree: worktree, Archived: entry.archived,
		})
	}
	bindings := coreplan.ResolvePlanBindings(bindingRequests)

	var items []PlanListItem
	// Hoist canonical workspace discovery out of the per-plan loop. The same
	// immutable snapshot expands qualified action targets and powers status
	// enrichment; actions never start a second scan or rediscover from CWD.
	var provider *workspace.Provider
	for _, binding := range bindings {
		if binding.Valid() {
			provider, _ = planutil.DiscoverWorkspaceProvider()
			break
		}
	}

	for _, entry := range entries {
		plan := entry.plan
		// Archived plans carry status "finished" by construction; they
		// bypass the live-list filters and are shown flagged instead.
		if !entry.archived {
			if plan.Config != nil && plan.Config.Status == "finished" {
				continue
			}
			if !showOnHold && plan.Config != nil && plan.Config.Status == "hold" {
				continue
			}
		}

		planInfo, statErr := os.Stat(plan.Directory)
		var lastUpdated time.Time
		if statErr == nil {
			lastUpdated = planInfo.ModTime()
		} else {
			lastUpdated = time.Now()
		}

		worktree := ""
		notes := ""
		if plan.Config != nil {
			worktree = plan.Config.Worktree
			notes = plan.Config.Notes
		}

		key := coreplan.NewPlanKey(plan.Directory)
		item := PlanListItem{
			Plan:          plan,
			Name:          plan.Name,
			Key:           key,
			Binding:       bindings[key.String()],
			WorkspaceRoot: cwdGitRoot,
			JobCount:      len(plan.Jobs),
			LastUpdated:   lastUpdated,
			Worktree:      worktree,
			Notes:         notes,
			MergeStatus:   "-",
			ReviewStatus:  formatConfigStatus(plan.Config),
			Archived:      entry.archived,
		}
		if plan.Config != nil {
			item.Repositories = append([]string(nil), plan.Config.Repos...)
		}
		if entry.archived {
			item.ReviewStatus = "Archived"
		}
		if target, targetErr := coreplan.ResolvePlanActionTarget(item.Binding, item.Repositories, provider); targetErr == nil {
			item.ActionTarget = target
		}

		if worktree != "" {
			var gitRoot string
			if cwdGitRoot != "" {
				if _, ok := workspace.ResolveWorktreePathByName(cwdGitRoot, worktree, nil); ok {
					gitRoot = cwdGitRoot
				}
			}
			if gitRoot == "" {
				project, err := workspace.GetProjectByPath(plan.Directory)
				if err == nil && project != nil {
					gitRoot = project.Path
				}
			}
			if gitRoot == "" {
				gitRoot, _ = git.GetGitRoot(plan.Directory)
			}
			if gitRoot == "" {
				gitRoot = planutil.FindGitRootForWorktree(plan.Directory, worktree)
			}

			if gitRoot != "" {
				worktreePath, ok := planutil.ResolveWorktreePath(gitRoot, worktree, provider)
				if !ok {
					worktreePath, ok = workspace.FindWorktreePath(gitRoot, worktree)
				}
				if ok {
					// Anchored/ecosystem plans have no superproject worktree we
					// care about — the container's own .git (if any) drifts from
					// main and is pure noise. Skip the top-level git.GetStatus for
					// them so the GIT column renders "-"; the per-module rollup in
					// the MERGE column carries the real status instead.
					isEcosystem := plan.Config != nil && len(plan.Config.Repos) > 0
					if !isEcosystem {
						gitStatus, statusErr := git.GetStatus(worktreePath)
						if statusErr == nil {
							gitStatus.AheadCount = planutil.CommitCount(worktreePath, "main..HEAD")
							gitStatus.BehindCount = planutil.CommitCount(worktreePath, "HEAD..main")
							item.GitStatus = gitStatus
							item.MergeStatus = planutil.MergeStatus(gitRoot, worktree)
							item.MergeVerdict = item.MergeStatus
						}
					} else if provider == nil {
						item.MergeStatus = "err (discovery failed)"
						item.MergeVerdict = "err"
					} else {
						item.EcosystemRepoStatuses, item.MergeStatus, item.MergeVerdict = planutil.EcosystemRepoDetails(plan, worktree, worktreePath, provider)
					}
				}
			}
		}

		statusCounts := make(map[orchestration.JobStatus]int)
		for _, job := range plan.Jobs {
			statusCounts[job.Status]++
		}

		statusParts := make(map[string]int)
		var statusStrParts []string
		if c := statusCounts[orchestration.JobStatusCompleted]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d completed", c))
			statusParts["completed"] = c
		}
		if c := statusCounts[orchestration.JobStatusRunning]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d running", c))
			statusParts["running"] = c
		}
		if c := statusCounts[orchestration.JobStatusPending] + statusCounts[orchestration.JobStatusPendingUser] + statusCounts[orchestration.JobStatusTodo]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d pending", c))
			statusParts["pending"] = c
		}
		if c := statusCounts[orchestration.JobStatusFailed]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d failed", c))
			statusParts["failed"] = c
		}
		if c := statusCounts[orchestration.JobStatusBlocked]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d blocked", c))
			statusParts["blocked"] = c
		}
		if c := statusCounts[orchestration.JobStatusHold]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d on hold", c))
			statusParts["hold"] = c
		}
		if c := statusCounts[orchestration.JobStatusAbandoned]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d abandoned", c))
			statusParts["abandoned"] = c
		}
		item.StatusParts = statusParts
		if len(statusStrParts) > 0 {
			item.Status = strings.Join(statusStrParts, ", ")
		} else {
			item.Status = "no jobs"
		}

		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].LastUpdated.After(items[j].LastUpdated)
	})

	return items, nil
}

// formatConfigStatus formats the plan's config status for display in the
// REVIEW column of the browser table.
func formatConfigStatus(config *orchestration.PlanConfig) string {
	if config == nil || config.Status == "" {
		return "-"
	}
	switch config.Status {
	case "review":
		return "Review"
	case "hold":
		return "Hold"
	case "finished":
		return "Finished"
	default:
		return config.Status
	}
}

// stateDir returns the directory whose ecosystem owns the active-plan state.
// core/state resolves this to its ecosystem/worktree root, so a process run
// outside any ecosystem refuses the write rather than touching a home-global
// state file.
func stateDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// executePlanFinish launches `flow plan finish` as a subprocess, first
// setting the active plan so the subprocess picks it up.
func executePlanFinish(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg {
			if err := state.Set(stateDir(), coreplan.StateKey, plan.Name); err != nil {
				return err
			}
			return nil
		},
		tea.ExecProcess(delegation.Command("flow", "plan", "finish"),
			func(err error) tea.Msg { return nil }),
	)
}

// executePlanOpen launches `flow plan open` as a subprocess.
func executePlanOpen(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg {
			if err := state.Set(stateDir(), coreplan.StateKey, plan.Name); err != nil {
				return err
			}
			return nil
		},
		func() tea.Cmd {
			openCmd := delegation.Command("flow", "plan", "open")
			openCmd.Env = append(os.Environ(), "GROVE_FLOW_TUI_MODE=true")
			return tea.ExecProcess(openCmd, func(err error) tea.Msg { return nil })
		}(),
	)
}

// executePlanReview shells out to `grove flow plan review` and returns
// its output via reviewCompleteMsg.
func executePlanReview(plan *orchestration.Plan) tea.Cmd {
	return func() tea.Msg {
		if err := state.Set(stateDir(), coreplan.StateKey, plan.Name); err != nil {
			return reviewCompleteMsg{err: err}
		}
		cmd := exec.Command("grove", "flow", "plan", "review")
		output, err := cmd.CombinedOutput()
		if err != nil {
			outputStr := strings.TrimSpace(string(output))
			if outputStr != "" {
				err = fmt.Errorf("%w: %s", err, outputStr)
			}
		}
		return reviewCompleteMsg{output: string(output), err: err}
	}
}

// executeNewPlan launches `flow plan init --tui` as a subprocess. The
// old plan-list TUI used to swap to the in-process plan_init_tui model,
// but that required cmd-package-internal coupling; delegating keeps this
// package self-contained and matches how finish/open are wired.
func executeNewPlan() tea.Cmd {
	return tea.ExecProcess(delegation.Command("flow", "plan", "init", "--tui"),
		func(err error) tea.Msg { return nil })
}
