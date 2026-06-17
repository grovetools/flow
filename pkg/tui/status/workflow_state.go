package status

import (
	"strings"

	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/agentlogs/pkg/transcript"
	"github.com/grovetools/flow/pkg/workflowmon"
)

// Workflow state accumulation for the inline workflow tree in the main jobs
// panel. These are the pure helpers migrated out of the retired W pane
// (workflow_pane.go): they fold workflowmon events into per-job state that
// buildDisplayRows renders as virtual rows under each job.

// workflowAgentLineCap bounds the buffered formatted transcript lines kept
// per agent (long sessions can run to megabytes of transcript).
const workflowAgentLineCap = 2000

// WorkflowPaneNode represents a run, phase, or agent node in a single run's
// resolved tree shape.
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
	Name      string // descriptive name (from daemon/hooks or script label)
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

// adhocRunID is the pseudo-run bucket for run-less subagents (ad-hoc
// Agent-tool spawns, or workflow agents whose run attribution hasn't
// arrived yet). Daemon-backed sources map empty RunIDs here.
const adhocRunID = workflowmon.AdhocRunID

// workflowPaneState is the per-job model of discovered runs. It is fed
// exclusively by workflowmon events — never by reading journal files.
type workflowPaneState struct {
	Runs     map[string]*workflowRunState
	RunOrder []string
	// Resolved Claude session directory (informational; used by the
	// transcript collector, not by rendering).
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
		s.migrateAdhocAgent(ev.RunID, ev.AgentID)
		run := s.ensureRun(ev.RunID)
		agent, ok := run.Agents[ev.AgentID]
		if !ok {
			agent = &workflowAgentState{ID: ev.AgentID}
			run.Agents[ev.AgentID] = agent
			run.AgentOrder = append(run.AgentOrder, ev.AgentID)
		}
		// Upsert semantics: a re-emitted AgentStarted enriches fields.
		if ev.Name != "" {
			agent.Name = ev.Name
		}
		if ev.Prompt != "" {
			agent.Prompt = ev.Prompt
		}
		if ev.Phase != "" {
			agent.Phase = ev.Phase
		}
	case workflowmon.AgentCompleted:
		s.migrateAdhocAgent(ev.RunID, ev.AgentID)
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

// migrateAdhocAgent removes an agent from the ad-hoc pseudo-run when an
// event attributes it to a real run (mirrors the daemon's adhoc→run
// migration so the agent doesn't render twice).
func (s *workflowPaneState) migrateAdhocAgent(runID, agentID string) {
	if runID == adhocRunID || runID == "" {
		return
	}
	adhoc, ok := s.Runs[adhocRunID]
	if !ok {
		return
	}
	if _, ok := adhoc.Agents[agentID]; !ok {
		return
	}
	delete(adhoc.Agents, agentID)
	for i, id := range adhoc.AgentOrder {
		if id == agentID {
			adhoc.AgentOrder = append(adhoc.AgentOrder[:i], adhoc.AgentOrder[i+1:]...)
			break
		}
	}
	if len(adhoc.AgentOrder) == 0 {
		delete(s.Runs, adhocRunID)
		for i, id := range s.RunOrder {
			if id == adhocRunID {
				s.RunOrder = append(s.RunOrder[:i], s.RunOrder[i+1:]...)
				break
			}
		}
	}
}

// counts returns started/completed agent counts for the run.
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
	if r.ID == adhocRunID {
		return "ad-hoc agents"
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
// list. When a run's meta declares phases AND agents carry phase attribution
// (a hooks/daemon-backed source), agents group under phase nodes; otherwise
// agents sit directly under the run (the file-tailing source cannot
// attribute phases).
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
		nodes = append(nodes, resolveRunChildNodes(run)...)
	}
	return nodes
}

// resolveRunChildNodes returns the phase/agent nodes under one run, with
// Depth relative to the run node (phase=1, grouped agent=2, flat agent=1).
func resolveRunChildNodes(run *workflowRunState) []*WorkflowPaneNode {
	var nodes []*WorkflowPaneNode

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
				RunID: run.ID,
				Name:  phase.Title,
				Depth: 1,
			})
			for _, id := range members {
				nodes = append(nodes, agentNode(run.ID, run.Agents[id], 2))
			}
		}
		for _, id := range run.AgentOrder {
			if !attributed[id] {
				nodes = append(nodes, agentNode(run.ID, run.Agents[id], 1))
			}
		}
	} else {
		for _, id := range run.AgentOrder {
			nodes = append(nodes, agentNode(run.ID, run.Agents[id], 1))
		}
	}
	return nodes
}

func agentNode(runID string, agent *workflowAgentState, depth int) *WorkflowPaneNode {
	return &WorkflowPaneNode{
		Type:    "agent",
		RunID:   runID,
		AgentID: agent.ID,
		Name:    agentDisplayName(agent),
		Depth:   depth,
	}
}

// agentDisplayName returns the human-readable name for an agent using the
// display precedence: (1) recovered static label, (2) phase + prompt slug,
// (3) raw agent ID.
func agentDisplayName(agent *workflowAgentState) string {
	// 1. Recovered static label (from meta.json description or script label)
	if agent.Name != "" {
		return agent.Name
	}
	// 2. Phase + prompt slug (first ~6 words of the prompt)
	if agent.Phase != "" || agent.Prompt != "" {
		slug := promptSlug(agent.Prompt, 6)
		if agent.Phase != "" && slug != "" {
			return agent.Phase + ": " + slug
		}
		if agent.Phase != "" {
			return agent.Phase
		}
		if slug != "" {
			return slug
		}
	}
	// 3. Raw agent ID
	return agent.ID
}

// promptSlug returns the first N words of a prompt, truncated for display.
func promptSlug(prompt string, maxWords int) string {
	if prompt == "" {
		return ""
	}
	// Take first line only
	line := strings.TrimSpace(prompt)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	// Split into words and take first N
	words := strings.Fields(line)
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	result := strings.Join(words, " ")
	// Truncate to ~60 chars max
	if len(result) > 60 {
		result = result[:57] + "..."
	}
	return result
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
// lines for the detail log viewer when an agent row is selected.
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
