package status

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newSizedModel builds a Model over n trivial jobs and gives it a size, so the
// pane Manager has real dimensions to distribute.
func newSizedModel(t *testing.T, n, width, height int) Model {
	t.Helper()
	plan := &orchestration.Plan{Name: "t", JobsByID: map[string]*orchestration.Job{}}
	for i := range n {
		job := &orchestration.Job{
			ID:       fmt.Sprintf("j%d", i),
			Filename: fmt.Sprintf("j%d.md", i),
			Title:    fmt.Sprintf("job %d", i),
		}
		plan.Jobs = append(plan.Jobs, job)
		plan.JobsByID[job.ID] = job
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{Plan: plan, Graph: graph})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return mdl.(Model)
}

// TestVisibleJobCountFillsPane pins the row budget the job table gets when it
// is the only pane: everything the host pager handed us minus the footer row
// and the table's own frame. The stale header/top-margin/scroll-indicator
// reservations this used to carry cost the table six rows of dead space.
func TestVisibleJobCountFillsPane(t *testing.T) {
	const height = 24
	m := newSizedModel(t, 30, 120, height)

	want := height - footerHeight - tableChrome
	if got := m.getVisibleJobCount(); got != want {
		t.Fatalf("getVisibleJobCount() = %d, want %d (height %d − footer %d − table chrome %d)",
			got, want, height, footerHeight, tableChrome)
	}
}

// TestViewFillsHeightWithoutDeadSpace is the end-to-end version: the rendered
// frame must be exactly as tall as the height it was given (no overflow), and
// every row of it that isn't table frame or footer must carry a job — no blank
// rows going begging under the table.
func TestViewFillsHeightWithoutDeadSpace(t *testing.T) {
	const height = 24
	m := newSizedModel(t, 30, 120, height)

	lines := strings.Split(m.View(), "\n")
	if len(lines) != height {
		t.Fatalf("View() rendered %d lines, want exactly %d", len(lines), height)
	}

	jobRows := 0
	for _, l := range lines {
		if strings.Contains(l, ".md") {
			jobRows++
		}
	}
	if want := m.getVisibleJobCount(); jobRows != want {
		t.Errorf("rendered %d job rows, want %d — the pane has rows the table isn't using", jobRows, want)
	}
}

// TestScrollIndicatorRidesFooter: the indicator shares the footer row rather
// than claiming a row (plus a spacer) of its own under the table.
func TestScrollIndicatorRidesFooter(t *testing.T) {
	m := newSizedModel(t, 30, 120, 24)

	lines := strings.Split(m.View(), "\n")
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, "[30/30]") {
		t.Errorf("footer row %q should carry the scroll indicator", footer)
	}
	for i, l := range lines[:len(lines)-1] {
		if strings.Contains(l, "[30/30]") {
			t.Errorf("scroll indicator also found on line %d (%q) — it should only ride the footer", i, l)
		}
	}
}

// TestVisibleJobCountStackedSplit: with the detail pane stacked below, the
// table's budget is what's left after the detail pane and the pane Manager's
// separator row — and the jobs pane is still filled exactly.
func TestVisibleJobCountStackedSplit(t *testing.T) {
	const height = 30
	m := newSizedModel(t, 30, 120, height)
	mdl, _ := m.openDetailPane(LogsPaneDetail)
	m = mdl.(Model)

	if m.LogSplitVertical {
		t.Skip("persisted tuiState prefers a side-by-side split")
	}

	want := height - footerHeight - tableChrome - m.LogViewerHeight - horizontalDividerHeight
	if got := m.getVisibleJobCount(); got != want {
		t.Fatalf("getVisibleJobCount() = %d, want %d (detail pane %d rows + %d divider)",
			got, want, m.LogViewerHeight, horizontalDividerHeight)
	}

	lines := strings.Split(m.View(), "\n")
	if len(lines) != height {
		t.Fatalf("View() rendered %d lines, want exactly %d", len(lines), height)
	}
	jobRows := 0
	for _, l := range lines {
		if strings.Contains(l, ".md") && strings.Contains(l, "│") {
			jobRows++
		}
	}
	if jobRows != want {
		t.Errorf("rendered %d job rows, want %d", jobRows, want)
	}
}

// TestEditDepsOverlayFitsHeight: the Edit Dependencies overlay shares
// getVisibleJobCount but replaces the whole view, so it must budget against its
// own chrome — reclaiming rows for the job table must not make it overflow.
func TestEditDepsOverlayFitsHeight(t *testing.T) {
	const height = 24
	m := newSizedModel(t, 30, 120, height)
	m.EditingDeps = true
	m.EditDepsJobIndex = 0

	lines := strings.Split(m.View(), "\n")
	if len(lines) > height {
		t.Errorf("edit-deps overlay rendered %d lines, overflowing a %d-row page", len(lines), height)
	}
	if len(lines) < height-1 {
		t.Errorf("edit-deps overlay rendered only %d lines of %d — dead space", len(lines), height)
	}
}

// TestFooterNeverWrapsWhenNarrow: the Jobs pane often occupies half a terminal,
// and a wrapped footer would cost back the row folding the indicator in just
// reclaimed. Narrow panes truncate the help text (or drop the indicator
// outright), never wrap — so the frame stays exactly `height` rows tall.
func TestFooterNeverWrapsWhenNarrow(t *testing.T) {
	const height = 24
	for _, width := range []int{10, 24, 40, 60, 120} {
		m := newSizedModel(t, 30, width, height)

		if footer := m.renderFooter(); strings.Contains(footer, "\n") {
			t.Errorf("width %d: footer wrapped to multiple lines:\n%s", width, footer)
		}
		if lines := strings.Split(m.View(), "\n"); len(lines) != height {
			t.Errorf("width %d: View() rendered %d lines, want %d", width, len(lines), height)
		}
	}
}

// TestFooterIndicatorIsRightAligned: when the pane is wide enough for both, the
// indicator sits flush against the right edge of the pane rather than trailing
// the help text.
func TestFooterIndicatorIsRightAligned(t *testing.T) {
	const width = 120
	m := newSizedModel(t, 30, width, 24)

	footer := m.renderFooter()
	if got := lipgloss.Width(footer); got != width {
		t.Fatalf("footer is %d columns wide, want %d (indicator flush right)", got, width)
	}
	if !strings.HasSuffix(strings.TrimRight(footer, " "), "[30/30]") {
		t.Errorf("footer should end with the scroll indicator, got %q", footer)
	}
}
