package status

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/grovetools/core/tui/components/logviewer"
	"github.com/grovetools/core/tui/keymap"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/workflowmon"
)

// newDisplayTestModel builds a minimal Model around real jobs, bypassing
// New() (which loads config, plans, and orchestrator state).
func newDisplayTestModel(jobs ...*orchestration.Job) *Model {
	indents := make(map[string]int)
	for _, j := range jobs {
		indents[j.ID] = 0
	}
	m := &Model{
		Jobs:              jobs,
		JobIndents:        indents,
		JobParents:        make(map[string]*orchestration.Job),
		OwnershipChildren: make(map[string][]*orchestration.Job),
		Selected:          make(map[string]bool),
		FoldState:         make(map[string]bool),
		WorkflowStates:    make(map[string]*workflowPaneState),
	}
	m.DisplayRows = m.buildDisplayRows()
	return m
}

func testJob(id string) *orchestration.Job {
	return &orchestration.Job{ID: id, Filename: id + ".md", Title: id}
}

// addTestRun folds a run with n agents (the first `completed` of them
// finished) into the model's workflow state for the given job.
func addTestRun(m *Model, jobID, runID string, agents, completed int) {
	st, ok := m.WorkflowStates[jobID]
	if !ok {
		st = newWorkflowPaneState()
		m.WorkflowStates[jobID] = st
	}
	applyWorkflowEvent(st, workflowmon.RunDiscovered{RunID: runID})
	for i := 0; i < agents; i++ {
		id := fmt.Sprintf("ag%d", i)
		applyWorkflowEvent(st, workflowmon.AgentStarted{RunID: runID, AgentID: id})
		if i < completed {
			applyWorkflowEvent(st, workflowmon.AgentCompleted{RunID: runID, AgentID: id})
		}
	}
}

func countRowType(m *Model, t RowType) int {
	n := 0
	for i := range m.DisplayRows {
		if m.DisplayRows[i].Type == t {
			n++
		}
	}
	return n
}

func TestBuildDisplayRows_JobsOnly(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"), testJob("c"))

	if len(m.DisplayRows) != 3 {
		t.Fatalf("rows = %d, want 3", len(m.DisplayRows))
	}
	for i, row := range m.DisplayRows {
		if row.Type != RowTypeJob {
			t.Errorf("row %d type = %v, want RowTypeJob", i, row.Type)
		}
		if row.Job != m.Jobs[i] {
			t.Errorf("row %d job mismatch", i)
		}
		if row.NodeID != jobNodeID(m.Jobs[i].ID) {
			t.Errorf("row %d NodeID = %q", i, row.NodeID)
		}
	}
}

func TestCurrentJob_ResolvesThroughRow(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"))
	m.Cursor = 1
	if got := m.CurrentJob(); got == nil || got.ID != "b" {
		t.Errorf("CurrentJob = %+v, want b", got)
	}
	m.Cursor = 99
	if got := m.CurrentJob(); got != nil {
		t.Errorf("CurrentJob out of bounds = %+v, want nil", got)
	}
}

func TestRebuildDisplayRows_CursorStableAcrossReload(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"), testJob("c"))
	m.Cursor = 1 // on job b

	// Simulate a RefreshMsg reflatten: fresh job pointers, one job removed,
	// order changed.
	m.Jobs = []*orchestration.Job{testJob("c"), testJob("b")}
	m.JobIndents = map[string]int{"c": 0, "b": 0}
	m.rebuildDisplayRows()

	if got := m.CurrentJob(); got == nil || got.ID != "b" {
		t.Errorf("cursor should follow job b across rebuild, got %+v", got)
	}

	// Removing the cursor job clamps within bounds.
	m.Jobs = []*orchestration.Job{testJob("c")}
	m.JobIndents = map[string]int{"c": 0}
	m.rebuildDisplayRows()
	if m.Cursor < 0 || m.Cursor >= len(m.DisplayRows) {
		t.Errorf("cursor out of bounds after job removal: %d", m.Cursor)
	}
}

func TestFlattenJobTree_OwnershipWinsPresentationOnly(t *testing.T) {
	parent := testJob("parent")
	dependency := testJob("dependency")
	child := testJob("child")
	child.ParentJobID = parent.ID
	child.Dependencies = []*orchestration.Job{dependency}
	plan := &orchestration.Plan{Jobs: []*orchestration.Job{parent, dependency, child}}

	jobs, parents, indents := flattenJobTreeWithParents(plan)
	if got := []string{jobs[0].ID, jobs[1].ID, jobs[2].ID}; fmt.Sprint(got) != "[parent child dependency]" {
		t.Fatalf("display order = %v, want [parent child dependency]", got)
	}
	if parents[child.ID] != parent || indents[child.ID] != 1 {
		t.Fatalf("child display parent/depth = %v/%d, want parent/1", parents[child.ID], indents[child.ID])
	}
	if len(child.Dependencies) != 1 || child.Dependencies[0] != dependency {
		t.Fatal("presentation flattening changed scheduling dependencies")
	}
}

func TestFlattenJobTree_RecursiveOwnershipAndMalformedFallback(t *testing.T) {
	parent := testJob("parent")
	child := testJob("child")
	grandchild := testJob("grandchild")
	orphan := testJob("orphan")
	cycleA := testJob("cycle-a")
	cycleB := testJob("cycle-b")
	child.ParentJobID = parent.ID
	grandchild.ParentJobID = child.ID
	orphan.ParentJobID = "missing"
	cycleA.ParentJobID = cycleB.ID
	cycleB.ParentJobID = cycleA.ID
	plan := &orchestration.Plan{Jobs: []*orchestration.Job{parent, orphan, cycleA, cycleB, child, grandchild}}

	jobs, parents, indents := flattenJobTreeWithParents(plan)
	if len(jobs) != len(plan.Jobs) {
		t.Fatalf("flattened jobs = %d, want %d", len(jobs), len(plan.Jobs))
	}
	if parents[child.ID] != parent || parents[grandchild.ID] != child || indents[grandchild.ID] != 2 {
		t.Fatalf("recursive ownership not preserved: parents=%v indents=%v", parents, indents)
	}
	for _, id := range []string{orphan.ID, cycleA.ID, cycleB.ID} {
		if parents[id] != nil || indents[id] != 0 {
			t.Errorf("malformed lineage %s should fall back to root, parent=%v depth=%d", id, parents[id], indents[id])
		}
	}
}

func newOwnershipDisplayTestModel(jobs ...*orchestration.Job) *Model {
	plan := &orchestration.Plan{Jobs: jobs}
	flat, parents, indents := flattenJobTreeWithParents(plan)
	m := newDisplayTestModel(flat...)
	m.JobParents = parents
	m.JobIndents = indents
	m.OwnershipChildren = indexOwnershipChildren(flat, parents)
	m.DisplayRows = m.buildDisplayRows()
	return m
}

func TestSubjobRows_AreSelectableNestedJobsWithSummary(t *testing.T) {
	parent := testJob("parent")
	parent.Status = orchestration.JobStatusCompleted
	done := testJob("done")
	done.ParentJobID = parent.ID
	done.Status = orchestration.JobStatusCompleted
	running := testJob("running")
	running.ParentJobID = parent.ID
	running.Status = orchestration.JobStatusRunning
	pending := testJob("pending")
	pending.ParentJobID = parent.ID
	pending.Status = orchestration.JobStatusPending
	m := newOwnershipDisplayTestModel(parent, done, running, pending)

	// A live descendant automatically exposes the family even though the
	// owner itself is terminal.
	if len(m.DisplayRows) != 4 {
		t.Fatalf("display rows = %d, want parent plus three children", len(m.DisplayRows))
	}
	for i := 1; i < 4; i++ {
		if m.DisplayRows[i].Type != RowTypeJob || m.JobIndents[m.DisplayRows[i].Job.ID] != 1 {
			t.Errorf("row %d is not a nested real job: %+v", i, m.DisplayRows[i])
		}
	}
	m.Cursor = 2
	if got := m.CurrentJob(); got != running {
		t.Fatalf("CurrentJob on child = %v, want running child", got)
	}
	badge := ansi.Strip(m.jobSubjobBadge(parent))
	if badge != "subjobs 1/3 · 1 running" {
		t.Errorf("subjob badge = %q", badge)
	}
}

func TestSubjobRows_FoldAndCursorFallback(t *testing.T) {
	parent := testJob("parent")
	parent.Status = orchestration.JobStatusCompleted
	child := testJob("child")
	child.ParentJobID = parent.ID
	child.Status = orchestration.JobStatusCompleted
	other := testJob("other")
	m := newOwnershipDisplayTestModel(parent, child, other)

	if len(m.DisplayRows) != 2 {
		t.Fatalf("terminal family should default collapsed beside other root, rows=%d", len(m.DisplayRows))
	}
	m.Cursor = 0
	if !m.toggleFoldAtCursor() || len(m.DisplayRows) != 3 {
		t.Fatalf("expanding owner should reveal selectable child, rows=%d", len(m.DisplayRows))
	}
	m.Cursor = m.rowIndexByNodeID(jobNodeID(child.ID))
	m.FoldState[jobNodeID(parent.ID)] = true
	m.rebuildDisplayRows()
	if row := m.currentRow(); row == nil || row.NodeID != jobNodeID(parent.ID) {
		t.Fatalf("folded child cursor should fall back to owner, got %+v", row)
	}
}

func TestSubjobRows_PrecedeLegacyWorkflowRows(t *testing.T) {
	parent := testJob("parent")
	parent.Status = orchestration.JobStatusRunning
	child := testJob("child")
	child.ParentJobID = parent.ID
	m := newOwnershipDisplayTestModel(parent, child)
	addTestRun(m, parent.ID, "wf", 1, 0)
	m.rebuildDisplayRows()

	if len(m.DisplayRows) < 3 || m.DisplayRows[1].Type != RowTypeJob || m.DisplayRows[1].Job != child || m.DisplayRows[2].Type != RowTypeRun {
		t.Fatalf("rows should order real child before legacy workflow activity: %+v", m.DisplayRows)
	}
}

func TestToggleFold_NoOpOnPlainJobRow(t *testing.T) {
	m := newDisplayTestModel(testJob("a"))
	m.Cursor = 0
	// A job row without workflow children is not foldable — Enter must
	// fall through to the default action (edit).
	if m.toggleFoldAtCursor() {
		t.Error("toggleFoldAtCursor should return false on a job row without workflow children")
	}
	if len(m.FoldState) != 0 {
		t.Errorf("FoldState should be untouched, got %v", m.FoldState)
	}
}

func TestVirtualRowsNeverInJobs(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"))
	// Invariant: building display rows never mutates m.Jobs.
	if len(m.Jobs) != 2 {
		t.Fatalf("m.Jobs mutated: %d", len(m.Jobs))
	}
	for _, j := range m.Jobs {
		if j == nil {
			t.Fatal("nil job in m.Jobs")
		}
	}
}

// ── Stage 3: virtual workflow rows ──────────────────────────────────────

func TestFoldPolicy_RunningJobAutoExpands(t *testing.T) {
	running := testJob("run-job")
	running.Status = orchestration.JobStatusRunning
	done := testJob("done-job")
	done.Status = orchestration.JobStatusCompleted

	m := newDisplayTestModel(running, done)
	addTestRun(m, "run-job", "wf_live", 2, 1)
	addTestRun(m, "done-job", "wf_old", 2, 2)
	m.rebuildDisplayRows()

	// The running job auto-expands: run row + 2 agent rows appear.
	if got := countRowType(m, RowTypeRun); got != 1 {
		t.Errorf("run rows = %d, want 1 (only the running job's)", got)
	}
	if got := countRowType(m, RowTypeAgent); got != 2 {
		t.Errorf("agent rows = %d, want 2", got)
	}
	// The completed job stays collapsed: no virtual rows under it.
	for i := range m.DisplayRows {
		row := &m.DisplayRows[i]
		if row.Type != RowTypeJob && row.Job.ID == "done-job" {
			t.Errorf("completed job rendered virtual row %q", row.NodeID)
		}
	}
}

func TestFoldPolicy_FullyCompletedRunCollapses(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(running)
	addTestRun(m, "j", "wf_done", 3, 3) // fully completed
	addTestRun(m, "j", "wf_live", 2, 0) // in flight
	m.rebuildDisplayRows()

	// Both run rows render, but only the live run expands its agents.
	if got := countRowType(m, RowTypeRun); got != 2 {
		t.Fatalf("run rows = %d, want 2", got)
	}
	if got := countRowType(m, RowTypeAgent); got != 2 {
		t.Errorf("agent rows = %d, want 2 (only the live run's)", got)
	}
}

func TestFoldPolicy_StaleRunCollapses(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(running)
	addTestRun(m, "j", "wf_stale", 2, 0)
	applyWorkflowEvent(m.WorkflowStates["j"], workflowmon.RunStale{RunID: "wf_stale"})
	m.rebuildDisplayRows()

	if got := countRowType(m, RowTypeAgent); got != 0 {
		t.Errorf("stale run should be collapsed, got %d agent rows", got)
	}
}

func TestToggleFold_ExpandsCollapsedJobTree(t *testing.T) {
	done := testJob("j")
	done.Status = orchestration.JobStatusCompleted
	m := newDisplayTestModel(done)
	addTestRun(m, "j", "wf_1", 2, 2)
	m.rebuildDisplayRows()

	if len(m.DisplayRows) != 1 {
		t.Fatalf("completed job should start collapsed, rows = %d", len(m.DisplayRows))
	}
	m.Cursor = 0
	if !m.toggleFoldAtCursor() {
		t.Fatal("Enter on a job row with workflow children must toggle the fold")
	}
	if got := countRowType(m, RowTypeRun); got != 1 {
		t.Errorf("run rows after expand = %d, want 1", got)
	}
	// Toggle back: collapsed again.
	m.Cursor = 0
	if !m.toggleFoldAtCursor() {
		t.Fatal("second Enter must re-collapse")
	}
	if len(m.DisplayRows) != 1 {
		t.Errorf("rows after re-collapse = %d, want 1", len(m.DisplayRows))
	}
}

func TestAgentCap_MoreRowAndExpansion(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(running)
	addTestRun(m, "j", "wf_big", maxWorkflowAgentRows+3, 0)
	m.rebuildDisplayRows()

	if got := countRowType(m, RowTypeAgent); got != maxWorkflowAgentRows {
		t.Errorf("agent rows = %d, want cap %d", got, maxWorkflowAgentRows)
	}
	if got := countRowType(m, RowTypeMore); got != 1 {
		t.Fatalf("more rows = %d, want 1", got)
	}
	var moreIdx int
	for i := range m.DisplayRows {
		if m.DisplayRows[i].Type == RowTypeMore {
			moreIdx = i
			if m.DisplayRows[i].MoreCnt != 3 {
				t.Errorf("MoreCnt = %d, want 3", m.DisplayRows[i].MoreCnt)
			}
		}
	}

	// Enter on the more row reveals the full agent list.
	m.Cursor = moreIdx
	if !m.toggleFoldAtCursor() {
		t.Fatal("Enter on the more row must be consumed")
	}
	if got := countRowType(m, RowTypeAgent); got != maxWorkflowAgentRows+3 {
		t.Errorf("agent rows after expand = %d, want %d", got, maxWorkflowAgentRows+3)
	}
	if got := countRowType(m, RowTypeMore); got != 0 {
		t.Errorf("more row should disappear after expand, got %d", got)
	}
}

func TestCursorStability_AgentRowAcrossRefreshAndFold(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	other := testJob("k")
	m := newDisplayTestModel(running, other)
	addTestRun(m, "j", "wf_1", 3, 0)
	m.rebuildDisplayRows()

	// Park the cursor on agent ag1.
	target := agentNodeID("j", "wf_1", "ag1")
	idx := m.rowIndexByNodeID(target)
	if idx < 0 {
		t.Fatalf("agent row %q not found in %d rows", target, len(m.DisplayRows))
	}
	m.Cursor = idx

	// Simulate the RefreshMsg reflatten: fresh job pointers, new order.
	fresh := testJob("j")
	fresh.Status = orchestration.JobStatusRunning
	m.Jobs = []*orchestration.Job{testJob("k"), fresh}
	m.JobIndents = map[string]int{"k": 0, "j": 0}
	m.rebuildDisplayRows()

	if row := m.currentRow(); row == nil || row.NodeID != target {
		t.Errorf("cursor should follow agent NodeID across rebuild, got %+v", row)
	}

	// Folding the owning job removes the agent row: the cursor falls back
	// to the parent job row.
	m.FoldState[jobNodeID("j")] = true
	m.rebuildDisplayRows()
	if row := m.currentRow(); row == nil || row.NodeID != jobNodeID("j") {
		t.Errorf("cursor should fall back to the owning job row, got %+v", row)
	}
}

func TestVirtualRows_NeverEnterJobsAndNotSelectable(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(running)
	addTestRun(m, "j", "wf_1", 2, 0)
	m.rebuildDisplayRows()

	// Building virtual rows never mutates m.Jobs.
	if len(m.Jobs) != 1 {
		t.Fatalf("m.Jobs mutated: %d entries", len(m.Jobs))
	}

	// Enter on an agent row is consumed without touching FoldState — it
	// must never fall through to job-file editing or execution.
	agentIdx := m.rowIndexByNodeID(agentNodeID("j", "wf_1", "ag0"))
	if agentIdx < 0 {
		t.Fatal("agent row not found")
	}
	m.Cursor = agentIdx
	if !m.toggleFoldAtCursor() {
		t.Error("Enter on an agent row must be consumed")
	}
	if len(m.FoldState) != 0 {
		t.Errorf("agent rows are not foldable, FoldState = %v", m.FoldState)
	}

	// Dispatch the real Select key (space) with the cursor on the agent
	// row: multi-select must remain job-rows-only.
	if row := m.currentRow(); row.Type == RowTypeJob {
		t.Fatal("cursor should be on a virtual row")
	}
	mm := *m
	mm.KeyMap = NewKeyMap(nil)
	mm.WhichKey = keymap.NewWhichKeyHost(nil, mm.KeyMap.Namespaces()...)
	updated, _ := mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	mm = updated.(Model)
	if len(mm.Selected) != 0 {
		t.Errorf("virtual row was selected: %v", mm.Selected)
	}

	// The same key on the job row selects it (the guard only blocks
	// virtual rows).
	mm.Cursor = mm.rowIndexByNodeID(jobNodeID("j"))
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	mm = updated.(Model)
	if !mm.Selected["j"] {
		t.Error("job row should be selectable")
	}
}

func TestBadge_CountsAndWidthAccounting(t *testing.T) {
	withRuns := testJob("j")
	bare := testJob("k")
	m := newDisplayTestModel(withRuns, bare)
	addTestRun(m, "j", "wf_1", 13, 9)
	m.rebuildDisplayRows()

	badge := m.jobWorkflowBadge(withRuns)
	if badge == "" {
		t.Fatal("job with runs must render a badge")
	}
	if !strings.Contains(stripANSI(badge), "9/13") {
		t.Errorf("badge = %q, want completed/started 9/13", badge)
	}
	if m.jobWorkflowBadge(bare) != "" {
		t.Error("job without runs must not render a badge")
	}

	// Width accounting: the badge widens the JOB column estimate.
	if w := m.jobBadgeWidth(withRuns); w <= 0 {
		t.Errorf("jobBadgeWidth = %d, want > 0", w)
	}
	if m.jobBadgeWidth(bare) != 0 {
		t.Error("jobBadgeWidth must be 0 without runs")
	}
	m.availableColumns = []string{"JOB"}
	m.columnVisibility = map[string]bool{"JOB": true}
	m.Height = 40 // ensure rows are visible to the estimators
	measure := func() int {
		headers := m.tableHeaders()
		return tableRenderedWidth(headers, m.measureTableColumns(headers))
	}
	withBadge := measure()
	delete(m.WorkflowStates, "j")
	m.rebuildDisplayRows()
	withoutBadge := measure()
	if withBadge <= withoutBadge {
		t.Errorf("measured table width must grow with the badge: with=%d without=%d", withBadge, withoutBadge)
	}
}

func TestVirtualRowCell_TruncatedAndConnected(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(running)
	st := newWorkflowPaneState()
	m.WorkflowStates["j"] = st
	applyWorkflowEvent(st, workflowmon.RunDiscovered{RunID: "wf_long"})
	applyWorkflowEvent(st, workflowmon.AgentStarted{
		RunID:   "wf_long",
		AgentID: "agent-with-a-very-long-identifier",
		Prompt:  strings.Repeat("long prompt text ", 20),
	})
	m.rebuildDisplayRows()

	for i := range m.DisplayRows {
		row := &m.DisplayRows[i]
		if row.Type == RowTypeJob {
			continue
		}
		cell := m.renderVirtualRowCell(i, row)
		if w := lipgloss.Width(cell); w > maxVirtualCellWidth {
			t.Errorf("virtual cell width %d exceeds cap %d: %q", w, maxVirtualCellWidth, cell)
		}
		if strings.Contains(cell, "\n") {
			t.Errorf("virtual cell must never wrap: %q", cell)
		}
		plain := stripANSI(cell)
		if !strings.Contains(plain, "└─ ") && !strings.Contains(plain, "├─ ") {
			t.Errorf("virtual cell missing tree connector: %q", plain)
		}
		if w := m.virtualRowCellWidth(row); w > maxVirtualCellWidth {
			t.Errorf("virtualRowCellWidth %d exceeds cap", w)
		}
	}
}

func TestVirtualRowTypeCell_LabelsKinds(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(running)
	st := newWorkflowPaneState()
	m.WorkflowStates["j"] = st
	// A real workflow run with a script-spawned agent.
	applyWorkflowEvent(st, workflowmon.RunDiscovered{RunID: "wf"})
	applyWorkflowEvent(st, workflowmon.AgentStarted{RunID: "wf", AgentID: "a1"})
	// An ad-hoc Agent/Task-tool subagent (lands in the synthetic ad-hoc run).
	applyWorkflowEvent(st, workflowmon.AgentStarted{RunID: adhocRunID, AgentID: "a2"})
	m.rebuildDisplayRows()

	for i := range m.DisplayRows {
		row := &m.DisplayRows[i]
		cell := stripANSI(m.virtualRowTypeCell(row))
		switch row.Type {
		case RowTypeRun:
			// The ad-hoc bucket is a grouping of subagents, not a workflow.
			want := "workflow"
			if row.Run != nil && row.Run.ID == adhocRunID {
				want = "subagents"
			}
			if !strings.Contains(cell, want) {
				t.Errorf("run row %q: TYPE cell %q missing label %q", row.RunID, cell, want)
			}
		case RowTypeAgent:
			if !strings.Contains(cell, "subagent") {
				t.Errorf("agent row: TYPE cell %q missing label %q", cell, "subagent")
			}
		case RowTypeJob:
			// Job rows render TYPE through the main column path, not this helper.
		}
	}
}

func TestWorkflowEventMsg_CoalescesRebuilds(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := *newDisplayTestModel(running)
	m.workflowDirtyJobs = make(map[string]bool)

	// First event schedules the tick; rows are NOT rebuilt yet.
	updated, cmd := m.Update(workflowEventMsg{
		JobID: "j",
		Event: workflowmon.AgentStarted{RunID: "wf_1", AgentID: "a1"},
	})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("first dirty event must schedule the coalescing tick")
	}
	if countRowType(&m, RowTypeAgent) != 0 {
		t.Error("rows must not rebuild per event")
	}

	// Second event coalesces: no extra tick.
	updated, cmd = m.Update(workflowEventMsg{
		JobID: "j",
		Event: workflowmon.AgentStarted{RunID: "wf_1", AgentID: "a2"},
	})
	m = updated.(Model)
	if cmd != nil {
		t.Error("second event while a tick is pending must not schedule another")
	}

	// The tick performs exactly one rebuild covering both events.
	updated, _ = m.Update(workflowRebuildTickMsg{})
	m = updated.(Model)
	if got := countRowType(&m, RowTypeAgent); got != 2 {
		t.Errorf("agent rows after tick = %d, want 2", got)
	}
	if m.workflowRebuildPending {
		t.Error("pending flag must clear after the tick")
	}
	if len(m.workflowDirtyJobs) != 0 {
		t.Errorf("dirty set must clear after the tick: %v", m.workflowDirtyJobs)
	}
}

// stripANSI removes ANSI escape sequences for plain-text assertions.
func stripANSI(s string) string {
	return ansi.Strip(s)
}

func TestAgentRowDetailRouting(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := *newDisplayTestModel(running)
	addTestRun(&m, "j", "wf_1", 2, 0)
	m.rebuildDisplayRows()
	m.workflowAgentLines = map[string][]string{"ag0": {"line one", "line two"}}

	// No pane open: cursor movement must never open one.
	m.Cursor = m.rowIndexByNodeID(agentNodeID("j", "wf_1", "ag0"))
	mm, _ := m.reloadActiveDetailPane()
	if mm.ActiveDetailPane != NoPane {
		t.Fatal("cursor movement must not open a detail pane")
	}

	// With the logs pane already open, resting on the agent row routes
	// that agent's buffered transcript into the viewport.
	m.LogViewer = logviewer.New(60, 20)
	m.ActiveDetailPane = LogsPaneDetail
	cancelled := false
	m.StreamCancel = func() { cancelled = true }
	m.StreamingJobID = "j"
	mm, _ = m.reloadActiveDetailPane()
	if mm.workflowSelectedAgentID != "ag0" {
		t.Errorf("selected agent = %q, want ag0", mm.workflowSelectedAgentID)
	}
	if !cancelled || mm.StreamingJobID != "" {
		t.Error("the job-log stream must stop while the agent transcript is shown")
	}
	if got := mm.workflowAgentViewerContent("ag0"); !strings.Contains(got, "line one") {
		t.Errorf("viewer content missing buffered transcript: %q", got)
	}

	// Returning to the job row restores normal job-log routing.
	mm.Cursor = mm.rowIndexByNodeID(jobNodeID("j"))
	mm, _ = mm.reloadActiveDetailPane()
	if mm.workflowSelectedAgentID != "" {
		t.Errorf("agent routing must clear on job rows, got %q", mm.workflowSelectedAgentID)
	}
}
