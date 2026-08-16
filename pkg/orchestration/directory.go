package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
	grovelogging "github.com/grovetools/core/logging"
	coreslug "github.com/grovetools/core/pkg/slug"
	"github.com/grovetools/core/pkg/workspace"
	"gopkg.in/yaml.v3"
)

// directoryUlog emits lifecycle events (event=job.created) for plan-directory
// mutations. StructuredOnly: audit record, not user-facing CLI output.
var directoryUlog = grovelogging.NewUnifiedLogger("grove-flow")

// sanitizeForFilename delegates to Grove's canonical note/job identity slugger.
func sanitizeForFilename(s string) string { return coreslug.Canonical(s) }

// GenerateUniqueJobID creates a globally unique job ID from a title string.
// This is the single source of truth for job ID generation across grove-flow.
func GenerateUniqueJobID(plan *Plan, title string) string {
	// Sanitize the title to create a human-readable slug
	slug := sanitizeForFilename(title)

	// Use a short UUID to guarantee uniqueness
	shortUUID := uuid.New().String()[:8]

	// Combine for a unique but still readable ID
	uniqueID := fmt.Sprintf("%s-%s", slug, shortUUID)

	// Final check for an extremely unlikely collision within the same plan
	if plan != nil {
		exists := false
		for _, job := range plan.Jobs {
			if job.ID == uniqueID {
				exists = true
				break
			}
		}
		if exists {
			// If collision, just use a different UUID
			return fmt.Sprintf("%s-%s", slug, uuid.New().String()[:8])
		}
	}

	return uniqueID
}

// getWorkspaceContext retrieves repository and branch information from the given directory.
// If dir is empty, falls back to the current working directory.
func getWorkspaceContext(dir string) (repository, branch, worktree string) {
	if dir == "" {
		dir = "."
	}

	// Get repository name and branch from git
	repoName, branchName, _ := git.GetRepoInfo(dir)

	// Get workspace node to check if we're in a worktree
	node, err := workspace.GetProjectByPath(dir)
	if err == nil && node.IsWorktree() {
		// If we're in a worktree, extract the worktree name from the path
		worktree = filepath.Base(node.Path)
	}

	return repoName, branchName, worktree
}

const (
	planMutationLockName    = ".flow-jobs.lock"
	planMutationLockTimeout = 10 * time.Second
	planMutationLockRetry   = 25 * time.Millisecond
)

// acquirePlanMutationLock serializes job-number allocation and creation across
// processes with an OS advisory lock held by an open descriptor. The lock file
// is intentionally persistent: deleting a pathname cannot invalidate or steal
// another process's live lock.
func acquirePlanMutationLock(dir string) (func(), error) {
	lockPath := filepath.Join(dir, planMutationLockName)
	fileLock := flock.New(lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), planMutationLockTimeout)
	defer cancel()

	locked, err := fileLock.TryLockContext(ctx, planMutationLockRetry)
	if err != nil {
		_ = fileLock.Close()
		return nil, fmt.Errorf("acquiring plan job-creation lock %s: %w", lockPath, err)
	}
	if !locked {
		_ = fileLock.Close()
		return nil, fmt.Errorf("timed out waiting for plan job-creation lock %s", lockPath)
	}

	return func() {
		_ = fileLock.Unlock()
		_ = fileLock.Close()
	}, nil
}

// AddJob adds a new job to the plan directory.
func AddJob(plan *Plan, job *Job) (string, error) {
	// Validate job
	if job.ID == "" {
		return "", fmt.Errorf("job ID is required")
	}
	if job.Title == "" {
		return "", fmt.Errorf("job title is required")
	}
	if job.Type == "" {
		job.Type = JobTypeOneshot
	}
	if job.Status == "" {
		job.Status = JobStatusPending
	}

	// Populate workspace context if not already set.
	// Use the plan's execution context working directory if available,
	// otherwise fall back to CWD via getWorkspaceContext("").
	if job.Repository == "" || job.Branch == "" {
		contextDir := ""
		if plan.Context != nil && plan.Context.WorkingDirectory != "" {
			contextDir = plan.Context.WorkingDirectory
		}
		repo, branch, worktree := getWorkspaceContext(contextDir)
		if job.Repository == "" {
			job.Repository = repo
		}
		if job.Branch == "" {
			job.Branch = branch
		}
		if job.Worktree == "" && worktree != "" {
			job.Worktree = worktree
		}
	}

	unlock, err := acquirePlanMutationLock(plan.Directory)
	if err != nil {
		return "", err
	}
	defer unlock()

	// Check the caller's snapshot and the authoritative directory while holding
	// the plan lock. Separate flow processes hold independently loaded Plan values.
	if existing, exists := plan.JobsByID[job.ID]; exists {
		return "", fmt.Errorf("job with ID %q already exists in file %s", job.ID, existing.Filename)
	}
	if diskPlan, loadErr := LoadPlan(plan.Directory); loadErr != nil {
		return "", fmt.Errorf("reloading plan under job-creation lock: %w", loadErr)
	} else if existing, exists := diskPlan.JobsByID[job.ID]; exists {
		return "", fmt.Errorf("job with ID %q already exists in file %s", job.ID, existing.Filename)
	}

	// Generate filename while holding the cross-process lock.
	nextNum, err := GetNextJobNumber(plan.Directory)
	if err != nil {
		return "", fmt.Errorf("getting next job number: %w", err)
	}

	filename := GenerateJobFilename(nextNum, job.Title)

	// A no_context job is answered from its prompt alone, so it gets no rules
	// file at all — stamping one would name a file nobody writes and send the
	// run into the unauthored-rules funnel. Declaring both is a contradiction:
	// refuse it here rather than silently picking a winner.
	if job.NoContext && job.RulesFile != "" {
		return "", fmt.Errorf("job %q sets both no_context and rules_file %q: a job either declares rules or declares none", job.ID, job.RulesFile)
	}

	// Auto-create per-job rules file if one isn't already set
	if job.RulesFile == "" && !job.NoContext {
		rulesRelPath := filepath.Join("rules", filename+".rules")
		rulesAbsPath := filepath.Join(plan.Directory, rulesRelPath)

		if err := os.MkdirAll(filepath.Join(plan.Directory, "rules"), 0o755); err != nil {
			return "", fmt.Errorf("creating rules directory: %w", err)
		}

		var rulesContent []byte
		cfg, cfgErr := config.LoadFrom(plan.Directory)
		if cfgErr == nil && cfg != nil && cfg.Context != nil && cfg.Context.DefaultRulesPath != "" {
			rulesContent, _ = os.ReadFile(cfg.Context.DefaultRulesPath)
		}

		// Leave an unseeded rules file absent. Agents can then author it without
		// tripping harnesses that require a Read before overwriting an existing
		// file; the frontmatter still names the canonical destination.
		if len(bytes.TrimSpace(rulesContent)) > 0 {
			if err := os.WriteFile(rulesAbsPath, rulesContent, 0o600); err != nil {
				return "", fmt.Errorf("writing rules file: %w", err)
			}
		}

		job.RulesFile = rulesRelPath
	}

	jobFilePath := filepath.Join(plan.Directory, filename)

	// Generate job content
	content, err := generateJobContent(job)
	if err != nil {
		return "", fmt.Errorf("generating job content: %w", err)
	}

	// Write completely off-path, then publish with link(2). The hard-link step
	// is atomic and refuses an existing destination, so readers never observe a
	// partial job and a non-cooperating writer is never overwritten.
	tmp, err := os.CreateTemp(plan.Directory, ".flow-job-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temporary job file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("setting temporary job permissions: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("writing temporary job file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("syncing temporary job file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing temporary job file: %w", err)
	}
	if err := os.Link(tmpPath, jobFilePath); err != nil {
		return "", fmt.Errorf("publishing job file: %w", err)
	}

	// Update plan structures
	job.Filename = filename
	job.FilePath = jobFilePath
	plan.Jobs = append(plan.Jobs, job)
	plan.JobsByID[job.ID] = job

	directoryUlog.Info("Job created").
		Field("event", "job.created").
		Field("plan_dir", plan.Directory).
		Field("job_file", filename).
		Field("job_type", string(job.Type)).
		Field("title", job.Title).
		StructuredOnly().
		Emit()

	return filename, nil
}

// GetNextJobNumber scans the directory and returns the next available job number.
func GetNextJobNumber(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading directory: %w", err)
	}

	maxNum := 0
	jobFileRegex := regexp.MustCompile(`^(\d+)-.*\.md$`)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		matches := jobFileRegex.FindStringSubmatch(entry.Name())
		if len(matches) > 1 {
			num, err := strconv.Atoi(matches[1])
			if err == nil && num > maxNum {
				maxNum = num
			}
		}
	}

	return maxNum + 1, nil
}

// GenerateJobFilename creates a filename from job number and title.
func GenerateJobFilename(number int, title string) string {
	slug := sanitizeForFilename(title)
	return fmt.Sprintf("%02d-%s.md", number, slug)
}

// CreateJobFromTemplate creates a new job with default values.
func CreateJobFromTemplate(jobType JobType, title string, opts JobOptions) *Job {
	job := &Job{
		ID:                  generateJobID(title),
		Title:               title,
		Status:              JobStatusPending,
		Type:                jobType,
		DependsOn:           opts.DependsOn,
		Include:             opts.Include,
		Worktree:            opts.Worktree,
		PromptBody:          opts.Prompt,
		PrependDependencies: opts.PrependDependencies,
	}

	return job
}

// generateJobID creates a unique job ID with timestamp.
func generateJobID(title string) string {
	timestamp := time.Now().Format("20060102-150405")
	slug := sanitizeForFilename(title)
	return fmt.Sprintf("%s-%s", timestamp, slug)
}

// generateJobContent creates the markdown content for a job using yaml.Marshal.
// The Job struct's yaml tags with omitempty handle conditional field inclusion automatically.
func generateJobContent(job *Job) ([]byte, error) {
	var yamlBuf bytes.Buffer
	encoder := yaml.NewEncoder(&yamlBuf)
	encoder.SetIndent(2)
	if err := encoder.Encode(job); err != nil {
		return nil, fmt.Errorf("marshaling job frontmatter: %w", err)
	}
	encoder.Close()

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlBuf.Bytes())
	buf.WriteString("---\n")

	// For chat-type jobs, add the template directive right after frontmatter
	if job.Type == JobTypeChat {
		buf.WriteString("\n<!-- grove: {\"template\": \"chat\"} -->\n")
		if job.PromptBody != "" {
			buf.WriteString("\n")
			buf.WriteString(job.PromptBody)
		} else {
			buf.WriteString("\n\n")
		}
	} else {
		buf.WriteString("\n")
		buf.WriteString(job.PromptBody)
	}

	return buf.Bytes(), nil
}

// ListJobs returns all job files in the directory sorted by filename.
func ListJobs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	var jobs []string
	jobFileRegex := regexp.MustCompile(`^\d+-.*\.md$`)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if jobFileRegex.MatchString(entry.Name()) {
			jobs = append(jobs, entry.Name())
		}
	}

	sort.Strings(jobs)
	return jobs, nil
}
