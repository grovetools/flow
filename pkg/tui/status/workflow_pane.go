package status

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/agentlogs/pkg/transcript"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/workflowmon"
)

// workflowAgentLineCap bounds the buffered formatted transcript lines kept
// per agent (long sessions can run to megabytes of transcript).
const workflowAgentLineCap = 2000

// WorkflowPaneNode represents a run, phase, or agent row in the workflow
// pane tree.
type WorkflowPaneNode struct {
	Type    string // "run", "phase", "agent"
	RunID   string
	AgentID string
	Name    string
	Depth   int
}

// workflowAgentState is the accumulated view of one workflow agent, built
// purely from workflowmon events.
type workflowAgentState struct {
	ID        string
	Prompt    string
	Phase     string
	Result    string
	Completed bool
}

// workflowRunState is the accumulated view of one workflow run.
type workflowRunState struct {
	ID         string
	Meta       *workflowmon.ScriptMeta
	Agents     map[string]*workflowAgentState
	AgentOrder []string
	Stale      bool
}

// workflowPaneState is the pane's full model of discovered runs. It is fed
// exclusively by workflowmon events — the pane never reads journal files.
type workflowPaneState struct {
	Runs     map[string]*workflowRunState
	RunOrder []string
	// Resolved Claude session directory (shown for context; also used by
	// the transcript collector, not by the pane itself).
	SessionDir string
	// Err is a discovery failure (e.g. no Claude session for the job).
	Err error
}

func newWorkflowPaneState() *workflowPaneState {
	return &workflowPaneState{Runs: make(map[string]*workflowRunState)}
}

// ensureRun returns the run state for runID, creating it if events arrived
// out of order (e.g. AgentStarted before RunDiscovered).
func (s *workflowPaneState) ensureRun(runID string) *workflowRunState {
	if run, ok := s.Runs[runID]; ok {
		return run
	}
	run := &workflowRunState{ID: runID, Agents: make(map[string]*workflowAgentState)}
	s.Runs[runID] = run
	s.RunOrder = append(s.RunOrder, runID)
	return run
}

// applyWorkflowEvent folds one workflowmon event into the pane state.
func applyWorkflowEvent(s *workflowPaneState, ev workflowmon.Event) {
	switch ev := ev.(type) {
	case workflowmon.RunDiscovered:
		run := s.ensureRun(ev.RunID)
		if ev.Meta != nil {
			run.Meta = ev.Meta
		}
	case workflowmon.AgentStarted:
		run := s.ensureRun(ev.RunID)
		agent, ok := run.Agents[ev.AgentID]
		if !ok {
			agent = &workflowAgentState{ID: ev.AgentID}
			run.Agents[ev.AgentID] = agent
			run.AgentOrder = append(run.AgentOrder, ev.AgentID)
		}
		// Upsert semantics: a re-emitted AgentStarted enriches fields.
		if ev.Prompt != "" {
			agent.Prompt = ev.Prompt
		}
		if ev.Phase != "" {
			agent.Phase = ev.Phase
		}
	case workflowmon.AgentCompleted:
		run := s.ensureRun(ev.RunID)
		agent, ok := run.Agents[ev.AgentID]
		if !ok {
			agent = &workflowAgentState{ID: ev.AgentID}
			run.Agents[ev.AgentID] = agent
			run.AgentOrder = append(run.AgentOrder, ev.AgentID)
		}
		agent.Completed = true
		agent.Result = ev.Result
	case workflowmon.RunStale:
		s.ensureRun(ev.RunID).Stale = true
	}
}

// completedCount returns started/completed agent counts for the run.
func (r *workflowRunState) counts() (started, completed int) {
	started = len(r.AgentOrder)
	for _, id := range r.AgentOrder {
		if r.Agents[id].Completed {
			completed++
		}
	}
	return started, completed
}

// displayName returns the run's human-readable title.
func (r *workflowRunState) displayName() string {
	if r.Meta != nil && r.Meta.Name != "" {
		return r.Meta.Name
	}
	return r.ID
}

// statusLabel returns the run's honest status: "stale" when the source
// signalled abandonment, "idle" when every started agent has a result, and
// "running" otherwise. Started-without-result is in-flight, never
// "interrupted".
func (r *workflowRunState) statusLabel() string {
	if r.Stale {
		return "stale"
	}
	started, completed := r.counts()
	if started > 0 && started == completed {
		return "idle"
	}
	return "running"
}

// resolveWorkflowPaneNodes flattens the run/agent state into an ordered node
// list for cursor navigation. When a run's meta declares phases AND agents
// carry phase attribution (a hooks/daemon-backed source), agents group under
// phase nodes; otherwise agents sit directly under the run (the file-tailing
// source cannot attribute phases).
func resolveWorkflowPaneNodes(s *workflowPaneState) []*WorkflowPaneNode {
	var nodes []*WorkflowPaneNode
	for _, runID := range s.RunOrder {
		run := s.Runs[runID]
		nodes = append(nodes, &WorkflowPaneNode{
			Type:  "run",
			RunID: runID,
			Name:  run.displayName(),
			Depth: 0,
		})

		grouped := false
		if run.Meta != nil && len(run.Meta.Phases) > 0 {
			for _, id := range run.AgentOrder {
				if run.Agents[id].Phase != "" {
					grouped = true
					break
				}
			}
		}

		if grouped {
			attributed := make(map[string]bool)
			for _, phase := range run.Meta.Phases {
				var members []string
				for _, id := range run.AgentOrder {
					if run.Agents[id].Phase == phase.Title {
						members = append(members, id)
						attributed[id] = true
					}
				}
				if len(members) == 0 {
					continue
				}
				nodes = append(nodes, &WorkflowPaneNode{
					Type:  "phase",
					RunID: runID,
					Name:  phase.Title,
					Depth: 1,
				})
				for _, id := range members {
					nodes = append(nodes, agentNode(runID, run.Agents[id], 2))
				}
			}
			for _, id := range run.AgentOrder {
				if !attributed[id] {
					nodes = append(nodes, agentNode(runID, run.Agents[id], 1))
				}
			}
		} else {
			for _, id := range run.AgentOrder {
				nodes = append(nodes, agentNode(runID, run.Agents[id], 1))
			}
		}
	}
	return nodes
}

func agentNode(runID string, agent *workflowAgentState, depth int) *WorkflowPaneNode {
	return &WorkflowPaneNode{
		Type:    "agent",
		RunID:   runID,
		AgentID: agent.ID,
		Name:    agent.ID,
		Depth:   depth,
	}
}

// workflowPaneResult holds the output of renderWorkflowPane.
type workflowPaneResult struct {
	treeContent   string
	detailContent string // detail for the selected run/phase node (agents use the transcript viewer)
	nodes         []*WorkflowPaneNode
}

// renderWorkflowPane builds the workflow pane tree view and the detail
// content for the selected node.
func renderWorkflowPane(s *workflowPaneState, cursor, width int) workflowPaneResult {
	t := theme.DefaultTheme
	empty := workflowPaneResult{}

	if s == nil {
		empty.treeContent = t.Muted.Render("Discovering workflow runs...")
		return empty
	}
	if s.Err != nil {
		empty.treeContent = t.Muted.Render(fmt.Sprintf("Workflow discovery unavailable: %v", s.Err))
		return empty
	}

	nodes := resolveWorkflowPaneNodes(s)

	var tree strings.Builder
	tree.WriteString(t.Info.Bold(true).Render("Workflow Runs"))
	tree.WriteString("\n\n")

	if len(nodes) == 0 {
		tree.WriteString(t.Muted.Render("No workflow runs discovered yet."))
		tree.WriteString("\n")
		if s.SessionDir != "" {
			tree.WriteString(t.Muted.Render("Watching: " + s.SessionDir))
			tree.WriteString("\n")
		}
		return workflowPaneResult{treeContent: tree.String()}
	}

	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(nodes) {
		cursor = len(nodes) - 1
	}

	for i, node := range nodes {
		renderWorkflowNodeLine(&tree, s, node, i == cursor, isLastWorkflowChild(nodes, i), width)
	}

	// Overall scoreboard.
	totalStarted, totalCompleted := 0, 0
	for _, runID := range s.RunOrder {
		started, completed := s.Runs[runID].counts()
		totalStarted += started
		totalCompleted += completed
	}
	tree.WriteString("\n")
	tree.WriteString(t.Muted.Render(fmt.Sprintf("%d run(s) • %d started / %d completed", len(s.RunOrder), totalStarted, totalCompleted)))
	tree.WriteString("\n")

	var detail strings.Builder
	selected := nodes[cursor]
	switch selected.Type {
	case "run":
		renderWorkflowRunDetail(&detail, s.Runs[selected.RunID])
	case "phase":
		if run := s.Runs[selected.RunID]; run != nil && run.Meta != nil {
			for _, phase := range run.Meta.Phases {
				if phase.Title == selected.Name {
					detail.WriteString(t.Bold.Render(phase.Title))
					if phase.Detail != "" {
						detail.WriteString("\n" + t.Muted.Render(phase.Detail))
					}
					detail.WriteString("\n")
				}
			}
		}
	case "agent":
		// Agent detail is the live transcript viewer; render a header line
		// with the prompt here for the non-transcript fallback.
		if run := s.Runs[selected.RunID]; run != nil {
			if agent := run.Agents[selected.AgentID]; agent != nil {
				renderWorkflowAgentHeader(&detail, agent)
			}
		}
	}

	return workflowPaneResult{
		treeContent:   tree.String(),
		detailContent: detail.String(),
		nodes:         nodes,
	}
}

// isLastWorkflowChild reports whether node i is the last node of its depth
// group (for └─ vs ├─ connectors).
func isLastWorkflowChild(nodes []*WorkflowPaneNode, i int) bool {
	node := nodes[i]
	if node.Depth == 0 {
		return false
	}
	if i+1 >= len(nodes) {
		return true
	}
	return nodes[i+1].Depth < node.Depth
}

// renderWorkflowNodeLine renders one tree row with cursor highlight.
func renderWorkflowNodeLine(b *strings.Builder, s *workflowPaneState, node *WorkflowPaneNode, isCursor, isLastChild bool, width int) {
	t := theme.DefaultTheme

	cursorStr := "  "
	if isCursor {
		cursorStr = t.Highlight.Render(theme.IconArrowRightBold + " ")
	}

	switch node.Type {
	case "run":
		run := s.Runs[node.RunID]
		started, completed := run.counts()
		var icon string
		var style lipgloss.Style
		switch run.statusLabel() {
		case "stale":
			icon = theme.IconWarning
			style = t.Warning
		case "idle":
			icon = theme.IconStatusCompleted
			style = t.Success
		default:
			icon = theme.IconStatusRunning
			style = t.Warning
		}
		scoreboard := fmt.Sprintf("%d/%d", completed, started)
		scoreStyle := t.Warning
		if completed == started && started > 0 {
			scoreStyle = t.Success
		}
		line := fmt.Sprintf("%s%s %s  %s  %s", cursorStr, style.Render(icon), t.Bold.Render(node.Name),
			scoreStyle.Render(scoreboard), style.Render("["+capitalizeFirst(run.statusLabel())+"]"))
		b.WriteString(line + "\n")

	case "phase":
		indent := strings.Repeat("  ", node.Depth)
		b.WriteString(fmt.Sprintf("%s%s%s\n", cursorStr, indent, t.Info.Render(node.Name)))

	case "agent":
		run := s.Runs[node.RunID]
		agent := run.Agents[node.AgentID]
		parentIndent := strings.Repeat("  ", node.Depth-1)
		connector := t.Muted.Render("├─")
		if isLastChild {
			connector = t.Muted.Render("└─")
		}
		var icon string
		var style lipgloss.Style
		if agent.Completed {
			icon = theme.IconStatusCompleted
			style = t.Success
		} else {
			icon = theme.IconStatusRunning
			style = t.Warning
		}
		summary := promptSummary(agent.Prompt, width-len(node.AgentID)-node.Depth*2-12)
		line := fmt.Sprintf("%s%s%s %s %s", cursorStr, parentIndent, connector, style.Render(icon), node.AgentID)
		if summary != "" {
			line += "  " + t.Muted.Render(summary)
		}
		b.WriteString(line + "\n")
	}
}

// renderWorkflowRunDetail renders the bottom-pane detail for a run node.
func renderWorkflowRunDetail(b *strings.Builder, run *workflowRunState) {
	if run == nil {
		return
	}
	t := theme.DefaultTheme
	labelStyle := t.Muted.Italic(true)

	b.WriteString(t.Bold.Render(run.displayName()))
	b.WriteString("  ")
	b.WriteString(t.Muted.Render("(" + run.ID + ")"))
	b.WriteString("\n")
	if run.Meta != nil && run.Meta.Description != "" {
		b.WriteString(t.Muted.Render(run.Meta.Description))
		b.WriteString("\n")
	}

	started, completed := run.counts()
	b.WriteString("\n")
	b.WriteString(labelStyle.Render("Agents: "))
	b.WriteString(fmt.Sprintf("%d started / %d completed", started, completed))
	b.WriteString("\n")

	if run.Stale {
		b.WriteString(t.Warning.Render("Run looks stale: no journal writes for >5m and the owning session is gone."))
		b.WriteString("\n")
	}

	if run.Meta != nil && len(run.Meta.Phases) > 0 {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Phases:"))
		b.WriteString("\n")
		for _, phase := range run.Meta.Phases {
			b.WriteString("  " + t.Info.Render(phase.Title))
			if phase.Detail != "" {
				b.WriteString(t.Muted.Render(" — " + phase.Detail))
			}
			b.WriteString("\n")
		}
	}
}

// renderWorkflowAgentHeader renders the agent's prompt/result header used
// when no transcript lines have arrived yet.
func renderWorkflowAgentHeader(b *strings.Builder, agent *workflowAgentState) {
	t := theme.DefaultTheme
	status := "running"
	style := t.Warning
	if agent.Completed {
		status = "completed"
		style = t.Success
	}
	b.WriteString(t.Bold.Render(agent.ID))
	b.WriteString("  ")
	b.WriteString(style.Render("[" + capitalizeFirst(status) + "]"))
	b.WriteString("\n")
	if agent.Prompt != "" {
		b.WriteString("\n")
		b.WriteString(t.Muted.Italic(true).Render("Prompt:"))
		b.WriteString("\n")
		b.WriteString(agent.Prompt)
		b.WriteString("\n")
	}
	if agent.Result != "" {
		b.WriteString("\n")
		b.WriteString(t.Muted.Italic(true).Render("Result:"))
		b.WriteString("\n")
		b.WriteString(agent.Result)
		b.WriteString("\n")
	}
}

// promptSummary collapses a prompt to a single truncated line for tree rows.
func promptSummary(prompt string, maxLen int) string {
	if prompt == "" {
		return ""
	}
	line := strings.TrimSpace(prompt)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	if maxLen < 12 {
		maxLen = 12
	}
	runes := []rune(line)
	if len(runes) > maxLen {
		return string(runes[:maxLen-1]) + "…"
	}
	return line
}

// formatWorkflowEntryLines renders a normalized transcript entry as styled
// lines for the workflow detail log viewer.
func formatWorkflowEntryLines(entry transcript.UnifiedEntry) []string {
	t := theme.DefaultTheme
	var lines []string

	for _, part := range entry.Parts {
		switch part.Type {
		case "text":
			content, ok := part.Content.(transcript.UnifiedTextContent)
			if !ok || strings.TrimSpace(content.Text) == "" {
				continue
			}
			icon := t.Muted.Render(theme.IconRobot)
			if entry.Role == "user" {
				icon = t.Warning.Render(theme.IconChevron)
			}
			for i, l := range strings.Split(strings.TrimRight(content.Text, "\n"), "\n") {
				if i == 0 {
					lines = append(lines, icon+" "+l)
				} else {
					lines = append(lines, "  "+l)
				}
			}
		case "tool_call":
			content, ok := part.Content.(transcript.UnifiedToolCall)
			if !ok {
				continue
			}
			arg := workflowToolArg(content.Input)
			line := t.Success.Render(theme.IconRobot + " " + content.Name)
			if arg != "" {
				line += " " + t.Muted.Render(arg)
			}
			lines = append(lines, line)
		case "tool_result":
			content, ok := part.Content.(transcript.UnifiedToolResult)
			if !ok || strings.TrimSpace(content.Output) == "" {
				continue
			}
			first := strings.SplitN(strings.TrimSpace(content.Output), "\n", 2)[0]
			lines = append(lines, "  "+t.Muted.Render("⎿ "+promptSummary(first, 100)))
		case "reasoning":
			content, ok := part.Content.(transcript.UnifiedReasoning)
			if !ok || strings.TrimSpace(content.Text) == "" {
				continue
			}
			first := strings.SplitN(strings.TrimSpace(content.Text), "\n", 2)[0]
			lines = append(lines, t.Muted.Italic(true).Render("✻ "+promptSummary(first, 100)))
		}
	}
	return lines
}

// ── Model integration ──────────────────────────────────────────────────────

// restartWorkflowMonitor tears down any existing monitor and starts
// discovery for the given job. Pane state is reset so the new session's
// events rebuild it from scratch.
func (m *Model) restartWorkflowMonitor(job *orchestration.Job) tea.Cmd {
	m.stopWorkflowMonitor()
	m.workflowState = newWorkflowPaneState()
	m.workflowAgentLines = make(map[string][]string)
	m.workflowPaneCursor = 0
	m.SkillSubFocus = 0
	m.refreshWorkflowPane()
	return startWorkflowMonitorCmd(job, m.MsgCh)
}

// stopWorkflowMonitor cancels the workflow event source and transcript
// collector, if running, and clears pane state.
func (m *Model) stopWorkflowMonitor() {
	if m.workflowCancel != nil {
		m.workflowCancel()
		m.workflowCancel = nil
	}
	m.workflowState = nil
	m.workflowSelectedAgentID = ""
	m.workflowPaneNodes = nil
	m.workflowPaneRawContent = ""
}

// refreshWorkflowPane re-renders the workflow tree and detail content for
// the current state and cursor, and swaps the transcript viewer's content
// when the selected agent changes.
func (m *Model) refreshWorkflowPane() {
	result := renderWorkflowPane(m.workflowState, m.workflowPaneCursor, m.LogViewerWidth)
	m.workflowPaneNodes = result.nodes
	if m.workflowPaneCursor >= len(result.nodes) {
		m.workflowPaneCursor = len(result.nodes) - 1
	}
	if m.workflowPaneCursor < 0 {
		m.workflowPaneCursor = 0
	}
	m.workflowPaneRawContent = result.treeContent
	m.workflowPaneViewport.SetContent(wrapContentForViewport(result.treeContent, m.workflowPaneViewport.Width-1))

	selected := ""
	if len(result.nodes) > 0 && m.workflowPaneCursor < len(result.nodes) {
		if node := result.nodes[m.workflowPaneCursor]; node.Type == "agent" {
			selected = node.AgentID
		}
	}
	if selected != m.workflowSelectedAgentID {
		m.workflowSelectedAgentID = selected
		if selected != "" {
			if lines := m.workflowAgentLines[selected]; len(lines) > 0 {
				m.workflowLogViewer.SetContent(strings.Join(lines, "\n"))
			} else {
				// No transcript yet — show the prompt/result header until
				// lines arrive.
				m.workflowLogViewer.SetContent(result.detailContent)
			}
		}
	}
	if selected == "" {
		m.workflowDetailViewport.SetContent(wrapContentForViewport(result.detailContent, m.workflowDetailViewport.Width-1))
	}
}

// refreshActiveTreePane refreshes whichever tree+detail pane is active, so
// shared focus-cycling code stays pane-agnostic.
func (m *Model) refreshActiveTreePane() {
	if m.ActiveDetailPane == WorkflowPaneDetail {
		m.refreshWorkflowPane()
		return
	}
	m.refreshSkillPane()
}

// updateWorkflowViewportSizes sets the dimensions for the workflow tree
// viewport and the detail viewer (viewport + transcript log viewer).
func (m *Model) updateWorkflowViewportSizes() {
	vpWidth := m.LogViewerWidth
	if vpWidth < 10 {
		vpWidth = 10
	}
	m.workflowPaneViewport.Width = vpWidth
	m.workflowDetailViewport.Width = vpWidth
	// 1 separator line + 2 newlines between the two viewports
	totalHeight := m.LogViewerHeight - logHeaderHeight - 3
	if totalHeight < 4 {
		totalHeight = 4
	}
	m.workflowPaneViewport.Height = totalHeight / 2
	bottomHeight := totalHeight - totalHeight/2
	m.workflowDetailViewport.Height = bottomHeight
	m.workflowLogViewer, _ = m.workflowLogViewer.Update(tea.WindowSizeMsg{Width: vpWidth, Height: bottomHeight})
}

// handleWorkflowTreeKey handles navigation keys for the workflow pane tree.
func (m Model) handleWorkflowTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	result, idx := m.Sequence.Process(msg, m.KeyMap.Top, m.KeyMap.Bottom)
	switch result {
	case keymap.SequenceMatch:
		m.Sequence.Clear()
		if idx == 0 {
			m.workflowPaneCursor = 0
		} else {
			m.workflowPaneCursor = len(m.workflowPaneNodes) - 1
		}
		m.refreshWorkflowPane()
		return m, nil
	case keymap.SequencePending:
		return m, nil
	}

	m.Sequence.Clear()

	if len(m.workflowPaneNodes) == 0 {
		return m, nil
	}

	switch msg.String() {
	case "j", "down":
		if m.workflowPaneCursor < len(m.workflowPaneNodes)-1 {
			m.workflowPaneCursor++
			m.refreshWorkflowPane()
		}
		return m, nil
	case "k", "up":
		if m.workflowPaneCursor > 0 {
			m.workflowPaneCursor--
			m.refreshWorkflowPane()
		}
		return m, nil
	case "ctrl+d":
		halfPage := len(m.workflowPaneNodes) / 4
		if halfPage < 1 {
			halfPage = 1
		}
		m.workflowPaneCursor += halfPage
		if m.workflowPaneCursor >= len(m.workflowPaneNodes) {
			m.workflowPaneCursor = len(m.workflowPaneNodes) - 1
		}
		m.refreshWorkflowPane()
		return m, nil
	case "ctrl+u":
		halfPage := len(m.workflowPaneNodes) / 4
		if halfPage < 1 {
			halfPage = 1
		}
		m.workflowPaneCursor -= halfPage
		if m.workflowPaneCursor < 0 {
			m.workflowPaneCursor = 0
		}
		m.refreshWorkflowPane()
		return m, nil
	}

	// Delegate remaining keys to the tree viewport for scrolling.
	var cmd tea.Cmd
	m.workflowPaneViewport, cmd = m.workflowPaneViewport.Update(msg)
	return m, cmd
}

// handleWorkflowDetailKey handles navigation keys for the workflow detail
// viewer (transcript log viewer for agents, viewport for runs/phases).
func (m Model) handleWorkflowDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	result, idx := m.Sequence.Process(msg, m.KeyMap.Top, m.KeyMap.Bottom)
	switch result {
	case keymap.SequenceMatch:
		m.Sequence.Clear()
		if m.workflowSelectedAgentID != "" {
			if idx == 0 {
				m.workflowLogViewer.GotoTop()
			} else {
				m.workflowLogViewer.GotoBottom()
			}
		} else {
			if idx == 0 {
				m.workflowDetailViewport.GotoTop()
			} else {
				m.workflowDetailViewport.GotoBottom()
			}
		}
		return m, nil
	case keymap.SequencePending:
		return m, nil
	}

	m.Sequence.Clear()

	var cmd tea.Cmd
	if m.workflowSelectedAgentID != "" {
		m.workflowLogViewer, cmd = m.workflowLogViewer.Update(msg)
	} else {
		m.workflowDetailViewport, cmd = m.workflowDetailViewport.Update(msg)
	}
	return m, cmd
}

// workflowToolArg extracts the most identifying argument of a tool call for
// one-line display.
func workflowToolArg(input map[string]interface{}) string {
	for _, key := range []string{"file_path", "path", "command", "pattern", "query", "description", "prompt", "url"} {
		if v, ok := input[key].(string); ok && v != "" {
			return promptSummary(v, 80)
		}
	}
	return ""
}
