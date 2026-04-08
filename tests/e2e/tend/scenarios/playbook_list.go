package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// PlaybookListScenario verifies `flow playbook list` enumerates all
// playbooks discoverable via the 4-tier resolver (ecosystem notebook
// + user tier) and supports --json.
var PlaybookListScenario = harness.NewScenario(
	"playbook-list-cli",
	"Verify flow playbook list enumerates playbooks across discovery tiers",
	[]string{"playbook", "cli"},
	[]harness.Step{
		harness.NewStep("Setup multiple playbooks in different tiers", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "list-project")
			if err != nil {
				return err
			}

			writePb := func(root, name, version, desc string) error {
				manifest := fmt.Sprintf(`name = "%s"
version = "%s"
description = "%s"
`, name, version, desc)
				if err := fs.WriteString(filepath.Join(root, name, "playbook.toml"), manifest); err != nil {
					return err
				}
				return fs.WriteString(filepath.Join(root, name, "skills", name+"-skill", "SKILL.md"),
					fmt.Sprintf("---\nname: %s-skill\ndescription: Skill for %s\n---\n\n# %s\n", name, name, name))
			}

			// pb-a in ecosystem notebook tier
			ecoPlaybooks := filepath.Join(notebooksRoot, "workspaces", "list-project", "playbooks")
			if err := writePb(ecoPlaybooks, "pb-a", "1.0.0", "Alpha playbook (ecosystem tier)"); err != nil {
				return err
			}
			// pb-b in ecosystem too — list should show every playbook
			// it discovers across the full search path
			if err := writePb(ecoPlaybooks, "pb-b", "2.0.0", "Beta playbook (ecosystem tier)"); err != nil {
				return err
			}
			// pb-c in user tier
			userPlaybooks := filepath.Join(ctx.ConfigDir(), "grove", "playbooks")
			if err := writePb(userPlaybooks, "pb-c", "3.0.0", "Gamma playbook (user tier)"); err != nil {
				return err
			}

			ctx.Set("list_project_dir", projectDir)
			return nil
		}),

		harness.NewStep("flow playbook list shows all three playbooks", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("list_project_dir")
			cmd := ctx.Bin("playbook", "list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("playbook list failed: %w", err)
			}
			for _, want := range []string{"pb-a", "pb-b", "pb-c", "1.0.0", "2.0.0", "3.0.0"} {
				if !strings.Contains(result.Stdout, want) {
					return fmt.Errorf("playbook list output missing %q in:\n%s", want, result.Stdout)
				}
			}
			if !strings.Contains(result.Stdout, "NAME") || !strings.Contains(result.Stdout, "VERSION") || !strings.Contains(result.Stdout, "DESCRIPTION") {
				return fmt.Errorf("expected table header columns in output, got:\n%s", result.Stdout)
			}
			return nil
		}),

		harness.NewStep("flow playbook list --json emits structured output", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("list_project_dir")
			cmd := ctx.Bin("playbook", "list", "--json")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			var parsed []struct {
				Manifest struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"manifest"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &parsed); err != nil {
				return fmt.Errorf("invalid JSON: %w\n%s", err, result.Stdout)
			}
			names := make(map[string]bool)
			for _, pb := range parsed {
				names[pb.Manifest.Name] = true
			}
			for _, want := range []string{"pb-a", "pb-b", "pb-c"} {
				if !names[want] {
					return fmt.Errorf("expected %q in list JSON, got %+v", want, parsed)
				}
			}
			return nil
		}),
	},
)
