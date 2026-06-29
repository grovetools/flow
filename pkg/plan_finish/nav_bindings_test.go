package plan_finish

import (
	"testing"

	"github.com/grovetools/core/pkg/models"
)

func TestNavBindingPathStale(t *testing.T) {
	wt := "/Users/me/.local/share/grove/worktrees/feat-abc123/my-feature"
	cases := []struct {
		name        string
		sessionPath string
		want        bool
	}{
		{"exact container", wt, true},
		{"repo subdir", wt + "/flow", true},
		{"nested subdir", wt + "/flow/cmd", true},
		{"trailing slash normalized", wt + "/", true},
		{"sibling worktree", "/Users/me/.local/share/grove/worktrees/feat-abc123/other-feature", false},
		{"path prefix but not boundary", wt + "-suffix", false},
		{"unrelated path", "/Users/me/.config", false},
		{"empty session", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := navBindingPathStale(tc.sessionPath, wt); got != tc.want {
				t.Errorf("navBindingPathStale(%q, %q) = %v, want %v", tc.sessionPath, wt, got, tc.want)
			}
		})
	}

	if navBindingPathStale(wt, "") {
		t.Error("empty worktreePath should never match")
	}
}

func TestPruneNavBindingsUnderPath(t *testing.T) {
	wt := "/Users/me/.local/share/grove/worktrees/feat-abc123/my-feature"
	bindings := &models.NavSessionsFile{
		Sessions: map[string]models.NavSessionConfig{
			"a": {Path: wt + "/flow"},                // stale
			"b": {Path: "/Users/me/code/grovetools"}, // keep
			"c": {Path: wt},                          // stale
			"o": {Path: "/Users/me/.config"},         // keep
		},
		LockedKeys: []string{"o"},
		Groups: map[string]models.NavGroupState{
			"personal": {
				Sessions: map[string]models.NavSessionConfig{
					"e": {Path: wt + "/nav"},               // stale
					"w": {Path: "/Users/me/code/solutils"}, // keep
				},
			},
			"work": {
				Sessions: map[string]models.NavSessionConfig{
					"x": {Path: "/Users/me/code/other"}, // keep
				},
			},
		},
	}

	if n := countNavBindingsUnderPath(bindings, wt); n != 3 {
		t.Fatalf("countNavBindingsUnderPath = %d, want 3", n)
	}

	removed := pruneNavBindingsUnderPath(bindings, wt)
	if removed != 3 {
		t.Fatalf("pruneNavBindingsUnderPath removed %d, want 3", removed)
	}

	// Default group: only stale keys removed; survivors and locked keys intact.
	if _, ok := bindings.Sessions["a"]; ok {
		t.Error("stale key 'a' should be removed from default group")
	}
	if _, ok := bindings.Sessions["c"]; ok {
		t.Error("stale key 'c' should be removed from default group")
	}
	if _, ok := bindings.Sessions["b"]; !ok {
		t.Error("live key 'b' should be retained")
	}
	if _, ok := bindings.Sessions["o"]; !ok {
		t.Error("live key 'o' should be retained")
	}
	if len(bindings.LockedKeys) != 1 || bindings.LockedKeys[0] != "o" {
		t.Errorf("LockedKeys mutated: %v", bindings.LockedKeys)
	}

	// Named group: stale removed, survivor kept.
	if _, ok := bindings.Groups["personal"].Sessions["e"]; ok {
		t.Error("stale key 'e' should be removed from personal group")
	}
	if _, ok := bindings.Groups["personal"].Sessions["w"]; !ok {
		t.Error("live key 'w' should be retained in personal group")
	}
	if _, ok := bindings.Groups["work"].Sessions["x"]; !ok {
		t.Error("unrelated group 'work' should be untouched")
	}

	// Idempotent: a second prune removes nothing.
	if again := pruneNavBindingsUnderPath(bindings, wt); again != 0 {
		t.Errorf("second prune removed %d, want 0", again)
	}
}

func TestPruneNavBindingsNilSafe(t *testing.T) {
	if got := pruneNavBindingsUnderPath(nil, "/x"); got != 0 {
		t.Errorf("nil bindings prune = %d, want 0", got)
	}
	if got := countNavBindingsUnderPath(nil, "/x"); got != 0 {
		t.Errorf("nil bindings count = %d, want 0", got)
	}
}
