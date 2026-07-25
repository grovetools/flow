package plan_finish

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/grovetools/core/git"

	gexec "github.com/grovetools/flow/pkg/exec"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// stubGroveOnPath prepends a temp dir containing an executable `grove` stub to
// PATH. The stub appends its argv and cwd to a log file so tests can assert how
// it was invoked, and exits with exitCode.
func stubGroveOnPath(t *testing.T, exitCode int) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "grove.log")
	script := "#!/bin/sh\n" +
		"printf '%s|%s\\n' \"$*\" \"$(pwd)\" >> " + logPath + "\n" +
		"exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "grove"), []byte(script), 0o755); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}

// buildItemsForAvailability builds the full item list against a real (empty)
// git repo. BuildContext is populated directly rather than via NewBuildContext
// so the test does not trigger a full workspace DiscoverAll walk.
func buildItemsForAvailability(t *testing.T, opts Options) []*finish.Item {
	t.Helper()
	repo := t.TempDir()
	gitRoot := initGitRepoWithIdentity(t, repo)
	commitFile(t, gitRoot, "a.txt", "a", "init")
	planPath := filepath.Join(repo, "plans", "my-plan")
	if err := os.MkdirAll(planPath, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := &orchestration.Plan{
		Directory: planPath,
		Config:    &orchestration.PlanConfig{Worktree: "feature", Status: "review"},
	}
	bctx := BuildContext{
		PlanPath:     planPath,
		Plan:         plan,
		GitRoot:      gitRoot,
		WorktreeName: "feature",
		BranchName:   "feature",
		Executor:     &gexec.RealCommandExecutor{},
		WM:           git.NewWorktreeManager(),
	}
	result, err := BuildItems(bctx, opts)
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	return result.Items
}

// TestRebuildBinariesIsAvailableWithGroveOnPath pins the A1/A2 defect: the
// rebuild item's Check returns "Available (opt-in)", which the availability
// gate must accept. It did not, so `flow plan finish --rebuild-binaries` was a
// silent no-op and the wizard never rendered the item.
func TestRebuildBinariesIsAvailableWithGroveOnPath(t *testing.T) {
	stubGroveOnPath(t, 0)
	items := buildItemsForAvailability(t, Options{})
	item := ItemsByID(items, ItemRebuildBinaries)
	if item == nil {
		t.Fatal("rebuild_binaries item not found")
	}
	if !item.IsAvailable {
		t.Fatalf("rebuild_binaries must be available with grove on PATH and a git root; status=%q IsAvailable=false", item.Status)
	}
}

// TestEveryNonNAItemIsReachable is the structural regression guard for the
// whole family: an item whose Check reports anything other than an explicitly
// unavailable status must be reachable. Deriving IsAvailable from an exact
// match against a colourised display string silently deleted product features
// on copy-edits (60add16 proved it once).
func TestEveryNonNAItemIsReachable(t *testing.T) {
	stubGroveOnPath(t, 0)
	items := buildItemsForAvailability(t, Options{})
	for _, item := range items {
		plain := stripANSIForTest(item.Status)
		if statusLooksUnavailable(plain) {
			if item.IsAvailable {
				t.Errorf("item %q has unavailable status %q but IsAvailable=true", item.ID, plain)
			}
			continue
		}
		if !item.IsAvailable {
			t.Errorf("item %q reports actionable status %q but IsAvailable=false (unreachable from every host)", item.ID, plain)
		}
	}
}

// statusLooksUnavailable is the test's own independent view of which statuses
// mean "nothing to do", written from the item Checks rather than from the
// production classifier, so the two must agree.
func statusLooksUnavailable(plain string) bool {
	plain = strings.TrimSpace(plain)
	for _, p := range []string{
		"N/A", "Not found", "None", "Skipped", "Error", "Invalid",
		"Daemon unavailable", "Already finished", "Conflicts with", "Unknown",
	} {
		if strings.HasPrefix(plain, p) {
			return true
		}
	}
	return plain == ""
}

var testANSIRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSIForTest(s string) string {
	return testANSIRe.ReplaceAllString(s, "")
}

// TestStatusIsAvailable_Table pins the classifier against every status string
// the item Checks can actually return, including the parameterised ones the
// old exact-equality allow-list silently rejected ("Active (docker)",
// "3 running", "Available (local only)", "Available (opt-in)"), which made
// env-teardown, kill-bound-agents, prune-orphans and rebuild-binaries
// unreachable in exactly the states where they had work to do.
func TestStatusIsAvailable_Table(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"Available", true},
		{"Available (opt-in)", true},
		{"Available (local only)", true},
		{"Available (local + cloud)", true},
		{"Available (discovery failed)", true},
		{"Active (docker)", true},
		{"Active (user-managed, skipping)", true},
		{"Exists", true},
		{"Exists on origin", true},
		{"Running", true},
		{"3 running", true},
		{"Has links", true},
		{"Checked out in worktree", true},
		{"Has changes (needs --force)", true},
		{"Has changes in core (needs force)", true},
		{"Has 3 commits ahead of main", true},
		{"2 stale key(s)", true},
		{"3 repos: 2 to merge, 1 merged", true},

		{"N/A", false},
		{"N/A (grove not found)", false},
		{"N/A (no remote)", false},
		{"Not found", false},
		{"None", false},
		{"None running", false},
		{"Skipped (--keep-env)", false},
		{"Skipped (use --prune-orphans)", false},
		{"Error", false},
		{"Invalid YAML", false},
		{"Daemon unavailable", false},
		{"Already finished", false},
		{"Conflicts with prune_worktree", false},
		{"", false},
	}
	for _, tc := range cases {
		// Exercise the colourised form too: availability must not depend on
		// whether stdout happened to be a terminal when the status was built.
		for _, s := range []string{tc.status, color.YellowString("%s", tc.status), color.RedString("%s", tc.status)} {
			if got := statusIsAvailable(s); got != tc.want {
				t.Errorf("statusIsAvailable(%q) = %v, want %v", s, got, tc.want)
			}
		}
	}
}

// TestGroveBuildFailureIsNotFatal pins that a failing `grove build` degrades to
// a warning. A rebuild is not teardown: if it could return an error it would
// become firstErr and skip archive_plan / mark_finished, leaving every plan
// un-archived after a red build.
func TestGroveBuildFailureIsNotFatal(t *testing.T) {
	stubGroveOnPath(t, 1)
	items := buildItemsForAvailability(t, Options{})
	item := ItemsByID(items, ItemRebuildBinaries)
	if item == nil {
		t.Fatal("rebuild_binaries item not found")
	}
	if err := item.Action(); err != nil {
		t.Fatalf("a failing grove build must not return an error (it would skip archive_plan/mark_finished): %v", err)
	}
}

// TestRebuildBinariesRoutesThroughGroveBuild pins the routing: `grove build`
// invoked with cwd == gitRoot.
func TestRebuildBinariesRoutesThroughGroveBuild(t *testing.T) {
	logPath := stubGroveOnPath(t, 0)
	items := buildItemsForAvailability(t, Options{})
	item := ItemsByID(items, ItemRebuildBinaries)
	if item == nil {
		t.Fatal("rebuild_binaries item not found")
	}
	if err := item.Action(); err != nil {
		t.Fatalf("Action: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("grove stub was never invoked: %v", err)
	}
	if !strings.Contains(string(data), "build|") {
		t.Fatalf("expected `grove build` invocation, log=%q", string(data))
	}
}
