package workflowmon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultPollInterval = 1 * time.Second
	defaultStaleAfter   = 5 * time.Minute
	// maxPromptBytes bounds how much of an agent transcript's first line is
	// read to recover the prompt (mission prompts can run to several KB).
	maxPromptBytes = 256 * 1024
)

// FileSourceOptions configures a FileSource.
type FileSourceOptions struct {
	// PollInterval between directory/journal scans. Defaults to 1s.
	PollInterval time.Duration
	// StaleAfter is how long a run's journal may go without writes before
	// the run can be considered stale. Defaults to 5m.
	StaleAfter time.Duration
	// SessionAlive reports whether the Claude session owning the runs is
	// still alive. Staleness requires BOTH a quiet journal and a gone
	// session; when nil, runs are never marked stale.
	SessionAlive func() bool
	// ScriptsDirs are the workflows/scripts directories searched for a
	// run's persisted orchestration script. Defaults to the source's own
	// <sessionDir>/workflows/scripts. Session artifacts can fragment across
	// project-slug dirs, so a run discovered under one slug may have its
	// script under another; pass every resolved session dir's scripts dir
	// to merge them.
	ScriptsDirs []string
}

// FileSource implements EventSource by polling a Claude Code session
// directory: wf_* run dirs under <sessionDir>/subagents/workflows/ for
// journal started/result events, and <sessionDir>/workflows/scripts/ for
// persisted script meta. The journal layout is undocumented Claude Code
// internals; parse failures skip lines rather than failing the stream.
type FileSource struct {
	sessionDir string
	opts       FileSourceOptions
	events     chan Event
	cancel     context.CancelFunc
	done       chan struct{}
	closeOnce  sync.Once
}

// fileRunState is the per-run tail state.
type fileRunState struct {
	journalOffset int64
	partial       []byte          // trailing bytes of an incomplete journal line
	started       map[string]bool // agents with an emitted AgentStarted
	completed     map[string]bool // agents with an emitted AgentCompleted
	promptPending map[string]bool // started agents whose transcript had no prompt yet
	staleEmitted  bool
}

// journalEvent mirrors one journal.jsonl line: {type, key, agentId} for
// "started", plus {result} for "result". There is no timestamp field.
type journalEvent struct {
	Type    string          `json:"type"`
	Key     string          `json:"key"`
	AgentID string          `json:"agentId"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// NewFileSource creates and starts a file-tailing event source for the given
// Claude session directory. Call Close() to stop it.
func NewFileSource(sessionDir string, opts FileSourceOptions) *FileSource {
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = defaultStaleAfter
	}
	if len(opts.ScriptsDirs) == 0 {
		opts.ScriptsDirs = []string{filepath.Join(sessionDir, "workflows", "scripts")}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &FileSource{
		sessionDir: sessionDir,
		opts:       opts,
		events:     make(chan Event, 256),
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	go s.run(ctx)
	return s
}

// Events implements EventSource.
func (s *FileSource) Events() <-chan Event { return s.events }

// Close implements EventSource. It stops the poll loop and closes the
// events channel.
func (s *FileSource) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
	})
	return nil
}

func (s *FileSource) run(ctx context.Context) {
	defer close(s.done)
	defer close(s.events)

	runs := make(map[string]*fileRunState)
	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()

	for {
		s.poll(ctx, runs)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *FileSource) poll(ctx context.Context, runs map[string]*fileRunState) {
	workflowsDir := filepath.Join(s.sessionDir, "subagents", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		// No workflows directory yet — the session may simply not have
		// launched a workflow. Keep polling.
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "wf_") {
			continue
		}
		runID := entry.Name()
		rs, known := runs[runID]
		if !known {
			rs = &fileRunState{
				started:       make(map[string]bool),
				completed:     make(map[string]bool),
				promptPending: make(map[string]bool),
			}
			runs[runID] = rs
			var meta *ScriptMeta
			for _, scriptsDir := range s.opts.ScriptsDirs {
				if meta = LoadRunMeta(scriptsDir, runID); meta != nil {
					break
				}
			}
			if !s.emit(ctx, RunDiscovered{RunID: runID, Meta: meta}) {
				return
			}
		}

		runDir := filepath.Join(workflowsDir, runID)
		if !s.pollJournal(ctx, runDir, runID, rs) {
			return
		}
		if !s.retryPrompts(ctx, runDir, runID, rs) {
			return
		}
		s.checkStale(ctx, runDir, runID, rs)
	}
}

// pollJournal reads any new complete lines from the run's journal.jsonl and
// emits the corresponding events. Returns false when the context is done.
func (s *FileSource) pollJournal(ctx context.Context, runDir, runID string, rs *fileRunState) bool {
	path := filepath.Join(runDir, "journal.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() <= rs.journalOffset {
		return true
	}

	newBytes := make([]byte, info.Size()-rs.journalOffset)
	if _, err := f.ReadAt(newBytes, rs.journalOffset); err != nil {
		return true
	}
	rs.journalOffset = info.Size()

	data := append(rs.partial, newBytes...)
	lines := bytes.Split(data, []byte("\n"))
	// The final element is either empty (data ended in \n) or a partial
	// line still being written — carry it to the next poll.
	rs.partial = append([]byte(nil), lines[len(lines)-1]...)
	lines = lines[:len(lines)-1]

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev journalEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // undocumented format; tolerate drift
		}
		switch ev.Type {
		case "started":
			if rs.started[ev.AgentID] {
				continue
			}
			rs.started[ev.AgentID] = true
			prompt := readAgentPrompt(runDir, ev.AgentID)
			if prompt == "" {
				rs.promptPending[ev.AgentID] = true
			}
			if !s.emit(ctx, AgentStarted{RunID: runID, AgentID: ev.AgentID, Prompt: prompt}) {
				return false
			}
		case "result":
			if rs.completed[ev.AgentID] {
				continue
			}
			rs.completed[ev.AgentID] = true
			delete(rs.promptPending, ev.AgentID)
			if !s.emit(ctx, AgentCompleted{RunID: runID, AgentID: ev.AgentID, Result: renderResult(ev.Result)}) {
				return false
			}
		}
	}
	return true
}

// retryPrompts re-emits AgentStarted (upsert) for agents whose transcript
// appeared after their journal started event.
func (s *FileSource) retryPrompts(ctx context.Context, runDir, runID string, rs *fileRunState) bool {
	for agentID := range rs.promptPending {
		prompt := readAgentPrompt(runDir, agentID)
		if prompt == "" {
			continue
		}
		delete(rs.promptPending, agentID)
		if !s.emit(ctx, AgentStarted{RunID: runID, AgentID: agentID, Prompt: prompt}) {
			return false
		}
	}
	return true
}

// checkStale emits RunStale (once) when the journal has been quiet for the
// staleness window AND the owning session is gone. In-flight agents are
// never inferred as interrupted from journal gaps alone.
func (s *FileSource) checkStale(ctx context.Context, runDir, runID string, rs *fileRunState) {
	if rs.staleEmitted || s.opts.SessionAlive == nil {
		return
	}
	info, err := os.Stat(filepath.Join(runDir, "journal.jsonl"))
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) < s.opts.StaleAfter {
		return
	}
	if s.opts.SessionAlive() {
		return
	}
	if s.emit(ctx, RunStale{RunID: runID}) {
		rs.staleEmitted = true
	}
}

// emit delivers an event, blocking until the consumer takes it or the
// context is cancelled. Returns false on cancellation.
func (s *FileSource) emit(ctx context.Context, ev Event) bool {
	select {
	case s.events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// readAgentPrompt extracts the task prompt from the first line of
// agent-<id>.jsonl (the initial user message). Returns "" when the
// transcript doesn't exist yet or the line can't be parsed.
func readAgentPrompt(runDir, agentID string) string {
	f, err := os.Open(filepath.Join(runDir, "agent-"+agentID+".jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, maxPromptBytes)
	n, _ := f.Read(buf)
	if n == 0 {
		return ""
	}
	// A first line longer than the cap stays truncated; json.Unmarshal then
	// fails and the prompt is retried on a later poll.
	line := buf[:n]
	if idx := bytes.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}

	var entry struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return ""
	}
	return contentText(entry.Message.Content)
}

// contentText renders a Claude message content field (string or array of
// {type:"text",text} blocks) as plain text.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// renderResult renders a journal result payload: bare JSON strings as plain
// text, anything else as indented JSON, unparseable payloads verbatim.
func renderResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err == nil {
		return buf.String()
	}
	return string(raw)
}
