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
	// Started holds per-agent wall-clock start times from the daemon's
	// enriched events.jsonl, used to order agents chronologically (matching
	// the live TUI). Nil when no enriched log was present.
	Started map[string]time.Time
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

	if !isClaudeSessionProvider(metadata.Provider) {
		// Workflow/subagent trees are written by Claude Code's workflow
		// runtime only; for other providers ClaudeSessionID is their native
		// id and there is nothing to archive. Silent, intentional degrade
		// (the transcript itself is archived by ArchiveSession regardless).
		ulog.Debug("[ARCHIVE] Non-claude provider; no workflow runs to archive").
			Field("job_id", job.ID).
			Field("provider", metadata.Provider).
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
	var startedTimes map[string]time.Time
	daemonEvents := filepath.Join(daemonEventsDir, runID+".jsonl")
	if _, err := os.Stat(daemonEvents); err == nil {
		if err := fs.CopyFile(daemonEvents, filepath.Join(destRunDir, "events.jsonl")); err != nil {
			ulog.Warn("[ARCHIVE] Failed to copy daemon workflow events").
				Field("run_id", runID).
				Err(err).
				Log(ctx)
		} else {
			durations, startedTimes = loadAgentDurations(filepath.Join(destRunDir, "events.jsonl"))
		}
	}

	// Render per-agent markdown transcripts (always on — these are small
	// relative to the raw jsonl and environment-independent).
	agentDocs := make(map[string]bool)
	for _, agentID := range sortedAgentIDs(run, startedTimes) {
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

	// Clean up superseded run-*-sub.* transcripts the upstream Claude runtime
	// leaves in the session's run dir: these are duplicate transcripts in an
	// old style, now replaced by the canonical agents/agent-<id>.md renders.
	// Conservative glob — only run-*-sub.* matches.
	removeSupersededSubTranscripts(ctx, srcRunDir, runID)

	summary := generateWorkflowSummary(title, runID, run, agentDocs, startedTimes)
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
		Started:   startedTimes,
		AgentDocs: agentDocs,
	}, nil
}

// subagentsSectionHeader is the job-.md section holding the durable record of
// standalone (Agent-tool) subagents captured at completion. Rebuilt wholesale
// on every completion and inserted before the agent chat transcript section.
const subagentsSectionHeader = "# Subagents"

// archivedStandaloneSubagent is the per-agent data collected while archiving a
// standalone (non-workflow) Agent-tool subagent, used to render the job .md's
// "# Subagents" section.
type archivedStandaloneSubagent struct {
	// ID is the agent ID (the agent-<id>.jsonl basename minus prefix/suffix).
	ID string
	// Name is the human display name from the sibling agent-<id>.meta.json
	// "description" field, falling back to the agent ID.
	Name string
}

// standaloneAgentMeta is the subset of a standalone subagent's
// agent-<id>.meta.json we read: "description" is a human-readable name.
type standaloneAgentMeta struct {
	Description string `json:"description"`
}

// ArchiveStandaloneSubagents durably captures STANDALONE (Agent-tool) subagent
// transcripts that workflow archival never touches. These live FLAT at
// <claudeSessionDir>/subagents/agent-<id>.jsonl (with a sibling
// agent-<id>.meta.json), as distinct from workflow agents nested under
// subagents/workflows/wf_*/. For each one it renders a canonical markdown
// transcript into .artifacts/<job>/subagents/agent-<id>.md (always), optionally
// copies the raw jsonl (gated on plan.Config.ArchiveAgentTranscripts), and
// rebuilds the job .md's "# Subagents" section. No session dir or no standalone
// agents is a silent no-op (no error, no empty section). It is called right
// after ArchiveWorkflowRuns in job completion and reuses the same session-dir
// resolution.
func ArchiveStandaloneSubagents(job *Job, plan *Plan) error {
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
		ulog.Debug("[ARCHIVE] Session binding unverified; skipping standalone subagent archival").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return nil
	}

	if !isClaudeSessionProvider(metadata.Provider) {
		// Standalone (Agent-tool) subagent trees only exist for Claude
		// sessions; other providers degrade silently.
		ulog.Debug("[ARCHIVE] Non-claude provider; no standalone subagents to archive").
			Field("job_id", job.ID).
			Field("provider", metadata.Provider).
			Log(ctx)
		return nil
	}

	sessionDirs, dirsErr := coresessions.ResolveClaudeSessionDirs(metadata.ClaudeSessionID)
	if dirsErr != nil || len(sessionDirs) == 0 {
		sessionDirs = []string{filepath.Join(filepath.Dir(metadata.TranscriptPath), metadata.ClaudeSessionID)}
	}

	includeTranscripts := plan.Config != nil && plan.Config.ArchiveAgentTranscripts
	return archiveStandaloneSubagentsFromDirs(ctx, job, plan, sessionDirs, includeTranscripts)
}

// archiveStandaloneSubagentsFromDirs globs the FLAT subagents/agent-*.jsonl
// files in each session dir (explicitly skipping the subagents/workflows/
// subtree, whose agents are handled by ArchiveWorkflowRuns), renders each to
// .artifacts/<job>/subagents/agent-<id>.md, optionally copies the raw jsonl,
// and rebuilds the job .md's "# Subagents" section. Agents are deduped by ID
// across dirs. No standalone agents leaves the job file untouched.
func archiveStandaloneSubagentsFromDirs(ctx context.Context, job *Job, plan *Plan, sessionDirs []string, includeTranscripts bool) error {
	destSubagentsDir := filepath.Join(plan.Directory, ".artifacts", job.ID, "subagents")

	var archived []*archivedStandaloneSubagent
	seen := make(map[string]bool)
	for _, dir := range sessionDirs {
		// Glob the FLAT subagents/agent-*.jsonl files. filepath.Glob's
		// agent-* pattern does not descend, so the subagents/workflows/
		// subtree is naturally excluded; the IsDir guard below skips any
		// non-file match defensively.
		matches, globErr := filepath.Glob(filepath.Join(dir, "subagents", "agent-*.jsonl"))
		if globErr != nil {
			continue
		}
		for _, src := range matches {
			info, statErr := os.Stat(src)
			if statErr != nil || info.IsDir() {
				continue
			}
			name := filepath.Base(src)
			agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
			if agentID == "" || seen[agentID] {
				continue
			}

			entries, err := agentworkflow.ReadAgentTranscript(src, agentID)
			if err != nil || len(entries) == 0 {
				// Unreadable or empty transcript: nothing to capture.
				continue
			}
			seen[agentID] = true

			displayName := standaloneAgentDisplayName(dir, agentID)
			if err := writeStandaloneSubagentMarkdown(destSubagentsDir, agentID, displayName, entries); err != nil {
				ulog.Warn("[ARCHIVE] Failed to render standalone subagent markdown").
					Field("job_id", job.ID).
					Field("agent_id", agentID).
					Err(err).
					Log(ctx)
				continue
			}

			if includeTranscripts {
				if err := os.MkdirAll(destSubagentsDir, 0o755); err != nil {
					return fmt.Errorf("failed to create subagents directory: %w", err)
				}
				if err := fs.CopyFile(src, filepath.Join(destSubagentsDir, name)); err != nil {
					ulog.Warn("[ARCHIVE] Failed to copy standalone subagent transcript").
						Field("job_id", job.ID).
						Field("agent_id", agentID).
						Err(err).
						Log(ctx)
				}
			}

			archived = append(archived, &archivedStandaloneSubagent{ID: agentID, Name: displayName})
			ulog.Debug("[ARCHIVE] Standalone subagent archived").
				Field("job_id", job.ID).
				Field("agent_id", agentID).
				Log(ctx)
		}
	}

	// Rebuild the job .md's "# Subagents" section from ALL captured agents.
	// None → no section (and an existing section is left alone: a later
	// partial re-archive must not erase a previously good record).
	if len(archived) > 0 && job.FilePath != "" {
		sort.Slice(archived, func(i, j int) bool { return archived[i].ID < archived[j].ID })
		content := renderStandaloneSubagentsSection(job.ID, archived)
		if _, err := NewStatePersister().UpdateJobSection(job, subagentsSectionHeader, content, transcriptSectionHeader, false); err != nil {
			ulog.Warn("[ARCHIVE] Failed to update subagents section in job file").
				Field("job_id", job.ID).
				Field("filepath", job.FilePath).
				Err(err).
				Log(ctx)
		}
	}

	return nil
}

// standaloneAgentDisplayName reads the sibling agent-<id>.meta.json's
// "description" as a human display name, falling back to the agent ID when the
// meta file is missing, unreadable, or has no description.
func standaloneAgentDisplayName(sessionDir, agentID string) string {
	metaPath := filepath.Join(sessionDir, "subagents", "agent-"+agentID+".meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return agentID
	}
	var meta standaloneAgentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return agentID
	}
	if name := strings.TrimSpace(meta.Description); name != "" {
		return name
	}
	return agentID
}

// writeStandaloneSubagentMarkdown renders one standalone subagent's transcript
// to subagents/agent-<id>.md under the job's artifact dir, via the same
// canonical agentlogs glyph renderer used for workflow agents (ANSI stripped).
func writeStandaloneSubagentMarkdown(destSubagentsDir, agentID, displayName string, entries []transcript.UnifiedEntry) error {
	if err := os.MkdirAll(destSubagentsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create subagents directory: %w", err)
	}
	var buf bytes.Buffer
	if displayName != "" && displayName != agentID {
		fmt.Fprintf(&buf, "# Subagent `%s` — %s\n\n", agentID, displayName)
	} else {
		fmt.Fprintf(&buf, "# Subagent `%s`\n\n", agentID)
	}
	if err := display.RenderUnifiedTranscriptPlain(&buf, entries, "full", display.DefaultToolFormatters()); err != nil {
		return fmt.Errorf("failed to render transcript: %w", err)
	}
	return os.WriteFile(filepath.Join(destSubagentsDir, "agent-"+agentID+".md"), buf.Bytes(), 0o600)
}

// renderStandaloneSubagentsSection renders the body of the job .md's
// "# Subagents" section (without the header itself): one bullet per captured
// standalone agent with its display name and a link into the rendered markdown
// transcript under .artifacts.
func renderStandaloneSubagentsSection(jobID string, agents []*archivedStandaloneSubagent) string {
	subagentsDir := filepath.ToSlash(filepath.Join(".artifacts", jobID, "subagents"))
	var b strings.Builder
	b.WriteString("\n")
	for _, a := range agents {
		link := fmt.Sprintf("%s/agent-%s.md", subagentsDir, a.ID)
		if a.Name != "" && a.Name != a.ID {
			fmt.Fprintf(&b, "- `%s` — %s: [agent-%s.md](%s)\n", a.ID, a.Name, a.ID, link)
		} else {
			fmt.Fprintf(&b, "- `%s`: [agent-%s.md](%s)\n", a.ID, a.ID, link)
		}
	}
	return b.String()
}

// writeAgentMarkdown renders one agent's transcript entries to
// agents/agent-<id>.md under the archived run dir, via the canonical
// agentlogs glyph renderer (theme icons + summarized tool rows, ANSI
// stripped for durable on-disk output). This gives archived subagent
// transcripts the same `aglogs read` experience as main-agent transcripts.
func writeAgentMarkdown(destRunDir, runID, agentID string, entries []transcript.UnifiedEntry) error {
	agentsDir := filepath.Join(destRunDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create agents directory: %w", err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Agent `%s` — %s\n\n", agentID, runID)
	if err := display.RenderUnifiedTranscriptPlain(&buf, entries, "full", display.DefaultToolFormatters()); err != nil {
		return fmt.Errorf("failed to render transcript: %w", err)
	}
	return os.WriteFile(filepath.Join(agentsDir, "agent-"+agentID+".md"), buf.Bytes(), 0o600)
}

// removeSupersededSubTranscripts deletes any run-*-sub.* files the upstream
// Claude runtime left in a workflow run's source directory. These are
// duplicate transcripts in an old style, superseded by the canonical
// agents/agent-<id>.md renders. The glob is deliberately narrow (run-*-sub.*)
// so nothing else in the run dir is touched, and removal failures are logged
// but never fail archival.
func removeSupersededSubTranscripts(ctx context.Context, srcRunDir, runID string) {
	matches, err := filepath.Glob(filepath.Join(srcRunDir, "run-*-sub.*"))
	if err != nil {
		return
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil {
			ulog.Warn("[ARCHIVE] Failed to remove superseded sub transcript").
				Field("run_id", runID).
				Field("file", filepath.Base(path)).
				Err(err).
				Log(ctx)
		}
	}
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

// loadAgentDurations computes per-agent wall-clock durations and earliest
// start times from the daemon's enriched workflow event log (lines of
// {"event": WorkflowEvent}). Durations cover only agents with both a started
// and a completed timestamp; started covers every agent the log saw start
// (used for chronological ordering). A nil durations/started map means the
// log was missing or unparseable.
func loadAgentDurations(eventsPath string) (durations map[string]time.Duration, started map[string]time.Time) {
	f, err := os.Open(eventsPath)
	if err != nil {
		return nil, nil
	}
	defer f.Close()

	startedTimes := make(map[string]time.Time)
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
			if t, ok := startedTimes[ev.AgentID]; !ok || ev.Timestamp.Before(t) {
				startedTimes[ev.AgentID] = ev.Timestamp
			}
		case models.WorkflowAgentCompleted:
			if t, ok := completed[ev.AgentID]; !ok || ev.Timestamp.After(t) {
				completed[ev.AgentID] = ev.Timestamp
			}
		}
	}

	durations = make(map[string]time.Duration)
	for agentID, start := range startedTimes {
		if end, ok := completed[agentID]; ok && !end.Before(start) {
			durations[agentID] = end.Sub(start)
		}
	}
	if len(durations) == 0 {
		durations = nil
	}
	if len(startedTimes) == 0 {
		startedTimes = nil
	}
	return durations, startedTimes
}

// sortedAgentIDs returns the run's agent IDs in chronological order by start
// time (earliest first), so rendered output matches the live TUI ordering
// (inventory → … → synthesize). Agents with a missing or equal start
// timestamp fall back to lexical ID order, which also makes output
// deterministic when started is nil (no enriched event log).
func sortedAgentIDs(run *agentworkflow.WorkflowRun, started map[string]time.Time) []string {
	ids := make([]string, 0, len(run.Agents))
	for id := range run.Agents {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		ti, oki := started[ids[i]]
		tj, okj := started[ids[j]]
		switch {
		case oki && okj:
			if !ti.Equal(tj) {
				return ti.Before(tj)
			}
			return ids[i] < ids[j]
		case oki != okj:
			// Agents with a known start time sort before those without.
			return oki
		default:
			return ids[i] < ids[j]
		}
	})
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

		agentIDs := sortedAgentIDs(ar.Run, ar.Started)
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
func generateWorkflowSummary(title, runID string, run *agentworkflow.WorkflowRun, agentDocs map[string]bool, started map[string]time.Time) string {
	startedCount, completed := countAgents(run)

	var b strings.Builder
	fmt.Fprintf(&b, "# Workflow Run: %s\n\n", title)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", runID)
	fmt.Fprintf(&b, "- Agents started: %d\n", startedCount)
	fmt.Fprintf(&b, "- Agents completed: %d\n", completed)

	agentIDs := sortedAgentIDs(run, started)

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
