package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"

	"github.com/grovetools/flow/pkg/orchestration"
)

// jobStatusByTitle returns the persisted status of the job with the given title.
func jobStatusByTitle(planPath, title string) (orchestration.JobStatus, error) {
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return "", err
	}
	for _, job := range plan.Jobs {
		if job.Title == title {
			return job.Status, nil
		}
	}
	return "", fmt.Errorf("no job titled %q in plan %s", title, planPath)
}

// ChatStripCommentsScenario mirrors OneshotStripCommentsScenario for chat jobs.
// It is the regression guard for the strip_comments-ignored bug on the chat
// path: a chat job's uploaded context is the job-scoped artifact under
// .artifacts/<id>/context/context, and strip_comments must control comment
// removal there exactly as it does for oneshot. It also covers the new hard-fail
// behavior: a chat whose context regeneration errors must fail the job rather
// than silently fall back to the shared, unstripped plan-level context. The
// shared helpers (stripSourceGo, markers, updateJobFrontmatter, jobHotContextPath)
// live in oneshot_strip_comments.go in this package.
var ChatStripCommentsScenario = harness.NewScenario(
	"chat-strip-comments",
	"Verifies strip_comments controls comment removal in the per-job generated context for chat jobs (default on, explicit off keeps comments), and that a chat whose context regen fails is marked failed instead of uploading the shared unstripped context.",
	[]string{"core", "chat", "context", "strip-comments"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "chat-strip-project")
			if err != nil {
				return err
			}

			if err := fs.WriteString(filepath.Join(projectDir, "go.mod"), "module chat-strip-project\n\ngo 1.22\n"); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "main.go"), stripSourceGo); err != nil {
				return err
			}

			// Default .grove/rules plus an explicit rules file at the project root
			// the jobs reference via `rules_file` (resolved from the run cwd), so
			// cx pulls main.go into the hot context — the path strip transforms.
			rulesDir := filepath.Join(projectDir, ".grove")
			if err := os.MkdirAll(rulesDir, 0o755); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(rulesDir, "rules"), "*.go\n"); err != nil {
				return err
			}
			return fs.WriteString(filepath.Join(projectDir, "strip.rules"), "**/*.go\n")
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Create mock LLM response", func(ctx *harness.Context) error {
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			if err := fs.WriteString(responseFile, "Mock chat response."); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)
			return nil
		}),

		harness.NewStep("Initialize plan and add two chat jobs", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "chat-strip-project", "plans", "chat-strip-plan")
			ctx.Set("plan_path", planPath)

			if result := ctx.Bin("plan", "init", "chat-strip-plan").Dir(projectDir).Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w\nStderr: %s", result.Error, result.Stderr)
			}

			// Job 1: default behavior (strip_comments unset -> defaults to true).
			addDefault := ctx.Bin("plan", "add", "chat-strip-plan",
				"--type", "chat", "--title", "chat-strip-default",
				"-p", "Describe the code")
			addDefault.Dir(projectDir)
			if err := addDefault.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("adding chat-strip-default job: %w", err)
			}

			// Job 2: strip_comments explicitly false -> comments preserved.
			addOff := ctx.Bin("plan", "add", "chat-strip-plan",
				"--type", "chat", "--title", "chat-strip-off",
				"-p", "Describe the code")
			addOff.Dir(projectDir)
			if err := addOff.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("adding chat-strip-off job: %w", err)
			}

			if err := updateJobFrontmatter(planPath, "chat-strip-default", map[string]interface{}{
				"rules_file": "strip.rules",
			}); err != nil {
				return fmt.Errorf("updating chat-strip-default frontmatter: %w", err)
			}
			return updateJobFrontmatter(planPath, "chat-strip-off", map[string]interface{}{
				"rules_file":     "strip.rules",
				"strip_comments": false,
			})
		}),

		harness.NewStep("Run the two chat jobs", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			runCmd := ctx.Bin("plan", "run", "--local", "--all", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", ctx.GetString("llm_response_file")))

			result := runCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}
			return nil
		}),

		harness.NewStep("Default chat job strips comments from generated context", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			ctxPath, err := jobHotContextPath(planPath, "chat-strip-default")
			if err != nil {
				return err
			}
			if err := fs.AssertExists(ctxPath); err != nil {
				return fmt.Errorf("generated context for chat-strip-default not found: %w", err)
			}
			got, err := fs.ReadString(ctxPath)
			if err != nil {
				return err
			}
			if !strings.Contains(got, stripFuncMarker) {
				return fmt.Errorf("expected code %q to survive stripping, context:\n%s", stripFuncMarker, got)
			}
			if strings.Contains(got, stripOrphanMarker) {
				return fmt.Errorf("orphan comment marker %q should have been stripped, context:\n%s", stripOrphanMarker, got)
			}
			if strings.Contains(got, stripTrailMarker) {
				return fmt.Errorf("trailing comment marker %q should have been stripped, context:\n%s", stripTrailMarker, got)
			}
			// The "//" inside the string literal must be preserved.
			if !strings.Contains(got, "http://example.com") {
				return fmt.Errorf("string literal content should be preserved, context:\n%s", got)
			}
			return nil
		}),

		harness.NewStep("Opt-out chat job keeps comments in generated context", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			ctxPath, err := jobHotContextPath(planPath, "chat-strip-off")
			if err != nil {
				return err
			}
			if err := fs.AssertExists(ctxPath); err != nil {
				return fmt.Errorf("generated context for chat-strip-off not found: %w", err)
			}
			got, err := fs.ReadString(ctxPath)
			if err != nil {
				return err
			}
			if !strings.Contains(got, stripOrphanMarker) {
				return fmt.Errorf("orphan comment marker %q should be preserved when strip_comments=false, context:\n%s", stripOrphanMarker, got)
			}
			if !strings.Contains(got, stripTrailMarker) {
				return fmt.Errorf("trailing comment marker %q should be preserved when strip_comments=false, context:\n%s", stripTrailMarker, got)
			}
			return nil
		}),

		harness.NewStep("Chat job with unresolvable rules_file hard-fails", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			addBogus := ctx.Bin("plan", "add", "chat-strip-plan",
				"--type", "chat", "--title", "chat-strip-bogus",
				"-p", "Describe the code")
			addBogus.Dir(projectDir)
			if err := addBogus.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("adding chat-strip-bogus job: %w", err)
			}
			if err := updateJobFrontmatter(planPath, "chat-strip-bogus", map[string]interface{}{
				"rules_file": "does-not-exist-anywhere.rules",
			}); err != nil {
				return fmt.Errorf("updating chat-strip-bogus frontmatter: %w", err)
			}

			// Run just this job. It is expected to fail, so we do NOT assert the
			// command succeeded — we assert the job was marked failed and produced
			// no job-scoped context (i.e. it did not silently upload anything).
			runCmd := ctx.Bin("run", "--local", "chat-strip-bogus", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", ctx.GetString("llm_response_file")))
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)

			status, err := jobStatusByTitle(planPath, "chat-strip-bogus")
			if err != nil {
				return err
			}
			if status != orchestration.JobStatusFailed {
				return fmt.Errorf("expected chat-strip-bogus status %q, got %q", orchestration.JobStatusFailed, status)
			}

			// A regen failure must leave no uploadable job-scoped context behind.
			ctxPath, err := jobHotContextPath(planPath, "chat-strip-bogus")
			if err != nil {
				return err
			}
			if _, statErr := os.Stat(ctxPath); statErr == nil {
				return fmt.Errorf("expected no job-scoped context for a failed regen, but %s exists", ctxPath)
			}
			return nil
		}),
	},
)
