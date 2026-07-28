package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/tui/embed"
)

// TestBulkFastForwardOnlyTouchesCleanConflictFreePlans is the end-to-end shape
// of the F button against real repositories: one plan whose repos are clean and
// cleanly rebasable is fast-forwarded, while a dirty plan, a plan with a
// predicted rebase conflict, and an already-current plan are reported as
// skipped and left bit-for-bit unchanged.
func TestBulkFastForwardOnlyTouchesCleanConflictFreePlans(t *testing.T) {
	eligibleA := bulkRepoFixture(t, "eligible-a", true)
	eligibleB := bulkRepoFixture(t, "eligible-b", true)
	dirty := bulkRepoFixture(t, "dirty", true)
	if err := os.WriteFile(filepath.Join(dirty, "uncommitted.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflicted := bulkRepoFixture(t, "conflicted", false)
	bulkCommit(t, conflicted, "shared.txt", "branch side")
	bulkCommit(t, bulkMainCheckout(t, conflicted), "shared.txt", "main side")
	current := bulkRepoFixture(t, "current", false)

	m := New(Config{})
	m.plans = []PlanListItem{
		bulkPlanRow("ecosystem", eligibleA, eligibleB),
		bulkPlanRow("dirty-plan", dirty),
		bulkPlanRow("conflicted-plan", conflicted),
		bulkPlanRow("current-plan", current),
	}
	archived := bulkPlanRow("archived-plan", bulkRepoFixture(t, "archived", true))
	archived.Archived = true
	m.plans = append(m.plans, archived)

	before := map[string]string{}
	for _, repo := range []string{dirty, conflicted, current} {
		before[repo] = bulkGitOut(t, repo, "rev-parse", "HEAD")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("F did not start a preflight sweep: %q", m.statusMessage)
	}
	preview, ok := cmd().(bulkPreviewMsg)
	if !ok {
		t.Fatalf("F emitted %T", cmd())
	}
	updated, _ = m.Update(preview)
	m = updated.(Model)

	if !m.bulkConfirming {
		t.Fatalf("preflight did not open a confirmation: %q", m.statusMessage)
	}
	if len(m.bulkCandidates) != 1 || m.bulkCandidates[0].name != "ecosystem" || m.bulkCandidates[0].repos != 2 {
		t.Fatalf("wrong candidate set: %#v", m.bulkCandidates)
	}
	reasons := map[string]string{}
	for _, skip := range m.bulkSkipped {
		reasons[skip.name] = skip.reason
	}
	for name, want := range map[string]string{
		"dirty-plan":      "worktree is not clean",
		"conflicted-plan": "predicted rebase conflict",
		"current-plan":    "already up to date",
		"archived-plan":   archivedReadOnlyMessage,
	} {
		if !strings.Contains(reasons[name], want) {
			t.Errorf("%s skip reason = %q, want it to mention %q", name, reasons[name], want)
		}
	}

	// The confirmation must name every plan and every refusal, so the operator
	// can see what F is about to touch without leaving the prompt.
	confirmView := m.renderBulkConfirm()
	for _, want := range []string{"ecosystem", "2 repos", "dirty-plan", "conflicted-plan"} {
		if !strings.Contains(confirmView, want) {
			t.Errorf("confirmation view is missing %q:\n%s", want, confirmView)
		}
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("confirmation did not execute: %q", m.statusMessage)
	}
	result, ok := cmd().(bulkResultMsg)
	if !ok {
		t.Fatalf("confirmation emitted %T", cmd())
	}
	if len(result.failed) != 0 || len(result.updated) != 1 || result.updated[0] != "ecosystem" {
		t.Fatalf("unexpected execution result: %#v", result)
	}
	updated, _ = m.Update(result)
	m = updated.(Model)
	if m.bulkConfirming || m.bulkPending || len(m.bulkCandidates) != 0 {
		t.Errorf("bulk state not cleared after execution: %#v", m)
	}
	if !strings.Contains(m.statusMessage, "ecosystem") {
		t.Errorf("summary does not report the updated plan: %q", m.statusMessage)
	}

	for _, repo := range []string{eligibleA, eligibleB} {
		mainRev := bulkGitOut(t, repo, "rev-parse", "main")
		if !strings.Contains(bulkGitOut(t, repo, "rev-list", "HEAD"), mainRev) {
			t.Errorf("%s was not rebased onto main", repo)
		}
	}
	for repo, want := range before {
		if got := bulkGitOut(t, repo, "rev-parse", "HEAD"); got != want {
			t.Errorf("ineligible repo %s was mutated: %s -> %s", repo, want, got)
		}
	}
}

// TestBulkFastForwardCancelMutatesNothing pins the escape hatch: the sweep is
// purely a preview until the operator accepts it.
func TestBulkFastForwardCancelMutatesNothing(t *testing.T) {
	repo := bulkRepoFixture(t, "cancel", true)
	before := bulkGitOut(t, repo, "rev-parse", "HEAD")

	m := New(Config{})
	m.plans = []PlanListItem{bulkPlanRow("plan", repo)}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	m = updated.(Model)
	updated, _ = m.Update(cmd().(bulkPreviewMsg))
	m = updated.(Model)
	if len(m.bulkCandidates) != 1 {
		t.Fatalf("fixture is not eligible: %#v", m.bulkSkipped)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("esc emitted %T", cmd())
	}
	if m.bulkConfirming || len(m.bulkCandidates) != 0 || !strings.Contains(m.statusMessage, "cancelled") {
		t.Fatalf("esc did not cancel the sweep: confirming=%v candidates=%d status=%q", m.bulkConfirming, len(m.bulkCandidates), m.statusMessage)
	}
	if got := bulkGitOut(t, repo, "rev-parse", "HEAD"); got != before {
		t.Fatalf("cancelled sweep mutated %s: %s -> %s", repo, before, got)
	}
}

// TestBulkFastForwardRefusesPlansThatDriftAfterConfirmation pins the freshness
// gate: the confirmation is bound to the state that was previewed, so a repo
// that goes dirty while the prompt is open is failed, reported by name, and
// left alone rather than rebased anyway.
func TestBulkFastForwardRefusesPlansThatDriftAfterConfirmation(t *testing.T) {
	stable := bulkRepoFixture(t, "stable", true)
	drifting := bulkRepoFixture(t, "drifting", true)

	m := New(Config{})
	m.plans = []PlanListItem{bulkPlanRow("stable-plan", stable), bulkPlanRow("drifting-plan", drifting)}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	m = updated.(Model)
	updated, _ = m.Update(cmd().(bulkPreviewMsg))
	m = updated.(Model)
	if len(m.bulkCandidates) != 2 {
		t.Fatalf("both fixtures should be eligible: %#v", m.bulkSkipped)
	}

	if err := os.WriteFile(filepath.Join(drifting, "late.txt"), []byte("late\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := bulkGitOut(t, drifting, "rev-parse", "HEAD")

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	result := cmd().(bulkResultMsg)
	if len(result.updated) != 1 || result.updated[0] != "stable-plan" {
		t.Errorf("clean plan should still be updated: %#v", result.updated)
	}
	if len(result.failed) != 1 || result.failed[0].name != "drifting-plan" {
		t.Fatalf("drifted plan was not failed: %#v", result.failed)
	}
	if got := bulkGitOut(t, drifting, "rev-parse", "HEAD"); got != before {
		t.Errorf("drifted repo was rebased anyway: %s -> %s", before, got)
	}
	summary := bulkResultSummary(result)
	if !strings.Contains(summary, "stable-plan") || !strings.Contains(summary, "drifting-plan") {
		t.Errorf("summary hides part of the outcome: %q", summary)
	}
	if strings.Contains(summary, "\n") {
		t.Errorf("summary must stay on one status line: %q", summary)
	}
}

// TestBulkFastForwardRefusesUnqualifiedRowsWithoutPreflight keeps the sweep on
// the same qualified-target contract as the single-row U handoff: rows that U
// would refuse never reach planops, and a row already handed off to Git Viewer
// is left to that operation.
func TestBulkFastForwardRefusesUnqualifiedRowsWithoutPreflight(t *testing.T) {
	planDir := filepath.Join(t.TempDir(), "plans", "same")
	bound := bulkPlanRow("in-flight", filepath.Join(t.TempDir(), "repo"))
	m := New(Config{})
	m.plans = []PlanListItem{
		{Name: "unbound", Key: coreplan.NewPlanKey(planDir)},
		{
			Name: "no-repos", Key: coreplan.NewPlanKey(planDir + "-2"),
			Binding:      coreplan.PlanBinding{Health: coreplan.BindingValid},
			ActionTarget: coreplan.PlanActionTarget{PlanDir: planDir, ContainerPath: "/containers/same"},
		},
		bound,
	}
	m.actionPending[planItemKey(bound)] = embed.GitOperationUpdateOnly

	targets, skipped := m.collectBulkTargets()
	if len(targets) != 0 {
		t.Fatalf("unqualified rows reached preflight: %#v", targets)
	}
	reasons := map[string]string{}
	for _, skip := range skipped {
		reasons[skip.name] = skip.reason
	}
	if !strings.Contains(reasons["unbound"], string(coreplan.BindingUnbound)) {
		t.Errorf("unbound row reason = %q", reasons["unbound"])
	}
	if !strings.Contains(reasons["no-repos"], "no qualified repository target") {
		t.Errorf("repo-less row reason = %q", reasons["no-repos"])
	}
	if !strings.Contains(reasons["in-flight"], "already in flight") {
		t.Errorf("in-flight row reason = %q", reasons["in-flight"])
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("F must not run a sweep with no eligible rows, emitted %T", cmd())
	}
	if !strings.Contains(m.statusMessage, "No plan can be fast-forwarded") {
		t.Fatalf("status = %q", m.statusMessage)
	}
}

// bulkRepoFixture builds "<root>/<name>/main" plus a linked worktree checkout
// on branch "feature", and optionally advances main so the worktree is behind
// by one non-conflicting commit.
func bulkRepoFixture(t *testing.T, name string, behind bool) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	main := filepath.Join(root, "main")
	bulkGit(t, t.TempDir(), "init", "--initial-branch=main", main)
	bulkGit(t, main, "config", "user.email", "test@example.com")
	bulkGit(t, main, "config", "user.name", "Browser Test")
	bulkCommit(t, main, "base.txt", "base")
	worktree := filepath.Join(root, "feature")
	bulkGit(t, main, "worktree", "add", "-b", "feature", worktree)
	bulkCommit(t, worktree, name+"-feature.txt", "feature work")
	if behind {
		bulkCommit(t, main, "main-advance.txt", "main advance")
	}
	return worktree
}

// bulkMainCheckout returns the primary checkout that owns a fixture worktree.
func bulkMainCheckout(t *testing.T, worktree string) string {
	t.Helper()
	return filepath.Join(filepath.Dir(worktree), "main")
}

// bulkPlanRow builds a bound row whose action target names the given checkouts,
// mirroring what daemon-live row assembly produces for a plan worktree.
func bulkPlanRow(name string, repos ...string) PlanListItem {
	planDir := filepath.Join("/plans", name)
	target := coreplan.PlanActionTarget{
		PlanDir: planDir, RegistryID: "registry/" + name, ContainerPath: filepath.Dir(repos[0]),
	}
	for _, repo := range repos {
		target.Repos = append(target.Repos, coreplan.RepoTarget{Name: filepath.Base(filepath.Dir(repo)), Path: repo})
	}
	return PlanListItem{
		Name: name, Key: coreplan.NewPlanKey(planDir),
		Binding:      coreplan.PlanBinding{Key: coreplan.NewPlanKey(planDir), Health: coreplan.BindingValid},
		ActionTarget: target,
	}
}

func bulkCommit(t *testing.T, repo, file, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, file), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bulkGit(t, repo, "add", file)
	bulkGit(t, repo, "commit", "-m", message)
}

func bulkGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func bulkGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
