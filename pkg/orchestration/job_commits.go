package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Job commit-range capture: records which commits a job produced, per member
// repo of its worktree container, into the job's per-job artifacts dir
// (.artifacts/<job-id>/commits.json — the same dir naming as the .status file
// and the hooks' accessed_files.jsonl). Start capture writes per-repo start
// HEADs when the job launches; finalize capture fills end HEADs plus the
// rev-list between them once the job reaches a terminal state. The JSON is a
// cross-repo contract consumed by review tooling (git-viewer) to diff exactly
// one job's work — bump jobCommitsSchemaVersion on any breaking change.

const (
	jobCommitsSchemaVersion = 1
	jobCommitsFileName      = "commits.json"

	// jobCommitsNoteStartMissing is the contractual note value consumers match
	// on when a repo's commit range could not be computed because the start
	// capture never ran (or its recorded SHA is no longer resolvable).
	jobCommitsNoteStartMissing = "start capture missing"
)

// JobCommitsRecord is the commits.json sidecar. A start-only record has
// StartedAt and per-repo StartHead set but no FinishedAt/EndHead/Commits;
// finalize tolerates (and completes) such a file.
type JobCommitsRecord struct {
	Schema     int              `json:"schema"`
	JobID      string           `json:"job_id"`
	JobFile    string           `json:"job_file"`
	Worktree   string           `json:"worktree"`
	StartedAt  string           `json:"started_at"`
	FinishedAt string           `json:"finished_at,omitempty"`
	Repos      []JobCommitsRepo `json:"repos"`
}

// JobCommitsRepo is one member repo's commit range. Commits is nil (JSON null)
// when the range could not be computed — distinct from an empty array, which
// means "captured, no commits produced".
type JobCommitsRepo struct {
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Branch     string   `json:"branch"`
	StartHead  string   `json:"start_head"`
	EndHead    string   `json:"end_head"`
	Commits    []string `json:"commits"`
	DirtyAtEnd bool     `json:"dirty_at_end"`
	Note       string   `json:"note,omitempty"`
}

// JobCommitsPath returns the sidecar path for a job, using the same
// .artifacts/<job-id>/ dir as headlessStatusPath and the hooks artifacts.
func JobCommitsPath(plan *Plan, job *Job) string {
	return filepath.Join(plan.Directory, ".artifacts", job.ID, jobCommitsFileName)
}

// ReadJobCommits reads and parses a job's commits.json sidecar.
func ReadJobCommits(plan *Plan, job *Job) (*JobCommitsRecord, error) {
	data, err := os.ReadFile(JobCommitsPath(plan, job))
	if err != nil {
		return nil, err
	}
	var rec JobCommitsRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", JobCommitsPath(plan, job), err)
	}
	return &rec, nil
}

func writeJobCommits(plan *Plan, job *Job, rec *JobCommitsRecord) error {
	path := JobCommitsPath(plan, job)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating artifacts dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// CaptureJobCommitsStart writes the start-of-job sidecar: every member repo of
// the container with its current HEAD. Crash-safe by design — if the job never
// finalizes, the start-only file remains valid and a later finalize completes
// it.
func CaptureJobCommitsStart(job *Job, plan *Plan, container string) error {
	repos, err := enumerateJobRepos(container)
	if err != nil {
		return err
	}

	startedAt := job.StartTime
	if startedAt.IsZero() {
		startedAt = time.Now()
	}

	rec := &JobCommitsRecord{
		Schema:    jobCommitsSchemaVersion,
		JobID:     job.ID,
		JobFile:   filepath.Base(job.FilePath),
		Worktree:  container,
		StartedAt: startedAt.Format(time.RFC3339),
	}
	for _, repoPath := range repos {
		head, _ := gitOutput(repoPath, "rev-parse", "--verify", "HEAD") // empty on unborn branch
		branch, _ := gitOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
		rec.Repos = append(rec.Repos, JobCommitsRepo{
			Name:      filepath.Base(repoPath),
			Path:      repoPath,
			Branch:    branch,
			StartHead: head,
		})
	}
	return writeJobCommits(plan, job, rec)
}

// FinalizeJobCommits completes the sidecar with end HEADs, per-repo commit
// lists (oldest-first), and dirty flags. It runs on both success and failure
// paths and is idempotent: a record that already has finished_at is left
// untouched, so a late repeat completion cannot misattribute commits made
// after the job. Tolerates a missing start file (commits null + note per the
// schema contract) and a start-only file (the normal case).
func FinalizeJobCommits(job *Job, plan *Plan) error {
	existing, readErr := ReadJobCommits(plan, job)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if existing != nil && existing.FinishedAt != "" {
		return nil // already finalized
	}

	var container string
	if existing != nil {
		container = existing.Worktree
	} else {
		if job.Worktree == "" {
			return nil // nothing was captured and there is no worktree to attribute
		}
		var err error
		container, err = resolveJobWorktreeContainer(job, plan)
		if err != nil {
			return fmt.Errorf("resolving worktree container: %w", err)
		}
	}

	rec, err := buildFinalizedJobCommits(job, container, existing)
	if err != nil {
		return err
	}
	return writeJobCommits(plan, job, rec)
}

// buildFinalizedJobCommits computes the finalized record for a container from
// an optional start-time record (nil when start capture never ran — every repo
// then gets null commits + the contractual note).
func buildFinalizedJobCommits(job *Job, container string, existing *JobCommitsRecord) (*JobCommitsRecord, error) {
	repos, err := enumerateJobRepos(container)
	if err != nil {
		return nil, err
	}

	startHeads := map[string]string{}
	hasStart := map[string]bool{}
	startedAt := ""
	if existing != nil {
		startedAt = existing.StartedAt
		for _, r := range existing.Repos {
			startHeads[r.Name] = r.StartHead
			hasStart[r.Name] = true
		}
	}

	finishedAt := time.Now().Format(time.RFC3339)
	if startedAt == "" {
		if !job.StartTime.IsZero() {
			startedAt = job.StartTime.Format(time.RFC3339)
		} else {
			startedAt = finishedAt
		}
	}

	rec := &JobCommitsRecord{
		Schema:     jobCommitsSchemaVersion,
		JobID:      job.ID,
		JobFile:    filepath.Base(job.FilePath),
		Worktree:   container,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	for _, repoPath := range repos {
		name := filepath.Base(repoPath)
		endHead, _ := gitOutput(repoPath, "rev-parse", "--verify", "HEAD")
		branch, _ := gitOutput(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
		statusOut, _ := gitOutput(repoPath, "status", "--porcelain")

		repo := JobCommitsRepo{
			Name:       name,
			Path:       repoPath,
			Branch:     branch,
			StartHead:  startHeads[name],
			EndHead:    endHead,
			DirtyAtEnd: statusOut != "",
		}
		if !hasStart[name] {
			repo.Commits = nil
			repo.Note = jobCommitsNoteStartMissing
		} else {
			commits, err := gitCommitRange(repoPath, repo.StartHead, endHead)
			if err != nil {
				// Recorded start SHA no longer resolves — same contractual
				// note as a never-ran start capture.
				repo.Commits = nil
				repo.Note = jobCommitsNoteStartMissing
			} else {
				repo.Commits = commits
			}
		}
		rec.Repos = append(rec.Repos, repo)
	}
	return rec, nil
}

// resolveJobWorktreeContainer locates the worktree container for a job via the
// shared registry-first resolver (anchored/XDG layouts included).
func resolveJobWorktreeContainer(job *Job, plan *Plan) (string, error) {
	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		return "", err
	}
	ownerRoot, worktreePath, exists := resolveWorktreeForJob(gitRoot, job.Worktree)
	if !exists {
		return "", fmt.Errorf("worktree %q not found for repository %s", job.Worktree, ownerRoot)
	}
	return worktreePath, nil
}

// enumerateJobRepos lists the git repos in a job's worktree scope. A container
// that is itself a git repo (single-repo worktree, or a no-container layout)
// is the sole entry; otherwise the immediate subdirectories that hold a .git
// entry (dir or worktree-style file) are the members. Non-repo dirs are
// skipped silently. Results are sorted by name for deterministic records.
func enumerateJobRepos(container string) ([]string, error) {
	if _, err := os.Stat(container); err != nil {
		return nil, fmt.Errorf("worktree container: %w", err)
	}
	if isGitRepoDir(container) {
		return []string{container}, nil
	}
	entries, err := os.ReadDir(container)
	if err != nil {
		return nil, fmt.Errorf("reading worktree container: %w", err)
	}
	var repos []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(container, entry.Name())
		if isGitRepoDir(path) {
			repos = append(repos, path)
		}
	}
	sort.Strings(repos)
	return repos, nil
}

// isGitRepoDir reports whether dir directly contains a .git entry (a real dir
// for a primary checkout, a gitfile for a linked worktree).
func isGitRepoDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitCommitRange returns start..end oldest-first. An unborn start ("" with a
// real end) yields the repo's full history up to end; identical or both-empty
// endpoints yield an empty (non-nil) list.
func gitCommitRange(repoPath, startHead, endHead string) ([]string, error) {
	if endHead == "" || startHead == endHead {
		return []string{}, nil
	}
	var out string
	var err error
	if startHead == "" {
		out, err = gitOutput(repoPath, "rev-list", "--reverse", endHead)
	} else {
		out, err = gitOutput(repoPath, "rev-list", "--reverse", startHead+".."+endHead)
	}
	if err != nil {
		return nil, err
	}
	if out == "" {
		return []string{}, nil
	}
	return strings.Split(out, "\n"), nil
}
