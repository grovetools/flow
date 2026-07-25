package status

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/grovetools/core/tui/theme"

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

// maxWorkflowAgentRows caps how many agent rows render per phase (or per
// flat run scope) before the remainder collapses into a "… +K more" row.
const maxWorkflowAgentRows = 5

// maxVirtualCellWidth caps the rendered JOB-column cell of a virtual row
// (tree connectors + label) so deep trees can never wrap the table.
const maxVirtualCellWidth = 40

// jobNodeID returns the stable NodeID for a job row.
func jobNodeID(jobID string) string { return "job:" + jobID }

func runNodeID(jobID, runID string) string { return "run:" + jobID + "/" + runID }

func phaseNodeID(jobID, runID, title string) string {
	return "phase:" + jobID + "/" + runID + "/" + title
}

func agentNodeID(jobID, runID, agentID string) string {
	return "agent:" + jobID + "/" + runID + "/" + agentID
}

// moreNodeID identifies a "… +K more" row; scope is the phase title, or ""
// for agents sitting directly under the run.
func moreNodeID(jobID, runID, scope string) string {
	return "more:" + jobID + "/" + runID + "/" + scope
}

// buildDisplayRows constructs selectable real-job rows plus virtual workflow
// rows. Ownership children render first beneath their parent, then legacy
// workflow/ad-hoc activity, then dependency-only children. Folding an owner
// hides its ownership subtree while every visible child remains RowTypeJob.
func (m *Model) buildDisplayRows() []DisplayRow {
	rows := make([]DisplayRow, 0, len(m.Jobs))
	children := make(map[string][]*orchestration.Job)
	var roots []*orchestration.Job
	for _, job := range m.Jobs {
		if parent := m.JobParents[job.ID]; parent != nil {
			children[parent.ID] = append(children[parent.ID], job)
		} else {
			roots = append(roots, job)
		}
	}

	visited := make(map[string]bool, len(m.Jobs))
	var appendJob func(*orchestration.Job)
	appendJob = func(job *orchestration.Job) {
		if job == nil || visited[job.ID] {
			return
		}
		visited[job.ID] = true
		row := DisplayRow{Type: RowTypeJob, NodeID: jobNodeID(job.ID), Job: job}
		rows = append(rows, row)

		collapsed := m.jobHasChildren(job) && m.isNodeCollapsed(&row)
		if !collapsed {
			for _, child := range children[job.ID] {
				if m.isOwnershipChild(child) {
					appendJob(child)
				}
			}
		}
		rows = m.appendWorkflowRows(rows, job)
		for _, child := range children[job.ID] {
			if !m.isOwnershipChild(child) {
				appendJob(child)
			}
		}
	}
	for _, root := range roots {
		appendJob(root)
	}
	return rows
}

func (m *Model) isOwnershipChild(job *orchestration.Job) bool {
	if job == nil || job.ParentJobID == "" {
		return false
	}
	parent := m.JobParents[job.ID]
	return parent != nil && parent.ID == job.ParentJobID
}

func (m *Model) ownershipChildren(job *orchestration.Job) []*orchestration.Job {
	if job == nil {
		return nil
	}
	return m.OwnershipChildren[job.ID]
}

// rebuildDisplayRows rebuilds DisplayRows and restores the cursor to the
// row it was on, by stable NodeID. If that node disappeared (job removed,
// parent folded), the cursor falls back to the owning job's row, then
// clamps.
func (m *Model) rebuildDisplayRows() {
	var prevNodeID string
	var fallbackJobIDs []string
	if row := m.currentRow(); row != nil {
		prevNodeID = row.NodeID
		if row.Job != nil {
			// If folding hides a real child, walk toward the nearest visible
			// presentation owner instead of merely clamping onto an unrelated row.
			seen := make(map[string]bool)
			for job := row.Job; job != nil && !seen[job.ID]; job = m.JobParents[job.ID] {
				seen[job.ID] = true
				fallbackJobIDs = append(fallbackJobIDs, job.ID)
			}
		}
	}

	m.DisplayRows = m.buildDisplayRows()

	if prevNodeID != "" {
		if idx := m.rowIndexByNodeID(prevNodeID); idx >= 0 {
			m.Cursor = idx
		} else {
			for _, jobID := range fallbackJobIDs {
				if idx := m.rowIndexByNodeID(jobNodeID(jobID)); idx >= 0 {
					m.Cursor = idx
					break
				}
			}
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
// phases, agents) to rows, honoring the fold state at every level. With no
// workflow state for the job this is a no-op.
func (m *Model) appendWorkflowRows(rows []DisplayRow, job *orchestration.Job) []DisplayRow {
	st := m.WorkflowStates[job.ID]
	if st == nil || len(st.RunOrder) == 0 {
		return rows
	}
	jobRow := DisplayRow{Type: RowTypeJob, NodeID: jobNodeID(job.ID), Job: job}
	if m.isNodeCollapsed(&jobRow) {
		return rows
	}
	for _, runID := range st.RunOrder {
		run := st.Runs[runID]
		runRow := DisplayRow{
			Type:   RowTypeRun,
			NodeID: runNodeID(job.ID, runID),
			Job:    job,
			Depth:  1,
			RunID:  runID,
			Run:    run,
		}
		rows = append(rows, runRow)
		if m.isNodeCollapsed(&runRow) {
			continue
		}
		rows = m.appendRunChildRows(rows, job, run)
	}
	return rows
}

// appendRunChildRows appends one run's phase/agent rows, reusing the pure
// phase-grouping resolver from the retired W pane. Agent rows are capped per
// phase scope (see appendAgentRows).
func (m *Model) appendRunChildRows(rows []DisplayRow, job *orchestration.Job, run *workflowRunState) []DisplayRow {
	nodes := resolveRunChildNodes(run)
	i := 0
	for i < len(nodes) {
		node := nodes[i]
		if node.Type == "phase" {
			phaseRow := DisplayRow{
				Type:   RowTypePhase,
				NodeID: phaseNodeID(job.ID, run.ID, node.Name),
				Job:    job,
				Depth:  1 + node.Depth,
				RunID:  run.ID,
				Run:    run,
				Phase:  node.Name,
			}
			rows = append(rows, phaseRow)
			// Collect the phase's member agents (deeper than the phase).
			j := i + 1
			var members []*WorkflowPaneNode
			for j < len(nodes) && nodes[j].Type == "agent" && nodes[j].Depth > node.Depth {
				members = append(members, nodes[j])
				j++
			}
			if !m.isNodeCollapsed(&phaseRow) {
				rows = m.appendAgentRows(rows, job, run, node.Name, members)
			}
			i = j
			continue
		}
		// Agents directly under the run (flat runs, or agents without
		// phase attribution): gather the contiguous span for capping.
		j := i
		var span []*WorkflowPaneNode
		for j < len(nodes) && nodes[j].Type == "agent" && nodes[j].Depth == node.Depth {
			span = append(span, nodes[j])
			j++
		}
		rows = m.appendAgentRows(rows, job, run, "", span)
		i = j
	}
	return rows
}

// appendAgentRows appends agent rows for one scope (a phase, or the run
// itself), capping at maxWorkflowAgentRows with a trailing RowTypeMore.
// Toggling the more row (FoldState false) reveals the full list.
func (m *Model) appendAgentRows(rows []DisplayRow, job *orchestration.Job, run *workflowRunState, phase string, nodes []*WorkflowPaneNode) []DisplayRow {
	if len(nodes) == 0 {
		return rows
	}
	moreID := moreNodeID(job.ID, run.ID, phase)
	capped := len(nodes) > maxWorkflowAgentRows
	if capped {
		// FoldState false = the user expanded the more row (show all).
		if collapsed, ok := m.FoldState[moreID]; ok && !collapsed {
			capped = false
		}
	}
	limit := len(nodes)
	if capped {
		limit = maxWorkflowAgentRows
	}
	for _, node := range nodes[:limit] {
		rows = append(rows, DisplayRow{
			Type:   RowTypeAgent,
			NodeID: agentNodeID(job.ID, run.ID, node.AgentID),
			Job:    job,
			Depth:  1 + node.Depth,
			RunID:  run.ID,
			Run:    run,
			Phase:  phase,
			Agent:  run.Agents[node.AgentID],
		})
	}
	if capped {
		rows = append(rows, DisplayRow{
			Type:    RowTypeMore,
			NodeID:  moreID,
			Job:     job,
			Depth:   1 + nodes[0].Depth,
			RunID:   run.ID,
			Run:     run,
			Phase:   phase,
			MoreCnt: len(nodes) - maxWorkflowAgentRows,
		})
	}
	return rows
}

// jobHasWorkflowTree reports whether the job has workflow activity that can
// render as a foldable sub-tree.
func (m *Model) jobHasWorkflowTree(job *orchestration.Job) bool {
	if job == nil {
		return false
	}
	st := m.WorkflowStates[job.ID]
	return st != nil && len(st.RunOrder) > 0
}

func (m *Model) jobHasChildren(job *orchestration.Job) bool {
	return m.jobHasWorkflowTree(job) || len(m.ownershipChildren(job)) > 0
}

func subjobNeedsAttention(job *orchestration.Job) bool {
	if job == nil {
		return false
	}
	switch job.Status {
	case orchestration.JobStatusRunning, orchestration.JobStatusFailed,
		orchestration.JobStatusNeedsReview, orchestration.JobStatusPendingUser:
		return true
	default:
		return false
	}
}

func (m *Model) hasAttentionOwnershipDescendant(job *orchestration.Job) bool {
	for _, child := range m.ownershipChildren(job) {
		if subjobNeedsAttention(child) || m.hasAttentionOwnershipDescendant(child) {
			return true
		}
	}
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
// override: everything collapsed, except jobs that are currently running
// (auto-expand so live activity is visible) and phases (visible whenever
// their run is expanded). Runs collapse when stale or fully completed.
func (m *Model) defaultCollapsed(row *DisplayRow) bool {
	switch row.Type {
	case RowTypeJob:
		if row.Job == nil {
			return true
		}
		return row.Job.Status != orchestration.JobStatusRunning && !m.hasAttentionOwnershipDescendant(row.Job)
	case RowTypeRun:
		if row.Run == nil || row.Run.Stale {
			return true
		}
		started, completed := row.Run.counts()
		return started > 0 && started == completed
	case RowTypePhase:
		return false
	}
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
		if !m.jobHasChildren(row.Job) {
			return false
		}
		m.FoldState[row.NodeID] = !m.isNodeCollapsed(row)
	case RowTypeRun, RowTypePhase, RowTypeMore:
		// For RowTypeMore, "collapsed" means capped at maxWorkflowAgentRows;
		// toggling reveals (or re-caps) the scope's full agent list.
		m.FoldState[row.NodeID] = !m.isNodeCollapsed(row)
	case RowTypeAgent:
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
// row (tree connectors + icon + label). globalIndex is the row's index in
// DisplayRows (needed for the last-sibling connector scan). Every label is
// ANSI-truncated so deep trees can never wrap the table.
func (m *Model) renderVirtualRowCell(globalIndex int, row *DisplayRow) string {
	cell := m.virtualTreePrefix(globalIndex, row) + m.virtualRowLabel(row, true)
	return ansi.Truncate(cell, maxVirtualCellWidth, "…")
}

// virtualTreePrefix draws the tree connectors for a virtual row at the
// parent job's indent plus the row's virtual depth, mirroring the job-row
// prefix convention ("  " per level above 1, then "├─ "/"└─ ").
func (m *Model) virtualTreePrefix(globalIndex int, row *DisplayRow) string {
	indent := m.JobIndents[row.Job.ID] + row.Depth
	prefix := ""
	if indent > 1 {
		prefix = strings.Repeat("  ", indent-1)
	}
	// Last-sibling scan over this job's remaining virtual rows: a virtual
	// row at the same depth means a following sibling; a shallower virtual
	// row or the next job row ends the scope.
	isLast := true
	for j := globalIndex + 1; j < len(m.DisplayRows); j++ {
		next := &m.DisplayRows[j]
		if next.Type == RowTypeJob || next.Depth < row.Depth {
			break
		}
		if next.Depth == row.Depth {
			isLast = false
			break
		}
	}
	if isLast {
		return prefix + "└─ "
	}
	return prefix + "├─ "
}

// virtualRowLabel renders a virtual row's icon + text. With styled=false it
// returns the plain (ANSI-free) form used for width accounting.
func (m *Model) virtualRowLabel(row *DisplayRow, styled bool) string {
	t := theme.DefaultTheme
	switch row.Type {
	case RowTypeRun:
		if row.Run == nil {
			return ""
		}
		started, completed := row.Run.counts()
		status := row.Run.statusLabel()
		label := fmt.Sprintf("%s %s %d/%d %s", theme.IconGear, row.Run.displayName(), completed, started, status)
		if !styled {
			return label
		}
		if status == "running" {
			return t.Info.Render(label)
		}
		return t.Muted.Render(label)
	case RowTypePhase:
		label := row.Phase
		if !styled {
			return label
		}
		return t.Bold.Render(label)
	case RowTypeAgent:
		if row.Agent == nil {
			return ""
		}
		icon := theme.IconRunning
		if row.Agent.Completed {
			icon = theme.IconSuccess
		}
		label := icon + " " + agentDisplayName(row.Agent)
		if s := promptSummary(row.Agent.Prompt, 24); s != "" {
			label += " " + s
		}
		if !styled {
			return label
		}
		if row.Agent.Completed {
			return t.Muted.Render(label)
		}
		return label
	case RowTypeMore:
		label := fmt.Sprintf("… +%d more", row.MoreCnt)
		if !styled {
			return label
		}
		return t.Muted.Render(label)
	}
	return ""
}

// virtualRowTypeCell renders the TYPE-column cell for a virtual workflow
// row. These nodes are Claude Code subagents / workflow runs, not
// grove-managed jobs, so they get a muted icon + label clarifying what the
// line is. Phases get a bare label; the "… +K more" overflow row gets
// nothing.
func (m *Model) virtualRowTypeCell(row *DisplayRow) string {
	var icon, label string
	switch row.Type {
	case RowTypeRun:
		// The ad-hoc bucket is the synthetic grouping for subagents spawned
		// directly via the Agent/Task tool — NOT a Workflow() script run.
		// Only real runs (a workflow run ID / script meta) are "workflow".
		if row.Run != nil && row.Run.ID == adhocRunID {
			icon, label = theme.IconRobot, "subagents"
		} else {
			icon, label = theme.IconGear, "workflow"
		}
	case RowTypeAgent:
		icon, label = theme.IconRobot, "subagent"
	case RowTypePhase:
		label = "phase"
	default:
		return ""
	}
	text := label
	if icon != "" {
		text = icon + " " + label
	}
	return theme.DefaultTheme.Muted.Render(text)
}

// jobSubjobBadge summarizes direct first-class ownership children.
func (m *Model) jobSubjobBadge(job *orchestration.Job) string {
	children := m.ownershipChildren(job)
	if len(children) == 0 {
		return ""
	}
	terminal, running := 0, 0
	attention := false
	for _, child := range children {
		switch child.Status {
		case orchestration.JobStatusCompleted, orchestration.JobStatusAbandoned:
			terminal++
		case orchestration.JobStatusRunning:
			running++
		}
		attention = attention || subjobNeedsAttention(child) || m.hasAttentionOwnershipDescendant(child)
	}
	label := fmt.Sprintf("subjobs %d/%d", terminal, len(children))
	if running > 0 {
		label += fmt.Sprintf(" · %d running", running)
	}
	if attention && running == 0 {
		label += " · attention"
	}
	if attention {
		return theme.DefaultTheme.Warning.Render(label)
	}
	return theme.DefaultTheme.Muted.Render(label)
}

// jobWorkflowBadge renders the "⚙ completed/started" badge appended to a
// job row's JOB cell, or "" when the job has no workflow runs (live or
// archived).
func (m *Model) jobWorkflowBadge(job *orchestration.Job) string {
	if !m.jobHasWorkflowTree(job) {
		return ""
	}
	st := m.WorkflowStates[job.ID]
	started, completed := 0, 0
	for _, runID := range st.RunOrder {
		s, c := st.Runs[runID].counts()
		started += s
		completed += c
	}
	return theme.DefaultTheme.Muted.Render(fmt.Sprintf("%s %d/%d", theme.IconGear, completed, started))
}

func (m *Model) jobBadges(job *orchestration.Job) string {
	var badges []string
	if badge := m.jobSubjobBadge(job); badge != "" {
		badges = append(badges, badge)
	}
	if badge := m.jobWorkflowBadge(job); badge != "" {
		badges = append(badges, badge)
	}
	return strings.Join(badges, " ")
}

// jobBadgeWidth returns the rendered width of all job badges including the
// separating space. Used by width calculators so badges do not wrap.
func (m *Model) jobBadgeWidth(job *orchestration.Job) int {
	badges := m.jobBadges(job)
	if badges == "" {
		return 0
	}
	return lipgloss.Width(badges) + 1
}

// virtualRowCellWidth returns the JOB-column cell width of a virtual row
// (tree connectors + icon + label), capped at maxVirtualCellWidth to match
// the truncation in renderVirtualRowCell.
func (m *Model) virtualRowCellWidth(row *DisplayRow) int {
	indent := m.JobIndents[row.Job.ID] + row.Depth
	prefixWidth := 3 // "└─ "
	if indent > 1 {
		prefixWidth += (indent - 1) * 2
	}
	w := prefixWidth + lipgloss.Width(m.virtualRowLabel(row, false))
	if w > maxVirtualCellWidth {
		w = maxVirtualCellWidth
	}
	return w
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
