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

// Distinctive tokens embedded only inside comments in the source file, so
// their presence/absence in the generated context proves whether comment
// stripping ran. Code tokens (funcMarker) must survive stripping in all cases.
const (
	stripOrphanMarker = "MARKER_ORPHAN_COMMENT_XYZZY"
	stripTrailMarker  = "MARKER_TRAILING_COMMENT_XYZZY"
	stripFuncMarker   = "func StripMe()"
)

// stripSourceGo is a Go file whose comments carry the marker tokens. After
// stripping, neither marker should remain, but the code (stripFuncMarker, and
// the "//" that lives inside a string literal) must survive.
const stripSourceGo = "package main\n" +
	"\n" +
	"// " + stripOrphanMarker + " orphan doc comment\n" +
	"func StripMe() string {\n" +
	"\turl := \"http://example.com\" // " + stripTrailMarker + "\n" +
	"\treturn url\n" +
	"}\n"

// updateJobFrontmatter applies the given frontmatter key/value updates to the
// job markdown file whose title matches, using the same helper flow uses.
func updateJobFrontmatter(planPath, title string, updates map[string]interface{}) error {
	jobPath, err := jobFilePathByTitle(planPath, title)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(jobPath)
	if err != nil {
		return err
	}
	updated, err := orchestration.UpdateFrontmatter(content, updates)
	if err != nil {
		return err
	}
	return os.WriteFile(jobPath, updated, 0o600)
}

// jobFilePathByTitle returns the absolute path to the job markdown file whose
// title matches the given value.
func jobFilePathByTitle(planPath, title string) (string, error) {
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return "", err
	}
	for _, job := range plan.Jobs {
		if job.Title == title {
			return filepath.Join(planPath, job.Filename), nil
		}
	}
	return "", fmt.Errorf("no job titled %q in plan %s", title, planPath)
}

// jobHotContextPath returns the per-job generated hot-context file path
// (<plan>/.artifacts/<job-id>/context/context) for the job with the given
// title, which is where cx writes the (optionally stripped) repository
// context that gets uploaded to the LLM.
func jobHotContextPath(planPath, title string) (string, error) {
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return "", err
	}
	for _, job := range plan.Jobs {
		if job.Title == title {
			return filepath.Join(planPath, ".artifacts", job.ID, "context", "context"), nil
		}
	}
	return "", fmt.Errorf("no job titled %q in plan %s", title, planPath)
}

var OneshotStripCommentsScenario = harness.NewScenario(
	"oneshot-strip-comments",
	"Verifies strip_comments frontmatter controls comment removal in the generated repository context for real oneshot jobs (default on, explicit off keeps comments).",
	[]string{"core", "oneshot", "context", "strip-comments"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "strip-project")
			if err != nil {
				return err
			}

			if err := fs.WriteString(filepath.Join(projectDir, "go.mod"), "module strip-project\n\ngo 1.22\n"); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "main.go"), stripSourceGo); err != nil {
				return err
			}

			// Default .grove/rules (matches how other context scenarios seed the
			// project) plus an explicit rules file at the project root. Jobs
			// reference the latter via `rules_file`, so cx resolves **/*.go
			// relative to the project dir (where main.go lives) and pulls it
			// into the hot context — the path that comment stripping transforms.
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
			if err := fs.WriteString(responseFile, "Mock LLM response."); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)
			return nil
		}),

		harness.NewStep("Initialize plan and add two oneshot jobs", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "strip-project", "plans", "strip-plan")
			ctx.Set("plan_path", planPath)

			if result := ctx.Bin("plan", "init", "strip-plan").Dir(projectDir).Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w\nStderr: %s", result.Error, result.Stderr)
			}

			// Job 1: default behavior (strip_comments unset -> defaults to true).
			addDefault := ctx.Bin("plan", "add", "strip-plan",
				"--type", "oneshot", "--title", "strip-default",
				"-p", "Review the code")
			addDefault.Dir(projectDir)
			if err := addDefault.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("adding strip-default job: %w", err)
			}

			// Job 2: strip_comments explicitly false -> comments preserved.
			addOff := ctx.Bin("plan", "add", "strip-plan",
				"--type", "oneshot", "--title", "strip-off",
				"-p", "Review the code")
			addOff.Dir(projectDir)
			if err := addOff.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("adding strip-off job: %w", err)
			}

			// Both jobs pull context from the project-root rules file. The
			// default job leaves strip_comments unset (defaults to true); the
			// opt-out job sets it false so comments are preserved.
			if err := updateJobFrontmatter(planPath, "strip-default", map[string]interface{}{
				"rules_file": "strip.rules",
			}); err != nil {
				return fmt.Errorf("updating strip-default frontmatter: %w", err)
			}
			return updateJobFrontmatter(planPath, "strip-off", map[string]interface{}{
				"rules_file":     "strip.rules",
				"strip_comments": false,
			})
		}),

		harness.NewStep("Run the plan", func(ctx *harness.Context) error {
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

		harness.NewStep("Default job strips comments from generated context", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			ctxPath, err := jobHotContextPath(planPath, "strip-default")
			if err != nil {
				return err
			}
			if err := fs.AssertExists(ctxPath); err != nil {
				return fmt.Errorf("generated context for strip-default not found: %w", err)
			}
			got, err := fs.ReadString(ctxPath)
			if err != nil {
				return err
			}
			// Code survives; comment markers are gone.
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

		harness.NewStep("Opt-out job keeps comments in generated context", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			ctxPath, err := jobHotContextPath(planPath, "strip-off")
			if err != nil {
				return err
			}
			if err := fs.AssertExists(ctxPath); err != nil {
				return fmt.Errorf("generated context for strip-off not found: %w", err)
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
	},
)
