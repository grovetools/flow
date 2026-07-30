package status

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newFilterKeyModel gives a display-test model the keymap, which-key host and
// filter input the update loop needs, so tests drive real keystrokes through
// Update instead of poking the filter helpers.
func newFilterKeyModel(m *Model) Model {
	mm := *m
	mm.KeyMap = NewKeyMap(nil)
	mm.WhichKey = keymap.NewWhichKeyHost(nil, mm.KeyMap.Namespaces()...)
	mm.jobFilterInput = newJobFilterInput()
	mm.availableColumns = []string{"JOB", "TYPE"}
	mm.columnVisibility = defaultColumnVisibility()
	mm.Height = 40
	return mm
}

// pressEsc drives one Escape through Update (pressKeys only sends runes).
func pressEsc(m Model) Model {
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	return updated.(Model)
}

func filterRowIDs(m *Model) []string {
	var ids []string
	for i := range m.DisplayRows {
		row := &m.DisplayRows[i]
		if row.Type == RowTypeJob {
			ids = append(ids, row.Job.ID)
		}
	}
	return ids
}

func setFilter(m *Model, query string) {
	m.jobFilterInput = newJobFilterInput()
	m.jobFilterInput.SetValue(query)
	m.applyJobFilter()
}

func TestJobFilter_MatchesFilenameAndTitleCaseInsensitively(t *testing.T) {
	design := testJob("design")
	design.Filename = "01-design-search.md"
	design.Title = "Design the search box"
	impl := testJob("impl")
	impl.Filename = "02-implement.md"
	impl.Title = "Implement SEARCH ergonomics"
	other := testJob("other")
	other.Filename = "03-release.md"
	other.Title = "Cut a release"

	m := newDisplayTestModel(design, impl, other)

	// Filename match only.
	setFilter(m, "01-design")
	if got := filterRowIDs(m); strings.Join(got, ",") != "design" {
		t.Errorf("filename filter rows = %v, want [design]", got)
	}

	// Title match, case-insensitive, hitting two jobs by different fields.
	setFilter(m, "search")
	if got := filterRowIDs(m); strings.Join(got, ",") != "design,impl" {
		t.Errorf("title filter rows = %v, want [design impl]", got)
	}

	// No match narrows to nothing rather than falling back to everything.
	setFilter(m, "nonexistent")
	if got := filterRowIDs(m); len(got) != 0 {
		t.Errorf("unmatched filter rows = %v, want none", got)
	}

	// Clearing restores every row.
	m.clearJobFilter()
	if got := filterRowIDs(m); len(got) != 3 {
		t.Errorf("cleared filter rows = %v, want all three", got)
	}
}

// A matching subjob must stay reachable: its ancestors are retained, and the
// collapsed fold that hides it in normal mode is ignored while filtering.
func TestJobFilter_RetainsAncestorsAndIgnoresFolds(t *testing.T) {
	parent := testJob("parent")
	parent.Status = orchestration.JobStatusCompleted // defaults collapsed
	parent.Filename = "00-coordinator.md"
	parent.Title = "Coordinator"
	child := testJob("child")
	child.ParentJobID = parent.ID
	child.Status = orchestration.JobStatusCompleted
	child.Filename = "01-widget-impl.md"
	child.Title = "Widget implementation"
	other := testJob("other")
	other.Filename = "02-unrelated.md"
	other.Title = "Unrelated"

	m := newOwnershipDisplayTestModel(parent, child, other)
	if len(m.DisplayRows) != 2 {
		t.Fatalf("family should start collapsed, rows = %v", filterRowIDs(m))
	}

	setFilter(m, "widget")
	if got := filterRowIDs(m); strings.Join(got, ",") != "parent,child" {
		t.Errorf("filtered rows = %v, want [parent child] (ancestor retained, fold ignored)", got)
	}
	// The cursor lands on the match itself, not on the retained ancestor.
	if row := m.currentRow(); row == nil || row.Job.ID != "child" {
		t.Errorf("cursor should reveal the match, got %+v", row)
	}
	// The explicit fold state is untouched — clearing the filter re-collapses.
	m.clearJobFilter()
	if len(m.DisplayRows) != 2 {
		t.Errorf("clearing the filter should restore the collapsed tree, rows = %v", filterRowIDs(m))
	}
}

// Virtual workflow rows (runs/phases/agents) drop out while filtering: the
// filter searches jobs, and a match's agent subtree is not part of the result.
func TestJobFilter_SuppressesVirtualWorkflowRows(t *testing.T) {
	job := testJob("job")
	job.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(job)
	addTestRun(m, "job", "wf_1", 2, 0)
	m.rebuildDisplayRows()
	if countRowType(m, RowTypeRun) != 1 {
		t.Fatalf("running job should show its run row, rows = %d", len(m.DisplayRows))
	}

	setFilter(m, "job")
	if countRowType(m, RowTypeRun) != 0 || countRowType(m, RowTypeAgent) != 0 {
		t.Errorf("filtering should leave only job rows, rows = %d", len(m.DisplayRows))
	}
	if len(m.DisplayRows) != 1 {
		t.Errorf("filtered rows = %d, want the single matching job", len(m.DisplayRows))
	}
}

func TestJobFilter_HeaderTakesOverJobColumn(t *testing.T) {
	m := newDisplayTestModel(testJob("alpha"))
	m.jobFilterInput = newJobFilterInput()
	m.availableColumns = []string{"JOB", "TYPE"}
	m.columnVisibility = defaultColumnVisibility()
	m.Height = 40

	headers := m.tableHeaders()
	if got := m.displayHeaders(headers); got[0] != "JOB" {
		t.Errorf("idle header = %q, want JOB", got[0])
	}

	// Focused: magnifying glass, the query, thin cursor.
	m.jobFilterInput.Focus()
	m.jobFilterInput.SetValue("alp")
	want := theme.IconSearch + " alp" + filterCursorFocused
	if got := m.displayHeaders(headers)[0]; got != want {
		t.Errorf("focused header = %q, want %q", got, want)
	}

	// Blurred but still applied: fat block cursor.
	m.jobFilterInput.Blur()
	want = theme.IconSearch + " alp" + filterCursorBlurred
	if got := m.displayHeaders(headers)[0]; got != want {
		t.Errorf("blurred header = %q, want %q", got, want)
	}

	// Cleared and blurred: the plain column name is back.
	m.jobFilterInput.SetValue("")
	if got := m.displayHeaders(headers)[0]; got != "JOB" {
		t.Errorf("cleared header = %q, want JOB", got)
	}
}

// An unmatched query must still render the table frame, or the search field
// (which lives in the header row) would vanish along with the rows.
func TestJobFilter_EmptyResultKeepsSearchHeader(t *testing.T) {
	m := newDisplayTestModel(testJob("alpha"))
	m.availableColumns = []string{"JOB", "TYPE"}
	m.columnVisibility = defaultColumnVisibility()
	m.Height = 40
	setFilter(m, "zzz")

	out := ansi.Strip(m.renderTableView())
	if !strings.Contains(out, theme.IconSearch+" zzz") {
		t.Errorf("empty-result table should keep the query visible, got:\n%s", out)
	}
	if !strings.Contains(out, "No matching jobs") {
		t.Errorf("empty-result table should say so, got:\n%s", out)
	}
}

// "/" focuses, every subsequent key types into the query — including the c…/v…/
// z… chord prefixes, which must never arm a namespace while filtering.
func TestJobFilter_SlashConsumesChordPrefixes(t *testing.T) {
	m := newFilterKeyModel(newDisplayTestModel(testJob("alpha"), testJob("beta")))

	m, _ = pressKeys(m, "/")
	if !m.jobFilterInput.Focused() {
		t.Fatal(`"/" should focus the job filter`)
	}
	if !m.IsTextEntryActive() {
		t.Error("a focused filter must report text entry so hosts stop intercepting keys")
	}

	m, _ = pressKeys(m, "c", "v", "z")
	if got := m.jobFilterInput.Value(); got != "cvz" {
		t.Errorf("chord prefixes should be typed, query = %q", got)
	}
	if m.WhichKey.IsPending() {
		t.Error("no chord may arm while the filter has focus")
	}
}

// ctrl+u is the one-key clear while typing: it empties the query and restores
// every row WITHOUT leaving search mode. It is inherited from the textinput's
// own KeyMap (DeleteBeforeCursor) rather than bound here, so a new case in the
// focused branch of Update would silently take it away — hence this test.
func TestJobFilter_CtrlUClearsQueryInPlace(t *testing.T) {
	m := newFilterKeyModel(newDisplayTestModel(testJob("alpha"), testJob("beta")))

	m, _ = pressKeys(m, "/", "b", "e", "t")
	if len(m.DisplayRows) != 1 {
		t.Fatalf("typing should narrow to beta, rows = %v", filterRowIDs(&m))
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(Model)

	if got := m.jobFilterInput.Value(); got != "" {
		t.Errorf("ctrl+u should empty the query, got %q", got)
	}
	if len(m.DisplayRows) != 2 {
		t.Errorf("ctrl+u should restore every row, rows = %v", filterRowIDs(&m))
	}
	if !m.jobFilterInput.Focused() {
		t.Error("ctrl+u must not leave search mode — the next keystroke belongs to a new query")
	}
}

// Esc blurs while PRESERVING the query; a second Esc clears it; "i" re-enters.
func TestJobFilter_EscPreservesThenClearsAndReEnters(t *testing.T) {
	alpha := testJob("alpha")
	beta := testJob("beta")
	beta.Title = "Beta job"
	m := newFilterKeyModel(newDisplayTestModel(alpha, beta))

	m, _ = pressKeys(m, "/", "b", "e", "t", "a")
	if len(m.DisplayRows) != 1 || m.DisplayRows[0].Job.ID != "beta" {
		t.Fatalf("typing should narrow to beta, rows = %v", filterRowIDs(&m))
	}

	m = pressEsc(m)
	if m.jobFilterInput.Focused() {
		t.Error("first Esc should blur the filter input")
	}
	if got := m.jobFilterInput.Value(); got != "beta" {
		t.Errorf("first Esc must preserve the query, got %q", got)
	}
	if len(m.DisplayRows) != 1 {
		t.Errorf("first Esc must preserve the narrowed rows, rows = %v", filterRowIDs(&m))
	}

	// Re-enter the preserved query (vim insert) and keep typing.
	m, _ = pressKeys(m, "i")
	if !m.jobFilterInput.Focused() {
		t.Fatal(`"i" should re-enter a preserved filter`)
	}
	m = pressEsc(m)

	m = pressEsc(m)
	if m.jobFilterInput.Value() != "" {
		t.Errorf("second Esc should clear the query, got %q", m.jobFilterInput.Value())
	}
	if len(m.DisplayRows) != 2 {
		t.Errorf("cleared filter should restore every row, rows = %v", filterRowIDs(&m))
	}
}

// Enter accepts the filter: it blurs keeping the value and reveals the first
// match rather than leaving the cursor wherever it was.
func TestJobFilter_EnterAcceptsAndRevealsFirstMatch(t *testing.T) {
	alpha := testJob("alpha")
	beta := testJob("beta")
	gamma := testJob("gamma")
	m := newFilterKeyModel(newDisplayTestModel(alpha, beta, gamma))

	m, _ = pressKeys(m, "/", "g")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	if m.jobFilterInput.Focused() {
		t.Error("Enter should blur the filter input")
	}
	if m.jobFilterInput.Value() != "g" {
		t.Errorf("Enter must keep the query, got %q", m.jobFilterInput.Value())
	}
	if row := m.currentRow(); row == nil || row.Job.ID != "gamma" {
		t.Errorf("Enter should reveal the first match, cursor row = %+v", row)
	}
}

// "i" stays the agent chat key when the job in view takes input: a live filter
// must never swallow a message meant for the agent.
func TestJobFilter_ReEnterYieldsToAgentInput(t *testing.T) {
	agent := testJob("agent")
	agent.Type = orchestration.JobTypeIsolatedAgent
	m := newFilterKeyModel(newDisplayTestModel(agent))
	m.ActiveLogJob = agent
	m.IsolatedAgentInput = textinput.New() // the real model builds this in New()

	m, _ = pressKeys(m, "/", "a")
	m = pressEsc(m)
	m, _ = pressKeys(m, "i")

	if m.jobFilterInput.Focused() {
		t.Error(`"i" must not re-enter the filter while an agent job takes input`)
	}
	if !m.IsolatedAgentInputActive {
		t.Error(`"i" should focus the agent chat input`)
	}
}
