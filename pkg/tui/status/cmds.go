package status

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/agentlogs/pkg/agentstream"
	"github.com/grovetools/agentlogs/pkg/display"
	aglogsession "github.com/grovetools/agentlogs/pkg/sessioninfo"
	"github.com/grovetools/agentlogs/pkg/transcript"
	"github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/tui/components/logviewer"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/util/delegation"
	notifications "github.com/grovetools/notify"
	notifyconfig "github.com/grovetools/notify/pkg/config"
	"gopkg.in/yaml.v3"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/workflowmon"
)

// Message types
// sendToMsgCh performs a non-blocking send of a tea.Msg to the Model's stream
// channel. If the channel is full or closed, the message is dropped; the
// recover guards against sends on closed channels. Background streaming
// goroutines use this to hand messages off to the Update loop without holding
// a reference to the tea.Program.
func sendToMsgCh(ch chan<- tea.Msg, msg tea.Msg) {
	defer func() { _ = recover() }()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
		// channel full; drop the message rather than block the sender
	}
}

type (
	RefreshMsg            struct{}
	ArchiveConfirmedMsg   struct{ Job *orchestration.Job }
	EditFileAndQuitMsg    struct{ FilePath string }
	EditFileInTmuxMsg     struct{ Err error }
	TickMsg               time.Time
	StatusUpdateMsg       string
	RefreshTickMsg        time.Time
	RenameCompleteMsg     struct{ Err error }
	UpdateDepsCompleteMsg struct{ Err error }
	CreateJobCompleteMsg  struct{ Err error }
	recipeAddedMsg        struct{ err error }
	JobRunFinishedMsg     struct {
		Jobs []*orchestration.Job // The jobs that were executed
		Err  error
	}
)
type RetryLoadAgentLogsMsg struct{}

type FrontmatterContentLoadedMsg struct {
	Content string
	Err     error
}

type BriefingContentLoadedMsg struct {
	Content string
	Err     error
}

type EditContentLoadedMsg struct {
	Content string
	Err     error
}

// daemonStateUpdateMsg is sent when the daemon pushes a state update via SSE.
type daemonStateUpdateMsg struct {
	update daemon.StateUpdate
}

// daemonStreamErrorMsg is sent when the daemon stream encounters an error or closes.
type daemonStreamErrorMsg struct {
	err error
}

// daemonStreamConnectedMsg is dispatched once subscribeToDaemonCmd has
// successfully opened an SSE stream. The channel and cancel func are stored
// on the Model so the stream can be torn down via Close(). Scoping the
// state per-Model lets multiple embedded status.Model instances coexist
// inside a single host (e.g. grove terminal) without sharing state.
type daemonStreamConnectedMsg struct {
	ch     <-chan daemon.StateUpdate
	cancel context.CancelFunc
}

// subscribeToDaemonCmd opens an SSE stream to the daemon and returns the
// channel + cancel function as a daemonStreamConnectedMsg. The Model owns
// the lifecycle and tears it down via Close().
func subscribeToDaemonCmd() tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewWithAutoStart()

		if !client.IsRunning() {
			client.Close()
			return nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		ch, err := client.StreamState(ctx)
		if err != nil {
			cancel()
			client.Close()
			return daemonStreamErrorMsg{err: err}
		}

		return daemonStreamConnectedMsg{ch: ch, cancel: cancel}
	}
}

// workflowSessionResolvedMsg reports the outcome of workflow run discovery
// for a job: the resolved Claude session directory plus the cancel func that
// tears down the event source and transcript collector, or the error that
// prevented discovery.
type workflowSessionResolvedMsg struct {
	JobID      string
	SessionDir string
	Cancel     context.CancelFunc
	Err        error
}

// workflowEventMsg wraps one workflowmon lifecycle event for the Update loop.
type workflowEventMsg struct {
	JobID string
	Event workflowmon.Event
}

// workflowDaemonSourceStartedMsg reports that the single daemon-backed
// workflow event source is running. Cancel tears down the source and its
// forwarding goroutine (Model.Close(), or a duplicate start).
type workflowDaemonSourceStartedMsg struct {
	Cancel context.CancelFunc
}

// startDaemonWorkflowSourceCmd starts the ONE daemon-backed workflow event
// source covering every job: a workflowmon.DaemonSource (snapshot replay +
// SSE deltas + reconnect with backoff) whose events are forwarded into
// MsgCh as workflowEventMsg routed by the event's own JobID. client may be
// nil — a private client is constructed (and owned) in that case. Returns
// nil when no daemon is reachable; the FileSource fallback (RefreshMsg
// reconciliation) covers that.
func startDaemonWorkflowSourceCmd(client daemon.Client, msgCh chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ownsClient := false
		if client == nil {
			client = daemon.NewWithAutoStart()
			ownsClient = true
		}
		if !client.IsRunning() {
			if ownsClient {
				client.Close()
			}
			return nil
		}

		source := workflowmon.NewDaemonSource(client, workflowmon.DaemonSourceOptions{})
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-ctx.Done()
			_ = source.Close()
			if ownsClient {
				client.Close()
			}
		}()
		go func() {
			for ev := range source.Events() {
				sendToMsgChBlocking(ctx, msgCh, workflowEventMsg{
					JobID: workflowmon.EventJobID(ev),
					Event: ev,
				})
			}
		}()
		return workflowDaemonSourceStartedMsg{Cancel: cancel}
	}
}

// workflowAgentLogMsg carries one formatted transcript line for a workflow
// agent, delivered via MsgCh by the transcript collector.
type workflowAgentLogMsg struct {
	JobID   string
	AgentID string
	Lines   []string
}

// workflowAgentTranscriptLoadedMsg carries a fully-loaded historical
// transcript (rendered to Markdown) for a completed workflow agent.
type workflowAgentTranscriptLoadedMsg struct {
	AgentID  string
	Markdown string
	Err      error
}

// workflowRebuildTickMsg fires the coalesced display-row rebuild after
// workflow activity marked jobs dirty. At most one tick is in flight at a
// time (workflowRebuildPending) — display rows are never rebuilt per event.
type workflowRebuildTickMsg struct{}

// workflowRebuildInterval is the coalescing window for workflow-driven
// display-row rebuilds and transcript viewport refreshes.
const workflowRebuildInterval = 100 * time.Millisecond

// scheduleWorkflowRebuildCmd schedules the next coalesced rebuild tick.
func scheduleWorkflowRebuildCmd() tea.Cmd {
	return tea.Tick(workflowRebuildInterval, func(time.Time) tea.Msg {
		return workflowRebuildTickMsg{}
	})
}

// startWorkflowMonitorCmd resolves the job's Claude session directories via
// the hooks session registry and starts background producers feeding MsgCh:
// a workflowmon.FileSource per directory forwarding typed lifecycle events,
// and an agentstream.StreamWorkflow collector per directory forwarding
// formatted transcript lines tagged by agent ID. The returned msg carries
// the cancel func owning all of them.
//
// Session artifacts fragment across ~/.claude/projects/ project-slug dirs
// when the shell cwd changes mid-session, so every resolved dir is watched;
// the path constructed next to the transcript is merged in so a session
// whose dir hasn't been created yet is still polled until it appears.
func startWorkflowMonitorCmd(job *orchestration.Job, msgCh chan tea.Msg) tea.Cmd {
	jobID := job.ID
	return func() tea.Msg {
		registry, err := sessions.NewFileSystemRegistry()
		if err != nil {
			return workflowSessionResolvedMsg{JobID: jobID, Err: err}
		}
		metadata, err := registry.Find(jobID)
		if err != nil {
			// No hooks-registry record. Before declaring "no Claude session",
			// ask agentlogs' tiered resolver (daemon job registry, opencode
			// pointer, multi-provider scan) whether this is a non-Claude
			// session — those legitimately have no workflow tree and should
			// degrade to the transcript view, not error.
			if info, rerr := aglogsession.Resolve(jobID); rerr == nil && info.Provider != "" && info.Provider != "claude" {
				return workflowSessionResolvedMsg{JobID: jobID, Err: workflowUnsupportedProviderErr(info.Provider)}
			}
			return workflowSessionResolvedMsg{JobID: jobID, Err: fmt.Errorf("no Claude session registered for this job")}
		}
		if metadata.Provider != "" && !strings.HasPrefix(metadata.Provider, "claude") {
			// Workflow/subagent trees are produced by Claude Code's workflow
			// runtime only; other providers degrade to the transcript view.
			return workflowSessionResolvedMsg{JobID: jobID, Err: workflowUnsupportedProviderErr(metadata.Provider)}
		}
		if metadata.TranscriptPath == "" || metadata.ClaudeSessionID == "" {
			return workflowSessionResolvedMsg{JobID: jobID, Err: fmt.Errorf("session metadata has no transcript path yet")}
		}
		// Transcript lives at ~/.claude/projects/<slug>/<session-id>.jsonl;
		// workflow runs live in the sibling <session-id>/ directory.
		primaryDir := filepath.Join(filepath.Dir(metadata.TranscriptPath), metadata.ClaudeSessionID)
		sessionDirs, _ := sessions.ResolveClaudeSessionDirs(metadata.ClaudeSessionID)
		hasPrimary := false
		for _, dir := range sessionDirs {
			if dir == primaryDir {
				hasPrimary = true
				break
			}
		}
		if !hasPrimary {
			sessionDirs = append(sessionDirs, primaryDir)
		}

		// A run discovered under one slug may have its script persisted
		// under another; let every source search all scripts dirs.
		scriptsDirs := make([]string, 0, len(sessionDirs))
		for _, dir := range sessionDirs {
			scriptsDirs = append(scriptsDirs, filepath.Join(dir, "workflows", "scripts"))
		}

		ctx, cancel := context.WithCancel(context.Background())

		claudeSessionID := metadata.ClaudeSessionID
		sessionAlive := func() bool {
			alive, err := registry.IsAlive(claudeSessionID)
			return err == nil && alive
		}
		for _, sessionDir := range sessionDirs {
			source := workflowmon.NewFileSource(sessionDir, workflowmon.FileSourceOptions{
				SessionAlive: sessionAlive,
				ScriptsDirs:  scriptsDirs,
			})
			go func() {
				<-ctx.Done()
				_ = source.Close()
			}()
			go func() {
				for ev := range source.Events() {
					sendToMsgChBlocking(ctx, msgCh, workflowEventMsg{JobID: jobID, Event: ev})
				}
			}()
			go collectWorkflowTranscripts(ctx, sessionDir, jobID, msgCh)
		}

		return workflowSessionResolvedMsg{JobID: jobID, SessionDir: strings.Join(sessionDirs, ", "), Cancel: cancel}
	}
}

// workflowUnsupportedProviderErr is the friendly degrade "error" shown in the
// workflow pane for non-Claude sessions: they have no workflow/subagent tree
// to monitor, and the transcript pane remains the observability surface.
func workflowUnsupportedProviderErr(provider string) error {
	return fmt.Errorf("workflow monitoring is Claude-only; %s sessions show the transcript view only", provider)
}

// collectWorkflowTranscripts fans the session's multiplexed workflow
// transcript stream into MsgCh as formatted lines, one message per entry.
// Journal scoreboard entries are skipped — lifecycle state arrives through
// the workflowmon event source instead.
func collectWorkflowTranscripts(ctx context.Context, sessionDir, jobID string, msgCh chan tea.Msg) {
	ch, err := agentstream.StreamWorkflow(ctx, sessionDir)
	if err != nil {
		return
	}
	for entry := range ch {
		if entry.Provider == "journal" || entry.AgentID == "" {
			continue
		}
		lines := formatWorkflowEntryLines(entry)
		if len(lines) == 0 {
			continue
		}
		sendToMsgChBlocking(ctx, msgCh, workflowAgentLogMsg{JobID: jobID, AgentID: entry.AgentID, Lines: lines})
	}
}

// loadHistoricalWorkflowTranscriptCmd reads an agent's transcript jsonl from
// disk, normalizes it, and renders it as Markdown. It handles BOTH on-disk
// locations: simple subagents at <sessionDir>/subagents/agent-<id>.jsonl AND
// workflow subagents at <sessionDir>/subagents/workflows/wf_*/agent-<id>.jsonl.
//
// When the live session dirs miss (e.g. after a cold load, when the upstream
// runtime's per-agent jsonl has been cleaned up but the run was archived), it
// falls back to the canonical glyph-format transcript the archiver wrote to
// <planDir>/.artifacts/<jobID>/workflows/<runID>/agents/agent-<id>.md, returning
// it as-is. planDir/jobID/runID may be empty, in which case the fallback is
// skipped.
func loadHistoricalWorkflowTranscriptCmd(sessionDir, agentID, planDir, jobID, runID string) tea.Cmd {
	return func() tea.Msg {
		// Try both locations: simple subagent and workflow subagent
		paths := []string{
			filepath.Join(sessionDir, "subagents", "agent-"+agentID+".jsonl"),
		}
		// Also search workflow run directories
		workflowsDir := filepath.Join(sessionDir, "subagents", "workflows")
		if dirEntries, err := os.ReadDir(workflowsDir); err == nil {
			for _, e := range dirEntries {
				if e.IsDir() && strings.HasPrefix(e.Name(), "wf_") {
					paths = append(paths, filepath.Join(workflowsDir, e.Name(), "agent-"+agentID+".jsonl"))
				}
			}
		}

		var transcriptPath string
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				transcriptPath = p
				break
			}
		}
		if transcriptPath == "" {
			// Fall back to the archived, canonical glyph-format transcript.
			if md, ok := loadArchivedAgentMarkdown(planDir, jobID, runID, agentID); ok {
				return workflowAgentTranscriptLoadedMsg{
					AgentID:  agentID,
					Markdown: md,
				}
			}
			return workflowAgentTranscriptLoadedMsg{
				AgentID: agentID,
				Err:     fmt.Errorf("no transcript found for agent %s", agentID),
			}
		}

		// Read and normalize the transcript. Workflow subagent transcripts
		// only exist for Claude sessions (the wf_* tree is written by Claude
		// Code's workflow runtime), so the provider is claude by construction.
		entries, err := normalizeTranscriptFile(transcriptPath, "claude")
		if err != nil {
			return workflowAgentTranscriptLoadedMsg{
				AgentID: agentID,
				Err:     err,
			}
		}

		// Render to Markdown using the canonical glyph renderer (theme icons +
		// summarized tool rows, ANSI-stripped) — the same format the archiver
		// writes to disk, so live and cold-loaded transcripts read identically.
		var buf bytes.Buffer
		if err := display.RenderUnifiedTranscriptPlain(&buf, entries, "full", display.DefaultToolFormatters()); err != nil {
			return workflowAgentTranscriptLoadedMsg{
				AgentID: agentID,
				Err:     err,
			}
		}
		return workflowAgentTranscriptLoadedMsg{
			AgentID:  agentID,
			Markdown: buf.String(),
		}
	}
}

// loadArchivedAgentMarkdown reads the archiver's canonical glyph-format
// transcript for an agent from
// <planDir>/.artifacts/<jobID>/workflows/<runID>/agents/agent-<id>.md.
// When runID is empty it scans every archived wf_* run dir for a match.
// Returns ("", false) when no archived transcript exists.
func loadArchivedAgentMarkdown(planDir, jobID, runID, agentID string) (string, bool) {
	if planDir == "" || jobID == "" {
		return "", false
	}
	var candidates []string
	if runID != "" {
		candidates = append(candidates, filepath.Join(planDir, ".artifacts", jobID, "workflows", runID, "agents", "agent-"+agentID+".md"))
	} else {
		workflowsDir := filepath.Join(planDir, ".artifacts", jobID, "workflows")
		if dirEntries, err := os.ReadDir(workflowsDir); err == nil {
			for _, e := range dirEntries {
				if e.IsDir() && strings.HasPrefix(e.Name(), "wf_") {
					candidates = append(candidates, filepath.Join(workflowsDir, e.Name(), "agents", "agent-"+agentID+".md"))
				}
			}
		}
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			return string(data), true
		}
	}
	return "", false
}

// normalizeTranscriptFile reads a jsonl transcript file and normalizes each
// line with the given provider's normalizer, selected through agentlogs'
// provider→normalizer routing table (agentstream.NormalizerForProvider —
// empty/claude yields the exact ClaudeNormalizer this used to construct
// directly).
func normalizeTranscriptFile(path, provider string) ([]transcript.UnifiedEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []transcript.UnifiedEntry
	normalizer := agentstream.NormalizerForProvider(provider)

	// Use a scanner with a larger buffer for long lines
	scanner := newLargeLineScanner(f)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		entry, err := normalizer.NormalizeLine(line)
		if err != nil || entry == nil {
			continue
		}
		entries = append(entries, *entry)
	}

	// Flush any buffered entries (e.g., tool calls awaiting results)
	if flusher, ok := normalizer.(agentstream.Flusher); ok {
		for _, entry := range flusher.Flush() {
			entries = append(entries, *entry)
		}
	}

	return entries, scanner.Err()
}

// newLargeLineScanner creates a bufio.Scanner with a large buffer for
// transcript lines that can be several MB (e.g., tool results).
func newLargeLineScanner(r io.Reader) *largeLineScanner {
	s := &largeLineScanner{r: r, buf: make([]byte, 0, 64*1024)}
	return s
}

// largeLineScanner wraps bufio.Scanner with a larger buffer and line limit.
type largeLineScanner struct {
	r    io.Reader
	s    *bufio.Scanner
	buf  []byte
	once sync.Once
}

func (l *largeLineScanner) init() {
	l.s = bufio.NewScanner(l.r)
	l.s.Buffer(l.buf, 16*1024*1024) // 16MB max line size
}

func (l *largeLineScanner) Scan() bool {
	l.once.Do(l.init)
	return l.s.Scan()
}

func (l *largeLineScanner) Bytes() []byte {
	return l.s.Bytes()
}

func (l *largeLineScanner) Err() error {
	if l.s == nil {
		return nil
	}
	return l.s.Err()
}

// sendToMsgChBlocking sends a tea.Msg to the Model's stream channel, blocking
// until the Update loop drains space or ctx is cancelled. Unlike sendToMsgCh
// it never drops on a full channel — transcript replay bursts would otherwise
// lose lines — while the recover still guards against sends on a closed
// channel during shutdown.
func sendToMsgChBlocking(ctx context.Context, ch chan<- tea.Msg, msg tea.Msg) {
	defer func() { _ = recover() }()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	case <-ctx.Done():
	}
}

// unverifiedBindingNotice is shown instead of a transcript when the hooks
// session registry has no native session id recorded for the job. Streaming
// by plan/job name is NOT a safe fallback — aglogs resolves such specs to
// ANY session that ever matched, which can bind another agent's transcript
// to this job (see issue: wrong-session-logs-bound-to-headless-tuimux-jobs).
const unverifiedBindingNotice = "session binding unverified — transcript not streamed"

// retryLoadAgentLogsAfterDelay creates a command that waits and then triggers a retry
func retryLoadAgentLogsAfterDelay() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return RetryLoadAgentLogsMsg{}
	})
}

// LogContentLoadedMsg is sent when historical log content has been loaded from a file.
type LogContentLoadedMsg struct {
	Content        string
	Err            error
	ShouldRetry    bool   // If true, we should retry loading after a delay
	StartStreaming bool   // If true, we should start streaming (agent session is ready)
	LogFilePath    string // The path to the log file to stream
	Provider       string // Session provider from the registry ("" = claude)
	JobID          string // The ID of the job this message belongs to
}

// StreamEndedMsg is sent when an aglogs stream process exits, allowing the TUI
// to clear StreamingJobID so streaming can be restarted if needed.
type StreamEndedMsg struct {
	JobID string
}

// loadLogContentCmd creates a command to asynchronously read a job's log file.
func loadLogContentCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		logPath, err := orchestration.GetJobLogPath(plan, job)
		if err != nil {
			return LogContentLoadedMsg{Err: err, JobID: job.ID}
		}

		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			// It's not an error if the log file doesn't exist yet.
			return LogContentLoadedMsg{Content: fmt.Sprintf("No logs found for %s.", job.Title), JobID: job.ID}
		}

		content, err := os.ReadFile(logPath)
		if err != nil {
			return LogContentLoadedMsg{Err: err, JobID: job.ID}
		}

		return LogContentLoadedMsg{Content: string(content), JobID: job.ID}
	}
}

// loadFrontmatterCmd creates a command to load and format a job's frontmatter.
func loadFrontmatterCmd(job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		content, err := os.ReadFile(job.FilePath)
		if err != nil {
			return FrontmatterContentLoadedMsg{Err: err}
		}

		fm, _, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			return FrontmatterContentLoadedMsg{Err: err}
		}

		// Marshal to YAML for pretty printing
		yamlBytes, err := yaml.Marshal(fm)
		if err != nil {
			return FrontmatterContentLoadedMsg{Err: err}
		}

		return FrontmatterContentLoadedMsg{Content: string(yamlBytes)}
	}
}

// loadBriefingCmd finds and loads the most recent briefing file for a job.
func loadBriefingCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		jobArtifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
		pattern := "briefing-*.xml"
		files, err := filepath.Glob(filepath.Join(jobArtifactDir, pattern))
		if err != nil {
			return BriefingContentLoadedMsg{Err: err}
		}

		if len(files) == 0 {
			return BriefingContentLoadedMsg{Content: "No briefing file found for this job."}
		}

		// Find the most recent file
		var mostRecentFile string
		var latestModTime time.Time
		for _, file := range files {
			info, err := os.Stat(file)
			if err == nil {
				if info.ModTime().After(latestModTime) {
					latestModTime = info.ModTime()
					mostRecentFile = file
				}
			}
		}

		if mostRecentFile == "" {
			return BriefingContentLoadedMsg{Content: "Could not determine the most recent briefing file."}
		}

		content, err := os.ReadFile(mostRecentFile)
		if err != nil {
			return BriefingContentLoadedMsg{Err: err}
		}

		return BriefingContentLoadedMsg{Content: string(content)}
	}
}

// loadJobFileContentCmd creates a command to load the raw content of a job file.
func loadJobFileContentCmd(job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		content, err := os.ReadFile(job.FilePath)
		if err != nil {
			return EditContentLoadedMsg{Err: err}
		}
		return EditContentLoadedMsg{Content: string(content)}
	}
}

// loadAndStreamAgentLogsCmd first loads existing agent logs, then triggers streaming.
// This function implements fast-path optimization:
// - For completed jobs: read directly from job.log if available
// - For running jobs: try to get direct transcript path from session registry
func loadAndStreamAgentLogsCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	logger := logging.NewLogger("flow-tui")
	logger.WithFields(map[string]interface{}{
		"job_id": job.ID,
		"plan":   plan.Name,
	}).Debug("loadAndStreamAgentLogsCmd called, creating command")

	return func() tea.Msg {
		logger := logging.NewLogger("flow-tui")

		// Resolve the session binding ONCE via the hooks session registry.
		// There is NO plan/job-name aglogs fallback: aglogs resolves such
		// specs to ANY session that ever matched the plan/job name, which
		// can silently stream another agent's transcript into this job's
		// log (the completion path dropped the same fallback in f21cc59).
		var claudeSessionID, transcriptPath, sessionProvider string
		if registry, regErr := sessions.NewFileSystemRegistry(); regErr == nil {
			if metadata, findErr := registry.Find(job.ID); findErr == nil && metadata != nil {
				claudeSessionID = metadata.ClaudeSessionID
				transcriptPath = metadata.TranscriptPath
				sessionProvider = metadata.Provider
			}
		}
		// The binding is verified only when the registry recorded the
		// agent's native session id for this job.
		verified := claudeSessionID != ""

		logger.WithFields(map[string]interface{}{
			"job_id":            job.ID,
			"job_status":        job.Status,
			"job_type":          job.Type,
			"binding_verified":  verified,
			"claude_session_id": claudeSessionID,
		}).Debug("loadAndStreamAgentLogsCmd executing")

		// Fast path: read from job.log which contains ANSI-formatted output (for both completed and running jobs)
		jobLogPath, err := orchestration.GetJobLogPath(plan, job)
		logger.WithFields(map[string]interface{}{
			"job_id":       job.ID,
			"job_log_path": jobLogPath,
			"get_path_err": err,
		}).Debug("GetJobLogPath result")

		if err == nil {
			statInfo, statErr := os.Stat(jobLogPath)
			logger.WithFields(map[string]interface{}{
				"job_id":   job.ID,
				"log_path": jobLogPath,
				"stat_err": statErr,
				"exists":   statErr == nil,
				"file_size": func() int64 {
					if statInfo != nil {
						return statInfo.Size()
					}
					return 0
				}(),
			}).Debug("Fast path: checking job.log file")

			if statErr == nil {
				// Read the job.log file directly - it contains ANSI-formatted aglogs output
				content, readErr := os.ReadFile(jobLogPath)
				logger.WithFields(map[string]interface{}{
					"job_id":      job.ID,
					"read_err":    readErr,
					"content_len": len(content),
				}).Debug("Fast path: read job.log result")

				if readErr == nil && len(content) > 0 {
					logger.WithFields(map[string]interface{}{
						"log_path":    jobLogPath,
						"is_running":  job.Status == orchestration.JobStatusRunning,
						"content_len": len(content),
					}).Debug("Fast path: successfully read job logs from job.log")

					contentStr := string(content)
					shouldStream := job.Status == orchestration.JobStatusRunning || job.Status == orchestration.JobStatusIdle

					if shouldStream {
						if !verified {
							// No verified binding yet — never fall back to a
							// plan/job-name spec. Retry: the registry entry
							// may appear once the session registers.
							return LogContentLoadedMsg{
								Content:     contentStr + "\n" + unverifiedBindingNotice,
								ShouldRetry: true,
								JobID:       job.ID,
							}
						}

						// Prefer the direct transcript path for streaming.
						logSpec := claudeSessionID
						if transcriptPath != "" {
							logSpec = transcriptPath
						}

						// Fetch historical transcript via aglogs read to show full history.
						// Don't use job.log content — it only has orchestrator launch output.
						readCmd := delegation.Command("aglogs", "read", logSpec)
						readCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
						if readOutput, readErr := readCmd.Output(); readErr == nil && len(readOutput) > 0 {
							contentStr = string(readOutput)
						} else {
							contentStr = "[...] Waiting for agent transcript...\n"
						}

						return LogContentLoadedMsg{
							Content:        contentStr,
							ShouldRetry:    false,
							StartStreaming: true,
							LogFilePath:    logSpec,
							Provider:       sessionProvider,
							JobID:          job.ID,
						}
					}

					// Completed job - just show the content
					return LogContentLoadedMsg{
						Content:        contentStr,
						ShouldRetry:    false,
						StartStreaming: false,
						JobID:          job.ID,
					}
				} else {
					logger.WithFields(map[string]interface{}{
						"job_id":      job.ID,
						"read_err":    readErr,
						"content_len": len(content),
					}).Debug("Fast path: job.log read failed or empty")
				}
			} else {
				logger.WithFields(map[string]interface{}{
					"job_id":   job.ID,
					"log_path": jobLogPath,
					"stat_err": statErr,
				}).Debug("Fast path: job.log stat failed")
			}
		} else {
			logger.WithFields(map[string]interface{}{
				"job_id": job.ID,
				"error":  err,
			}).Debug("Fast path: GetJobLogPath failed")
		}

		// Fallback: job.log doesn't exist yet (running job just started)
		logger.WithFields(map[string]interface{}{
			"job_id":     job.ID,
			"job_status": job.Status,
		}).Debug("Fast path failed, checking fallback for running job")

		// For agent jobs, if status is pending but we're in the TUI (indicated by being called
		// from runJobsWithOrchestrator), the job is about to start. Treat it like a running job.
		// Idle status also indicates an agent waiting for input - still needs streaming.
		isPending := job.Status == orchestration.JobStatusPending
		isRunning := job.Status == orchestration.JobStatusRunning
		isIdle := job.Status == orchestration.JobStatusIdle
		isAgentJob := job.Type == orchestration.JobTypeHeadlessAgent || job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeIsolatedAgent

		logger.WithFields(map[string]interface{}{
			"job_id":       job.ID,
			"is_pending":   isPending,
			"is_running":   isRunning,
			"is_idle":      isIdle,
			"is_agent_job": isAgentJob,
			"will_stream":  isRunning || isIdle || (isPending && isAgentJob),
		}).Debug("Checking if job should use streaming fallback")

		if isRunning || isIdle || (isPending && isAgentJob) {
			if !verified {
				// Session not registered (yet). Never fall back to a
				// plan/job-name spec; retry until the registry entry shows up.
				logger.WithFields(map[string]interface{}{
					"job_id": job.ID,
				}).Debug("Session binding unverified; not streaming")
				return LogContentLoadedMsg{
					Content:     unverifiedBindingNotice + "\n(Waiting for the hooks session registry entry; this may take a few seconds)\n",
					ShouldRetry: true,
					JobID:       job.ID,
				}
			}

			// Use the native session id recorded by the registry
			// (e.g., ses_xxx for opencode, UUID for claude).
			logSpec := claudeSessionID

			// Try to read historical logs using aglogs read
			readCmd := delegation.Command("aglogs", "read", logSpec)
			readCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
			readOutput, readErr := readCmd.Output()

			logger.WithFields(map[string]interface{}{
				"log_spec":   logSpec,
				"output_len": len(readOutput),
				"error":      readErr,
			}).Debug("aglogs read completed for running job")

			if readErr != nil {
				// Session is still initializing, retry
				return LogContentLoadedMsg{
					Content:     "[...] Waiting for agent session to start...\n(This may take a few seconds)\n",
					ShouldRetry: true,
					JobID:       job.ID,
				}
			}

			content := string(readOutput)
			if content == "" {
				content = "[...] Agent session found, waiting for logs...\n"
			}

			return LogContentLoadedMsg{
				Content:        content,
				ShouldRetry:    false,
				StartStreaming: true,
				LogFilePath:    logSpec,
				Provider:       sessionProvider,
				JobID:          job.ID,
			}
		}

		// Fallback for completed jobs: aglogs read on the verified session id.
		// This handles the case where the job completed but job.log doesn't
		// have the formatted transcript yet.
		if verified {
			logger.WithFields(map[string]interface{}{
				"job_id":            job.ID,
				"claude_session_id": claudeSessionID,
				"job_status":        job.Status,
			}).Debug("Trying aglogs read fallback for completed job")

			readCmd := delegation.Command("aglogs", "read", claudeSessionID)
			readCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
			readOutput, readErr := readCmd.Output()

			logger.WithFields(map[string]interface{}{
				"job_id":     job.ID,
				"output_len": len(readOutput),
				"error":      readErr,
			}).Debug("aglogs read fallback result")

			if readErr == nil && len(readOutput) > 0 {
				return LogContentLoadedMsg{
					Content:     string(readOutput),
					ShouldRetry: false,
					JobID:       job.ID,
				}
			}
		} else {
			logger.WithFields(map[string]interface{}{
				"job_id":    job.ID,
				"job_title": job.Title,
			}).Warn("Session binding unverified for completed job; transcript not loaded")

			return LogContentLoadedMsg{
				Content:     theme.DefaultTheme.Muted.Render(unverifiedBindingNotice),
				ShouldRetry: false,
				JobID:       job.ID,
			}
		}

		// Completed job with no logs
		logger.WithFields(map[string]interface{}{
			"job_id":    job.ID,
			"job_title": job.Title,
		}).Warn("No agent logs found for completed job")

		return LogContentLoadedMsg{
			Content:     theme.DefaultTheme.Muted.Render(fmt.Sprintf("No agent logs found for completed job '%s'.", job.Title)),
			ShouldRetry: false,
			JobID:       job.ID,
		}
	}
}

// streamAgentLogsCmd creates a background process to stream agent logs from a
// specific file. provider selects the transcript normalizer through agentlogs'
// router ("" = claude, the historical default).
func streamAgentLogsCmd(ctx context.Context, plan *orchestration.Plan, job *orchestration.Job, logFilePath, provider string, msgCh chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		// Get the log file path for this job (for writing streamed content)
		jobLogPath, err := orchestration.GetJobLogPath(plan, job)
		if err != nil {
			return LogContentLoadedMsg{Err: fmt.Errorf("failed to get log path: %w", err), JobID: job.ID}
		}

		// Open the log file in append mode for writing streamed content
		logFile, err := os.OpenFile(jobLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return LogContentLoadedMsg{Err: fmt.Errorf("failed to open log file: %w", err), JobID: job.ID}
		}

		// Stream transcript entries in-process via agentstream
		entries, err := agentstream.Stream(ctx, agentstream.StreamOptions{
			TranscriptPath: logFilePath,
			Provider:       provider,
		})
		if err != nil {
			logFile.Close()
			go sendToMsgCh(msgCh, StreamEndedMsg{JobID: job.ID})
			return LogContentLoadedMsg{Err: fmt.Errorf("failed to start agentstream: %w", err), JobID: job.ID}
		}

		// Stream formatted entries to TUI and log file
		go func() {
			logger := logging.NewLogger("flow-tui")
			defer logFile.Close()

			// Notify the TUI that streaming ended so it can clear StreamingJobID
			defer sendToMsgCh(msgCh, StreamEndedMsg{JobID: job.ID})

			defer func() {
				if r := recover(); r != nil {
					logger.WithFields(map[string]interface{}{
						"panic": r,
					}).Warn("Recovered from panic in agent log streaming goroutine")
				}
			}()

			toolFmts := display.DefaultToolFormatters()
			lineCount := 0
			for entry := range entries {
				lineCount++
				formatted := display.FormatUnifiedEntry(entry, "normal", toolFmts)

				// Write to log file
				fmt.Fprintln(logFile, formatted)
				_ = logFile.Sync()

				// Hand the log line to the Update loop via the Model's channel.
				sendToMsgCh(msgCh, logviewer.LogLineMsg{
					Workspace: job.ID,
					Line:      formatted,
					NoPrefix:  true,
				})
			}

			logger.WithFields(map[string]interface{}{
				"line_count": lineCount,
			}).Debug("Agent log stream ended")
		}()

		return nil
	}
}

// JobSubmittedMsg is sent when jobs have been submitted to the daemon.
type JobSubmittedMsg struct {
	Jobs   []*orchestration.Job
	JobIDs []string // Daemon-assigned job IDs
	Err    error
}

// submitJobsViaDaemonCmd submits jobs to the daemon for execution.
// Unlike runJobsWithOrchestrator, this returns immediately — the daemon handles execution.
// hosted indicates the TUI is running inside groveterm (native panes) vs tmux.
func submitJobsViaDaemonCmd(client daemon.Client, plan *orchestration.Plan, jobs []*orchestration.Job, hosted bool) tea.Cmd {
	return func() tea.Msg {
		var jobIDs []string
		// The caller (TUI) knows whether it's hosted in groveterm.
		// No env var sniffing — the Hosted flag is set by the terminal
		// panel wrapper at construction time.
		agentTarget := "tmux"
		if hosted {
			agentTarget = "native"
		} else if mux.ActiveMux() == mux.MuxTuimux {
			agentTarget = "tuimux"
		}

		logging.NewUnifiedLogger("flow.submit").Info("Resolved agent target for TUI submission").
			Field("agent_target", agentTarget).
			Field("active_mux", string(mux.ActiveMux())).
			Field("tuimux_pty", os.Getenv("TUIMUX_PTY")).
			Field("grove_terminal", os.Getenv("GROVE_TERMINAL")).
			Field("hosted", hosted).
			StructuredOnly().Log(context.Background())

		for _, job := range jobs {
			info, err := client.SubmitJob(context.Background(), models.JobSubmitRequest{
				PlanDir:     plan.Directory,
				JobFile:     job.Filename,
				AgentTarget: agentTarget,
			})
			if err != nil {
				return JobSubmittedMsg{Jobs: jobs, Err: fmt.Errorf("submit %s: %w", job.Filename, err)}
			}
			jobIDs = append(jobIDs, info.ID)
		}
		return JobSubmittedMsg{Jobs: jobs, JobIDs: jobIDs}
	}
}

// DaemonLogLineMsg wraps a log line received from the daemon SSE stream.
type DaemonLogLineMsg struct {
	JobID string
	Line  string
}

// DaemonJobStatusMsg is sent when the daemon reports a job status change.
type DaemonJobStatusMsg struct {
	JobID  string
	Status string
	Error  string
}

// streamDaemonLogsCmd subscribes to the daemon's SSE log stream for a job
// and sends log lines to the TUI via the Model's MsgCh channel.
func streamDaemonLogsCmd(ctx context.Context, client daemon.Client, jobID string, msgCh chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ch, err := client.StreamJobLogs(ctx, jobID)
		if err != nil {
			go sendToMsgCh(msgCh, StreamEndedMsg{JobID: jobID})
			return LogContentLoadedMsg{
				Err:   fmt.Errorf("stream daemon logs: %w", err),
				JobID: jobID,
			}
		}

		// Stream in background goroutine
		go func() {
			defer func() {
				_ = recover() // TUI may have shut down
				// Ensure streaming state clears when SSE channel closes
				sendToMsgCh(msgCh, StreamEndedMsg{JobID: jobID})
			}()

			for event := range ch {
				select {
				case <-ctx.Done():
					return
				default:
				}

				switch event.Event {
				case "log":
					if event.Line != nil {
						sendToMsgCh(msgCh, logviewer.LogLineMsg{
							Workspace: jobID,
							Line:      event.Line.Line,
							NoPrefix:  true,
						})
					}
				case "status":
					sendToMsgCh(msgCh, DaemonJobStatusMsg{
						JobID:  jobID,
						Status: event.Status,
						Error:  event.Error,
					})
				}
			}
		}()

		return nil
	}
}

// cancelJobViaDaemonCmd cancels a running job via the daemon.
func cancelJobViaDaemonCmd(client daemon.Client, jobID string) tea.Cmd {
	return func() tea.Msg {
		if err := client.CancelJob(context.Background(), jobID); err != nil {
			return StatusUpdateMsg(fmt.Sprintf("Failed to cancel job: %v", err))
		}
		return StatusUpdateMsg("Job cancellation requested.")
	}
}

func renameJobCmd(plan *orchestration.Plan, job *orchestration.Job, newTitle string) tea.Cmd {
	return func() tea.Msg {
		err := orchestration.RenameJob(plan, job, newTitle)
		return RenameCompleteMsg{Err: err}
	}
}

func updateDepsCmd(job *orchestration.Job, newDeps []string) tea.Cmd {
	return func() tea.Msg {
		err := orchestration.UpdateJobDependencies(job, newDeps)
		return UpdateDepsCompleteMsg{Err: err}
	}
}

// blink returns a command that sends a tick message every 500ms for cursor blinking
func blink() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

const (
	// refreshInterval is the cadence of the plan-refresh tick that reloads
	// job status from disk.
	refreshInterval = 2 * time.Second
	// refreshStallThreshold is how long the refresh tick may go silent before
	// a regained focus treats the loop as dead and re-arms it. The tick
	// re-arms itself only by handling the message it produced, so a host that
	// drops that message (a background workspace session in the terminal
	// host, say) kills the loop permanently. Generous enough that a merely
	// busy event loop is never mistaken for a dead one.
	refreshStallThreshold = 3 * refreshInterval
)

// refreshTick returns a command that sends a refresh message periodically
func refreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return RefreshTickMsg(t)
	})
}

// runJobsWithOrchestrator executes jobs using the orchestrator and streams output to the TUI.
// IMPORTANT: This function spawns a background goroutine and returns immediately to avoid
// blocking the bubbletea event loop. The goroutine sends JobRunFinishedMsg when done.
func runJobsWithOrchestrator(orchestrator *orchestration.Orchestrator, jobs []*orchestration.Job, msgCh chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		logger := logging.NewLogger("flow-tui")
		logger.WithFields(map[string]interface{}{
			"num_jobs": len(jobs),
		}).Info("runJobsWithOrchestrator started - spawning background goroutine")

		// TUI mode is already set in newStatusTUIModel, but ensure it's set here too
		os.Setenv("GROVE_FLOW_TUI_MODE", "true")

		// Spawn background goroutine to run jobs - DO NOT block the tea.Cmd
		// This is critical to avoid deadlocks with bubbletea's event loop
		go func() {
			// Recover from any panics in the background goroutine
			defer func() {
				if r := recover(); r != nil {
					logger.WithFields(map[string]interface{}{
						"panic": r,
					}).Error("Panic recovered in runJobsWithOrchestrator background goroutine")
					sendToMsgCh(msgCh, JobRunFinishedMsg{Jobs: jobs, Err: fmt.Errorf("panic during job execution: %v", r)})
				}
			}()

			ctx := context.Background()

			// Create a stream writer for live TUI updates with smart tagging.
			// Uses the Model's channel rather than program.Send so this works
			// when the status_tui is embedded inside a host program.
			var writerTag string
			if len(jobs) == 1 {
				writerTag = jobs[0].ID
			} else {
				writerTag = "Job Output"
			}
			tuiWriter := newChanStreamWriter(msgCh, writerTag)

			if len(jobs) == 1 {
				tuiWriter.NoWorkspacePrefix = true
			}

			// Run jobs concurrently
			var wg sync.WaitGroup
			errChan := make(chan error, len(jobs))

			for _, job := range jobs {
				wg.Add(1)
				go func(j *orchestration.Job) {
					defer wg.Done()
					// Recover from panics in individual job goroutines
					defer func() {
						if r := recover(); r != nil {
							logger.WithFields(map[string]interface{}{
								"job_id": j.ID,
								"panic":  r,
							}).Error("Panic recovered in job execution goroutine")
							errChan <- fmt.Errorf("job %s panicked: %v", j.ID, r)
						}
					}()

					logger.WithFields(map[string]interface{}{
						"job_id":       j.ID,
						"job_filename": j.Filename,
					}).Info("Executing job via orchestrator")

					// Get the log file path for this specific job
					logFilePath, err := orchestration.GetJobLogPath(orchestrator.Plan, j)
					if err != nil {
						errChan <- fmt.Errorf("failed to get log path for job %s: %w", j.ID, err)
						return
					}

					// Open the job's log file for appending
					logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
					if err != nil {
						errChan <- fmt.Errorf("failed to open log file %s: %w", logFilePath, err)
						return
					}
					defer logFile.Close()

					// For agent jobs, don't write to TUI directly - agent log streaming handles that
					// via aglogs stream. Writing to both would cause duplicate output.
					isAgentJob := j.Type == orchestration.JobTypeInteractiveAgent ||
						j.Type == orchestration.JobTypeHeadlessAgent ||
						j.Type == orchestration.JobTypeIsolatedAgent

					var multiWriter io.Writer
					if isAgentJob {
						// Agent jobs use separate log streaming via aglogs, so only write to log file
						multiWriter = logFile
					} else {
						// Non-agent jobs write to both log file and TUI
						multiWriter = io.MultiWriter(logFile, tuiWriter)
					}

					// Execute the job
					if execErr := orchestrator.ExecuteJobWithWriter(ctx, j, multiWriter); execErr != nil {
						errChan <- fmt.Errorf("job %s failed: %w", j.ID, execErr)
					}
				}(job)
			}

			// Wait for all jobs to finish
			wg.Wait()

			close(errChan)
			var execErrors []error
			for err := range errChan {
				execErrors = append(execErrors, err)
			}

			// Send completion message to TUI
			if len(execErrors) > 0 {
				var errStrings []string
				for _, e := range execErrors {
					errStrings = append(errStrings, e.Error())
				}
				sendToMsgCh(msgCh, JobRunFinishedMsg{Jobs: jobs, Err: fmt.Errorf("%s", strings.Join(errStrings, "\n"))})
			} else {
				logger.Info("All jobs completed successfully")
				sendToMsgCh(msgCh, JobRunFinishedMsg{Jobs: jobs, Err: nil})
			}
		}()

		// Return nil immediately - the goroutine will send JobRunFinishedMsg when done
		return nil
	}
}

// runJobsCmd creates a tea.Cmd that executes one or more jobs in the background,
// streaming their output to a log file for the TUI to display.
// DEPRECATED: This is the old implementation that spawns CLI commands.
// Use runJobsWithOrchestrator instead.
func runJobsCmd(logFile, planDir string, jobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// This command runs in a goroutine managed by the Bubble Tea runtime.

		// Use the log file if provided, otherwise use a discard writer
		var f io.Writer
		var closer func()
		var sync func()
		if logFile != "" {
			file, err := os.Create(logFile)
			if err != nil {
				return JobRunFinishedMsg{Err: fmt.Errorf("failed to create log file: %w", err)}
			}
			f = file
			closer = func() { file.Close() }
			sync = func() { _ = file.Sync() }
		} else {
			// No log file - use a no-op writer
			f = io.Discard
			closer = func() {}
			sync = func() {}
		}
		defer closer()

		// Write initial status to log
		fmt.Fprintf(f, "Starting job execution...\n")
		fmt.Fprintf(f, "Plan directory: %s\n", planDir)
		fmt.Fprintf(f, "Running %d job(s):\n", len(jobs))
		for _, job := range jobs {
			fmt.Fprintf(f, "  - %s (%s)\n", job.Title, job.Filename)
		}
		fmt.Fprintf(f, "\n")
		sync() // Ensure it's written immediately

		// Build the command arguments
		// Run from the plan directory and pass just the filenames
		args := []string{"flow", "plan", "run", "--yes"}
		for _, job := range jobs {
			// Use just the filename, no path
			args = append(args, job.Filename)
		}

		// Log the command being run (format for readability)
		fmt.Fprintf(f, "Command (running from: %s):\n", planDir)
		fmt.Fprintf(f, "  grove flow plan run --yes")
		for _, job := range jobs {
			fmt.Fprintf(f, " %s", job.Filename)
		}
		fmt.Fprintf(f, "\n")
		fmt.Fprintf(f, "================================================================================\n\n")
		sync()

		// Use 'grove flow' to ensure proper environment setup for worktrees
		cmd := delegation.Command(args[0], args[1:]...)
		// Set the working directory to the plan directory
		cmd.Dir = planDir
		cmd.Stdout = f
		cmd.Stderr = f
		// Set an environment variable to indicate the job is run from the TUI
		cmd.Env = append(os.Environ(), "GROVE_FLOW_TUI_MODE=true")

		runErr := cmd.Run()

		// Log completion status
		fmt.Fprintf(f, "\n================================================================================\n")
		if runErr != nil {
			fmt.Fprintf(f, "Job execution failed: %v\n", runErr)
		} else {
			fmt.Fprintf(f, "Job execution completed successfully.\n")
		}
		sync()

		// After completion, return a message with the result.
		return JobRunFinishedMsg{Jobs: jobs, Err: runErr}
	}
}

// refreshPlan reloads the plan from disk
func refreshPlan(planDir string) tea.Cmd {
	return func() tea.Msg {
		return RefreshMsg{}
	}
}

func doArchiveJob(planDir string, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Archive the job by moving it to an archive directory
		archiveDir := filepath.Join(planDir, ".archive")
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			return err
		}

		oldPath := filepath.Join(planDir, job.Filename)
		newPath := filepath.Join(archiveDir, job.Filename)

		if err := os.Rename(oldPath, newPath); err != nil {
			return err
		}

		return nil // Just return nil, we'll refresh after
	}
}

func doArchiveJobs(planDir string, jobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Archive the jobs by moving them to an archive directory
		archiveDir := filepath.Join(planDir, ".archive")
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			return err
		}

		for _, job := range jobs {
			oldPath := filepath.Join(planDir, job.Filename)
			newPath := filepath.Join(archiveDir, job.Filename)

			if err := os.Rename(oldPath, newPath); err != nil {
				return err
			}
		}

		return nil // Just return nil, we'll refresh after
	}
}

func editJob(job *orchestration.Job, hosted bool) tea.Cmd {
	// If running inside Neovim plugin, signal to quit and let plugin handle editing
	if os.Getenv("GROVE_NVIM_PLUGIN") == "true" {
		return func() tea.Msg {
			return EditFileAndQuitMsg{FilePath: job.FilePath}
		}
	}

	// Check for mux popup
	if mux.ActiveMux() != mux.MuxNone {
		if engine, err := mux.DetectMuxEngine(context.Background()); err == nil {
			if tuiEngine, ok := engine.(mux.MuxTUIEngine); ok {
				isPopup, _ := tuiEngine.IsPopup(context.Background())
				if isPopup {
					return func() tea.Msg {
						editor := os.Getenv("EDITOR")
						if editor == "" {
							editor = "vi"
						}
						ctx := context.Background()
						err := tuiEngine.OpenInEditorWindow(ctx, editor, job.FilePath, "plan", 3, false)
						if err != nil {
							return EditFileInTmuxMsg{Err: err}
						}
						if err := tuiEngine.ClosePopup(ctx); err != nil {
							return EditFileInTmuxMsg{Err: fmt.Errorf("failed to close popup: %w", err)}
						}
						return EditFileInTmuxMsg{Err: nil}
					}
				}
			}
		}
	}

	// Hosted in groveterm: emit SplitEditorRequestMsg so the host creates a
	// BSP split with neovim alongside the plan panel.
	if hosted {
		return func() tea.Msg {
			return embed.SplitEditorRequestMsg{Path: job.FilePath, Focus: true}
		}
	}

	// Standalone: emit embed.EditRequestMsg. StandaloneHost translates it
	// into tea.ExecProcess transparently.
	return func() tea.Msg {
		return embed.EditRequestMsg{Path: job.FilePath}
	}
}

// PlanResumeMsg carries the outcome of a background `flow plan resume` run.
type PlanResumeMsg struct {
	JobTitle string
	Output   string
	Err      error
}

// executePlanResume runs `flow plan resume` in the background. The command
// never needs the terminal — it launches the agent into the plan's tmux,
// native, or tuimux target — so tea.ExecProcess (which suspends the whole TUI
// and, under treemux, briefly drops to the backing terminal) is wrong here.
// The outcome comes back as PlanResumeMsg so failures land in StatusSummary
// instead of vanishing.
func executePlanResume(job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		output, err := delegation.Command("flow", "plan", "resume", job.FilePath).CombinedOutput()
		return PlanResumeMsg{
			JobTitle: job.Title,
			Output:   strings.TrimSpace(string(output)),
			Err:      err,
		}
	}
}

// JobCompletedMsg is sent when a job completion attempt finishes
type JobCompletedMsg struct {
	Err error
}

// IsolatedAgentInputSentMsg is sent when input has been sent to an isolated agent
type IsolatedAgentInputSentMsg struct {
	JobID string
	Input string
	Err   error
}

// PollAgentStatusMsg triggers a poll for agent status
type PollAgentStatusMsg struct{}

// AgentStatusMsg contains the parsed agent status
type AgentStatusMsg struct {
	Status *AgentStatus
	JobID  string
	Err    error
}

func setJobCompleted(job *orchestration.Job, plan *orchestration.Plan, completeJobFunc func(*orchestration.Job, *orchestration.Plan, bool) error) tea.Cmd {
	return func() tea.Msg {
		// Use the shared completion function (silent mode for TUI)
		// Wrap in defer/recover to catch any panics from exec.Command calls
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic during job completion: %v", r)
				}
			}()
			err = completeJobFunc(job, plan, true)
		}()

		return JobCompletedMsg{Err: err}
	}
}

// setJobFieldCmd commits one scalar config field to every target job through the
// generic persistence layer (orchestration.StatePersister.UpdateJobFields, a thin
// wrapper over UpdateFrontmatter that bumps updated_at and syncs the in-memory
// Job). It is the single command behind the schema-driven field editor and the
// bool toggles, replacing the per-field setJob*/setMultiple* pairs. value is a
// string for enum/text fields and a bool for the memory/auto_complete toggles.
// Returns RefreshMsg on success so the caller's tea.Sequence + refreshPlan idiom
// still reloads the plan from disk.
func setJobFieldCmd(jobs []*orchestration.Job, fieldName string, value any) tea.Cmd {
	return func() tea.Msg {
		sp := orchestration.NewStatePersister()
		updates := map[string]interface{}{fieldName: value}
		for _, job := range jobs {
			if err := sp.UpdateJobFields(job, updates); err != nil {
				return err
			}
		}
		return RefreshMsg{}
	}
}

// DemoteJobMsg is returned after demoting a job to an nb note.
type DemoteJobMsg struct {
	NotePath string
	Err      error
}

// demoteJobCmd shells out to `flow plan demote <job-path>` to create an nb
// note from the job and mark it as abandoned.
func demoteJobCmd(job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("flow", "plan", "demote", job.FilePath) //nolint:gosec // job.FilePath is internal
		output, err := cmd.Output()
		if err != nil {
			return DemoteJobMsg{Err: fmt.Errorf("demote failed: %w", err)}
		}
		notePath := strings.TrimSpace(string(output))
		return DemoteJobMsg{NotePath: notePath}
	}
}

// createGenericJobWithTitle creates a new job with the given title and optional dependencies.
func createGenericJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		jobID := orchestration.GenerateUniqueJobID(plan, customTitle)

		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Inherit worktree from parent jobs, fall back to plan config default.
		worktree := ""
		if len(selectedJobs) > 0 {
			worktree = selectedJobs[0].Worktree
		}
		if worktree == "" && plan.Config != nil && plan.Config.Worktree != "" {
			worktree = plan.Config.Worktree
		}

		newJob := &orchestration.Job{
			ID:        jobID,
			Title:     customTitle,
			Status:    orchestration.JobStatusPending,
			DependsOn: depIDs,
			Worktree:  worktree,
		}

		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return CreateJobCompleteMsg{Err: err}
		}

		return CreateJobCompleteMsg{Err: nil}
	}
}

// createXmlPlanJobWithTitle creates an XML plan job with a custom title
func createXmlPlanJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		xmlID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:                  xmlID,
			Title:               customTitle,
			Type:                orchestration.JobTypeOneshot,
			Status:              orchestration.JobStatusPending,
			DependsOn:           depIDs,
			Worktree:            worktree,
			RulesFile:           selectedJobs[0].RulesFile,
			Template:            "agent-xml",
			PromptBody:          "generate a detailed plan",
			PrependDependencies: true,
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return CreateJobCompleteMsg{Err: err}
		}

		return CreateJobCompleteMsg{Err: nil}
	}
}

// createImplementationJobWithTitle creates an implementation job with a custom title
func createImplementationJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		implID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:        implID,
			Title:     customTitle,
			Type:      orchestration.JobTypeInteractiveAgent,
			Status:    orchestration.JobStatusPending,
			DependsOn: depIDs,
			Worktree:  worktree,
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return CreateJobCompleteMsg{Err: err}
		}

		return CreateJobCompleteMsg{Err: nil}
	}
}

func createAgentFromChatJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		jobID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job using the agent-from-chat template
		newJob := &orchestration.Job{
			ID:               jobID,
			Title:            customTitle,
			Type:             orchestration.JobTypeInteractiveAgent,
			Status:           orchestration.JobStatusPending,
			DependsOn:        depIDs,
			Worktree:         worktree,
			Template:         "agent-from-chat",
			GeneratePlanFrom: true,
			PromptBody:       "Implement the detailed plan that will be generated from the dependency.",
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return CreateJobCompleteMsg{Err: err}
		}

		return CreateJobCompleteMsg{Err: nil}
	}
}

// sendIsolatedAgentInputCmd sends input to an isolated agent via its dedicated tmux socket
func sendIsolatedAgentInputCmd(jobID, input string) tea.Cmd {
	return func() tea.Msg {
		err := orchestration.SendInputToIsolatedAgent(jobID, input)
		return IsolatedAgentInputSentMsg{
			JobID: jobID,
			Input: input,
			Err:   err,
		}
	}
}

// sendInteractiveAgentInputCmd sends input to an interactive agent via the worktree tmux session
func sendInteractiveAgentInputCmd(plan *orchestration.Plan, job *orchestration.Job, input string) tea.Cmd {
	return func() tea.Msg {
		err := orchestration.SendInputToInteractiveAgent(plan, job, input)
		return IsolatedAgentInputSentMsg{
			JobID: job.ID,
			Input: input,
			Err:   err,
		}
	}
}

// pollAgentStatusAfterDelay creates a command that waits and then triggers a status poll
func pollAgentStatusAfterDelay() tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(t time.Time) tea.Msg {
		return PollAgentStatusMsg{}
	})
}

// fetchAgentStatusCmd fetches the current agent status. Claude sessions are
// pane-scraped (the historical ParseAgentPane path, unchanged); other
// providers' TUIs are never scraped — their status is derived from transcript
// activity via agentstream.DeriveTranscriptStatus (recency + in-flight tool
// calls), so only the provider-neutral State/Activity fields are populated.
func fetchAgentStatusCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		logger := logging.NewLogger("flow-tui")

		// The session registry records the provider that actually launched.
		var provider, transcriptPath string
		if registry, regErr := sessions.NewFileSystemRegistry(); regErr == nil {
			if metadata, findErr := registry.Find(job.ID); findErr == nil && metadata != nil {
				provider = metadata.Provider
				transcriptPath = metadata.TranscriptPath
			}
		}

		if provider != "" && provider != "claude" {
			return fetchTranscriptAgentStatus(job, provider, transcriptPath)
		}

		var output string
		var err error

		if job.Type == orchestration.JobTypeIsolatedAgent {
			output, err = orchestration.CaptureIsolatedAgentOutput(job.ID)
		} else if job.Type == orchestration.JobTypeInteractiveAgent {
			output, err = orchestration.CaptureInteractiveAgentOutput(plan, job)
		} else {
			return AgentStatusMsg{JobID: job.ID, Err: fmt.Errorf("job type %s does not support status polling", job.Type)}
		}

		if err != nil {
			logger.WithFields(map[string]interface{}{
				"job_id": job.ID,
				"error":  err.Error(),
			}).Debug("Agent tmux capture failed")
			return AgentStatusMsg{JobID: job.ID, Err: err}
		}

		status := ParseAgentPane(output)

		var stateStr string
		var tokens int
		if status != nil {
			stateStr = status.State
			tokens = status.TotalTokens
		}

		logger.WithFields(map[string]interface{}{
			"job_id":     job.ID,
			"state":      stateStr,
			"tokens":     tokens,
			"output_len": len(output),
		}).Debug("Agent status captured")
		return AgentStatusMsg{Status: status, JobID: job.ID, Err: nil}
	}
}

// fetchTranscriptAgentStatus derives a non-Claude agent's status from its
// transcript. When the registry didn't record a transcript path (e.g.
// opencode's plugin-deferred registration), agentlogs' tiered resolver fills
// it in by job ID.
func fetchTranscriptAgentStatus(job *orchestration.Job, provider, transcriptPath string) tea.Msg {
	if transcriptPath == "" {
		if info, err := aglogsession.Resolve(job.ID); err == nil && info.LogFilePath != "" {
			transcriptPath = info.LogFilePath
			if info.Provider != "" {
				provider = info.Provider
			}
		}
	}
	if transcriptPath == "" {
		return AgentStatusMsg{JobID: job.ID, Err: fmt.Errorf("no transcript recorded yet for %s session", provider)}
	}
	status, err := agentstream.DeriveTranscriptStatus(transcriptPath, provider, time.Now())
	if err != nil {
		return AgentStatusMsg{JobID: job.ID, Err: err}
	}
	return AgentStatusMsg{Status: status, JobID: job.ID, Err: nil}
}

// InterruptAgentMsg is sent when an agent interrupt attempt finishes
type InterruptAgentMsg struct {
	JobID string
	Err   error
}

// interruptAgentCmd sends Ctrl+C to interrupt the running agent
func interruptAgentCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		if job == nil {
			return InterruptAgentMsg{Err: fmt.Errorf("no job to interrupt")}
		}

		var err error
		if job.Type == orchestration.JobTypeIsolatedAgent {
			// For isolated agents, use dedicated socket: tmux -L flow-job-<jobID> send-keys -t main:0 C-c
			err = orchestration.SendInterruptToIsolatedAgent(job.ID)
		} else if job.Type == orchestration.JobTypeInteractiveAgent {
			// For interactive agents, use worktree session
			err = orchestration.SendInterruptToInteractiveAgent(plan, job)
		} else {
			err = fmt.Errorf("job type %s does not support interrupt", job.Type)
		}

		return InterruptAgentMsg{JobID: job.ID, Err: err}
	}
}

// addJobsFromRecipeCmd creates a command to add jobs from a recipe to the plan
func addJobsFromRecipeCmd(plan *orchestration.Plan, recipeName string, externalDeps []string) tea.Cmd {
	return func() tea.Msg {
		recipe, err := orchestration.GetRecipe(recipeName, "")
		if err != nil {
			return recipeAddedMsg{err: err}
		}

		// For now, we will use empty template data in the TUI
		templateData := struct {
			PlanName string
			Vars     map[string]string
		}{
			PlanName: plan.Name,
			Vars:     make(map[string]string),
		}

		_, err = orchestration.AddJobsFromRecipe(plan, recipe, externalDeps, templateData)
		return recipeAddedMsg{err: err}
	}
}

// editSkillOrArtifactCmd emits an embed.EditRequestMsg so the host
// (groveterm in-pane editor or StandaloneHost's tea.ExecProcess)
// opens $EDITOR on a skill or artifact file.
func editSkillOrArtifactCmd(plan *orchestration.Plan, job *orchestration.Job, node *SkillPaneNode, hostWorkDir string) tea.Cmd {
	var targetPath string
	if node.IsArtifact {
		targetPath = filepath.Join(plan.Directory, ".artifacts", job.ID, node.FilePath)
	} else {
		// For skill nodes, open the SKILL.md from the skills directory
		// Try to find the skill file in the workspace. Prefer the host's
		// active workspace path over os.Getwd() — the latter is pinned to
		// the host process's launch directory when embedded in treemux.
		workDir := plan.Directory
		if plan.Config != nil && plan.Config.Worktree != "" {
			if hostWorkDir != "" {
				workDir = hostWorkDir
			} else if wd, err := os.Getwd(); err == nil {
				workDir = wd
			}
		}
		skillPath := filepath.Join(workDir, ".grove", "skills", node.Name, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			// Fall back to artifact directory status file
			targetPath = filepath.Join(plan.Directory, ".artifacts", job.ID, node.Name+"-status.json")
		} else {
			targetPath = skillPath
		}
	}

	if targetPath == "" {
		return func() tea.Msg {
			return embed.EditFinishedMsg{Err: fmt.Errorf("could not resolve path for %s", node.Name)}
		}
	}

	return func() tea.Msg {
		return embed.EditRequestMsg{Path: targetPath}
	}
}

// --- Claw (channels + autonomous) commands ---

type clawResultMsg struct {
	JobID   string
	Enabled bool
	Err     error
}

func clawJobCmd(plan *orchestration.Plan, job *orchestration.Job, idleMinutes int, prompt, signalTarget string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// Session-record mutation: must reach the daemon that HOLDS the
		// session, which is the interactive host's when one published itself
		// (daemon.HostSocketEnv), not whatever GROVE_SCOPE happens to say.
		client := daemon.NewSessionHostClient("")
		defer client.Close()

		// Enable signal channel
		if err := client.UpdateSessionChannels(ctx, job.ID, models.SessionChannelsRequest{Channels: []string{"signal"}, SignalTarget: signalTarget}); err != nil {
			return clawResultMsg{JobID: job.ID, Err: fmt.Errorf("enable channels: %w", err)}
		}

		// Enable autonomous pinger
		if err := client.UpdateSessionAutonomous(ctx, job.ID, &models.AutonomousConfig{
			Enabled:     true,
			IdleMinutes: idleMinutes,
			Prompt:      prompt,
		}); err != nil {
			return clawResultMsg{JobID: job.ID, Err: fmt.Errorf("enable autonomous: %w", err)}
		}

		// Resolve tmux target and notify the agent
		targetPane, err := orchestration.ResolveInteractiveAgentPane(plan, job)
		if err == nil && targetPane != "" {
			_ = client.UpdateSessionTmuxTarget(ctx, job.ID, targetPane)

			notifyCfg := notifyconfig.Load()
			instructions := notifications.AgentInstructions(notifyCfg, []string{"signal"}, signalTarget)
			if instructions != "" {
				msg := fmt.Sprintf("System: Signal messaging and autonomous mode have been enabled for this session.\n\n%s", instructions)
				if engine, err := mux.DetectMuxEngine(ctx); err == nil {
					_ = engine.SendKeys(ctx, targetPane, msg, "C-m")
				}
			}
		}

		// Update job frontmatter so TUI indicator shows correctly
		if job.FilePath != "" {
			content, err := os.ReadFile(job.FilePath)
			if err == nil {
				// Channels + signal_target (simple values — UpdateFrontmatter handles fine)
				updates := map[string]any{"channels": []string{"signal"}}
				if signalTarget != "" {
					updates["signal_target"] = signalTarget
				}
				if newContent, err := orchestration.UpdateFrontmatter(content, updates); err == nil {
					// Autonomous (nested struct — insert manually before closing ---)
					s := string(newContent)
					// Remove existing autonomous block first
					if idx := strings.Index(s, "autonomous:\n"); idx >= 0 {
						end := idx + len("autonomous:\n")
						remaining := s[end:]
						for _, line := range strings.SplitAfter(remaining, "\n") {
							if strings.HasPrefix(line, "  ") {
								end += len(line)
							} else {
								break
							}
						}
						s = s[:idx] + s[end:]
					}
					autoYAML := fmt.Sprintf("autonomous:\n  enabled: true\n  idle_minutes: %d", idleMinutes)
					if prompt != "" {
						autoYAML += fmt.Sprintf("\n  prompt: %q", prompt)
					}
					if idx := strings.LastIndex(s, "\n---\n"); idx >= 0 {
						s = s[:idx] + "\n" + autoYAML + s[idx:]
					}
					_ = os.WriteFile(job.FilePath, []byte(s), 0o600)
				}
			}
		}

		return clawResultMsg{JobID: job.ID, Enabled: true}
	}
}

func unclawJobCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		client := daemon.NewSessionHostClient("") // see clawJobCmd
		defer client.Close()

		_ = client.UpdateSessionChannels(ctx, job.ID, models.SessionChannelsRequest{})
		_ = client.UpdateSessionAutonomous(ctx, job.ID, &models.AutonomousConfig{Enabled: false})

		// Remove from frontmatter — set channels to empty and remove autonomous block
		if job.FilePath != "" {
			content, err := os.ReadFile(job.FilePath)
			if err == nil {
				updates := map[string]any{"channels": []string{}, "signal_target": ""}
				if newContent, err := orchestration.UpdateFrontmatter(content, updates); err == nil {
					s := string(newContent)
					// Remove signal_target line if still present
					if idx := strings.Index(s, "\nsignal_target:"); idx >= 0 {
						end := idx + 1
						line := s[end:]
						lineEnd := strings.Index(line, "\n")
						if lineEnd >= 0 {
							end += lineEnd + 1
						} else {
							end += len(line)
						}
						s = s[:idx+1] + s[end:]
					}
					// Remove autonomous block (simple string removal)
					// Remove "autonomous:\n  enabled: ...\n  idle_minutes: ...\n  prompt: ...\n" block
					if idx := strings.Index(s, "autonomous:\n"); idx >= 0 {
						end := idx
						lines := strings.Split(s[idx:], "\n")
						end += len(lines[0]) + 1 // "autonomous:\n"
						for i := 1; i < len(lines); i++ {
							if strings.HasPrefix(lines[i], "  ") {
								end += len(lines[i]) + 1
							} else {
								break
							}
						}
						s = s[:idx] + s[end:]
					}
					_ = os.WriteFile(job.FilePath, []byte(s), 0o600)
				}
			}
		}

		return clawResultMsg{JobID: job.ID, Enabled: false}
	}
}
