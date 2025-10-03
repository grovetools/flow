package orchestration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mattsolo1/grove-core/util/sanitize"
	"gopkg.in/yaml.v3"
)

// jobFilePattern matches job files like 01-job-name.md
var jobFilePattern = regexp.MustCompile(`^\d{2}-.*\.md$`)

// LoadPlan loads all jobs from a plan directory.
func LoadPlan(dir string) (*Plan, error) {
	// Check if directory exists
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("plan directory not found: %w", err)
	}

	plan := &Plan{
		Name:      filepath.Base(dir),
		Directory: dir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}

	// Check for spec.md
	specPath := filepath.Join(dir, "spec.md")
	if _, err := os.Stat(specPath); err == nil {
		plan.SpecFile = specPath
	}

	// Load .grove-plan.yml if it exists
	planConfigPath := filepath.Join(dir, ".grove-plan.yml")
	if _, err := os.Stat(planConfigPath); err == nil {
		yamlFile, err := os.ReadFile(planConfigPath)
		if err == nil {
			var planConfig PlanConfig
			if yaml.Unmarshal(yamlFile, &planConfig) == nil {
				plan.Config = &planConfig
			}
		}
	}

	// Read all files in directory
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading plan directory: %w", err)
	}

	// Load each job file
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		// Check for markdown files
		if !strings.HasSuffix(filename, ".md") {
			continue
		}

		filepath := filepath.Join(dir, filename)
		job, err := LoadJob(filepath)
		if err != nil {
			// Skip files that are not jobs
			var notAJob ErrNotAJob
			if errors.As(err, &notAJob) {
				continue
			}
			return nil, fmt.Errorf("loading job %s: %w", filename, err)
		}

		// Set derived fields
		job.Filename = filename
		job.FilePath = filepath

		// Add to plan
		plan.Jobs = append(plan.Jobs, job)
		if job.ID != "" {
			if existing, exists := plan.JobsByID[job.ID]; exists {
				return nil, fmt.Errorf("duplicate job ID %q in files %s and %s",
					job.ID, existing.Filename, job.Filename)
			}
			plan.JobsByID[job.ID] = job
		}
	}

	// Resolve dependencies
	if err := plan.ResolveDependencies(); err != nil {
		return nil, err
	}

	return plan, nil
}

// ErrNotAJob is returned when a file is not a valid job file
type ErrNotAJob struct {
	Reason string
}

func (e ErrNotAJob) Error() string {
	return e.Reason
}

// LoadJob loads a single job from a markdown file.
func LoadJob(filepath string) (*Job, error) {
	content, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("reading job file: %w", err)
	}

	// Parse frontmatter
	frontmatter, body, err := ParseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	// Check if this file has a type field - if not, it's not a job
	if typeField, ok := frontmatter["type"]; !ok || typeField == nil {
		return nil, ErrNotAJob{Reason: "no 'type' field in frontmatter"}
	}

	// Convert frontmatter map to Job struct
	// Sanitize UTF-8 to prevent encoding errors in LLM client
	job := &Job{
		PromptBody: sanitize.UTF8(body),
	}

	// Marshal frontmatter to YAML and unmarshal to Job struct
	// This handles the type conversions properly
	yamlBytes, err := yaml.Marshal(frontmatter)
	if err != nil {
		return nil, fmt.Errorf("marshaling frontmatter: %w", err)
	}

	if err := yaml.Unmarshal(yamlBytes, job); err != nil {
		return nil, fmt.Errorf("unmarshaling to job struct: %w", err)
	}

	// Validate job type first - only job types are processed
	if job.Type != JobTypeOneshot && job.Type != JobTypeAgent && job.Type != JobTypeHeadlessAgent && job.Type != JobTypeShell && job.Type != JobTypeChat && job.Type != JobTypeInteractiveAgent && job.Type != JobTypeGenerateRecipe {
		return nil, ErrNotAJob{Reason: fmt.Sprintf("not a job type: %s", job.Type)}
	}

	// Validate required fields
	if job.ID == "" {
		return nil, fmt.Errorf("job missing required field: id")
	}
	if job.Title == "" {
		return nil, fmt.Errorf("job missing required field: title")
	}
	if job.Status == "" {
		return nil, fmt.Errorf("job missing required field: status")
	}
	if job.Type == "" {
		return nil, fmt.Errorf("job missing required field: type")
	}

	// Validate job status
	switch job.Status {
	case JobStatusPending, JobStatusRunning, JobStatusCompleted,
		JobStatusFailed, JobStatusBlocked, JobStatusNeedsReview,
		JobStatusPendingUser, JobStatusPendingLLM:
		// Valid status
	default:
		return nil, fmt.Errorf("invalid job status: %s", job.Status)
	}

	return job, nil
}

// ResolveDependencies converts dependency IDs to Job pointers and checks for cycles.
func (p *Plan) ResolveDependencies() error {
	// Build a map of filenames to jobs for dependency resolution
	jobsByFilename := make(map[string]*Job)
	for _, job := range p.Jobs {
		if job.Filename != "" {
			jobsByFilename[job.Filename] = job
		}
	}

	// Build dependency graph
	for _, job := range p.Jobs {
		if job == nil {
			continue
		}
		job.Dependencies = make([]*Job, 0, len(job.DependsOn))

		for _, depRef := range job.DependsOn {
			// Try to resolve by job ID first
			depJob, exists := p.JobsByID[depRef]
			if !exists {
				// Try to resolve by filename
				depJob, exists = jobsByFilename[depRef]
				if !exists {
					// Append nil for missing dependency instead of failing
					job.Dependencies = append(job.Dependencies, nil)
					continue
				}
			}
			job.Dependencies = append(job.Dependencies, depJob)
		}
	}

	// Check for circular dependencies
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	for _, job := range p.Jobs {
		if job == nil || job.ID == "" {
			continue
		}
		if err := p.checkCycles(job.ID, visited, recStack); err != nil {
			return err
		}
	}

	return nil
}

// checkCycles uses DFS to detect circular dependencies.
func (p *Plan) checkCycles(jobID string, visited, recStack map[string]bool) error {
	visited[jobID] = true
	recStack[jobID] = true

	job := p.JobsByID[jobID]
	if job == nil {
		return fmt.Errorf("job with ID %s not found", jobID)
	}

	// Check dependencies using the resolved job references
	for _, dep := range job.Dependencies {
		if dep == nil || dep.ID == "" {
			continue
		}
		depID := dep.ID
		if !visited[depID] {
			if err := p.checkCycles(depID, visited, recStack); err != nil {
				return err
			}
		} else if recStack[depID] {
			// Found a cycle
			return fmt.Errorf("circular dependency detected: %s -> %s", jobID, depID)
		}
	}

	recStack[jobID] = false
	return nil
}

// GetRunnableJobs returns all jobs that can currently be executed.
func (p *Plan) GetRunnableJobs() []*Job {
	var runnable []*Job

	for _, job := range p.Jobs {
		if job.IsRunnable() {
			runnable = append(runnable, job)
		}
	}

	return runnable
}

// GetJobByFilename returns a job by its filename.
func (p *Plan) GetJobByFilename(filename string) (*Job, bool) {
	for _, job := range p.Jobs {
		if job.Filename == filename {
			return job, true
		}
	}
	return nil, false
}

// GetJobByID returns a job by its ID.
func (p *Plan) GetJobByID(id string) (*Job, bool) {
	job, exists := p.JobsByID[id]
	return job, exists
}

// GetJobsSortedByFilename returns all jobs sorted by their filename.
func (p *Plan) GetJobsSortedByFilename() []*Job {
	// Create a copy of the jobs slice
	jobs := make([]*Job, len(p.Jobs))
	copy(jobs, p.Jobs)

	// Sort by filename
	for i := 0; i < len(jobs)-1; i++ {
		for j := i + 1; j < len(jobs); j++ {
			if strings.Compare(jobs[i].Filename, jobs[j].Filename) > 0 {
				jobs[i], jobs[j] = jobs[j], jobs[i]
			}
		}
	}

	return jobs
}

// SavePlan saves a plan structure to disk (mainly used for tests).
func SavePlan(dir string, plan *Plan) error {
	// Create directory if it doesn't exist
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating plan directory: %w", err)
	}

	// Set plan directory
	plan.Directory = dir
	plan.Name = filepath.Base(dir)

	// Save each job
	for _, job := range plan.Jobs {
		if job.Filename == "" {
			continue
		}

		// Generate job content
		content, err := generateJobContent(job)
		if err != nil {
			return fmt.Errorf("generating content for job %s: %w", job.ID, err)
		}

		// Write job file
		filepath := filepath.Join(dir, job.Filename)
		if err := os.WriteFile(filepath, content, 0644); err != nil {
			return fmt.Errorf("writing job file %s: %w", job.Filename, err)
		}
	}

	return nil
}

