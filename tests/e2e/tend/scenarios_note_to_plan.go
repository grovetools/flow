package main

import (
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// NoteToPlanWorkflowScenario tests the new note-to-plan lifecycle with review/finish commands.
func NoteToPlanWorkflowScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-note-to-plan-workflow",
		Description: "Tests the new two-stage plan completion (review/finish) and note_ref hooks.",
		Tags:        []string{"plan", "review", "finish", "hooks", "note-to-plan"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with mock note and nb command", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create a mock note file
				notesDir := filepath.Join(ctx.RootDir, "notes")
				fs.CreateDir(notesDir)
				notePath := filepath.Join(notesDir, "my-test-note.md")
				fs.WriteString(notePath, "# My Test Note\n\nThis is the content of the note.")
				ctx.Set("note_path", notePath)

				// Clean up previous mock call logs
				os.Remove("/tmp/nb_mock_calls.log")

				return nil
			}),
			setupTestEnvironment(map[string]interface{}{
				"subprocessSafe": true, // Use subprocess-safe mocks for hook testing
			}),
			harness.NewStep("Initialize plan with --note-ref", func(ctx *harness.Context) error {
				notePath := ctx.GetString("note_path")
				return InitPlanWithNoteRef("note-plan", notePath).Func(ctx)
			}),
			VerifyHooksConfigured("note-plan", "on_review"),
			harness.NewStep("Verify job has note_ref in frontmatter", func(ctx *harness.Context) error {
				notePath := ctx.GetString("note_path")
				return VerifyJobFrontmatterField("note-plan", "01-chat.md", "note_ref", notePath).Func(ctx)
			}),
			VerifyPlanFinishGated("note-plan"),
			RunPlanReview("note-plan"),
			VerifyPlanStatus("note-plan", "review"),
			RunPlanFinish("note-plan", "--yes", "--prune-worktree", "--delete-branch"),
		},
	}
}
