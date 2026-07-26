package finish

import (
	"regexp"
)

// RepoStatus represents the merge status of a single repository, used
// for the optional inline detail view under the items list.
type RepoStatus struct {
	Name   string
	Status string // "merged", "needs_merge", "needs_rebase", "not_found"
}

// Item represents a single cleanup action displayed by the wizard.
// The wizard only reads the display fields and mutates IsEnabled as
// the user toggles selections. Action and Check are optional host
// fields: the wizard never invokes them. Hosts (CLI wrapper, view
// meta-panel) are responsible for running any Action closures on
// enabled items after the wizard emits DoneMsg.
type Item struct {
	// ID is a stable identifier used by hosts to look up items by
	// kind (e.g. "delete_local_branch") without depending on the
	// order the factory returned them. The wizard itself does not
	// read this field.
	ID          string
	Name        string
	Status      string
	IsAvailable bool
	IsEnabled   bool
	Details     []RepoStatus
	// ExclusiveGroup, when non-empty, marks this item as one of a set of
	// mutually exclusive actions: AT MOST ONE item per group may be enabled
	// at a time, and the wizard enforces that as the user selects.
	//
	// The winner within a group is the FIRST such item in list order, which
	// is why the factory emits archive_worktree ahead of prune_worktree —
	// "select all" must land on the recoverable retirement, not the
	// destructive one. Zero items enabled is a valid state: the exclusion is
	// "at most one", never "exactly one".
	ExclusiveGroup string
	// Advanced places an uncommon/expensive action in the Advanced section.
	// Advanced actions are deliberately excluded from "select all" and must be
	// opted into individually.
	Advanced bool
	// Action and Check are opaque to the wizard. They exist so hosts
	// can keep all per-item state on a single struct instead of
	// maintaining parallel slices.
	Action func() error
	Check  func() (string, error)
}

// GroupWorktreeRetirement is the exclusive group shared by the two ways to
// retire a plan's worktree container: archiving it (moved under the grove
// worktree archive, with per-repo bundles) and pruning it (deleted). Running
// both in one finish is never valid — the second acts on a container the first
// already moved or removed.
const GroupWorktreeRetirement = "worktree_retirement"

// ansiRegex matches ANSI escape sequences so stripANSI can present
// plain status text to getStatusStyle regardless of whether the host
// built the status with color helpers.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes ANSI escape codes from a string.
func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}
