package scenarios

import (
	"fmt"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var PlanDomainFilteringScenario = harness.NewScenario(
	"plan-domain-filtering",
	"Tests the --domain flag for plan recipes list and plan templates list commands.",
	[]string{"core", "plan", "domain", "recipes", "templates"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "test-domain-filtering-project")
			return err
		}),

		harness.NewStep("Test 'plan recipes list' without domain filter", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "recipes", "list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan recipes list failed: %w", err)
			}

			// Verify output contains DOMAIN column header
			if err := assert.Contains(result.Stdout, "DOMAIN", "output should contain DOMAIN column"); err != nil {
				return err
			}

			// Verify output contains both generic and grove domains
			if err := assert.Contains(result.Stdout, "generic", "output should contain generic domain"); err != nil {
				return err
			}
			if err := assert.Contains(result.Stdout, "grove", "output should contain grove domain"); err != nil {
				return err
			}

			// Store the full output for comparison
			ctx.Set("recipes_all_output", result.Stdout)

			return nil
		}),

		harness.NewStep("Test 'plan recipes list --domain generic'", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "recipes", "list", "--domain", "generic")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan recipes list --domain generic failed: %w", err)
			}

			// Verify output contains generic domain
			if err := assert.Contains(result.Stdout, "generic", "output should contain generic domain"); err != nil {
				return err
			}

			// Verify output does NOT contain grove domain
			// We need to be careful here - the word "grove" might appear in the header
			// So we check that there are no lines with "grove" in the DOMAIN column
			lines := strings.Split(result.Stdout, "\n")
			for i, line := range lines {
				// Skip header lines
				if i == 0 || strings.TrimSpace(line) == "" {
					continue
				}
				// Check if line contains grove domain (but not in headers)
				fields := strings.Fields(line)
				if len(fields) > 1 && fields[1] == "grove" {
					return fmt.Errorf("output should not contain recipes with grove domain, but found: %s", line)
				}
			}

			return nil
		}),

		harness.NewStep("Test 'plan recipes list --domain grove'", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "recipes", "list", "--domain", "grove")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan recipes list --domain grove failed: %w", err)
			}

			// Verify output contains grove domain
			if err := assert.Contains(result.Stdout, "grove", "output should contain grove domain"); err != nil {
				return err
			}

			// Verify output does NOT contain generic domain
			lines := strings.Split(result.Stdout, "\n")
			for i, line := range lines {
				// Skip header lines
				if i == 0 || strings.TrimSpace(line) == "" {
					continue
				}
				// Check if line contains generic domain
				fields := strings.Fields(line)
				if len(fields) > 1 && fields[1] == "generic" {
					return fmt.Errorf("output should not contain recipes with generic domain, but found: %s", line)
				}
			}

			return nil
		}),

		harness.NewStep("Test 'plan recipes list --domain nonexistent'", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "recipes", "list", "--domain", "nonexistent")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan recipes list --domain nonexistent failed: %w", err)
			}

			// Should show no recipes found message or empty output (except headers)
			if err := assert.Contains(result.Stdout, "No plan recipes found", "output should indicate no recipes found"); err != nil {
				// Alternative: check that output only contains header
				lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
				// Should have at most 1 line (the header)
				if len(lines) > 1 {
					// Check if there's any actual data beyond the header
					hasData := false
					for i, line := range lines {
						if i > 0 && strings.TrimSpace(line) != "" {
							hasData = true
							break
						}
					}
					if hasData {
						return fmt.Errorf("output should not contain any recipes for nonexistent domain")
					}
				}
			}

			return nil
		}),

		harness.NewStep("Test 'plan templates list' without domain filter", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "templates", "list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan templates list failed: %w", err)
			}

			// Verify output contains DOMAIN column header
			if err := assert.Contains(result.Stdout, "DOMAIN", "output should contain DOMAIN column"); err != nil {
				return err
			}

			// Verify output contains TYPE column header
			if err := assert.Contains(result.Stdout, "TYPE", "output should contain TYPE column"); err != nil {
				return err
			}

			// Verify output contains both generic and grove domains
			if err := assert.Contains(result.Stdout, "generic", "output should contain generic domain"); err != nil {
				return err
			}
			if err := assert.Contains(result.Stdout, "grove", "output should contain grove domain"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Test 'plan templates list --domain generic'", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "templates", "list", "--domain", "generic")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan templates list --domain generic failed: %w", err)
			}

			// Verify output contains generic domain
			if err := assert.Contains(result.Stdout, "generic", "output should contain generic domain"); err != nil {
				return err
			}

			// Verify output does NOT contain grove domain in data rows
			lines := strings.Split(result.Stdout, "\n")
			for i, line := range lines {
				// Skip header lines
				if i == 0 || strings.TrimSpace(line) == "" {
					continue
				}
				// Check if line contains grove domain (in DOMAIN column, which is second column)
				fields := strings.Fields(line)
				if len(fields) > 1 && fields[1] == "grove" {
					return fmt.Errorf("output should not contain templates with grove domain, but found: %s", line)
				}
			}

			return nil
		}),

		harness.NewStep("Test 'plan templates list --domain grove'", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "templates", "list", "--domain", "grove")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan templates list --domain grove failed: %w", err)
			}

			// Verify output contains grove domain
			if err := assert.Contains(result.Stdout, "grove", "output should contain grove domain"); err != nil {
				return err
			}

			// Verify output does NOT contain generic domain in data rows
			lines := strings.Split(result.Stdout, "\n")
			for i, line := range lines {
				// Skip header lines
				if i == 0 || strings.TrimSpace(line) == "" {
					continue
				}
				// Check if line contains generic domain
				fields := strings.Fields(line)
				if len(fields) > 1 && fields[1] == "generic" {
					return fmt.Errorf("output should not contain templates with generic domain, but found: %s", line)
				}
			}

			return nil
		}),

		harness.NewStep("Test 'plan templates list --domain nonexistent'", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "templates", "list", "--domain", "nonexistent")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan templates list --domain nonexistent failed: %w", err)
			}

			// Should show no templates found message or empty output (except headers)
			if err := assert.Contains(result.Stdout, "No job templates found", "output should indicate no templates found"); err != nil {
				// Alternative: check that output only contains header
				lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
				// Should have at most 1 line (the header)
				if len(lines) > 1 {
					// Check if there's any actual data beyond the header
					hasData := false
					for i, line := range lines {
						if i > 0 && strings.TrimSpace(line) != "" {
							hasData = true
							break
						}
					}
					if hasData {
						return fmt.Errorf("output should not contain any templates for nonexistent domain")
					}
				}
			}

			return nil
		}),
	},
)
