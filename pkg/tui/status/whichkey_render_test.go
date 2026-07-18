package status

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/core/tui/components/whichkey"
	"github.com/grovetools/flow/pkg/orchestration"
)

// TestOverlayWhichKeyBottomPreservesHeight pins the two properties that keep the
// popup on-screen and low: the overlay does NOT grow the base's line count (so
// it can never push the footer past the terminal — the "way too low / invisible"
// regression), and the popup lands on the BOTTOM rows.
func TestOverlayWhichKeyBottomPreservesHeight(t *testing.T) {
	base := strings.Join([]string{
		"row0", "row1", "row2", "row3", "row4",
		"row5", "row6", "row7", "row8", "row9",
	}, "\n")
	popup := strings.Join([]string{"POPUP-A", "POPUP-B", "POPUP-C"}, "\n")

	rule := strings.Repeat("-", 12)
	out := whichkey.OverlayBottom(base, popup, rule)
	lines := strings.Split(out, "\n")

	if len(lines) != 10 {
		t.Fatalf("overlay changed height: got %d lines, want 10 (base height)", len(lines))
	}
	// The top-border rule replaces the row directly above the popup (row 6).
	if !strings.Contains(lines[6], "----") {
		t.Errorf("expected top-border rule on row 6, got %q", lines[6])
	}
	// Top rows untouched (row 6 becomes the border, rows 7-9 the popup).
	for i := 0; i < 6; i++ {
		if !strings.Contains(lines[i], "row"+string(rune('0'+i))) {
			t.Errorf("top row %d was disturbed: %q", i, lines[i])
		}
	}
	// Popup occupies the bottom 3 rows.
	for i, want := range []string{"POPUP-A", "POPUP-B", "POPUP-C"} {
		if !strings.Contains(lines[7+i], want) {
			t.Errorf("bottom row %d = %q, want to contain %q", 7+i, lines[7+i], want)
		}
	}
}

// TestWhichKeyPopupVisibleInView drives the real View() path and asserts the
// which-key popup actually renders (the "invisible" complaint), and that it sits
// in the lower portion of the frame — not appended below the footer.
func TestWhichKeyPopupVisibleInView(t *testing.T) {
	job := &orchestration.Job{ID: "j1", Filename: "j1.md", Title: "job one"}
	plan := &orchestration.Plan{
		Name:     "t",
		Jobs:     []*orchestration.Job{job},
		JobsByID: map[string]*orchestration.Job{job.ID: job},
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{Plan: plan, Graph: graph})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = mdl.(Model)

	// Arm the View ("v") namespace prefix.
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = mdl.(Model)
	// Force the show-delay to elapsed so the popup is visible this frame
	// (default is 400ms wall-clock; the delay gate is exercised elsewhere).
	m.WhichKey.Delay = 0

	if !m.WhichKey.PopupVisible() {
		t.Fatal("expected whichKeyPopupVisible after arming 'v' with zero delay")
	}

	out := m.View()
	lines := strings.Split(out, "\n")
	titleLine := -1
	for i, l := range lines {
		if strings.Contains(l, "View (v") {
			titleLine = i
			break
		}
	}
	if titleLine < 0 {
		t.Fatalf("which-key popup title not found in rendered View():\n%s", out)
	}
	// The popup must be in the lower half of the frame (bottom-anchored), not up
	// top and not scrolled off the end.
	if titleLine < len(lines)/2 {
		t.Errorf("popup title at line %d of %d — expected the lower half (bottom-anchored)", titleLine, len(lines))
	}
}
