package orchestration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/workspace"
	"gopkg.in/yaml.v3"
)

// directoryUlog emits lifecycle events (event=job.created) for plan-directory
// mutations. StructuredOnly: audit record, not user-facing CLI output.
var directoryUlog = grovelogging.NewUnifiedLogger("grove-flow")

// sanitizeForFilename sanitizes a string for use in a filename (kebab-case).
func sanitizeForFilename(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	// Remove non-alphanumeric characters, except hyphens
	s = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(s, "")
	// Collapse multiple hyphens
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 { // Truncate long names
		s = s[:50]
		// Remove trailing dash after truncation
		s = strings.TrimRight(s, "-")
	}
	return s
}

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

	// Check for duplicate ID
	if existing, exists := plan.JobsByID[job.ID]; exists {
		return "", fmt.Errorf("job with ID %q already exists in file %s", job.ID, existing.Filename)
	}

	// Generate filename
	nextNum, err := GetNextJobNumber(plan.Directory)
	if err != nil {
		return "", fmt.Errorf("getting next job number: %w", err)
	}

	filename := GenerateJobFilename(nextNum, job.Title)

	// Auto-create per-job rules file if one isn't already set
	if job.RulesFile == "" {
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

		if err := os.WriteFile(rulesAbsPath, rulesContent, 0o600); err != nil {
			return "", fmt.Errorf("writing rules file: %w", err)
		}

		job.RulesFile = rulesRelPath
	}

	jobFilePath := filepath.Join(plan.Directory, filename)

	// Generate job content
	content, err := generateJobContent(job)
	if err != nil {
		return "", fmt.Errorf("generating job content: %w", err)
	}

	// Write job file
	if err := os.WriteFile(jobFilePath, content, 0o600); err != nil {
		return "", fmt.Errorf("writing job file: %w", err)
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
