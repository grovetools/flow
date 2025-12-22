package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var StandardFeatureRecipeScenario = harness.NewScenario(
	"standard-feature-recipe",
	"Tests the complete standard-feature recipe workflow with worktree, from spec through implementation to review.",
	[]string{"recipe", "standard-feature", "worktree", "agent"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "feature-project")
			if err != nil {
				return err
			}

			// Create a git repo with initial commit
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Feature Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Initialize plan from standard-feature recipe with worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "add-user-auth", "--recipe", "standard-feature", "--worktree")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify worktree and plan creation", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "feature-project"

			// Verify worktree was created
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "add-user-auth")
			if err := fs.AssertExists(worktreePath); err != nil {
				return err
			}

			// Verify plan directory
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "add-user-auth")
			ctx.Set("plan_path", planPath)

			if err := fs.AssertExists(planPath); err != nil {
				return err
			}
			return fs.AssertExists(filepath.Join(planPath, ".grove-plan.yml"))
		}),

		harness.NewStep("Verify all recipe jobs were created with correct structure", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			expectedJobs := map[string]struct {
				jobType    orchestration.JobType
				template   string
				dependsOn  []string
				prependDep bool
				gitChanges bool
			}{
				"01-cx.md": {
					jobType:  orchestration.JobTypeInteractiveAgent,
					template: "cx-builder",
				},
				"02-spec.md": {
					jobType:   orchestration.JobTypeOneshot,
					template:  "chat",
					dependsOn: []string{"01-cx.md"},
				},
				"03-generate-plan.md": {
					jobType:    orchestration.JobTypeOneshot,
					template:   "agent-xml",
					dependsOn:  []string{"02-spec.md"},
					prependDep: true,
				},
				"04-implement.md": {
					jobType:   orchestration.JobTypeHeadlessAgent,
					dependsOn: []string{"03-generate-plan.md"},
				},
				"05-spec-tests.md": {
					jobType:    orchestration.JobTypeOneshot,
					dependsOn:  []string{"02-spec.md", "03-generate-plan.md", "04-implement.md"},
					prependDep: true,
				},
				"06-impl-tests.md": {
					jobType:    orchestration.JobTypeInteractiveAgent,
					template:   "tend-tester",
					dependsOn:  []string{"05-spec-tests.md"},
					prependDep: true,
					gitChanges: true,
				},
				"07-review.md": {
					jobType:    orchestration.JobTypeOneshot,
					dependsOn:  []string{"06-impl-tests.md"},
					prependDep: true,
					gitChanges: true,
				},
				"08-follow-up.md": {
					jobType:   orchestration.JobTypeInteractiveAgent,
					dependsOn: []string{"02-spec.md", "03-generate-plan.md", "04-implement.md", "05-spec-tests.md", "06-impl-tests.md", "07-review.md"},
				},
			}

			for jobFile, expected := range expectedJobs {
				jobPath := filepath.Join(planPath, jobFile)
				if err := fs.AssertExists(jobPath); err != nil {
					return fmt.Errorf("expected job file %s to exist: %w", jobFile, err)
				}

				// Load and verify job structure
				job, err := orchestration.LoadJob(jobPath)
				if err != nil {
					return fmt.Errorf("loading job %s: %w", jobFile, err)
				}

				// Verify type
				if job.Type != expected.jobType {
					return fmt.Errorf("job %s: expected type %s, got %s", jobFile, expected.jobType, job.Type)
				}

				// Verify template if specified
				if expected.template != "" && job.Template != expected.template {
					return fmt.Errorf("job %s: expected template %s, got %s", jobFile, expected.template, job.Template)
				}

				// Verify dependencies
				if len(expected.dependsOn) > 0 {
					if len(job.DependsOn) != len(expected.dependsOn) {
						return fmt.Errorf("job %s: expected %d dependencies, got %d", jobFile, len(expected.dependsOn), len(job.DependsOn))
					}
					for i, dep := range expected.dependsOn {
						if job.DependsOn[i] != dep {
							return fmt.Errorf("job %s: expected dependency %s, got %s", jobFile, dep, job.DependsOn[i])
						}
					}
				}

				// Verify prepend_dependencies
				if expected.prependDep && !job.PrependDependencies {
					return fmt.Errorf("job %s: expected prepend_dependencies to be true", jobFile)
				}

				// Verify git_changes
				if expected.gitChanges && !job.GitChanges {
					return fmt.Errorf("job %s: expected git_changes to be true", jobFile)
				}
			}

			return nil
		}),

		harness.NewStep("Verify dependency chain is correct", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Verify the dependency chain
			testCases := []struct {
				jobFile  string
				expected []string
			}{
				{"02-spec.md", []string{"01-cx.md"}},
				{"03-generate-plan.md", []string{"02-spec.md"}},
				{"04-implement.md", []string{"03-generate-plan.md"}},
				{"05-spec-tests.md", []string{"02-spec.md", "03-generate-plan.md", "04-implement.md"}},
				{"06-impl-tests.md", []string{"05-spec-tests.md"}},
				{"07-review.md", []string{"06-impl-tests.md"}},
				{"08-follow-up.md", []string{"02-spec.md", "03-generate-plan.md", "04-implement.md", "05-spec-tests.md", "06-impl-tests.md", "07-review.md"}},
			}

			for _, tc := range testCases {
				jobPath := filepath.Join(planPath, tc.jobFile)
				job, err := orchestration.LoadJob(jobPath)
				if err != nil {
					return fmt.Errorf("loading job %s: %w", tc.jobFile, err)
				}

				if len(job.DependsOn) != len(tc.expected) {
					return fmt.Errorf("job %s: expected %d dependencies, got %d", tc.jobFile, len(tc.expected), len(job.DependsOn))
				}

				for i, dep := range tc.expected {
					if job.DependsOn[i] != dep {
						return fmt.Errorf("job %s: expected dependency %s, got %s", tc.jobFile, dep, job.DependsOn[i])
					}
				}
			}

			return nil
		}),

		harness.NewStep("Mark cx job as completed", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			cxJobPath := filepath.Join(planPath, "01-cx.md")

			// Mark cx job as completed (interactive_agent jobs don't create briefing files with --model mock)
			content, err := fs.ReadString(cxJobPath)
			if err != nil {
				return fmt.Errorf("reading cx job: %w", err)
			}
			content = strings.Replace(content, "status: pending", "status: completed", 1)
			if err := fs.WriteString(cxJobPath, content); err != nil {
				return fmt.Errorf("updating cx job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Run spec job and verify briefing file", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			specJobPath := filepath.Join(planPath, "02-spec.md")

			// Run the spec job with mock model (cx job already completed from previous step)
			cmd := ctx.Bin("plan", "run", specJobPath, "--model", "mock", "-y")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Load the spec job to get its ID
			specJob, err := orchestration.LoadJob(specJobPath)
			if err != nil {
				return fmt.Errorf("loading spec job: %w", err)
			}
			jobID := specJob.ID

			// Verify briefing file was created in job's artifact directory
			jobArtifactDir := filepath.Join(planPath, ".artifacts", jobID)
			briefingFiles, err := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if err != nil {
				return fmt.Errorf("checking for briefing files: %w", err)
			}
			if len(briefingFiles) == 0 {
				return fmt.Errorf("expected briefing file for spec job in %s", jobArtifactDir)
			}

			// Verify briefing file has proper XML structure
			briefingContent, err := fs.ReadString(briefingFiles[0])
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			if !strings.Contains(briefingContent, "<prompt>") {
				return fmt.Errorf("briefing file missing <prompt> tag")
			}
			if !strings.Contains(briefingContent, "<user_request") {
				return fmt.Errorf("briefing file missing <user_request> tag")
			}

			return nil
		}),

		harness.NewStep("Add spec content and run generate-plan job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			specJobPath := filepath.Join(planPath, "02-spec.md")
			planJobPath := filepath.Join(planPath, "03-generate-plan.md")

			// Add some spec content to the spec job
			specContent := `

## Specification

### User Stories
- As a developer, I want to test the recipe workflow

### Technical Requirements
- Must generate proper job dependencies
`
			// Read existing content and append
			existingContent, err := fs.ReadString(specJobPath)
			if err != nil {
				return fmt.Errorf("reading spec job: %w", err)
			}
			if err := fs.WriteString(specJobPath, existingContent+specContent); err != nil {
				return fmt.Errorf("appending to spec job: %w", err)
			}

			// Run the generate-plan job with mock model
			cmd := ctx.Bin("plan", "run", planJobPath, "--model", "mock", "-y")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Load the generate-plan job to get its ID
			planJob, err := orchestration.LoadJob(planJobPath)
			if err != nil {
				return fmt.Errorf("loading plan job: %w", err)
			}
			jobID := planJob.ID

			// Verify briefing file was created in job's artifact directory
			jobArtifactDir := filepath.Join(planPath, ".artifacts", jobID)
			briefingFiles, err := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if err != nil {
				return fmt.Errorf("checking for briefing files: %w", err)
			}
			if len(briefingFiles) == 0 {
				return fmt.Errorf("expected briefing file for generate-plan job")
			}

			// Verify briefing file has prepended dependency
			briefingContent, err := fs.ReadString(briefingFiles[0])
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			// Should have the agent-xml template
			if !strings.Contains(briefingContent, `<system_instructions template="agent-xml">`) {
				return fmt.Errorf("briefing file missing agent-xml system instructions")
			}

			// Should have prepended dependency with spec content
			if !strings.Contains(briefingContent, "<prepended_dependency") {
				return fmt.Errorf("briefing file missing <prepended_dependency> tag")
			}
			if !strings.Contains(briefingContent, "02-spec.md") {
				return fmt.Errorf("briefing file missing reference to spec dependency")
			}
			if !strings.Contains(briefingContent, "User Stories") {
				return fmt.Errorf("briefing file missing spec content from prepended dependency")
			}

			return nil
		}),

		harness.NewStep("Run implement job and verify briefing file", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			implementJobPath := filepath.Join(planPath, "04-implement.md")

			// Run the implement job with mock model (dependencies already completed)
			cmd := ctx.Bin("plan", "run", implementJobPath, "--model", "mock", "-y")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Load the implement job to get its ID
			implementJob, err := orchestration.LoadJob(implementJobPath)
			if err != nil {
				return fmt.Errorf("loading implement job: %w", err)
			}
			jobID := implementJob.ID

			// Verify briefing file was created
			jobArtifactDir := filepath.Join(planPath, ".artifacts", jobID)
			briefingFiles, err := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if err != nil {
				return fmt.Errorf("checking for briefing files: %w", err)
			}
			if len(briefingFiles) == 0 {
				return fmt.Errorf("expected briefing file for implement job")
			}

			// Verify briefing file has proper structure
			briefingContent, err := fs.ReadString(briefingFiles[0])
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			if !strings.Contains(briefingContent, "<prompt>") {
				return fmt.Errorf("briefing file missing <prompt> tag")
			}
			// Headless agent should have context
			if !strings.Contains(briefingContent, "<context>") {
				return fmt.Errorf("briefing file missing <context> tag")
			}

			return nil
		}),

		harness.NewStep("Run spec-tests job and verify briefing file", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			specTestsJobPath := filepath.Join(planPath, "05-spec-tests.md")

			// Run the spec-tests job with mock model (dependencies already completed)
			cmd := ctx.Bin("plan", "run", specTestsJobPath, "--model", "mock", "-y")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Load the spec-tests job to get its ID
			specTestsJob, err := orchestration.LoadJob(specTestsJobPath)
			if err != nil {
				return fmt.Errorf("loading spec-tests job: %w", err)
			}
			jobID := specTestsJob.ID

			// Verify briefing file was created
			jobArtifactDir := filepath.Join(planPath, ".artifacts", jobID)
			briefingFiles, err := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if err != nil {
				return fmt.Errorf("checking for briefing files: %w", err)
			}
			if len(briefingFiles) == 0 {
				return fmt.Errorf("expected briefing file for spec-tests job")
			}

			// Verify briefing file has prepended dependencies
			briefingContent, err := fs.ReadString(briefingFiles[0])
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			if !strings.Contains(briefingContent, "<prepended_dependency") {
				return fmt.Errorf("briefing file missing <prepended_dependency> tag")
			}
			if !strings.Contains(briefingContent, "02-spec.md") {
				return fmt.Errorf("briefing file missing reference to spec dependency")
			}

			return nil
		}),

		harness.NewStep("Simulate implementation by creating code changes in worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "add-user-auth")

			repo, err := git.SetupTestRepo(worktreePath)
			if err != nil {
				return fmt.Errorf("setting up git repo: %w", err)
			}

			// 1. Create a file and COMMIT it (will show in "git diff main...HEAD")
			committedFilePath := filepath.Join(worktreePath, "models.go")
			committedFileContent := `package main

// User represents an authenticated user
type User struct {
	ID       int
	Username string
	Email    string
	PasswordHash string
}
`
			if err := fs.WriteString(committedFilePath, committedFileContent); err != nil {
				return fmt.Errorf("creating models.go: %w", err)
			}
			if err := repo.AddCommit("Add User model"); err != nil {
				return fmt.Errorf("committing models.go: %w", err)
			}

			// 2. Create a file and STAGE it (will show in "git diff --cached")
			stagedFilePath := filepath.Join(worktreePath, "handlers.go")
			stagedFileContent := `package main

import "net/http"

// LoginHandler handles login requests
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement login logic
}
`
			if err := fs.WriteString(stagedFilePath, stagedFileContent); err != nil {
				return fmt.Errorf("creating handlers.go: %w", err)
			}
			if err := repo.Add("handlers.go"); err != nil {
				return fmt.Errorf("staging handlers.go: %w", err)
			}

			// 3. Modify an existing file without staging (will show in "git diff")
			readmePath := filepath.Join(worktreePath, "README.md")
			existingReadme, err := fs.ReadString(readmePath)
			if err != nil {
				return fmt.Errorf("reading README.md: %w", err)
			}

			modifiedReadme := existingReadme + `

## Authentication System

This project now includes a complete authentication system:

- User model with password hashing
- Login/logout handlers
- JWT token generation
- Session management

## API Endpoints

- POST /api/login - Authenticate user
- POST /api/logout - End session
- GET /api/profile - Get current user
`
			if err := fs.WriteString(readmePath, modifiedReadme); err != nil {
				return fmt.Errorf("modifying README.md: %w", err)
			}

			// 4. Create a new untracked file (will show in new files section with full content)
			untrackedFilePath := filepath.Join(worktreePath, "auth.go")
			untrackedFileContent := `package main

import (
	"crypto/bcrypt"
	"errors"
)

// HashPassword hashes a password using bcrypt
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

// CheckPasswordHash compares a password with a hash
func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}
`
			if err := fs.WriteString(untrackedFilePath, untrackedFileContent); err != nil {
				return fmt.Errorf("creating auth.go: %w", err)
			}

			return nil
		}),

		harness.NewStep("Mark impl-tests job as completed", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			implTestsJobPath := filepath.Join(planPath, "06-impl-tests.md")

			// Mark impl-tests job as completed (interactive_agent jobs don't create briefing files with --model mock)
			content, err := fs.ReadString(implTestsJobPath)
			if err != nil {
				return fmt.Errorf("reading impl-tests job: %w", err)
			}
			content = strings.Replace(content, "status: pending", "status: completed", 1)
			if err := fs.WriteString(implTestsJobPath, content); err != nil {
				return fmt.Errorf("updating impl-tests job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Run review job and verify briefing contains git changes", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "add-user-auth")
			reviewJobPath := filepath.Join(planPath, "07-review.md")

			// Run the review job from the worktree directory (all dependencies already completed)
			cmd := ctx.Bin("plan", "run", reviewJobPath, "-y", "--model", "mock")
			cmd.Dir(worktreePath)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Load the review job to get its ID
			reviewJob, err := orchestration.LoadJob(reviewJobPath)
			if err != nil {
				return fmt.Errorf("loading review job: %w", err)
			}
			jobID := reviewJob.ID

			// Verify briefing file was created in job's artifact directory
			jobArtifactDir := filepath.Join(planPath, ".artifacts", jobID)
			briefingFiles, err := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if err != nil {
				return fmt.Errorf("checking for briefing files: %w", err)
			}
			if len(briefingFiles) == 0 {
				return fmt.Errorf("expected briefing file for review job in %s", jobArtifactDir)
			}
			briefingFile := briefingFiles[0]

			briefingContent, err := fs.ReadString(briefingFile)
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			// Verify the briefing contains the git_changes block
			if !strings.Contains(briefingContent, "<git_changes>") {
				return fmt.Errorf("briefing file missing <git_changes> block")
			}

			// Verify it contains content from each category of change
			// 1. Committed changes (models.go)
			if !strings.Contains(briefingContent, `<git_change type="committed"`) {
				return fmt.Errorf("briefing file missing committed changes section")
			}
			if !strings.Contains(briefingContent, "type User struct") {
				return fmt.Errorf("briefing file missing committed changes for models.go")
			}

			// 2. Staged changes (handlers.go)
			if !strings.Contains(briefingContent, `<git_change type="staged"`) {
				return fmt.Errorf("briefing file missing staged changes section")
			}
			if !strings.Contains(briefingContent, "LoginHandler") {
				return fmt.Errorf("briefing file missing staged changes for handlers.go")
			}

			// 3. Uncommitted changes (README.md)
			if !strings.Contains(briefingContent, `<git_change type="uncommitted"`) {
				return fmt.Errorf("briefing file missing uncommitted changes section")
			}
			if !strings.Contains(briefingContent, "Authentication System") {
				return fmt.Errorf("briefing file missing uncommitted changes for README.md")
			}

			// 4. Untracked files (auth.go)
			if !strings.Contains(briefingContent, `<git_change type="untracked_files"`) {
				return fmt.Errorf("briefing file missing untracked files section")
			}
			if !strings.Contains(briefingContent, "HashPassword") {
				return fmt.Errorf("briefing file missing untracked file content for auth.go")
			}

			return nil
		}),

		harness.NewStep("Mark follow-up job as completed", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			followUpJobPath := filepath.Join(planPath, "08-follow-up.md")

			// Mark follow-up job as completed (interactive_agent jobs don't create briefing files with --model mock)
			content, err := fs.ReadString(followUpJobPath)
			if err != nil {
				return fmt.Errorf("reading follow-up job: %w", err)
			}
			content = strings.Replace(content, "status: pending", "status: completed", 1)
			if err := fs.WriteString(followUpJobPath, content); err != nil {
				return fmt.Errorf("updating follow-up job: %w", err)
			}

			return nil
		}),
	},
)
