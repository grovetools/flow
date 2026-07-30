package status

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// The job filter is the "/"-activated incremental search over the jobs table.
// It holds to the shared Grove TUI search/filter ergonomics contract, of which
// nav's sessionizer is the reference implementation (nb's browser is the other
// implementation at parity):
//
//   - "/" (keymap.Base.Search) focuses the input, which then consumes every
//     keystroke — including the c…/v…/z…/gg chord prefixes, which must be typed
//     into the query rather than arming a chord.
//   - Esc blurs but PRESERVES the query and the narrowed rows; a second Esc
//     clears it. Enter also blurs-while-keeping-value and reveals the first
//     match.
//   - While focused, the readline editing keys come free from the textinput's
//     own KeyMap because the update loop forwards every unhandled key to it:
//     ctrl+u wipes the query without leaving search (the one-key clear),
//     ctrl+w drops a word, ctrl+a/ctrl+e jump to the ends. Nothing here binds
//     them — a new case in the focused branch would shadow them, so
//     TestJobFilter_CtrlUClearsQueryInPlace pins ctrl+u.
//   - "i" re-enters a preserved (non-empty, blurred) query — vim insert.
//   - The query renders IN PLACE of the JOB column header behind
//     theme.IconSearch, with a thin cursor while focused and a fat block while
//     blurred-but-active, so the two states are never ambiguous.
//   - Matching is case-insensitive substring over the job's filename and title,
//     and ancestors of a match are retained so a matching subjob stays
//     reachable in the tree.
//
// The substring/parent-retention logic is deliberately per-tool (nav matches
// *api.Project, nb matches *DisplayNode, this matches *orchestration.Job); no
// shared core helper exists to reuse.

const (
	// filterCursorFocused is the thin cursor shown while the query is being
	// typed; filterCursorBlurred is the fat block shown while the filter is
	// applied but the input no longer has focus. Same glyph pair nav and nb use.
	filterCursorFocused = "▏"
	filterCursorBlurred = "█"
)

// newJobFilterInput builds the filter's text input. The prompt is never
// rendered by the input itself (the header cell draws the icon and value), but
// it is set to match nav so a future inline render stays consistent.
func newJobFilterInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = ""
	ti.Prompt = theme.DefaultTheme.Muted.Render(theme.IconSearch + " ")
	ti.CharLimit = 256
	ti.Width = 50
	return ti
}

// ensureJobFilterInput initializes a zero-valued filter input in place,
// preserving any query already set. Models built as struct literals (tests,
// embedders that don't go through New) would otherwise focus an input with no
// key bindings, which reads as a search box that swallows keys.
func (m *Model) ensureJobFilterInput() {
	if m.jobFilterInput.CharLimit != 0 {
		return
	}
	value := m.jobFilterInput.Value()
	m.jobFilterInput = newJobFilterInput()
	m.jobFilterInput.SetValue(value)
}

// JobFilterFocused reports whether the job filter input has focus, i.e. whether
// single-letter keys belong to the query rather than to actions. Feeds
// IsTextEntryActive so hosts stop intercepting shortcuts while searching.
func (m Model) JobFilterFocused() bool {
	return m.jobFilterInput.Focused()
}

// jobFilterQuery returns the active query, lowercased for matching. Empty when
// no filter is applied.
func (m Model) jobFilterQuery() string {
	return strings.ToLower(m.jobFilterInput.Value())
}

// jobFilterApplied reports whether rows are currently narrowed by a query.
func (m Model) jobFilterApplied() bool {
	return m.jobFilterInput.Value() != ""
}

// jobFilterVisible reports whether the search field should render in place of
// the JOB header: while focused (even empty, so "/" gives immediate feedback)
// or while a query is applied.
func (m Model) jobFilterVisible() bool {
	return m.jobFilterInput.Focused() || m.jobFilterApplied()
}

// jobMatchesFilter reports whether a job matches the lowercased query:
// case-insensitive substring over its filename and its title, the two fields
// the JOB and TITLE columns display.
func jobMatchesFilter(job *orchestration.Job, query string) bool {
	if job == nil {
		return false
	}
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(job.Filename), query) ||
		strings.Contains(strings.ToLower(job.Title), query)
}

// jobFilterVisibleIDs returns the set of job IDs the filter admits: every
// match plus the ancestors of every match, so a matching subjob is reachable
// through its parent chain instead of collapsing the tree to bare leaves.
// Returns nil when no filter is applied, which callers read as "no filtering".
func (m *Model) jobFilterVisibleIDs() map[string]bool {
	query := m.jobFilterQuery()
	if query == "" {
		return nil
	}
	visible := make(map[string]bool, len(m.Jobs))
	for _, job := range m.Jobs {
		if !jobMatchesFilter(job, query) {
			continue
		}
		visible[job.ID] = true
		seen := map[string]bool{job.ID: true}
		for parent := m.JobParents[job.ID]; parent != nil && !seen[parent.ID]; parent = m.JobParents[parent.ID] {
			seen[parent.ID] = true
			visible[parent.ID] = true
		}
	}
	return visible
}

// applyJobFilter re-narrows the table after the query changed and parks the
// cursor on the first match. Called on every keystroke that edits the query.
func (m *Model) applyJobFilter() {
	m.DisplayRows = m.buildDisplayRows()
	m.clampCursor()
	m.revealFirstJobFilterMatch()
	m.adjustScrollOffset()
}

// clearJobFilter drops the query and restores the fold-respecting rows. The
// cursor rides along by NodeID (rebuildDisplayRows), so clearing a filter
// leaves you on the job you just found rather than back at the top.
func (m *Model) clearJobFilter() {
	m.jobFilterInput.SetValue("")
	m.jobFilterInput.Blur()
	m.rebuildDisplayRows()
	m.adjustScrollOffset()
}

// revealFirstJobFilterMatch moves the cursor to the first row that actually
// matches the query, skipping the ancestor rows retained only for context. A
// no-op when no filter is applied.
func (m *Model) revealFirstJobFilterMatch() {
	query := m.jobFilterQuery()
	if query == "" {
		return
	}
	for i := range m.DisplayRows {
		row := &m.DisplayRows[i]
		if row.Type == RowTypeJob && jobMatchesFilter(row.Job, query) {
			m.Cursor = i
			m.adjustScrollOffset()
			return
		}
	}
	m.Cursor = 0
	m.adjustScrollOffset()
}

// filterNavCmd mirrors the Up/Down arms of the flat key switch: a cursor move
// made from inside the filter still reloads an open detail pane (and re-emits
// the context scope), so the pane follows the row being stepped through.
func (m Model) filterNavCmd() (Model, tea.Cmd) {
	if m.ActiveDetailPane == NoPane {
		return m, nil
	}
	m, reloadCmd := m.reloadActiveDetailPane()
	if scopeCmd := m.emitContextScopeUpdate(); scopeCmd != nil {
		return m, tea.Batch(reloadCmd, scopeCmd)
	}
	return m, reloadCmd
}

// jobFilterHeaderLabel is the header cell the search field renders as:
// magnifying glass, the query, and the cursor glyph for the current state.
func (m Model) jobFilterHeaderLabel() string {
	cursor := filterCursorBlurred
	if m.jobFilterInput.Focused() {
		cursor = filterCursorFocused
	}
	return theme.IconSearch + " " + m.jobFilterInput.Value() + cursor
}

// jobFilterHeaderIndex returns the header slot the search field takes over: the
// JOB column, or — when the user has hidden JOB via the column toggle — the
// leftmost non-SEL column, so an active filter is never invisible. Returns -1
// when the search field isn't showing.
func (m Model) jobFilterHeaderIndex(headers []string) int {
	if !m.jobFilterVisible() {
		return -1
	}
	fallback := -1
	for i, h := range headers {
		if h == "JOB" {
			return i
		}
		if fallback < 0 && h != "SEL" {
			fallback = i
		}
	}
	return fallback
}

// displayHeaders returns headers as they render, with the search field
// substituted into its slot. The canonical names from tableHeaders stay in use
// for cell dispatch and width keys — only the drawn text changes.
func (m Model) displayHeaders(headers []string) []string {
	idx := m.jobFilterHeaderIndex(headers)
	if idx < 0 {
		return headers
	}
	display := make([]string, len(headers))
	copy(display, headers)
	display[idx] = m.jobFilterHeaderLabel()
	return display
}
