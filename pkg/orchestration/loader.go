package orchestration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/grovetools/core/util/sanitize"
	"gopkg.in/yaml.v3"
)

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

	// Sort jobs by numeric prefix so 99 < 100 < 101
	numPrefix := regexp.MustCompile(`^(\d+)`)
	sort.Slice(plan.Jobs, func(i, j int) bool {
		mi := numPrefix.FindString(plan.Jobs[i].Filename)
		mj := numPrefix.FindString(plan.Jobs[j].Filename)
		ni, _ := strconv.Atoi(mi)
		nj, _ := strconv.Atoi(mj)
		if ni != nj {
			return ni < nj
		}
		return plan.Jobs[i].Filename < plan.Jobs[j].Filename
	})

	// Resolve dependencies
	if err := plan.ResolveDependencies(); err != nil {
		return nil, err
	}

	return plan, nil
}

// LoadPlanLenient loads a plan for browse/index consumers, tolerating invalid
// job files. LoadPlan's hard failure is right for execution paths — running a
// plan with a malformed job must error — but an indexer that propagates it
// makes the WHOLE plan vanish from every daemon client the moment one .md has
// broken frontmatter, including the half-written window while a plan is being
// created. Here such files are skipped and reported; the plan row survives
// with the jobs that did load.
func LoadPlanLenient(dir string) (*Plan, []error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, []error{fmt.Errorf("plan directory not found: %w", err)}
	}

	plan := &Plan{
		Name:      filepath.Base(dir),
		Directory: dir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}
	var problems []error

	specPath := filepath.Join(dir, "spec.md")
	if _, err := os.Stat(specPath); err == nil {
		plan.SpecFile = specPath
	}

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

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{fmt.Errorf("reading plan directory: %w", err)}
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		filename := entry.Name()
		job, err := LoadJob(filepath.Join(dir, filename))
		if err != nil {
			var notAJob ErrNotAJob
			if !errors.As(err, &notAJob) {
				problems = append(problems, fmt.Errorf("skipping job %s: %w", filename, err))
			}
			continue
		}
		job.Filename = filename
		job.FilePath = filepath.Join(dir, filename)
		if job.ID != "" {
			if existing, exists := plan.JobsByID[job.ID]; exists {
				problems = append(problems, fmt.Errorf("skipping job %s: duplicate job ID %q also in %s",
					filename, job.ID, existing.Filename))
				continue
			}
			plan.JobsByID[job.ID] = job
		}
		plan.Jobs = append(plan.Jobs, job)
	}

	numPrefix := regexp.MustCompile(`^(\d+)`)
	sort.Slice(plan.Jobs, func(i, j int) bool {
		mi := numPrefix.FindString(plan.Jobs[i].Filename)
		mj := numPrefix.FindString(plan.Jobs[j].Filename)
		ni, _ := strconv.Atoi(mi)
		nj, _ := strconv.Atoi(mj)
		if ni != nj {
			return ni < nj
		}
		return plan.Jobs[i].Filename < plan.Jobs[j].Filename
	})

	// Dependency cycles are an execution-time problem; for browsing, report
	// and keep the plan.
	if err := plan.ResolveDependencies(); err != nil {
		problems = append(problems, err)
	}

	return plan, problems
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

	// Extract the raw YAML string without allocating a
	// map[string]interface{} or doing a yaml.Marshal round-trip — the
	// old path showed up as ~50% of the browser refresh CPU profile.
	yamlStr, body, err := ExtractFrontmatterString(content)
	if err != nil {
		return nil, fmt.Errorf("extracting frontmatter: %w", err)
	}

	if strings.TrimSpace(yamlStr) == "" {
		return nil, ErrNotAJob{Reason: "no frontmatter"}
	}

	job := &Job{
		PromptBody: sanitize.UTF8(body),
	}

	if err := yaml.Unmarshal([]byte(yamlStr), job); err != nil {
		return nil, fmt.Errorf("unmarshaling to job struct: %w", err)
	}

	// Flat metadata fields (last_error, retry_count) are stored at the top
	// level of the frontmatter but belong inside job.Metadata in memory.
	// Unmarshal once more into a narrow shim to pick them up without
	// reintroducing the map allocation. The shim also detects the REMOVED
	// pinned_context key (spec 19 D5): it maps to no Job field anymore, so
	// its presence is recorded on the job and rejected at execution time with
	// an actionable error (loading must keep working so the plan stays
	// browsable and the failure can surface as job status/last_error).
	type flatMeta struct {
		LastError     string      `yaml:"last_error"`
		RetryCount    int         `yaml:"retry_count"`
		PinnedContext interface{} `yaml:"pinned_context"`
	}
	var fm flatMeta
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err == nil {
		job.Metadata.LastError = fm.LastError
		job.Metadata.RetryCount = fm.RetryCount
		job.HasLegacyPinnedContext = fm.PinnedContext != nil
	}

	// Rewrite any reference to a deleted builtin template to its current
	// replacement (either a skill or the default chat template). See
	// template_shim.go for the mapping.
	applyTemplateShim(job)

	// Validate job type first - only job types are processed
	if job.Type != JobTypeOneshot && job.Type != JobTypeAgent && job.Type != JobTypeHeadlessAgent && job.Type != JobTypeShell && job.Type != JobTypeChat && job.Type != JobTypeInteractiveAgent && job.Type != JobTypeIsolatedAgent && job.Type != JobTypeGenerateRecipe && job.Type != JobTypeFile {
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
		JobStatusFailed, JobStatusBlocked, JobStatusNeedsReview, JobStatusPendingUser,
		JobStatusPendingLLM, JobStatusHold, JobStatusTodo, JobStatusAbandoned, JobStatusIdle:
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating plan directory: %w", err)
	}

	// Set plan directory
	plan.Directory = dir
	plan.Name = filepath.Base(dir)

	// Persist plan-level configuration (.grove-plan.yml) when present so
	// callers that mutate plan.Config (e.g. MarkPlanReview flipping Status to
	// "review") have their changes written back. plan.Config carries the full
	// config loaded by LoadPlan, so marshaling it preserves all other fields.
	if plan.Config != nil {
		yamlData, err := yaml.Marshal(plan.Config)
		if err != nil {
			return fmt.Errorf("marshaling plan config: %w", err)
		}
		configPath := filepath.Join(dir, ".grove-plan.yml")
		if err := os.WriteFile(configPath, yamlData, 0o600); err != nil {
			return fmt.Errorf("writing plan config: %w", err)
		}
	}

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
		if err := os.WriteFile(filepath, content, 0o600); err != nil {
			return fmt.Errorf("writing job file %s: %w", job.Filename, err)
		}
	}

	return nil
}
