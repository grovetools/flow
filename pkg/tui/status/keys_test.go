package status

import (
	"testing"

	"github.com/grovetools/core/tui/keymap"
)

// TestStatusKeyMapAuditCoverage asserts the flow status TUI keymap has no
// coverage gaps: every enabled binding appears in exactly one Sections()
// entry, no help-label lies, and no empty-help bindings. If this fails, the
// disable list or the Sections() membership in NewKeyMap is wrong — fix the
// code, not the test.
func TestStatusKeyMapAuditCoverage(t *testing.T) {
	if gaps := keymap.AuditCoverage(NewKeyMap(nil)); len(gaps) != 0 {
		t.Fatalf("audit gaps: %+v", gaps)
	}
}
