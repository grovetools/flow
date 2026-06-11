package orchestration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/grovetools/agentlogs/pkg/display"
	"github.com/grovetools/agentlogs/pkg/transcript"
	agentworkflow "github.com/grovetools/agentlogs/pkg/workflow"
	"github.com/grovetools/core/fs"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/paths"
	coresessions "github.com/grovetools/core/pkg/sessions"
)

// ArchiveContextRules copies the active rules file to the job's artifact directory
// and updates the job's frontmatter to track it.
// For oneshot/shell jobs this creates a single context.rules artifact.
func ArchiveContextRules(job *Job, plan *Plan, usedRulesPath string) error {
	if usedRulesPath == "" {
		return nil
	}

	relPath, err := archiveRulesFile(plan, job.ID, "context.rules", usedRulesPath)
	if err != nil {
		return err
	}

	// Update job struct
	job.UsedRulesFile = relPath

	// Update job file frontmatter directly
	jobContent, err := os.ReadFile(job.FilePath)
	if err == nil {
		updates := map[string]interface{}{
			"used_rules_file": relPath,
		}
		newContent, err := UpdateFrontmatter(jobContent, updates)
		if err == nil {
			_ = os.WriteFile(job.FilePath, newContent, 0o600)
		}
	}

	return nil
}

// ArchiveContextRulesForTurn copies the active rules file to a per-turn artifact
// within the job's artifact directory. Returns the relative artifact path for
// inclusion in the turn's <!-- grove: {} --> metadata tag.
// Returns "" if usedRulesPath is empty (no rules to archive).
func ArchiveContextRulesForTurn(plan *Plan, jobID, turnID, usedRulesPath string) (string, error) {
	if usedRulesPath == "" {
		return "", nil
	}

	filename := turnID + "-context.rules"
	return archiveRulesFile(plan, jobID, filename, usedRulesPath)
}

// archiveRulesFile copies usedRulesPath into .artifacts/{jobID}/{filename}
// and returns the relative artifact path.
func archiveRulesFile(plan *Plan, jobID, filename, usedRulesPath string) (string, error) {
	content, err := os.ReadFile(usedRulesPath)
	if err != nil {
		return "", fmt.Errorf("failed to read used rules file: %w", err)
	}

	destArtifactDir := filepath.Join(plan.Directory, ".artifacts", jobID)
	if err := os.MkdirAll(destArtifactDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create artifact directory: %w", err)
	}

	destRulesPath := filepath.Join(destArtifactDir, filename)
	if err := os.WriteFile(destRulesPath, content, 0o600); err != nil {
		return "", fmt.Errorf("failed to write archived rules file: %w", err)
	}

	return filepath.Join(".artifacts", jobID, filename), nil
}

// ArchiveInteractiveSession copies session metadata and the transcript to the plan's artifacts.
func ArchiveInteractiveSession(job *Job, plan *Plan) error {
	ctx := context.Background()

	ulog.Debug("[ARCHIVE] Starting session archival").
		Field("job_id", job.ID).
		Field("job_type", string(job.Type)).
		Field("plan_name", plan.Name).
		Log(ctx)

	// This function should only operate on jobs that have a native agent session.
	if job.Type != JobTypeInteractiveAgent && job.Type != JobTypeHeadlessAgent && job.Type != JobTypeIsolatedAgent {
		ulog.Debug("[ARCHIVE] Skipping non-agent job type").
			Field("job_id", job.ID).
			Field("job_type", string(job.Type)).
			Log(ctx)
		return nil
	}

	// 1. Find the session metadata.
	sessionsBaseDir := filepath.Join(paths.StateDir(), "hooks", "sessions")
	ulog.Debug("[ARCHIVE] Looking for session registry").
		Field("job_id", job.ID).
		Field("sessions_base_dir", sessionsBaseDir).
		Log(ctx)

	// List all sessions in the registry for debugging
	entries, listErr := os.ReadDir(sessionsBaseDir)
	if listErr != nil {
		ulog.Debug("[ARCHIVE] Failed to list sessions directory").
			Field("sessions_base_dir", sessionsBaseDir).
			Err(listErr).
			Log(ctx)
	} else {
		sessionIDs := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				sessionIDs = append(sessionIDs, entry.Name())
			}
		}
		ulog.Debug("[ARCHIVE] Available sessions in registry").
			Field("session_count", len(sessionIDs)).
			Field("session_ids", sessionIDs).
			Log(ctx)
	}

	registry, err := coresessions.NewFileSystemRegistry()
	if err != nil {
		ulog.Error("[ARCHIVE] Failed to create session registry").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return fmt.Errorf("failed to create session registry: %w", err)
	}

	ulog.Debug("[ARCHIVE] Searching for session by job ID").
		Field("job_id", job.ID).
		Log(ctx)

	metadata, err := registry.Find(job.ID)
	if err != nil {
		ulog.Error("[ARCHIVE] Failed to find session metadata").
			Field("job_id", job.ID).
			Field("sessions_base_dir", sessionsBaseDir).
			Err(err).
			Log(ctx)
		return fmt.Errorf("failed to find session metadata for job %s: %w", job.ID, err)
	}

	ulog.Debug("[ARCHIVE] Found session metadata").
		Field("job_id", job.ID).
		Field("claude_session_id", metadata.ClaudeSessionID).
		Field("transcript_path", metadata.TranscriptPath).
		Field("provider", metadata.Provider).
		Log(ctx)

	// 2. Construct the source session directory path.
	// Sessions are stored at $XDG_STATE_HOME/grove/hooks/sessions/{claude-session-id}/
	sourceSessionDir := filepath.Join(sessionsBaseDir, metadata.ClaudeSessionID)
	sourceMetadataPath := filepath.Join(sourceSessionDir, "metadata.json")

	ulog.Debug("[ARCHIVE] Checking source session directory").
		Field("source_session_dir", sourceSessionDir).
		Field("source_metadata_path", sourceMetadataPath).
		Log(ctx)

	if _, statErr := os.Stat(sourceMetadataPath); statErr != nil {
		ulog.Error("[ARCHIVE] Source metadata.json not found").
			Field("source_metadata_path", sourceMetadataPath).
			Err(statErr).
			Log(ctx)
	}

	// 3. Define the destination artifact path.
	destArtifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	ulog.Debug("[ARCHIVE] Creating artifact directory").
		Field("dest_artifact_dir", destArtifactDir).
		Log(ctx)

	if err := os.MkdirAll(destArtifactDir, 0o755); err != nil {
		ulog.Error("[ARCHIVE] Failed to create artifact directory").
			Field("dest_artifact_dir", destArtifactDir).
			Err(err).
			Log(ctx)
		return fmt.Errorf("failed to create artifact directory %s: %w", destArtifactDir, err)
	}

	// 4. Copy metadata.json.
	destMetadataPath := filepath.Join(destArtifactDir, "metadata.json")
	ulog.Debug("[ARCHIVE] Copying metadata.json").
		Field("source", sourceMetadataPath).
		Field("dest", destMetadataPath).
		Log(ctx)

	if err := fs.CopyFile(sourceMetadataPath, destMetadataPath); err != nil {
		ulog.Error("[ARCHIVE] Failed to copy metadata.json").
			Field("source", sourceMetadataPath).
			Field("dest", destMetadataPath).
			Err(err).
			Log(ctx)
		return fmt.Errorf("failed to copy metadata.json: %w", err)
	}

	// 5. Copy the transcript file.
	if metadata.TranscriptPath != "" {
		destTranscriptPath := filepath.Join(destArtifactDir, "transcript.jsonl")
		ulog.Debug("[ARCHIVE] Copying transcript file").
			Field("source", metadata.TranscriptPath).
			Field("dest", destTranscriptPath).
			Log(ctx)

		if err := fs.CopyFile(metadata.TranscriptPath, destTranscriptPath); err != nil {
			ulog.Error("[ARCHIVE] Failed to copy transcript file").
				Field("source", metadata.TranscriptPath).
				Field("dest", destTranscriptPath).
				Err(err).
				Log(ctx)
			return fmt.Errorf("failed to copy transcript file from %s: %w", metadata.TranscriptPath, err)
		}
	} else {
		ulog.Warn("[ARCHIVE] No transcript path in metadata").
			Field("job_id", job.ID).
			Field("claude_session_id", metadata.ClaudeSessionID).
			Log(ctx)
	}

	ulog.Debug("[ARCHIVE] Session archival completed successfully").
		Field("job_id", job.ID).
		Field("dest_artifact_dir", destArtifactDir).
		Log(ctx)

	return nil
}

// workflowRunsSectionHeader is the job-.md section holding the durable
// per-run workflow record. It is rebuilt wholesale on every completion and
// always inserted before the agent chat transcript section.
const workflowRunsSectionHeader = "# Workflow Runs"

// workflowResultCapLines is the maximum number of result lines embedded in
// the job .md's workflow section per agent; longer results are truncated
// with a link into .artifacts (results are unbounded — never inline them
// whole into the job file).
const workflowResultCapLines = 15

// archivedWorkflowRun is the per-run data collected while archiving, used
// to render the job .md's "# Workflow Runs" section.
type archivedWorkflowRun struct {
	RunID string
	// Title is the human-readable run name: script meta name, script
	// filename prefix, or the run ID as a last resort.
	Title string
	Run   *agentworkflow.WorkflowRun
	// Durations holds per-agent wall-clock durations computed from the
	// daemon's enriched events.jsonl (the raw journal has no timestamps).
	Durations map[string]time.Duration
	// AgentDocs marks agents whose rendered markdown transcript exists at
	// agents/agent-<id>.md under the archived run dir.
	AgentDocs map[string]bool
}

// ArchiveWorkflowRuns copies Claude Code workflow run artifacts (journal,
// orchestration script, daemon-enriched events, rendered per-agent markdown
// transcripts, generated summary, and optionally raw per-agent jsonl) from
// the session's directories under ~/.claude/projects/ into
// plans/<plan>/.artifacts/<job-id>/workflows/<runId>/, then rebuilds the
// job .md's "# Workflow Runs" section. Sessions with no workflow runs are a
// silent no-op, as is an unverified session binding (no registry entry or
// no recorded Claude session ID).
//
// Session artifacts fragment across project-slug dirs when the shell cwd
// changes mid-session (a workflow's runs can land under the worktree slug
// while its scripts land under a submodule slug), so discovery resolves
// every slug dir holding this session and merges what it finds, rather than
// constructing a single path from the transcript location.
func ArchiveWorkflowRuns(job *Job, plan *Plan) error {
	ctx := context.Background()

	if job.Type != JobTypeInteractiveAgent && job.Type != JobTypeHeadlessAgent && job.Type != JobTypeIsolatedAgent {
		return nil
	}

	registry, err := coresessions.NewFileSystemRegistry()
	if err != nil {
		return fmt.Errorf("failed to create session registry: %w", err)
	}

	metadata, err := registry.Find(job.ID)
	if err != nil || metadata.TranscriptPath == "" || metadata.ClaudeSessionID == "" {
		// Unverified binding: no registry entry for the job, or the entry
		// never recorded the Claude session. Silent skip — there is nothing
		// trustworthy to archive and no section to write.
		ulog.Debug("[ARCHIVE] Session binding unverified; skipping workflow archival").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return nil
	}

	sessionDirs, dirsErr := coresessions.ResolveClaudeSessionDirs(metadata.ClaudeSessionID)
	if dirsErr != nil || len(sessionDirs) == 0 {
		// Fall back to the single path constructed next to the transcript
		// (~/.claude/projects/<slug>/<session-id>.jsonl with workflow runs
		// in the sibling <session-id>/ directory).
		sessionDirs = []string{filepath.Join(filepath.Dir(metadata.TranscriptPath), metadata.ClaudeSessionID)}
	}

	includeTranscripts := plan.Config != nil && plan.Config.ArchiveAgentTranscripts
	daemonEventsDir := filepath.Join(paths.StateDir(), "daemon", "workflows")
	return archiveWorkflowRunsFromDirs(ctx, job, plan, sessionDirs, daemonEventsDir, includeTranscripts)
}

// archiveWorkflowRunsFromDirs archives every wf_* run found under any of the
// resolved session dirs, searching all dirs' workflows/scripts/ for each
// run's persisted script. Runs are deduped by run ID across dirs. After
// archiving, the job .md's "# Workflow Runs" section is rebuilt wholesale
// from all archived runs (no per-run splices); no runs means no section.
func archiveWorkflowRunsFromDirs(ctx context.Context, job *Job, plan *Plan, sessionDirs []string, daemonEventsDir string, includeTranscripts bool) error {
	var scriptsDirs []string
	for _, dir := range sessionDirs {
		scriptsDirs = append(scriptsDirs, filepath.Join(dir, "workflows", "scripts"))
	}

	var archived []*archivedWorkflowRun
	seen := make(map[string]bool)
	for _, dir := range sessionDirs {
		runsDir := filepath.Join(dir, "subagents", "workflows")
		runs, err := os.ReadDir(runsDir)
		if err != nil {
			// No subagents/workflows directory under this slug.
			continue
		}

		for _, run := range runs {
			if !run.IsDir() || !strings.HasPrefix(run.Name(), "wf_") || seen[run.Name()] {
				continue
			}
			seen[run.Name()] = true
			srcRunDir := filepath.Join(runsDir, run.Name())
			destRunDir := filepath.Join(plan.Directory, ".artifacts", job.ID, "workflows", run.Name())
			ar, err := archiveWorkflowRun(ctx, srcRunDir, scriptsDirs, destRunDir, daemonEventsDir, includeTranscripts)
			if err != nil {
				// Never fail job completion over a single run's artifacts.
				ulog.Warn("[ARCHIVE] Failed to archive workflow run").
					Field("job_id", job.ID).
					Field("run_id", run.Name()).
					Err(err).
					Log(ctx)
				continue
			}
			archived = append(archived, ar)
			ulog.Debug("[ARCHIVE] Workflow run archived").
				Field("job_id", job.ID).
				Field("run_id", run.Name()).
				Field("dest", destRunDir).
				Log(ctx)
		}
	}

	// Rebuild the job .md's "# Workflow Runs" section from ALL archived
	// runs. No runs → no section (and an existing section is left alone:
	// a later partial re-archive must not erase a previously good record).
	if len(archived) > 0 && job.FilePath != "" {
		sort.Slice(archived, func(i, j int) bool { return archived[i].RunID < archived[j].RunID })
		content := renderWorkflowRunsSection(job.ID, archived)
		if _, err := NewStatePersister().UpdateJobSection(job, workflowRunsSectionHeader, content, transcriptSectionHeader, false); err != nil {
			ulog.Warn("[ARCHIVE] Failed to update workflow runs section in job file").
				Field("job_id", job.ID).
				Field("filepath", job.FilePath).
				Err(err).
				Log(ctx)
		}
	}

	return nil
}

// archiveWorkflowRun copies one wf_* run directory's durable artifacts into
// destRunDir and writes a generated summary.md. The run's persisted script
// is searched across all scriptsDirs (slug fragmentation can put it in a
// different session dir than the run itself). The daemon's enriched event
// log (wall-clock timestamps) is merged in as events.jsonl when present,
// and each agent transcript is rendered to agents/agent-<id>.md (always on;
// raw jsonl copies stay behind the archive_agent_transcripts flag).
func archiveWorkflowRun(ctx context.Context, srcRunDir string, scriptsDirs []string, destRunDir, daemonEventsDir string, includeTranscripts bool) (*archivedWorkflowRun, error) {
	runID := filepath.Base(srcRunDir)

	if err := os.MkdirAll(destRunDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create workflow artifact directory: %w", err)
	}

	journalSrc := filepath.Join(srcRunDir, "journal.jsonl")
	if _, err := os.Stat(journalSrc); err == nil {
		if err := fs.CopyFile(journalSrc, filepath.Join(destRunDir, "journal.jsonl")); err != nil {
			return nil, fmt.Errorf("failed to copy journal.jsonl: %w", err)
		}
	}

	run, err := agentworkflow.ReadWorkflowRun(srcRunDir, scriptsDirs)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow run: %w", err)
	}

	scriptPath, scriptTitle := findWorkflowScript(scriptsDirs, runID)
	if scriptPath != "" {
		if err := fs.CopyFile(scriptPath, filepath.Join(destRunDir, "script.js")); err != nil {
			return nil, fmt.Errorf("failed to copy workflow script: %w", err)
		}
	}
	title := scriptTitle
	if run.Meta != nil && run.Meta.Name != "" {
		title = run.Meta.Name
	}
	if title == "" {
		title = runID
	}

	// Merge the daemon's enriched event log: it carries the wall-clock
	// timestamps the raw journal lacks, so the durable record gets ordering
	// and per-agent durations.
	var durations map[string]time.Duration
	daemonEvents := filepath.Join(daemonEventsDir, runID+".jsonl")
	if _, err := os.Stat(daemonEvents); err == nil {
		if err := fs.CopyFile(daemonEvents, filepath.Join(destRunDir, "events.jsonl")); err != nil {
			ulog.Warn("[ARCHIVE] Failed to copy daemon workflow events").
				Field("run_id", runID).
				Err(err).
				Log(ctx)
		} else {
			durations = loadAgentDurations(filepath.Join(destRunDir, "events.jsonl"))
		}
	}

	// Render per-agent markdown transcripts (always on — these are small
	// relative to the raw jsonl and environment-independent).
	agentDocs := make(map[string]bool)
	for _, agentID := range sortedAgentIDs(run) {
		agent := run.Agents[agentID]
		if len(agent.Entries) == 0 {
			continue
		}
		if err := writeAgentMarkdown(destRunDir, runID, agentID, agent.Entries); err != nil {
			ulog.Warn("[ARCHIVE] Failed to render agent markdown transcript").
				Field("run_id", runID).
				Field("agent_id", agentID).
				Err(err).
				Log(ctx)
			continue
		}
		agentDocs[agentID] = true
	}

	summary := generateWorkflowSummary(title, runID, run, agentDocs)
	if err := os.WriteFile(filepath.Join(destRunDir, "summary.md"), []byte(summary), 0o600); err != nil {
		return nil, fmt.Errorf("failed to write summary.md: %w", err)
	}

	if includeTranscripts {
		entries, err := os.ReadDir(srcRunDir)
		if err != nil {
			return nil, fmt.Errorf("failed to list workflow run directory: %w", err)
		}
		agentsDir := filepath.Join(destRunDir, "agents")
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-") {
				continue
			}
			if err := os.MkdirAll(agentsDir, 0o755); err != nil {
				return nil, fmt.Errorf("failed to create agents directory: %w", err)
			}
			src := filepath.Join(srcRunDir, entry.Name())
			if err := fs.CopyFile(src, filepath.Join(agentsDir, entry.Name())); err != nil {
				ulog.Warn("[ARCHIVE] Failed to copy agent transcript").
					Field("run_id", runID).
					Field("file", entry.Name()).
					Err(err).
					Log(ctx)
			}
		}
	}

	return &archivedWorkflowRun{
		RunID:     runID,
		Title:     title,
		Run:       run,
		Durations: durations,
		AgentDocs: agentDocs,
	}, nil
}

// writeAgentMarkdown renders one agent's transcript entries to
// agents/agent-<id>.md under the archived run dir, via the agentlogs
// markdown renderer (stable role labels, injection-safe indented tool
// blocks, no TTY/theme dependence).
func writeAgentMarkdown(destRunDir, runID, agentID string, entries []transcript.UnifiedEntry) error {
	agentsDir := filepath.Join(destRunDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create agents directory: %w", err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Agent `%s` — %s\n\n", agentID, runID)
	opts := display.RenderOptions{Style: display.StyleMarkdown, DetailLevel: "full"}
	if err := display.RenderUnifiedTranscript(&buf, entries, opts, nil); err != nil {
		return fmt.Errorf("failed to render markdown transcript: %w", err)
	}
	return os.WriteFile(filepath.Join(agentsDir, "agent-"+agentID+".md"), buf.Bytes(), 0o600)
}

// findWorkflowScript locates the persisted orchestration script for a run,
// searching each scripts dir in order. Scripts are saved as
// <name>-<runId>.js; the <name> prefix becomes the run's human-readable
// title. Returns "", "" when no script matches.
func findWorkflowScript(scriptsDirs []string, runID string) (path, title string) {
	for _, dir := range scriptsDirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*-"+runID+".js"))
		if err != nil || len(matches) == 0 {
			continue
		}
		path = matches[0]
		title = strings.TrimSuffix(filepath.Base(path), "-"+runID+".js")
		return path, title
	}
	return "", ""
}

// loadAgentDurations computes per-agent wall-clock durations from the
// daemon's enriched workflow event log (lines of {"event": WorkflowEvent}).
// Only agents with both a started and a completed timestamp get an entry.
func loadAgentDurations(eventsPath string) map[string]time.Duration {
	f, err := os.Open(eventsPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	started := make(map[string]time.Time)
	completed := make(map[string]time.Time)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var payload struct {
			Event models.WorkflowEvent `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			continue
		}
		ev := payload.Event
		if ev.AgentID == "" || ev.Timestamp.IsZero() {
			continue
		}
		switch ev.Kind {
		case models.WorkflowAgentStarted:
			if t, ok := started[ev.AgentID]; !ok || ev.Timestamp.Before(t) {
				started[ev.AgentID] = ev.Timestamp
			}
		case models.WorkflowAgentCompleted:
			if t, ok := completed[ev.AgentID]; !ok || ev.Timestamp.After(t) {
				completed[ev.AgentID] = ev.Timestamp
			}
		}
	}

	durations := make(map[string]time.Duration)
	for agentID, start := range started {
		if end, ok := completed[agentID]; ok && !end.Before(start) {
			durations[agentID] = end.Sub(start)
		}
	}
	if len(durations) == 0 {
		return nil
	}
	return durations
}

// sortedAgentIDs returns the run's agent IDs in lexical order so rendered
// output is deterministic (WorkflowRun.Agents is a map).
func sortedAgentIDs(run *agentworkflow.WorkflowRun) []string {
	ids := make([]string, 0, len(run.Agents))
	for id := range run.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// countAgents computes the run scoreboard: started counts agents the
// journal saw start (a result implies a start the journal may have missed),
// completed counts agents with a recorded result.
func countAgents(run *agentworkflow.WorkflowRun) (started, completed int) {
	for _, agent := range run.Agents {
		if agent.Started || agent.Result != nil {
			started++
		}
		if agent.Result != nil {
			completed++
		}
	}
	return started, completed
}

// agentPromptSummary extracts a one-line prompt summary from an agent's
// transcript: the first line of the first user message, truncated.
func agentPromptSummary(entries []transcript.UnifiedEntry) string {
	for _, entry := range entries {
		if entry.Role != "user" {
			continue
		}
		for _, part := range entry.Parts {
			if part.Type != "text" {
				continue
			}
			text := ""
			switch content := part.Content.(type) {
			case transcript.UnifiedTextContent:
				text = content.Text
			case map[string]interface{}:
				text, _ = content["text"].(string)
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
			const maxLen = 100
			if runes := []rune(line); len(runes) > maxLen {
				line = string(runes[:maxLen]) + "…"
			}
			return line
		}
	}
	return ""
}

// formatAgentDuration renders a duration for display, second precision.
func formatAgentDuration(d time.Duration) string {
	return d.Round(time.Second).String()
}

// capResultLines truncates text to at most max lines, reporting whether
// truncation happened.
func capResultLines(text string, max int) (string, bool) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= max {
		return strings.Join(lines, "\n"), false
	}
	return strings.Join(lines[:max], "\n"), true
}

// renderWorkflowRunsSection renders the body of the job .md's
// "# Workflow Runs" section (without the section header itself): one
// "## Workflow Run: <name>" block per archived run, each with scoreboard,
// phases, and per-agent details. Result payloads are capped at
// workflowResultCapLines lines with a link into .artifacts for the rest;
// embedded result/prompt content is 4-space indented (injection-safe
// against markdown fences and headings in agent output).
func renderWorkflowRunsSection(jobID string, runs []*archivedWorkflowRun) string {
	var b strings.Builder
	for i, ar := range runs {
		if i > 0 {
			b.WriteString("\n")
		}
		artifactDir := filepath.ToSlash(filepath.Join(".artifacts", jobID, "workflows", ar.RunID))
		started, completed := countAgents(ar.Run)

		fmt.Fprintf(&b, "## Workflow Run: %s\n\n", ar.Title)
		fmt.Fprintf(&b, "- Run ID: `%s`\n", ar.RunID)
		fmt.Fprintf(&b, "- Agents: %d started / %d completed\n", started, completed)
		if ar.Run.Meta != nil && len(ar.Run.Meta.Phases) > 0 {
			titles := make([]string, 0, len(ar.Run.Meta.Phases))
			for _, phase := range ar.Run.Meta.Phases {
				titles = append(titles, phase.Title)
			}
			fmt.Fprintf(&b, "- Phases: %s\n", strings.Join(titles, " → "))
		}
		fmt.Fprintf(&b, "- Artifacts: [%s/](%s/)\n", artifactDir, artifactDir)

		agentIDs := sortedAgentIDs(ar.Run)
		if len(agentIDs) == 0 {
			continue
		}
		b.WriteString("\n### Agents\n")
		for _, agentID := range agentIDs {
			agent := ar.Run.Agents[agentID]
			fmt.Fprintf(&b, "\n#### `%s`\n\n", agentID)
			if prompt := agentPromptSummary(agent.Entries); prompt != "" {
				fmt.Fprintf(&b, "- Prompt: %s\n", prompt)
			}
			if ar.Durations != nil {
				if d, ok := ar.Durations[agentID]; ok {
					fmt.Fprintf(&b, "- Duration: %s\n", formatAgentDuration(d))
				}
			}
			if ar.AgentDocs[agentID] {
				fmt.Fprintf(&b, "- Transcript: [agent-%s.md](%s/agents/agent-%s.md)\n", agentID, artifactDir, agentID)
			}
			if agent.Result == nil {
				if agent.Started {
					b.WriteString("- Result: _(none — agent never returned a result)_\n")
				}
				continue
			}
			capped, truncated := capResultLines(resultPlainText(agent.Result), workflowResultCapLines)
			b.WriteString("- Result:\n\n")
			for _, line := range strings.Split(capped, "\n") {
				if strings.TrimSpace(line) == "" {
					b.WriteString("\n")
				} else {
					fmt.Fprintf(&b, "    %s\n", line)
				}
			}
			if truncated {
				fmt.Fprintf(&b, "\n  [full result in %s/summary.md](%s/summary.md)\n", artifactDir, artifactDir)
			}
		}
	}
	return b.String()
}

// generateWorkflowSummary renders the human-readable summary.md for a run:
// title, started/completed agent counts, an interrupted-run note when agents
// started but never returned, and each result payload in full, with links
// to the rendered per-agent markdown transcripts.
func generateWorkflowSummary(title, runID string, run *agentworkflow.WorkflowRun, agentDocs map[string]bool) string {
	started, completed := countAgents(run)

	var b strings.Builder
	fmt.Fprintf(&b, "# Workflow Run: %s\n\n", title)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", runID)
	fmt.Fprintf(&b, "- Agents started: %d\n", started)
	fmt.Fprintf(&b, "- Agents completed: %d\n", completed)

	agentIDs := sortedAgentIDs(run)

	var interrupted []string
	for _, agentID := range agentIDs {
		agent := run.Agents[agentID]
		if agent.Started && agent.Result == nil {
			interrupted = append(interrupted, agentID)
		}
	}
	if len(interrupted) > 0 {
		fmt.Fprintf(&b, "\n> Run interrupted: %d agent(s) started but never returned a result: %s\n",
			len(interrupted), strings.Join(interrupted, ", "))
	}

	wroteHeader := false
	for _, agentID := range agentIDs {
		agent := run.Agents[agentID]
		if agent.Result == nil {
			continue
		}
		if !wroteHeader {
			b.WriteString("\n## Results\n")
			wroteHeader = true
		}
		fmt.Fprintf(&b, "\n### Agent `%s`\n\n", agentID)
		if agentDocs[agentID] {
			fmt.Fprintf(&b, "[Transcript](agents/agent-%s.md)\n\n", agentID)
		}
		b.WriteString(renderJournalResult(agent.Result))
		b.WriteString("\n")
	}

	return b.String()
}

// renderJournalResult renders a result payload for summary.md: bare JSON
// strings (schemaless agent results) as plain text, objects as indented
// JSON, and anything unparseable verbatim.
func renderJournalResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "_(empty result)_"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err == nil {
		return "```json\n" + buf.String() + "\n```"
	}
	return "```\n" + string(raw) + "\n```"
}

// resultPlainText renders a result payload without markdown fences, for
// embedding inside the job .md's indented (preformatted) result blocks
// where a fence would appear as literal backticks.
func resultPlainText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(empty result)"
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
