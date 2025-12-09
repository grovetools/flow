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
			}{
				"01-spec.md": {
					jobType: orchestration.JobTypeOneshot,
				},
				"02-generate-plan.md": {
					jobType:    orchestration.JobTypeOneshot,
					template:   "agent-xml",
					dependsOn:  []string{"01-spec.md"},
					prependDep: true,
				},
				"03-implement.md": {
					jobType:   orchestration.JobTypeHeadlessAgent,
					dependsOn: []string{"02-generate-plan.md"},
				},
				"04-git-status.md": {
					jobType:   orchestration.JobTypeShell,
					dependsOn: []string{"03-implement.md"},
				},
				"06-review.md": {
					jobType:    orchestration.JobTypeOneshot,
					dependsOn:  []string{"03-implement.md", "04-git-status.md"},
					prependDep: true,
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
			}

			return nil
		}),

		harness.NewStep("Verify git-status job shell script content", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			gitStatusJobPath := filepath.Join(planPath, "04-git-status.md")

			content, err := fs.ReadString(gitStatusJobPath)
			if err != nil {
				return fmt.Errorf("reading git-status job: %w", err)
			}

			// Verify it contains the comprehensive git commands
			expectedContent := []string{
				"git diff main...HEAD",
				"git diff",
				"git diff --cached",
				"for file in $(git ls-files --others --exclude-standard)",
				"cat \"$file\"",
			}

			for _, expected := range expectedContent {
				if !strings.Contains(content, expected) {
					return fmt.Errorf("git-status job missing expected content: %s", expected)
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
				{"02-generate-plan.md", []string{"01-spec.md"}},
				{"03-implement.md", []string{"02-generate-plan.md"}},
				{"04-git-status.md", []string{"03-implement.md"}},
				{"06-review.md", []string{"03-implement.md", "04-git-status.md"}},
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

		harness.NewStep("Run spec job and verify briefing file", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			specJobPath := filepath.Join(planPath, "01-spec.md")

			// Run the spec job with mock model
			cmd := ctx.Bin("plan", "run", specJobPath, "--model", "mock", "-y")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify briefing file was created
			artifactsDir := filepath.Join(planPath, ".artifacts")
			briefingFiles, err := filepath.Glob(filepath.Join(artifactsDir, "briefing-specification-*.xml"))
			if err != nil {
				return fmt.Errorf("checking for briefing files: %w", err)
			}
			if len(briefingFiles) == 0 {
				return fmt.Errorf("expected briefing file for spec job")
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
			specJobPath := filepath.Join(planPath, "01-spec.md")
			planJobPath := filepath.Join(planPath, "02-generate-plan.md")

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

			// Verify briefing file was created
			artifactsDir := filepath.Join(planPath, ".artifacts")
			briefingFiles, err := filepath.Glob(filepath.Join(artifactsDir, "briefing-generate-implementation-plan-*.xml"))
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
			if !strings.Contains(briefingContent, "01-spec.md") {
				return fmt.Errorf("briefing file missing reference to spec dependency")
			}
			if !strings.Contains(briefingContent, "User Stories") {
				return fmt.Errorf("briefing file missing spec content from prepended dependency")
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

		harness.NewStep("Run git-status job and verify it captures changes", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "add-user-auth")
			gitStatusJobPath := filepath.Join(planPath, "04-git-status.md")

			// Mark the generate-plan and implement jobs as completed so git-status can run
			planJobPath := filepath.Join(planPath, "02-generate-plan.md")
			implementJobPath := filepath.Join(planPath, "03-implement.md")

			// Update plan job to completed
			planContent, err := fs.ReadString(planJobPath)
			if err != nil {
				return fmt.Errorf("reading plan job: %w", err)
			}
			planContent = strings.Replace(planContent, "status: completed", "status: completed", 1)
			if err := fs.WriteString(planJobPath, planContent); err != nil {
				return fmt.Errorf("updating plan job: %w", err)
			}

			// Mark implement job as completed
			implementContent := `---
id: implement-add-user-auth
title: "Implement add-user-auth"
status: completed
type: headless_agent
depends_on:
  - 02-generate-plan.md
---

Implementation completed.
`
			if err := fs.WriteString(implementJobPath, implementContent); err != nil {
				return fmt.Errorf("updating implement job: %w", err)
			}

			// Run the git-status job from the worktree directory
			cmd := ctx.Bin("plan", "run", gitStatusJobPath, "-y")
			cmd.Dir(worktreePath)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify the git-status job output contains our changes
			gitStatusContent, err := fs.ReadString(gitStatusJobPath)
			if err != nil {
				return fmt.Errorf("reading git-status job: %w", err)
			}

			// Should have an output section
			if !strings.Contains(gitStatusContent, "## Output") {
				return fmt.Errorf("git-status job missing output section")
			}

			// Should have section headers
			if !strings.Contains(gitStatusContent, "=== Committed changes (diff from main) ===") {
				return fmt.Errorf("git-status output missing committed changes section")
			}
			if !strings.Contains(gitStatusContent, "=== Uncommitted changes (working directory) ===") {
				return fmt.Errorf("git-status output missing uncommitted changes section")
			}
			if !strings.Contains(gitStatusContent, "=== Staged changes (index) ===") {
				return fmt.Errorf("git-status output missing staged changes section")
			}
			if !strings.Contains(gitStatusContent, "=== New/untracked files (full content) ===") {
				return fmt.Errorf("git-status output missing new files section")
			}

			// 1. Verify committed changes (models.go)
			if !strings.Contains(gitStatusContent, "models.go") {
				return fmt.Errorf("git-status output missing committed file models.go")
			}
			if !strings.Contains(gitStatusContent, "type User struct") {
				return fmt.Errorf("git-status output missing models.go content")
			}

			// 2. Verify staged changes (handlers.go)
			if !strings.Contains(gitStatusContent, "handlers.go") {
				return fmt.Errorf("git-status output missing staged file handlers.go")
			}
			if !strings.Contains(gitStatusContent, "LoginHandler") {
				return fmt.Errorf("git-status output missing handlers.go content")
			}

			// 3. Verify uncommitted changes (README.md)
			if !strings.Contains(gitStatusContent, "README.md") {
				return fmt.Errorf("git-status output missing uncommitted README.md changes")
			}
			if !strings.Contains(gitStatusContent, "Authentication System") {
				return fmt.Errorf("git-status output missing README.md content changes")
			}
			if !strings.Contains(gitStatusContent, "API Endpoints") {
				return fmt.Errorf("git-status output missing README.md API documentation")
			}

			// 4. Verify new untracked file (auth.go)
			if !strings.Contains(gitStatusContent, "auth.go") {
				return fmt.Errorf("git-status output missing new auth.go file")
			}
			if !strings.Contains(gitStatusContent, "HashPassword") {
				return fmt.Errorf("git-status output missing code content from auth.go")
			}
			if !strings.Contains(gitStatusContent, "CheckPasswordHash") {
				return fmt.Errorf("git-status output missing CheckPasswordHash function from auth.go")
			}

			return nil
		}),
	},
)
