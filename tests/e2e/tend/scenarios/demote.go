package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// DemoteToNoteScenario tests the `flow plan demote` CLI command with a
// cross-workspace sandbox: a job in workspace-b's plan (with note_ref
// pointing to workspace-a) is demoted back to a note in workspace-a's inbox.
var DemoteToNoteScenario = harness.NewScenario(
	"flow-demote-to-note",
	"Verifies flow plan demote creates a note routed via note_ref, marks job abandoned.",
	[]string{"plan", "demote", "cross-workspace"},
	[]harness.Step{
		// Mock nb so the demote command can shell out to it
		harness.SetupMocks(harness.Mock{CommandName: "nb"}),

		harness.NewStep("Setup cross-workspace sandbox", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()

			// Create a multi-workspace notebook structure:
			//   notebooks/test-notebook/
			//     workspaces/
			//       workspace-a/inbox/   (target for demoted note)
			//       workspace-b/plans/active-plan/
			//         .grove-plan.yml
			//         01-existing-job.md  (job with note_ref → workspace-a)
			notebookRoot := filepath.Join(homeDir, "notebooks", "test-notebook")

			// Workspace A: source workspace (where the note originally came from)
			wsADir := filepath.Join(notebookRoot, "workspaces", "workspace-a")
			wsAInbox := filepath.Join(wsADir, "inbox")
			if err := fs.CreateDir(wsAInbox); err != nil {
				return fmt.Errorf("creating workspace-a inbox: %w", err)
			}
			ctx.Set("workspace_a_dir", wsADir)
			ctx.Set("workspace_a_inbox", wsAInbox)

			// Workspace B: plan workspace
			planDir := filepath.Join(notebookRoot, "workspaces", "workspace-b", "plans", "active-plan")
			if err := fs.CreateDir(planDir); err != nil {
				return fmt.Errorf("creating plan dir: %w", err)
			}

			planConfig := `name: active-plan
worktree: ""
`
			if err := fs.WriteString(filepath.Join(planDir, ".grove-plan.yml"), planConfig); err != nil {
				return fmt.Errorf("writing plan config: %w", err)
			}

			// Create a job file with note_ref pointing to workspace-a.
			// This simulates a job that was previously promoted from workspace-a.
			noteRefPath := filepath.Join(wsAInbox, "original-bug-report.md")
			jobContent := fmt.Sprintf(`---
id: stale-task-from-bug-report
title: Stale Task From Bug Report
type: chat
status: pending_user
note_ref: %s
---

## Bug: Widget crashes on empty input

Steps to reproduce the issue and proposed fix.
`, noteRefPath)

			jobPath := filepath.Join(planDir, "01-stale-task.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return fmt.Errorf("writing job file: %w", err)
			}

			ctx.Set("plan_dir", planDir)
			ctx.Set("job_path", jobPath)
			ctx.Set("note_ref_path", noteRefPath)
			return nil
		}),

		harness.NewStep("Run flow plan demote command", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "demote", jobPath)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("flow plan demote failed: %w", err)
			}

			// The command prints the new note path to stdout
			notePath := strings.TrimSpace(result.Stdout)
			ctx.Set("demoted_note_path", notePath)
			return nil
		}),

		harness.NewStep("Verify note created in workspace-a inbox (routed via note_ref)", func(ctx *harness.Context) error {
			wsAInbox := ctx.GetString("workspace_a_inbox")

			// List files in workspace-a's inbox to find the new note
			files, err := fs.ListFiles(wsAInbox)
			if err != nil {
				return fmt.Errorf("listing workspace-a inbox: %w", err)
			}

			// Find a note file (exclude the original note_ref target which doesn't exist)
			var noteFiles []string
			for _, f := range files {
				if strings.HasSuffix(f, ".md") {
					noteFiles = append(noteFiles, f)
				}
			}

			if len(noteFiles) == 0 {
				return fmt.Errorf("no note files found in workspace-a inbox; expected demoted note to be routed here via note_ref")
			}

			// Read the note and verify content
			notePath := filepath.Join(wsAInbox, noteFiles[0])
			content, err := fs.ReadString(notePath)
			if err != nil {
				return fmt.Errorf("reading demoted note: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("note has job title", content, "Stale Task From Bug Report")
				v.Contains("note has job prompt body", content, "Widget crashes on empty input")
			})
		}),

		harness.NewStep("Verify job status changed to abandoned", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading demoted job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job status is abandoned", string(orchestration.JobStatusAbandoned), string(job.Status))
			})
		}),
	},
)
