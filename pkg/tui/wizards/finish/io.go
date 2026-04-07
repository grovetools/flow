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
	Name        string
	Status      string
	IsAvailable bool
	IsEnabled   bool
	Details     []RepoStatus
	// Action and Check are opaque to the wizard. They exist so hosts
	// can keep all per-item state on a single struct instead of
	// maintaining parallel slices.
	Action func() error
	Check  func() (string, error)
}

// ansiRegex matches ANSI escape sequences so stripANSI can present
// plain status text to getStatusStyle regardless of whether the host
// built the status with color helpers.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes ANSI escape codes from a string.
func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}
