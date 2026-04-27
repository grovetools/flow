package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/nb/pkg/frontmatter"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"

	"github.com/grovetools/flow/pkg/orchestration"
)

// DemoteToNoteScenario tests the `flow plan demote` CLI command with a
// cross-workspace sandbox: a job in workspace-b's plan (with note_ref
// pointing to workspace-a's in_progress/) is demoted back to workspace-a's inbox.
var DemoteToNoteScenario = harness.NewScenario(
	"flow-demote-to-note",
	"Verifies flow plan demote moves in_progress note back to inbox, marks job abandoned.",
	[]string{"plan", "demote", "cross-workspace"},
	[]harness.Step{
		harness.NewStep("Setup cross-workspace sandbox", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()

			// Create a multi-workspace notebook structure:
			//   notebooks/test-notebook/
			//     workspaces/
			//       workspace-a/
			//         inbox/           (target for demoted note)
			//         in_progress/     (note currently here, simulating post-promote state)
			//           original-bug-report.md
			//       workspace-b/plans/active-plan/
			//         .grove-plan.yml
			//         01-stale-task.md  (job with note_ref → workspace-a/in_progress/)
			notebookRoot := filepath.Join(homeDir, "notebooks", "test-notebook")

			// Workspace A: source workspace
			wsADir := filepath.Join(notebookRoot, "workspaces", "workspace-a")
			wsAInbox := filepath.Join(wsADir, "inbox")
			wsAInProgress := filepath.Join(wsADir, "in_progress")
			if err := fs.CreateDir(wsAInbox); err != nil {
				return fmt.Errorf("creating workspace-a inbox: %w", err)
			}
			if err := fs.CreateDir(wsAInProgress); err != nil {
				return fmt.Errorf("creating workspace-a in_progress: %w", err)
			}
			ctx.Set("workspace_a_dir", wsADir)
			ctx.Set("workspace_a_inbox", wsAInbox)

			// Create a note in in_progress/ (simulates a previously promoted note)
			noteRefPath := filepath.Join(wsAInProgress, "original-bug-report.md")
			noteContent := `---
title: Original Bug Report
type: inbox
plan_ref: active-plan/01-stale-task.md
---

## Bug: Widget crashes on empty input

Steps to reproduce the issue and proposed fix.
`
			if err := fs.WriteString(noteRefPath, noteContent); err != nil {
				return fmt.Errorf("writing in_progress note: %w", err)
			}
			ctx.Set("note_ref_path", noteRefPath)

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

			// Create a job file with note_ref pointing to workspace-a's in_progress note
			jobContent := fmt.Sprintf(`---
id: stale-task-from-bug-report
title: Stale Task From Bug Report
type: chat
status: pending_user
note_ref: %s
---

<!-- grove: {"template": "chat"} -->

See linked note: %s
`, noteRefPath, noteRefPath)

			jobPath := filepath.Join(planDir, "01-stale-task.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return fmt.Errorf("writing job file: %w", err)
			}

			ctx.Set("plan_dir", planDir)
			ctx.Set("job_path", jobPath)
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

			// The command prints the note path to stdout
			notePath := strings.TrimSpace(result.Stdout)
			ctx.Set("demoted_note_path", notePath)
			return nil
		}),

		harness.NewStep("Verify note moved back to workspace-a inbox", func(ctx *harness.Context) error {
			wsAInbox := ctx.GetString("workspace_a_inbox")
			noteRefPath := ctx.GetString("note_ref_path")
			demotedNotePath := ctx.GetString("demoted_note_path")

			// The note should now be in inbox/
			expectedPath := filepath.Join(wsAInbox, filepath.Base(noteRefPath))

			// in_progress/ note should be gone
			if err := fs.AssertNotExists(noteRefPath); err != nil {
				return fmt.Errorf("in_progress note should have been moved: %w", err)
			}

			// inbox/ note should exist
			if err := fs.AssertExists(expectedPath); err != nil {
				return fmt.Errorf("note not found in inbox: %w", err)
			}

			// Read the note and verify content is preserved
			content, err := fs.ReadString(expectedPath)
			if err != nil {
				return fmt.Errorf("reading demoted note: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("demote output matches inbox path", expectedPath, demotedNotePath)
				v.Contains("note has original content", content, "Widget crashes on empty input")
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

// DemoteWithWorkspaceFlagScenario tests the --workspace flag on flow plan demote.
var DemoteWithWorkspaceFlagScenario = harness.NewScenario(
	"flow-demote-workspace-flag",
	"Verifies flow plan demote --workspace routes note to specified workspace inbox.",
	[]string{"plan", "demote", "workspace-flag"},
	[]harness.Step{
		harness.NewStep("Setup sandbox with workspace override target", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			notebookRoot := filepath.Join(homeDir, "notebooks", "test-notebook")

			// Workspace A: has the in_progress note
			wsADir := filepath.Join(notebookRoot, "workspaces", "workspace-a")
			wsAInProgress := filepath.Join(wsADir, "in_progress")
			if err := fs.CreateDir(filepath.Join(wsADir, "inbox")); err != nil {
				return fmt.Errorf("creating workspace-a inbox: %w", err)
			}
			if err := fs.CreateDir(wsAInProgress); err != nil {
				return fmt.Errorf("creating workspace-a in_progress: %w", err)
			}

			noteRefPath := filepath.Join(wsAInProgress, "routed-bug.md")
			noteContent := `---
title: Routed Bug
type: inbox
---

Bug that will be routed to a different workspace.
`
			if err := fs.WriteString(noteRefPath, noteContent); err != nil {
				return fmt.Errorf("writing in_progress note: %w", err)
			}
			ctx.Set("note_ref_path", noteRefPath)

			// Workspace C: the override target
			wsCDir := filepath.Join(notebookRoot, "workspaces", "workspace-c")
			wsCInbox := filepath.Join(wsCDir, "inbox")
			if err := fs.CreateDir(wsCInbox); err != nil {
				return fmt.Errorf("creating workspace-c inbox: %w", err)
			}
			ctx.Set("workspace_c_dir", wsCDir)
			ctx.Set("workspace_c_inbox", wsCInbox)

			// Plan with job pointing to workspace-a's in_progress note
			planDir := filepath.Join(notebookRoot, "workspaces", "workspace-b", "plans", "reroute-plan")
			if err := fs.CreateDir(planDir); err != nil {
				return fmt.Errorf("creating plan dir: %w", err)
			}
			if err := fs.WriteString(filepath.Join(planDir, ".grove-plan.yml"), "name: reroute-plan\nworktree: \"\"\n"); err != nil {
				return fmt.Errorf("writing plan config: %w", err)
			}

			jobContent := fmt.Sprintf(`---
id: reroute-job
title: Reroute Job
type: chat
status: pending_user
note_ref: %s
---

See linked note: %s
`, noteRefPath, noteRefPath)

			jobPath := filepath.Join(planDir, "01-reroute-job.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return fmt.Errorf("writing job file: %w", err)
			}
			ctx.Set("job_path", jobPath)
			return nil
		}),

		harness.NewStep("Demote with --workspace flag", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")
			wsCDir := ctx.GetString("workspace_c_dir")

			cmd := ctx.Bin("plan", "demote", jobPath, "--workspace", wsCDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("flow plan demote --workspace failed: %w", err)
			}

			demotedPath := strings.TrimSpace(result.Stdout)
			ctx.Set("demoted_note_path", demotedPath)
			return nil
		}),

		harness.NewStep("Verify note routed to workspace-c inbox", func(ctx *harness.Context) error {
			wsCInbox := ctx.GetString("workspace_c_inbox")
			noteRefPath := ctx.GetString("note_ref_path")
			demotedPath := ctx.GetString("demoted_note_path")

			expectedPath := filepath.Join(wsCInbox, filepath.Base(noteRefPath))

			// in_progress/ note should be gone
			if err := fs.AssertNotExists(noteRefPath); err != nil {
				return fmt.Errorf("in_progress note should have been moved: %w", err)
			}

			// workspace-c inbox should have the note
			if err := fs.AssertExists(expectedPath); err != nil {
				return fmt.Errorf("note not found in workspace-c inbox: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("demote output matches workspace-c inbox path", expectedPath, demotedPath)
			})
		}),
	},
)

// PromoteDemoteRoundTripScenario tests the full lifecycle:
// simulate promote (note → in_progress with plan_ref) → demote (in_progress → inbox).
// The promote is simulated by manually creating the in_progress state so we don't
// need the nb binary. The demote uses the real flow binary.
var PromoteDemoteRoundTripScenario = harness.NewScenario(
	"flow-promote-demote-round-trip",
	"Verifies promote → demote round-trip: note ends up back in inbox with plan_ref.",
	[]string{"plan", "promote", "demote", "round-trip"},
	[]harness.Step{
		harness.NewStep("Setup sandbox simulating post-promote state", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			notebookRoot := filepath.Join(homeDir, "notebooks", "test-notebook")

			// Single workspace with inbox, in_progress, and a plan
			wsDir := filepath.Join(notebookRoot, "workspaces", "workspace-a")
			wsInbox := filepath.Join(wsDir, "inbox")
			wsInProgress := filepath.Join(wsDir, "in_progress")
			if err := fs.CreateDir(wsInbox); err != nil {
				return fmt.Errorf("creating inbox: %w", err)
			}
			if err := fs.CreateDir(wsInProgress); err != nil {
				return fmt.Errorf("creating in_progress: %w", err)
			}

			// Simulate post-promote state: note is in in_progress/ with plan_ref
			inProgressPath := filepath.Join(wsInProgress, "round-trip-note.md")
			noteContent := `---
title: Round Trip Note
type: inbox
plan_ref: round-trip-plan/02-round-trip-job.md
---

This note was promoted and should be demoted back.
`
			if err := fs.WriteString(inProgressPath, noteContent); err != nil {
				return fmt.Errorf("writing in_progress note: %w", err)
			}
			ctx.Set("in_progress_path", inProgressPath)
			ctx.Set("workspace_dir", wsDir)
			ctx.Set("inbox_dir", wsInbox)

			// Create a plan with a job pointing to the in_progress note
			planDir := filepath.Join(wsDir, "plans", "round-trip-plan")
			if err := fs.CreateDir(planDir); err != nil {
				return fmt.Errorf("creating plan dir: %w", err)
			}
			if err := fs.WriteString(filepath.Join(planDir, ".grove-plan.yml"), "name: round-trip-plan\nworktree: \"\"\n"); err != nil {
				return fmt.Errorf("writing plan config: %w", err)
			}

			jobContent := fmt.Sprintf(`---
id: round-trip-job
title: Round Trip Note
type: chat
status: pending_user
note_ref: %s
---

<!-- grove: {"template": "chat"} -->

See linked note: %s
`, inProgressPath, inProgressPath)

			jobPath := filepath.Join(planDir, "02-round-trip-job.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return fmt.Errorf("writing job file: %w", err)
			}
			ctx.Set("job_path", jobPath)
			return nil
		}),

		harness.NewStep("Demote job", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "demote", jobPath)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("demote failed: %w", err)
			}

			demotedPath := strings.TrimSpace(result.Stdout)
			ctx.Set("demoted_path", demotedPath)
			return nil
		}),

		harness.NewStep("Verify note back in inbox after demote", func(ctx *harness.Context) error {
			inProgressPath := ctx.GetString("in_progress_path")
			inboxDir := ctx.GetString("inbox_dir")

			expectedPath := filepath.Join(inboxDir, "round-trip-note.md")

			// in_progress should be empty
			if err := fs.AssertNotExists(inProgressPath); err != nil {
				return fmt.Errorf("in_progress note should have been moved: %w", err)
			}

			// inbox should have the note back
			if err := fs.AssertExists(expectedPath); err != nil {
				return fmt.Errorf("note not found back in inbox: %w", err)
			}

			// Verify the note still has plan_ref from the promote step
			content, err := fs.ReadString(expectedPath)
			if err != nil {
				return fmt.Errorf("reading round-tripped note: %w", err)
			}

			fm, _, err := frontmatter.Parse(content)
			if err != nil {
				return fmt.Errorf("parsing round-tripped note frontmatter: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("note plan_ref preserved after round-trip", "round-trip-plan/02-round-trip-job.md", fm.PlanRef)
			})
		}),

		harness.NewStep("Verify job status abandoned after demote", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job status is abandoned", string(orchestration.JobStatusAbandoned), string(job.Status))
			})
		}),
	},
)
