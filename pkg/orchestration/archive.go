package orchestration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/fs"
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

// JournalEvent is a single line of a workflow run's journal.jsonl.
// Claude Code workflow journals carry only {type, key, agentId} for
// "started" events, plus {result} for "result" events — there is no
// timestamp field. The format is undocumented Claude Code internals, so
// parsing must tolerate drift.
type JournalEvent struct {
	Type    string          `json:"type"`    // "started" or "result"
	Key     string          `json:"key"`     // content-hash key, e.g. "v2:<sha256>"
	AgentID string          `json:"agentId"` // maps to agent-<agentId>.jsonl
	Result  json.RawMessage `json:"result,omitempty"`
}

// ArchiveWorkflowRuns copies Claude Code workflow run artifacts (journal,
// orchestration script, generated summary, and optionally per-agent
// transcripts) from the session's directory under ~/.claude/projects/ into
// plans/<plan>/.artifacts/<job-id>/workflows/<runId>/. Sessions with no
// workflow runs are a silent no-op.
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
	if err != nil {
		return fmt.Errorf("failed to find session metadata for job %s: %w", job.ID, err)
	}

	if metadata.TranscriptPath == "" || metadata.ClaudeSessionID == "" {
		ulog.Debug("[ARCHIVE] No transcript path or session ID; skipping workflow archival").
			Field("job_id", job.ID).
			Log(ctx)
		return nil
	}

	// The transcript lives at ~/.claude/projects/<slug>/<session-id>.jsonl;
	// workflow runs live in the sibling <session-id>/ directory.
	sessionDir := filepath.Join(filepath.Dir(metadata.TranscriptPath), metadata.ClaudeSessionID)
	runsDir := filepath.Join(sessionDir, "subagents", "workflows")

	runs, err := os.ReadDir(runsDir)
	if err != nil {
		// No subagents/workflows directory means no workflow runs.
		return nil
	}

	scriptsDir := filepath.Join(sessionDir, "workflows", "scripts")
	includeTranscripts := plan.Config != nil && plan.Config.ArchiveAgentTranscripts

	for _, run := range runs {
		if !run.IsDir() || !strings.HasPrefix(run.Name(), "wf_") {
			continue
		}
		srcRunDir := filepath.Join(runsDir, run.Name())
		destRunDir := filepath.Join(plan.Directory, ".artifacts", job.ID, "workflows", run.Name())
		if err := archiveWorkflowRun(ctx, srcRunDir, scriptsDir, destRunDir, includeTranscripts); err != nil {
			// Never fail job completion over a single run's artifacts.
			ulog.Warn("[ARCHIVE] Failed to archive workflow run").
				Field("job_id", job.ID).
				Field("run_id", run.Name()).
				Err(err).
				Log(ctx)
			continue
		}
		ulog.Debug("[ARCHIVE] Workflow run archived").
			Field("job_id", job.ID).
			Field("run_id", run.Name()).
			Field("dest", destRunDir).
			Log(ctx)
	}

	return nil
}

// archiveWorkflowRun copies one wf_* run directory's durable artifacts into
// destRunDir and writes a generated summary.md.
func archiveWorkflowRun(ctx context.Context, srcRunDir, scriptsDir, destRunDir string, includeTranscripts bool) error {
	runID := filepath.Base(srcRunDir)

	if err := os.MkdirAll(destRunDir, 0o755); err != nil {
		return fmt.Errorf("failed to create workflow artifact directory: %w", err)
	}

	journalSrc := filepath.Join(srcRunDir, "journal.jsonl")
	journalExists := false
	if _, err := os.Stat(journalSrc); err == nil {
		journalExists = true
		if err := fs.CopyFile(journalSrc, filepath.Join(destRunDir, "journal.jsonl")); err != nil {
			return fmt.Errorf("failed to copy journal.jsonl: %w", err)
		}
	}

	scriptPath, title := findWorkflowScript(scriptsDir, runID)
	if scriptPath != "" {
		if err := fs.CopyFile(scriptPath, filepath.Join(destRunDir, "script.js")); err != nil {
			return fmt.Errorf("failed to copy workflow script: %w", err)
		}
	}
	if title == "" {
		title = runID
	}

	var events []JournalEvent
	if journalExists {
		var parseErr error
		events, parseErr = parseWorkflowJournal(journalSrc)
		if parseErr != nil {
			ulog.Warn("[ARCHIVE] Failed to parse workflow journal; summarizing partial events").
				Field("run_id", runID).
				Err(parseErr).
				Log(ctx)
		}
	}

	summary := generateWorkflowSummary(title, runID, events)
	if err := os.WriteFile(filepath.Join(destRunDir, "summary.md"), []byte(summary), 0o600); err != nil {
		return fmt.Errorf("failed to write summary.md: %w", err)
	}

	if includeTranscripts {
		entries, err := os.ReadDir(srcRunDir)
		if err != nil {
			return fmt.Errorf("failed to list workflow run directory: %w", err)
		}
		agentsDir := filepath.Join(destRunDir, "agents")
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-") {
				continue
			}
			if err := os.MkdirAll(agentsDir, 0o755); err != nil {
				return fmt.Errorf("failed to create agents directory: %w", err)
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

	return nil
}

// findWorkflowScript locates the persisted orchestration script for a run.
// Scripts are saved as <name>-<runId>.js; the <name> prefix becomes the
// run's human-readable title. Returns "", "" when no script matches.
func findWorkflowScript(scriptsDir, runID string) (path, title string) {
	matches, err := filepath.Glob(filepath.Join(scriptsDir, "*-"+runID+".js"))
	if err != nil || len(matches) == 0 {
		return "", ""
	}
	path = matches[0]
	title = strings.TrimSuffix(filepath.Base(path), "-"+runID+".js")
	return path, title
}

// parseWorkflowJournal reads journal.jsonl, skipping lines that fail to
// unmarshal (the format is undocumented and may drift). It returns the
// events parsed so far alongside any read error.
func parseWorkflowJournal(path string) ([]JournalEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Result events embed full agent results on a single line and can be
	// far larger than bufio.Scanner's 64KB default token size.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	var events []JournalEvent
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev JournalEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}

// generateWorkflowSummary renders the human-readable summary.md for a run:
// title, started/completed agent counts, an interrupted-run note when agents
// started but never returned, and each result payload.
func generateWorkflowSummary(title, runID string, events []JournalEvent) string {
	started := make(map[string]bool)
	completed := make(map[string]bool)
	for _, ev := range events {
		switch ev.Type {
		case "started":
			started[ev.AgentID] = true
		case "result":
			completed[ev.AgentID] = true
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Workflow Run: %s\n\n", title)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", runID)
	fmt.Fprintf(&b, "- Agents started: %d\n", len(started))
	fmt.Fprintf(&b, "- Agents completed: %d\n", len(completed))

	var interrupted []string
	for _, ev := range events {
		if ev.Type == "started" && !completed[ev.AgentID] {
			interrupted = append(interrupted, ev.AgentID)
			completed[ev.AgentID] = true // dedupe repeat started events
		}
	}
	if len(interrupted) > 0 {
		fmt.Fprintf(&b, "\n> Run interrupted: %d agent(s) started but never returned a result: %s\n",
			len(interrupted), strings.Join(interrupted, ", "))
	}

	wroteHeader := false
	for _, ev := range events {
		if ev.Type != "result" {
			continue
		}
		if !wroteHeader {
			b.WriteString("\n## Results\n")
			wroteHeader = true
		}
		fmt.Fprintf(&b, "\n### Agent `%s`\n\n", ev.AgentID)
		b.WriteString(renderJournalResult(ev.Result))
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
