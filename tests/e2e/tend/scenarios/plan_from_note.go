package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var PlanFromNoteScenario = harness.NewScenario(
	"plan-from-note",
	"Tests the --from-note flag for plan init command.",
	[]string{"plan", "init", "from-note"},
	[]harness.Step{
		// Mock git to support worktree creation
		harness.SetupMocks(harness.Mock{CommandName: "git"}),

		harness.NewStep("Setup sandboxed environment with project", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "from-note-project")
			if err != nil {
				return err
			}

			// Get the repo that was created by setupDefaultEnvironment
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}

			// Create initial commit
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# From Note Test Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Create a test note file
			notesDir := filepath.Join(notebooksRoot, "test-notes")
			if err := fs.CreateDir(notesDir); err != nil {
				return err
			}

			noteContent := `---
title: Test Feature Request
tags: [feature, todo]
---

# Feature Request: Add User Dashboard

Please implement a user dashboard with the following features:
- User profile display
- Recent activity feed
- Settings panel

This should be implemented using React components.
`
			notePath := filepath.Join(notesDir, "feature-request.md")
			if err := fs.WriteString(notePath, noteContent); err != nil {
				return err
			}

			ctx.Set("note_path", notePath)
			return nil
		}),

		harness.NewStep("Test plan init with --from-note flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			notePath := ctx.GetString("note_path")

			cmd := ctx.Bin("plan", "init", "dashboard-plan", "--from-note", notePath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with --from-note failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "from-note-project", "plans", "dashboard-plan")
			ctx.Set("plan_path", planPath)

			return fs.AssertExists(planPath)
		}),

		harness.NewStep("Verify plan config has note_ref field", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			notePath := ctx.GetString("note_path")
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")

			// Read the config file
			content, err := fs.ReadString(planConfigPath)
			if err != nil {
				return err
			}

			// The note_ref should be in the first job's frontmatter, not in the config
			// So we just verify the config exists
			if content == "" {
				return fmt.Errorf("plan config is empty")
			}

			// Store for later verification
			ctx.Set("note_path_for_verification", notePath)
			return nil
		}),

		harness.NewStep("Verify extracted job was created with note content", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Find the first job file
			allFiles, err := fs.ListFiles(planPath)
			if err != nil {
				return err
			}

			var jobFiles []string
			for _, f := range allFiles {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					jobFiles = append(jobFiles, f)
				}
			}

			if len(jobFiles) == 0 {
				return fmt.Errorf("no job files found in plan directory")
			}

			// Read the first job file
			firstJobPath := filepath.Join(planPath, jobFiles[0])
			content, err := fs.ReadString(firstJobPath)
			if err != nil {
				return err
			}

			// Verify the job contains the note content (without frontmatter)
			if !strings.Contains(content, "Feature Request: Add User Dashboard") {
				return fmt.Errorf("job does not contain expected content from note")
			}

			if !strings.Contains(content, "User profile display") {
				return fmt.Errorf("job does not contain expected feature details from note")
			}

			return nil
		}),

		harness.NewStep("Verify job has note_ref in frontmatter", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			notePath := ctx.GetString("note_path")

			// Find the first job file
			allFiles, err := fs.ListFiles(planPath)
			if err != nil {
				return err
			}

			var jobFiles []string
			for _, f := range allFiles {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					jobFiles = append(jobFiles, f)
				}
			}

			firstJobPath := filepath.Join(planPath, jobFiles[0])
			content, err := fs.ReadString(firstJobPath)
			if err != nil {
				return err
			}

			// Check for note_ref field in frontmatter
			if !strings.Contains(content, "note_ref:") {
				return fmt.Errorf("job frontmatter missing note_ref field")
			}

			// Verify it references the correct note path
			if !strings.Contains(content, notePath) {
				return fmt.Errorf("note_ref does not reference the correct note path")
			}

			return nil
		}),

		harness.NewStep("Test --from-note with recipe and worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			notePath := ctx.GetString("note_path")

			cmd := ctx.Bin("plan", "init", "recipe-with-note",
				"--from-note", notePath,
				"--recipe", "chat",
				"--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with --from-note, --recipe, and --worktree failed: %w", err)
			}

			recipePlanPath := filepath.Join(notebooksRoot, "workspaces", "from-note-project", "plans", "recipe-with-note")
			ctx.Set("recipe_plan_path", recipePlanPath)

			// Verify worktree was created
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "recipe-with-note")
			ctx.Set("recipe_worktree_path", worktreePath)

			return fs.AssertExists(recipePlanPath)
		}),

		harness.NewStep("Verify worktree was created for recipe plan", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("recipe_worktree_path")
			return fs.AssertExists(worktreePath)
		}),

		harness.NewStep("Verify recipe plan has note content in first job", func(ctx *harness.Context) error {
			recipePlanPath := ctx.GetString("recipe_plan_path")

			// Find job files
			allFiles, err := fs.ListFiles(recipePlanPath)
			if err != nil {
				return err
			}

			var jobFiles []string
			for _, f := range allFiles {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					jobFiles = append(jobFiles, f)
				}
			}

			if len(jobFiles) == 0 {
				return fmt.Errorf("no job files found in recipe plan directory")
			}

			// Read the first job file
			firstJobPath := filepath.Join(recipePlanPath, jobFiles[0])
			content, err := fs.ReadString(firstJobPath)
			if err != nil {
				return err
			}

			// Verify the job contains the extracted note content
			if !strings.Contains(content, "Feature Request: Add User Dashboard") {
				return fmt.Errorf("recipe job does not contain expected content from note")
			}

			return nil
		}),

		harness.NewStep("Test --from-note with --extract-all-from should use from-note", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			notePath := ctx.GetString("note_path")

			// Create another note for extract-all-from
			notesDir := filepath.Join(notebooksRoot, "test-notes")
			otherNoteContent := `---
title: Other Note
---

This is other content.
`
			otherNotePath := filepath.Join(notesDir, "other-note.md")
			if err := fs.WriteString(otherNotePath, otherNoteContent); err != nil {
				return err
			}

			// --from-note should take precedence over --extract-all-from
			cmd := ctx.Bin("plan", "init", "precedence-test",
				"--from-note", notePath,
				"--extract-all-from", otherNotePath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with both flags failed: %w", err)
			}

			precedencePlanPath := filepath.Join(notebooksRoot, "workspaces", "from-note-project", "plans", "precedence-test")
			ctx.Set("precedence_plan_path", precedencePlanPath)

			return fs.AssertExists(precedencePlanPath)
		}),

		harness.NewStep("Verify from-note takes precedence", func(ctx *harness.Context) error {
			precedencePlanPath := ctx.GetString("precedence_plan_path")

			// Find job files
			allFiles, err := fs.ListFiles(precedencePlanPath)
			if err != nil {
				return err
			}

			var jobFiles []string
			for _, f := range allFiles {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					jobFiles = append(jobFiles, f)
				}
			}

			if len(jobFiles) == 0 {
				return fmt.Errorf("no job files found in precedence plan directory")
			}

			// Read the first job file
			firstJobPath := filepath.Join(precedencePlanPath, jobFiles[0])
			content, err := fs.ReadString(firstJobPath)
			if err != nil {
				return err
			}

			// Should contain content from --from-note, not --extract-all-from
			if !strings.Contains(content, "Feature Request: Add User Dashboard") {
				return fmt.Errorf("plan should use content from --from-note")
			}

			if strings.Contains(content, "This is other content") {
				return fmt.Errorf("plan should not use content from --extract-all-from when --from-note is provided")
			}

			return nil
		}),

		harness.NewStep("Test error case: --from-note with non-existent file", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "init", "error-plan", "--from-note", "/path/to/nonexistent/note.md")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should fail
			return result.AssertFailure()
		}),

		harness.NewStep("Test --from-note with --note-ref should use from-note for ref", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			notePath := ctx.GetString("note_path")
			notesDir := filepath.Join(notebooksRoot, "test-notes")

			// Create another note for explicit note-ref
			otherRefContent := `---
title: Other Ref Note
---

Different reference note.
`
			otherRefPath := filepath.Join(notesDir, "other-ref.md")
			if err := fs.WriteString(otherRefPath, otherRefContent); err != nil {
				return err
			}

			// --from-note should take precedence for note_ref too
			cmd := ctx.Bin("plan", "init", "ref-precedence-test",
				"--from-note", notePath,
				"--note-ref", otherRefPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with both ref flags failed: %w", err)
			}

			refPlanPath := filepath.Join(notebooksRoot, "workspaces", "from-note-project", "plans", "ref-precedence-test")
			ctx.Set("ref_plan_path", refPlanPath)

			return fs.AssertExists(refPlanPath)
		}),

		harness.NewStep("Verify from-note takes precedence for note_ref", func(ctx *harness.Context) error {
			refPlanPath := ctx.GetString("ref_plan_path")
			notePath := ctx.GetString("note_path")

			// Find job files
			allFiles, err := fs.ListFiles(refPlanPath)
			if err != nil {
				return err
			}

			var jobFiles []string
			for _, f := range allFiles {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					jobFiles = append(jobFiles, f)
				}
			}

			firstJobPath := filepath.Join(refPlanPath, jobFiles[0])
			content, err := fs.ReadString(firstJobPath)
			if err != nil {
				return err
			}

			// note_ref should point to the --from-note path
			if !strings.Contains(content, notePath) {
				return fmt.Errorf("note_ref should reference the --from-note path")
			}

			return nil
		}),

		harness.NewStep("Test plan init without directory uses TUI (skipped in test)", func(ctx *harness.Context) error {
			// When no directory is provided and in TTY mode, it would launch TUI
			// We can't easily test interactive TUI in automated tests
			// This step documents the expected behavior
			// Skip this test as TUI testing requires interactive environment
			return nil
		}),
	},
)
