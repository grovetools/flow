package status

import (
	"github.com/grovetools/flow/pkg/orchestration"
)

// The display-row layer separates the data model (m.Jobs, real file-backed
// jobs owned by the orchestrator) from the view model (m.DisplayRows, what
// the jobs table actually renders). Virtual rows (workflow runs, phases,
// agents) appear ONLY in DisplayRows — they must never enter m.Jobs, where
// the orchestrator could execute them, the StatePersister would write their
// status to files, and the 2s RefreshMsg reflatten would wipe them.
//
// The cursor and scroll offset index DisplayRows. Every job action resolves
// through the row's Job pointer (the owning job for virtual rows), so job
// actions keep working when the cursor rests on a virtual child row.

// RowType discriminates display rows.
type RowType int

const (
	RowTypeJob RowType = iota
	RowTypeRun
	RowTypePhase
	RowTypeAgent
	RowTypeMore // "… +K more" overflow indicator
)

// DisplayRow is one rendered row of the jobs table.
type DisplayRow struct {
	Type RowType
	// NodeID is the stable identity used for cursor restoration and fold
	// state across rebuilds: "job:<jobID>", "run:<jobID>/<runID>",
	// "phase:<jobID>/<runID>/<title>", "agent:<jobID>/<runID>/<agentID>",
	// "more:<jobID>/<runID>/<scope>".
	NodeID string
	// Job is the owning job: the job itself for RowTypeJob, the parent job
	// for every virtual row. Never nil.
	Job *orchestration.Job
	// Depth is the virtual depth below the job row (run=1, phase=2 or
	// agent=2 when flat, agent=3 when phase-grouped). 0 for job rows.
	Depth int

	// Workflow node data (nil/empty for RowTypeJob).
	RunID   string
	Run     *workflowRunState
	Phase   string
	Agent   *workflowAgentState
	MoreCnt int // RowTypeMore: number of hidden agents
}

// jobNodeID returns the stable NodeID for a job row.
func jobNodeID(jobID string) string { return "job:" + jobID }

// buildDisplayRows constructs the display rows from the current jobs slice.
// Virtual workflow rows are appended under their owning job by the workflow
// tree builder (see appendWorkflowRows); with no workflow state the result
// is exactly one row per job, in m.Jobs order.
func (m *Model) buildDisplayRows() []DisplayRow {
	rows := make([]DisplayRow, 0, len(m.Jobs))
	for _, job := range m.Jobs {
		rows = append(rows, DisplayRow{
			Type:   RowTypeJob,
			NodeID: jobNodeID(job.ID),
			Job:    job,
		})
		rows = m.appendWorkflowRows(rows, job)
	}
	return rows
}

// rebuildDisplayRows rebuilds DisplayRows and restores the cursor to the
// row it was on, by stable NodeID. If that node disappeared (job removed,
// parent folded), the cursor falls back to the owning job's row, then
// clamps.
func (m *Model) rebuildDisplayRows() {
	var prevNodeID string
	var prevJobID string
	if row := m.currentRow(); row != nil {
		prevNodeID = row.NodeID
		if row.Job != nil {
			prevJobID = row.Job.ID
		}
	}

	m.DisplayRows = m.buildDisplayRows()

	if prevNodeID != "" {
		if idx := m.rowIndexByNodeID(prevNodeID); idx >= 0 {
			m.Cursor = idx
		} else if idx := m.rowIndexByNodeID(jobNodeID(prevJobID)); idx >= 0 {
			m.Cursor = idx
		}
	}
	m.clampCursor()
}

// clampCursor keeps the cursor inside DisplayRows bounds.
func (m *Model) clampCursor() {
	if m.Cursor >= len(m.DisplayRows) {
		m.Cursor = len(m.DisplayRows) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

// rowIndexByNodeID returns the display index of the row with the given
// NodeID, or -1.
func (m *Model) rowIndexByNodeID(nodeID string) int {
	if nodeID == "" {
		return -1
	}
	for i := range m.DisplayRows {
		if m.DisplayRows[i].NodeID == nodeID {
			return i
		}
	}
	return -1
}

// rowAt returns the display row at index i, or nil when out of bounds.
func (m *Model) rowAt(i int) *DisplayRow {
	if i < 0 || i >= len(m.DisplayRows) {
		return nil
	}
	return &m.DisplayRows[i]
}

// currentRow returns the row under the cursor, or nil.
func (m *Model) currentRow() *DisplayRow {
	return m.rowAt(m.Cursor)
}

// CurrentJob resolves the job owning the cursor row: the job itself on a
// job row, the parent job on a virtual row, nil when there are no rows.
// Every job-action key handler resolves through this so actions work from
// virtual rows too.
func (m *Model) CurrentJob() *orchestration.Job {
	if row := m.currentRow(); row != nil {
		return row.Job
	}
	return nil
}

// jobAtRow resolves the owning job for an arbitrary display index (used by
// dialogs that captured a display index when they opened).
func (m *Model) jobAtRow(i int) *orchestration.Job {
	if row := m.rowAt(i); row != nil {
		return row.Job
	}
	return nil
}

// jobIndexInJobs returns the index of a job (by ID) within m.Jobs, or -1.
// Used by the EditDeps modal, which navigates m.Jobs directly.
func (m *Model) jobIndexInJobs(jobID string) int {
	for i, job := range m.Jobs {
		if job.ID == jobID {
			return i
		}
	}
	return -1
}

// enterJobIndexCursor converts the display-index cursor to the owning job's
// index within m.Jobs, for full-screen modals (EditDeps) that navigate
// m.Jobs directly. Returns that job index.
func (m *Model) enterJobIndexCursor() int {
	idx := 0
	if job := m.CurrentJob(); job != nil {
		if i := m.jobIndexInJobs(job.ID); i >= 0 {
			idx = i
		}
	}
	m.Cursor = idx
	return idx
}

// exitJobIndexCursor converts a job-index cursor (set while a m.Jobs-backed
// modal was open) back to the display index of that job's row.
func (m *Model) exitJobIndexCursor() {
	if m.Cursor >= 0 && m.Cursor < len(m.Jobs) {
		if idx := m.rowIndexByNodeID(jobNodeID(m.Jobs[m.Cursor].ID)); idx >= 0 {
			m.Cursor = idx
		}
	}
	m.clampCursor()
}

// appendWorkflowRows appends the job's virtual workflow child rows (runs,
// phases, agents) to rows. With no workflow state for the job this is a
// no-op; the full tree builder lands with the workflow registry.
func (m *Model) appendWorkflowRows(rows []DisplayRow, job *orchestration.Job) []DisplayRow {
	return rows
}

// jobHasWorkflowTree reports whether the job has workflow activity that can
// render as a foldable sub-tree.
func (m *Model) jobHasWorkflowTree(job *orchestration.Job) bool {
	return false
}

// isNodeCollapsed reports whether a foldable row is currently collapsed:
// the user's explicit FoldState override when present, else the default
// policy.
func (m *Model) isNodeCollapsed(row *DisplayRow) bool {
	if v, ok := m.FoldState[row.NodeID]; ok {
		return v
	}
	return m.defaultCollapsed(row)
}

// defaultCollapsed is the fold default for nodes without an explicit
// override. The workflow tree builder refines this (running jobs
// auto-expand); with no workflow trees everything defaults collapsed.
func (m *Model) defaultCollapsed(row *DisplayRow) bool {
	return true
}

// toggleFoldAtCursor toggles the fold state of the row under the cursor.
// It returns true when the key was consumed: fold toggled, or the cursor
// is on a non-foldable virtual row (Enter must never edit/execute from a
// virtual row). Returns false on a job row without workflow children so
// the caller falls through to the default Enter action (edit).
func (m *Model) toggleFoldAtCursor() bool {
	row := m.currentRow()
	if row == nil {
		return false
	}
	switch row.Type {
	case RowTypeJob:
		if !m.jobHasWorkflowTree(row.Job) {
			return false
		}
		m.FoldState[row.NodeID] = !m.isNodeCollapsed(row)
	case RowTypeRun, RowTypePhase:
		m.FoldState[row.NodeID] = !m.isNodeCollapsed(row)
	case RowTypeAgent, RowTypeMore:
		// Not foldable; consume the key so Enter on a virtual row never
		// falls through to job-file editing.
		return true
	default:
		return false
	}
	m.rebuildDisplayRows()
	m.adjustScrollOffset()
	return true
}

// renderVirtualRowCell renders the JOB-column cell of a virtual workflow
// row (tree connectors + icon + label).
func (m *Model) renderVirtualRowCell(row *DisplayRow) string {
	return ""
}

// jobWorkflowBadge renders the "⚙ completed/started" badge appended to a
// job row's JOB cell, or "" when the job has no workflow activity.
func (m *Model) jobWorkflowBadge(job *orchestration.Job) string {
	return ""
}

// jobBadgeWidth returns the rendered width of the job's workflow badge
// ("⚙ completed/started"), 0 when the job has no workflow activity. Used by
// the width calculators so the vertical split reserves badge space.
func (m *Model) jobBadgeWidth(job *orchestration.Job) int {
	return 0
}

// virtualRowCellWidth returns the JOB-column cell width of a virtual row
// (tree connectors + icon + label).
func (m *Model) virtualRowCellWidth(row *DisplayRow) int {
	return 0
}

// getVisibleRows returns the slice of DisplayRows inside the viewport.
func (m *Model) getVisibleRows() []DisplayRow {
	visibleCount := m.getVisibleJobCount()
	start := m.ScrollOffset
	end := start + visibleCount
	if end > len(m.DisplayRows) {
		end = len(m.DisplayRows)
	}
	if start >= end {
		return nil
	}
	return m.DisplayRows[start:end]
}
