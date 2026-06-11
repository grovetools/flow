package status

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/workflowmon"
)

// Archived-run fallback: completed jobs whose live Claude session dirs are
// gone still show their workflow tree, reconstructed once from the durable
// artifacts that ArchiveWorkflowRuns copied to
// plans/<plan>/.artifacts/<job-id>/workflows/<runId>/ (journal.jsonl +
// script.js). The load runs in a tea.Cmd (never inline in Update) and is
// attempted at most once per job per TUI session (workflowArchiveChecked).

// workflowArchivedRunsMsg delivers the workflow state reconstructed from a
// completed job's archived run artifacts. State is nil when the job has no
// archived runs.
type workflowArchivedRunsMsg struct {
	JobID string
	State *workflowPaneState
}

// syncArchivedWorkflowLoads returns load commands for completed jobs whose
// workflow state is still empty. Jobs are marked checked immediately so the
// 2s refresh never re-fires a load.
func (m *Model) syncArchivedWorkflowLoads() []tea.Cmd {
	if m.workflowArchiveChecked == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, job := range m.Jobs {
		if job.Status != orchestration.JobStatusCompleted {
			continue
		}
		if m.workflowArchiveChecked[job.ID] {
			continue
		}
		m.workflowArchiveChecked[job.ID] = true
		if st := m.WorkflowStates[job.ID]; st != nil && len(st.RunOrder) > 0 {
			continue // live state already present
		}
		cmds = append(cmds, loadArchivedWorkflowRunsCmd(m.PlanDir, job.ID))
	}
	return cmds
}

// loadArchivedWorkflowRunsCmd reconstructs a job's workflow state from its
// archived run directories.
func loadArchivedWorkflowRunsCmd(planDir, jobID string) tea.Cmd {
	return func() tea.Msg {
		dir := filepath.Join(planDir, ".artifacts", jobID, "workflows")
		return workflowArchivedRunsMsg{JobID: jobID, State: loadArchivedWorkflowState(dir)}
	}
}

// loadArchivedWorkflowState folds each archived wf_* run dir's script meta
// and journal into a fresh pane state via the same pure applyWorkflowEvent
// fold the live sources use. Returns nil when no archived runs exist.
func loadArchivedWorkflowState(dir string) *workflowPaneState {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var st *workflowPaneState
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "wf_") {
			continue
		}
		if st == nil {
			st = newWorkflowPaneState()
		}
		runID := entry.Name()
		runDir := filepath.Join(dir, runID)

		var meta *workflowmon.ScriptMeta
		if src, err := os.ReadFile(filepath.Join(runDir, "script.js")); err == nil {
			meta = workflowmon.ParseScriptMeta(src)
		}
		applyWorkflowEvent(st, workflowmon.RunDiscovered{RunID: runID, Meta: meta})
		applyArchivedJournal(st, runID, filepath.Join(runDir, "journal.jsonl"))

		// Agents without results in an archived run are abandoned, never
		// in-flight — mark the run stale so it reads honestly (and folds
		// collapsed by default).
		if started, completed := st.Runs[runID].counts(); started != completed {
			applyWorkflowEvent(st, workflowmon.RunStale{RunID: runID})
		}
	}
	if st == nil || len(st.RunOrder) == 0 {
		return nil
	}
	return st
}

// applyArchivedJournal replays one archived journal.jsonl into the state.
// The journal format is undocumented Claude Code internals — unparseable
// lines are skipped, mirroring the live FileSource.
func applyArchivedJournal(st *workflowPaneState, runID, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Result events embed full agent results on a single line and can be
	// far larger than bufio.Scanner's 64KB default token size.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev struct {
			Type    string          `json:"type"`
			AgentID string          `json:"agentId"`
			Result  json.RawMessage `json:"result,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "started":
			applyWorkflowEvent(st, workflowmon.AgentStarted{RunID: runID, AgentID: ev.AgentID})
		case "result":
			applyWorkflowEvent(st, workflowmon.AgentCompleted{RunID: runID, AgentID: ev.AgentID, Result: archivedResultText(ev.Result)})
		}
	}
}

// archivedResultText renders a journal result payload: bare JSON strings as
// plain text, anything else verbatim.
func archivedResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}
