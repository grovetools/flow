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
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/workspace"
)

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

// InitPlan creates a new plan directory with initial structure.
func InitPlan(dir string, specFile string) error {
	// Create plan directory if it doesn't exist
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating plan directory: %w", err)
	}

	// Check if spec file exists
	if _, err := os.Stat(specFile); err != nil {
		return fmt.Errorf("spec file not found: %w", err)
	}

	// Copy spec file into directory
	specContent, err := os.ReadFile(specFile)
	if err != nil {
		return fmt.Errorf("reading spec file: %w", err)
	}

	specDest := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(specDest, specContent, 0644); err != nil {
		return fmt.Errorf("writing spec file: %w", err)
	}

	// Create initial planning job
	jobID := generateJobID("initial-plan")
	job := &Job{
		ID:           jobID,
		Title:        "Create High-Level Implementation Plan",
		Status:       JobStatusPending,
		Type:         JobTypeOneshot,
		PromptSource: []string{"spec.md"},
		Output: OutputConfig{
			Type: "generate_jobs",
		},
	}

	// Generate job content from template
	tmpl, err := template.New("initial").Parse(InitialPlanTemplate)
	if err != nil {
		return fmt.Errorf("parsing initial plan template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, job); err != nil {
		return fmt.Errorf("executing initial plan template: %w", err)
	}

	// Write initial job file
	jobPath := filepath.Join(dir, "01-high-level-plan.md")
	if err := os.WriteFile(jobPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing initial job file: %w", err)
	}

	return nil
}

// getWorkspaceContext retrieves repository and branch information from the current directory.
func getWorkspaceContext() (repository, branch, worktree string) {
	// Get repository name and branch from git
	repoName, branchName, _ := git.GetRepoInfo(".")

	// Get workspace node to check if we're in a worktree
	node, err := workspace.GetProjectByPath(".")
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

	// Populate workspace context if not already set
	if job.Repository == "" || job.Branch == "" {
		repo, branch, worktree := getWorkspaceContext()
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
	filepath := filepath.Join(plan.Directory, filename)

	// Generate job content
	var content []byte
	if job.Type == JobTypeAgent || job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent {
		content, err = generateAgentJobContent(job)
	} else {
		content, err = generateJobContent(job)
	}
	if err != nil {
		return "", fmt.Errorf("generating job content: %w", err)
	}

	// Write job file
	if err := os.WriteFile(filepath, content, 0644); err != nil {
		return "", fmt.Errorf("writing job file: %w", err)
	}

	// Update plan structures
	job.Filename = filename
	job.FilePath = filepath
	plan.Jobs = append(plan.Jobs, job)
	plan.JobsByID[job.ID] = job

	return filename, nil
}

// GetNextJobNumber scans the directory and returns the next available job number.
func GetNextJobNumber(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading directory: %w", err)
	}

	maxNum := 0
	jobFileRegex := regexp.MustCompile(`^(\d{2})-.*\.md$`)

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
		PromptSource:        opts.PromptSource,
		Worktree:            opts.Worktree,
		PromptBody:          opts.Prompt,
		PrependDependencies: opts.PrependDependencies,
	}

	// Set default output type
	if opts.OutputType != "" {
		job.Output.Type = opts.OutputType
	} else if jobType == JobTypeAgent {
		job.Output.Type = "commit"
	} else {
		job.Output.Type = "file"
	}

	return job
}

// generateJobID creates a unique job ID with timestamp.
func generateJobID(title string) string {
	timestamp := time.Now().Format("20060102-150405")
	slug := sanitizeForFilename(title)
	return fmt.Sprintf("%s-%s", timestamp, slug)
}

// generateJobContent creates the markdown content for a job.
func generateJobContent(job *Job) ([]byte, error) {
	// Create frontmatter
	frontmatter := map[string]interface{}{
		"id":     job.ID,
		"title":  job.Title,
		"status": job.Status,
		"type":   job.Type,
	}

	// Add plan_type field (same as type for consistency)
	frontmatter["plan_type"] = string(job.Type)

	if len(job.DependsOn) > 0 {
		frontmatter["depends_on"] = job.DependsOn
	}
	if len(job.PromptSource) > 0 {
		frontmatter["prompt_source"] = job.PromptSource
	}
	if job.SourceBlock != "" {
		frontmatter["source_block"] = job.SourceBlock
	}
	if job.Template != "" {
		frontmatter["template"] = job.Template
	}
	if job.PrependDependencies {
		frontmatter["prepend_dependencies"] = job.PrependDependencies
	}
	if job.GeneratePlanFrom {
		frontmatter["generate_plan_from"] = job.GeneratePlanFrom
	}
	if job.Repository != "" {
		frontmatter["repository"] = job.Repository
	}
	if job.Branch != "" {
		frontmatter["branch"] = job.Branch
	}
	if job.Worktree != "" {
		frontmatter["worktree"] = job.Worktree
	}
	if job.Model != "" {
		frontmatter["model"] = job.Model
	}
	if job.NoteRef != "" {
		frontmatter["note_ref"] = job.NoteRef
	}
	if job.Output.Type != "" {
		output := map[string]interface{}{
			"type": job.Output.Type,
		}
		if job.Output.Message != "" {
			output["message"] = job.Output.Message
		}
		if job.Output.Path != "" {
			output["path"] = job.Output.Path
		}
		frontmatter["output"] = output
	}

	// Create YAML
	yamlContent := "---\n"
	yamlContent += formatYAML(frontmatter)
	yamlContent += "---\n\n"
	
	// Add the prompt body
	promptBody := job.PromptBody
	
	// If output type is generate_jobs and it's a oneshot job, append the special prompt
	if job.Type == JobTypeOneshot && job.Output.Type == "generate_jobs" {
		// Extract the generate_jobs prompt from InitialJobContent
		// Starting after the YAML frontmatter
		startMarker := "---\n\n"
		if idx := strings.Index(InitialJobContent, startMarker); idx != -1 {
			generateJobsPrompt := InitialJobContent[idx+len(startMarker):]
			// Remove the %s placeholders from the template
			generateJobsPrompt = strings.ReplaceAll(generateJobsPrompt, "%s", "")
			promptBody += "\n\n" + generateJobsPrompt
		}
	}
	
	yamlContent += promptBody

	return []byte(yamlContent), nil
}

// generateAgentJobContent creates content using the agent job template.
func generateAgentJobContent(job *Job) ([]byte, error) {
	// For agent jobs with templates, use the regular generateJobContent
	// which handles the template field properly
	if job.Template != "" {
		return generateJobContent(job)
	}
	
	tmpl, err := template.New("agent").Parse(AgentJobTemplate)
	if err != nil {
		return nil, fmt.Errorf("parsing agent job template: %w", err)
	}

	data := struct {
		ID                  string
		Title               string
		Type                string
		PlanType            string
		DependsOn           []string
		PromptSource        []string
		Repository          string
		Branch              string
		Worktree            string
		NoteRef             string
		OutputType          string
		Prompt              string
		AgentContinue       bool
		PrependDependencies bool
	}{
		ID:                  job.ID,
		Title:               job.Title,
		Type:                string(job.Type),
		PlanType:            string(job.Type),
		DependsOn:           job.DependsOn,
		PromptSource:        job.PromptSource,
		Repository:          job.Repository,
		Branch:              job.Branch,
		Worktree:            job.Worktree,
		NoteRef:             job.NoteRef,
		OutputType:          job.Output.Type,
		Prompt:              job.PromptBody,
		AgentContinue:       job.AgentContinue,
		PrependDependencies: job.PrependDependencies,
	}

	if data.OutputType == "" {
		data.OutputType = "commit"
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("executing agent job template: %w", err)
	}

	return buf.Bytes(), nil
}

// formatYAML formats a map as YAML with proper indentation.
func formatYAML(data map[string]interface{}) string {
	var buf bytes.Buffer
	
	// Sort keys for consistent output
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value := data[key]
		formatYAMLValue(&buf, key, value, 0)
	}

	return buf.String()
}

// formatYAMLValue formats a single YAML value with proper indentation.
func formatYAMLValue(buf *bytes.Buffer, key string, value interface{}, indent int) {
	indentStr := ""
	for i := 0; i < indent; i++ {
		indentStr += "  "
	}

	switch v := value.(type) {
	case string:
		buf.WriteString(fmt.Sprintf("%s%s: %s\n", indentStr, key, v))
	case []string:
		buf.WriteString(fmt.Sprintf("%s%s:\n", indentStr, key))
		for _, item := range v {
			buf.WriteString(fmt.Sprintf("%s  - %s\n", indentStr, item))
		}
	case map[string]interface{}:
		buf.WriteString(fmt.Sprintf("%s%s:\n", indentStr, key))
		// Sort nested keys
		nestedKeys := make([]string, 0, len(v))
		for k := range v {
			nestedKeys = append(nestedKeys, k)
		}
		sort.Strings(nestedKeys)
		for _, k := range nestedKeys {
			formatYAMLValue(buf, k, v[k], indent+1)
		}
	default:
		buf.WriteString(fmt.Sprintf("%s%s: %v\n", indentStr, key, v))
	}
}

// ListJobs returns all job files in the directory sorted by filename.
func ListJobs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	var jobs []string
	jobFileRegex := regexp.MustCompile(`^\d{2}-.*\.md$`)

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