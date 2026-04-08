package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// PlaybookShowScenario verifies the `flow playbook show` command
// covering: explicit name, non-existent name error, --json output,
// and active-playbook resolution from a plan's .grove-plan.yml.
var PlaybookShowScenario = harness.NewScenario(
	"playbook-show-cli",
	"Verify flow playbook show — explicit, missing, JSON, and active-plan resolution",
	[]string{"playbook", "cli"},
	[]harness.Step{
		harness.NewStep("Setup playbook environment", func(ctx *harness.Context) error {
			_, _, _, err := setupPlaybookEnvironment(ctx, "show-project", "test-pb")
			return err
		}),

		harness.NewStep("show test-pb prints overview", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("playbook", "show", "test-pb")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("playbook show failed: %w", err)
			}
			expectations := []string{
				"PLAYBOOK: test-pb",
				"v1.0.0",
				"DESC:",
				"Minimal test playbook",
				"SKILLS:",
				"pb-hello",
				"pb-goodbye",
				"PROMPTS:",
				"greet.md",
				"RECIPES:",
				"test-recipe.md",
			}
			for _, want := range expectations {
				if !strings.Contains(result.Stdout, want) {
					return fmt.Errorf("show output missing %q in:\n%s", want, result.Stdout)
				}
			}
			return nil
		}),

		harness.NewStep("show nonexistent playbook fails", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("playbook", "show", "no-such-playbook")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if result.ExitCode == 0 {
				return fmt.Errorf("expected non-zero exit for missing playbook, got stdout=%s", result.Stdout)
			}
			combined := result.Stdout + result.Stderr
			if !strings.Contains(combined, "not found") {
				return fmt.Errorf("expected 'not found' in error output, got: %s", combined)
			}
			return nil
		}),

		harness.NewStep("show --json emits structured output", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("playbook", "show", "test-pb", "--json")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("playbook show --json failed: %w", err)
			}
			var parsed struct {
				Manifest struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"manifest"`
				Skills  []map[string]any `json:"skills"`
				Prompts []map[string]any `json:"prompts"`
				Recipes []map[string]any `json:"recipes"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &parsed); err != nil {
				return fmt.Errorf("invalid JSON from playbook show --json: %w\nstdout:\n%s", err, result.Stdout)
			}
			if parsed.Manifest.Name != "test-pb" {
				return fmt.Errorf("expected manifest.name=test-pb, got %q", parsed.Manifest.Name)
			}
			if parsed.Manifest.Version != "1.0.0" {
				return fmt.Errorf("expected manifest.version=1.0.0, got %q", parsed.Manifest.Version)
			}
			if len(parsed.Skills) != 2 {
				return fmt.Errorf("expected 2 skills, got %d", len(parsed.Skills))
			}
			if len(parsed.Prompts) == 0 {
				return fmt.Errorf("expected at least one prompt in JSON output")
			}
			if len(parsed.Recipes) == 0 {
				return fmt.Errorf("expected at least one recipe in JSON output")
			}
			return nil
		}),

		harness.NewStep("show without args reads active plan playbook", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			// Drop a minimal .grove-plan.yml in a subdirectory and run from
			// inside it — show with no args should pick up the playbook
			// field.
			planDir := filepath.Join(projectDir, "active-plan-dir")
			if err := fs.CreateDir(planDir); err != nil {
				return err
			}
			manifest := "playbook: test-pb\n"
			if err := fs.WriteString(filepath.Join(planDir, ".grove-plan.yml"), manifest); err != nil {
				return err
			}

			cmd := ctx.Bin("playbook", "show")
			cmd.Dir(planDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("playbook show with active plan failed: %w", err)
			}
			if !strings.Contains(result.Stdout, "PLAYBOOK: test-pb") {
				return fmt.Errorf("expected active plan to resolve test-pb, got:\n%s", result.Stdout)
			}
			return nil
		}),
	},
)
