package scenarios

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var AgentWorktreeLifecycleScenario = harness.NewScenario(
	"agent-worktree-lifecycle",
	"Tests agent job execution in a git worktree and manual completion.",
	[]string{"core", "cli", "agent", "worktree"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "worktree-project")
			if err != nil {
				return err
			}

			// Create a dummy file for initial commit
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Test Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"}, // Mocks `grove aglogs`
			harness.Mock{CommandName: "claude"}, // Mock claude to prevent actual agent launch
			harness.Mock{CommandName: "tmux"},   // Mock tmux to prevent real sessions
		),

		harness.NewStep("Initialize plan with a worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "agent-plan", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify worktree and branch creation", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "agent-plan")
			if err := fs.AssertExists(worktreePath); err != nil {
				return err
			}

			// Check if branch exists using git branch command
			cmd := exec.Command("git", "branch", "--list", "agent-plan")
			cmd.Dir = projectDir
			output, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("failed to check for branch: %w", err)
			}
			if !strings.Contains(string(output), "agent-plan") {
				return fmt.Errorf("expected branch 'agent-plan' to be created")
			}
			return nil
		}),

		harness.NewStep("Add dependency jobs to the plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "worktree-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "agent-plan")
			ctx.Set("plan_path", planPath)

			// Add first dependency job (chat type)
			cmd := ctx.Bin("plan", "add", "agent-plan",
				"--type", "chat",
				"--title", "Design Document",
				"-p", "Create a design document for the feature")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Mark the first dependency as completed and add some content
			designJobPath := filepath.Join(planPath, "01-design-document.md")
			content, err := fs.ReadString(designJobPath)
			if err != nil {
				return fmt.Errorf("reading design job file: %w", err)
			}

			// Update status to completed and add content to the body
			updatedContent := strings.Replace(content, "status: pending_user", "status: completed", 1)
			// Add some design content after the frontmatter
			updatedContent += "\n## Design Overview\n\nThis is the design document content that should appear in the briefing file.\n\n### Key Requirements\n- Requirement 1: Build feature X\n- Requirement 2: Use technology Y\n"
			if err := fs.WriteString(designJobPath, updatedContent); err != nil {
				return fmt.Errorf("updating design job: %w", err)
			}

			// Add second dependency job (oneshot type)
			cmd = ctx.Bin("plan", "add", "agent-plan",
				"--type", "oneshot",
				"--title", "Architecture Plan",
				"-p", "Define the architecture")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Mark the second dependency as completed and add content
			archJobPath := filepath.Join(planPath, "02-architecture-plan.md")
			content, err = fs.ReadString(archJobPath)
			if err != nil {
				return fmt.Errorf("reading arch job file: %w", err)
			}

			updatedContent = strings.Replace(content, "status: pending", "status: completed", 1)
			updatedContent += "\n## Architecture\n\nThe system will use a three-tier architecture.\n\n### Components\n1. Frontend: React\n2. Backend: Go\n3. Database: PostgreSQL\n"
			if err := fs.WriteString(archJobPath, updatedContent); err != nil {
				return fmt.Errorf("updating arch job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Add first interactive_agent job WITH prepend_dependencies", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Add the interactive agent job that depends on the previous two
			// Using --prepend-dependencies to inline dependency content
			cmd := ctx.Bin("plan", "add", "agent-plan",
				"--type", "interactive_agent",
				"--title", "Implement Task",
				"-p", "Implement a test feature based on the design and architecture",
				"-d", "01-design-document.md",
				"-d", "02-architecture-plan.md",
				"--prepend-dependencies")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify the job has prepend_dependencies: true
			jobPath := filepath.Join(planPath, "03-implement-task.md")
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}
			if !strings.Contains(content, "prepend_dependencies: true") {
				return fmt.Errorf("job file missing prepend_dependencies: true flag")
			}

			return nil
		}),

		harness.NewStep("Add second interactive_agent job WITHOUT prepend_dependencies", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Add another interactive agent job without prepend_dependencies
			// This should just list the dependency file paths
			cmd := ctx.Bin("plan", "add", "agent-plan",
				"--type", "interactive_agent",
				"--title", "Review Task",
				"-p", "Review the implementation and ensure quality",
				"-d", "03-implement-task.md")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify the job does NOT have prepend_dependencies: true
			jobPath := filepath.Join(planPath, "04-review-task.md")
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}
			if strings.Contains(content, "prepend_dependencies: true") {
				return fmt.Errorf("job file should not have prepend_dependencies: true flag")
			}

			// Store this job path for later testing
			ctx.Set("review_job_path", jobPath)

			return nil
		}),

		harness.NewStep("Simulate agent launch by setting job to running", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "03-implement-task.md")
			ctx.Set("job_path", jobPath)

			// Actually run the job with --skip-interactive to trigger briefing file generation
			// but prevent the tmux session from launching
			cmd := ctx.Bin("plan", "run", "--skip-interactive", jobPath)
			cmd.Dir(projectDir)
			_ = cmd.Run() // Ignore result as --skip-interactive causes intentional failure

			// Now manually set the job to running and create a lock file
			// to simulate a successful agent launch for the rest of the test
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}

			// Replace status: failed with status: running (since --skip-interactive causes failure)
			updatedContent := strings.Replace(content, "status: failed", "status: running", 1)
			if err := fs.WriteString(jobPath, updatedContent); err != nil {
				return fmt.Errorf("updating job status: %w", err)
			}

			// Create a lock file to simulate an active session
			lockPath := jobPath + ".lock"
			if err := fs.WriteString(lockPath, fmt.Sprintf("pid: 12345\nsession: agent-plan\n")); err != nil {
				return fmt.Errorf("creating lock file: %w", err)
			}

			// Verify the briefing file was created in .artifacts
			artifactsDir := filepath.Join(planPath, ".artifacts")
			if err := fs.AssertExists(artifactsDir); err != nil {
				return fmt.Errorf("expected .artifacts directory to exist: %w", err)
			}

			// Check that at least one briefing file exists
			files, err := filepath.Glob(filepath.Join(artifactsDir, "briefing-*.md"))
			if err != nil {
				return fmt.Errorf("error checking for briefing files: %w", err)
			}
			if len(files) == 0 {
				return fmt.Errorf("expected at least one briefing file in .artifacts directory")
			}

			// Store the briefing file path for later verification
			ctx.Set("briefing_file", files[0])

			return nil
		}),

		harness.NewStep("Verify job is in 'running' state", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")
			briefingFile := ctx.GetString("briefing_file")

			// Assert job status
			if err := assert.YAMLField(jobPath, "status", "running", "Job status should be 'running'"); err != nil {
				return err
			}

			// Assert lock file exists
			if err := fs.AssertExists(jobPath + ".lock"); err != nil {
				return err
			}

			// Verify briefing file content
			briefingContent, err := fs.ReadString(briefingFile)
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			// Check that briefing file contains expected sections
			expectedSections := []string{
				"# Agent Briefing:",
				"**Job ID:**",
				"**Work Directory:**",
				"## Context from Dependencies",
				"## Primary Task",
			}
			for _, section := range expectedSections {
				if !strings.Contains(briefingContent, section) {
					return fmt.Errorf("briefing file missing expected section: %s", section)
				}
			}

			// Verify dependency content is included
			expectedDependencyContent := []string{
				"01-design-document.md",
				"Design Overview",
				"This is the design document content that should appear in the briefing file",
				"Requirement 1: Build feature X",
				"02-architecture-plan.md",
				"Architecture",
				"The system will use a three-tier architecture",
				"Frontend: React",
			}
			for _, content := range expectedDependencyContent {
				if !strings.Contains(briefingContent, content) {
					return fmt.Errorf("briefing file missing expected dependency content: %s", content)
				}
			}

			// Verify the primary task content
			if !strings.Contains(briefingContent, "Implement a test feature based on the design and architecture") {
				return fmt.Errorf("briefing file missing primary task content")
			}

			return nil
		}),

		harness.NewStep("Complete job 03 first", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			job03Path := filepath.Join(planPath, "03-implement-task.md")

			// Remove job 03's lock file before completing (simulates session ending)
			lockPath := job03Path + ".lock"
			if err := fs.RemoveIfExists(lockPath); err != nil {
				return fmt.Errorf("removing job 03 lock file: %w", err)
			}

			// Complete job 03 using the plan complete command
			cmd := ctx.Bin("plan", "complete", job03Path)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Run job 04 WITHOUT prepend_dependencies", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			reviewJobPath := ctx.GetString("review_job_path")

			// Run job 04 with --skip-interactive to trigger briefing file generation
			cmd := ctx.Bin("plan", "run", "--skip-interactive", reviewJobPath)
			cmd.Dir(projectDir)
			_ = cmd.Run() // Ignore result as --skip-interactive causes intentional failure

			// Manually set job 04 to running
			content, err := fs.ReadString(reviewJobPath)
			if err != nil {
				return fmt.Errorf("reading review job file: %w", err)
			}

			updatedContent := strings.Replace(content, "status: failed", "status: running", 1)
			if err := fs.WriteString(reviewJobPath, updatedContent); err != nil {
				return fmt.Errorf("updating review job status: %w", err)
			}

			// Create a lock file for job 04
			lockPath := reviewJobPath + ".lock"
			if err := fs.WriteString(lockPath, "pid: 12346\nsession: agent-plan-review\n"); err != nil {
				return fmt.Errorf("creating lock file: %w", err)
			}

			return nil
		}),

		harness.NewStep("Verify both briefing files exist with correct formats", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			artifactsDir := filepath.Join(planPath, ".artifacts")

			// Check that we now have TWO briefing files
			briefingFiles, err := filepath.Glob(filepath.Join(artifactsDir, "briefing-*.md"))
			if err != nil {
				return fmt.Errorf("error finding briefing files: %w", err)
			}
			if len(briefingFiles) != 2 {
				// Debug: list what we found
				fileList := ""
				for _, f := range briefingFiles {
					fileList += filepath.Base(f) + " "
				}
				return fmt.Errorf("expected exactly 2 briefing files, found %d: %s", len(briefingFiles), fileList)
			}

			// Find which briefing belongs to which job by checking the Job ID or title in the filename
			var job03Briefing, job04Briefing string
			for _, briefingPath := range briefingFiles {
				// The filename format is: briefing-{job-id}-{timestamp}.md
				// Job IDs follow the pattern: {sanitized-title}-{hash}
				filename := filepath.Base(briefingPath)

				// Read content to identify job
				content, err := fs.ReadString(briefingPath)
				if err != nil {
					return fmt.Errorf("reading briefing file %s: %w", briefingPath, err)
				}

				// Check by title in the briefing header
				if strings.Contains(content, "# Agent Briefing: Implement Task") {
					job03Briefing = briefingPath
				} else if strings.Contains(content, "# Agent Briefing: Review Task") {
					job04Briefing = briefingPath
				} else if strings.Contains(filename, "implement-task") {
					job03Briefing = briefingPath
				} else if strings.Contains(filename, "review-task") {
					job04Briefing = briefingPath
				}
			}

			if job03Briefing == "" {
				return fmt.Errorf("could not find briefing file for job 03 (Implement Task)")
			}
			if job04Briefing == "" {
				return fmt.Errorf("could not find briefing file for job 04 (Review Task)")
			}

			// Verify job 03's briefing (WITH prepend_dependencies: true) has INLINED content
			job03Content, err := fs.ReadString(job03Briefing)
			if err != nil {
				return fmt.Errorf("reading job 03 briefing: %w", err)
			}

			if !strings.Contains(job03Content, "## Context from Dependencies") {
				return fmt.Errorf("job 03 briefing missing 'Context from Dependencies' section")
			}

			// Should contain the actual dependency content since prepend_dependencies is true
			if !strings.Contains(job03Content, "Design Overview") {
				return fmt.Errorf("job 03 briefing missing inlined dependency content from design doc")
			}
			if !strings.Contains(job03Content, "Architecture") {
				return fmt.Errorf("job 03 briefing missing inlined dependency content from architecture")
			}

			// Verify job 04's briefing (WITHOUT prepend_dependencies) has FILE PATHS only
			job04Content, err := fs.ReadString(job04Briefing)
			if err != nil {
				return fmt.Errorf("reading job 04 briefing: %w", err)
			}

			if !strings.Contains(job04Content, "## Dependency Files") {
				return fmt.Errorf("job 04 briefing missing 'Dependency Files' section")
			}

			// Should list file paths, not inline content
			if !strings.Contains(job04Content, "Read the following dependency files for context:") {
				return fmt.Errorf("job 04 briefing missing instruction to read dependency files")
			}

			// Should contain path to job 03
			if !strings.Contains(job04Content, "03-implement-task.md") {
				return fmt.Errorf("job 04 briefing missing path to dependency file")
			}

			// Should NOT contain the actual content from job 03 (no inlining)
			if strings.Contains(job04Content, "Design Overview") {
				return fmt.Errorf("job 04 briefing should NOT inline dependency content")
			}

			ctx.Set("job04_briefing", job04Briefing)
			ctx.Set("review_job_path", ctx.GetString("review_job_path"))

			return nil
		}),

		harness.NewStep("Complete job 04 manually", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			reviewJobPath := ctx.GetString("review_job_path")

			// Remove the lock file before completing (simulates session ending)
			lockPath := reviewJobPath + ".lock"
			if err := fs.RemoveIfExists(lockPath); err != nil {
				return fmt.Errorf("removing lock file: %w", err)
			}

			// Complete job 04
			cmd := ctx.Bin("plan", "complete", reviewJobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify both jobs are 'completed' and cleaned up", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			job03Path := filepath.Join(planPath, "03-implement-task.md")
			reviewJobPath := ctx.GetString("review_job_path")

			// Verify job 03 is completed
			if err := assert.YAMLField(job03Path, "status", "completed", "Job 03 status should be 'completed'"); err != nil {
				return err
			}

			// Verify job 03 lock file is removed
			if err := fs.AssertNotExists(job03Path + ".lock"); err != nil {
				return err
			}

			// Verify job 03 has transcript
			if err := fs.AssertContains(job03Path, "## Transcript"); err != nil {
				return err
			}

			// Verify job 04 is completed
			if err := assert.YAMLField(reviewJobPath, "status", "completed", "Job 04 status should be 'completed'"); err != nil {
				return err
			}

			// Verify job 04 lock file is removed
			if err := fs.AssertNotExists(reviewJobPath + ".lock"); err != nil {
				return err
			}

			// Verify job 04 has transcript
			return fs.AssertContains(reviewJobPath, "## Transcript")
		}),
	},
)
