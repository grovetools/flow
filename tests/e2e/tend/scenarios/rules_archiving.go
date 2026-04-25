package scenarios

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// RulesArchivingWorkflowScenario tests that running a job with a custom rules_file
// archives the rules to .artifacts/{jobID}/context.rules and updates frontmatter.
var RulesArchivingWorkflowScenario = harness.NewScenario(
	"rules-archiving-workflow",
	"Verifies that context rules are archived after job execution and frontmatter is updated.",
	[]string{"core", "cli", "oneshot", "rules", "archiving"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", setupRulesArchivingEnv),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Create mock LLM response", createRulesArchivingMockResponse),
		harness.NewStep("Initialize plan and add oneshot job with rules_file", initRulesArchivingPlan),
		harness.NewStep("Inject rules_file into job frontmatter", injectRulesFileIntoJob),
		harness.NewStep("Run the plan", runRulesArchivingPlan),
		harness.NewStep("Verify rules archiving and frontmatter", verifyRulesArchiving),
	},
)

func setupRulesArchivingEnv(ctx *harness.Context) error {
	projectDir, _, err := setupDefaultEnvironment(ctx, "rules-archive-project")
	if err != nil {
		return err
	}

	// Create Go project files
	if err := fs.WriteString(filepath.Join(projectDir, "main.go"), "package main\n\nfunc main() {}\n"); err != nil {
		return err
	}

	// Create context rules file in .grove/
	rulesDir := filepath.Join(projectDir, ".grove")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return err
	}
	if err := fs.WriteString(filepath.Join(rulesDir, "rules"), "*.go\n"); err != nil {
		return err
	}

	// Create custom rules file at the project root
	customRulesContent := "# Custom Rules for Archiving Test\ninclude: **/*.go\nexclude: vendor/\n"
	if err := fs.WriteString(filepath.Join(projectDir, "custom.rules"), customRulesContent); err != nil {
		return err
	}
	ctx.Set("custom_rules_content", customRulesContent)

	return nil
}

func createRulesArchivingMockResponse(ctx *harness.Context) error {
	responseContent := "Mock LLM response for rules archiving test."
	responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
	if err := fs.WriteString(responseFile, responseContent); err != nil {
		return err
	}
	ctx.Set("llm_response_file", responseFile)
	return nil
}

func initRulesArchivingPlan(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	notebooksRoot := ctx.GetString("notebooks_root")
	planPath := filepath.Join(notebooksRoot, "workspaces", "rules-archive-project", "plans", "rules-archive-plan")
	ctx.Set("plan_path", planPath)

	// Init plan
	initCmd := ctx.Bin("plan", "init", "rules-archive-plan")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w\nStderr: %s", result.Error, result.Stderr)
	}

	// Add oneshot job
	addCmd := ctx.Bin("plan", "add", "rules-archive-plan",
		"--type", "oneshot",
		"--title", "test-archive-job",
		"-p", "Review the project")
	addCmd.Dir(projectDir)
	result := addCmd.Run()
	return result.AssertSuccess()
}

func injectRulesFileIntoJob(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")
	jobPath := filepath.Join(planPath, "01-test-archive-job.md")

	content, err := os.ReadFile(jobPath)
	if err != nil {
		return fmt.Errorf("failed to read job file: %w", err)
	}

	// Add rules_file to the frontmatter
	updates := map[string]interface{}{
		"rules_file": "custom.rules",
	}
	newContent, err := orchestration.UpdateFrontmatter(content, updates)
	if err != nil {
		return fmt.Errorf("failed to update frontmatter: %w", err)
	}
	if err := os.WriteFile(jobPath, newContent, 0644); err != nil {
		return fmt.Errorf("failed to write updated job file: %w", err)
	}

	ctx.Set("job_path", jobPath)
	return nil
}

func runRulesArchivingPlan(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	llmResponseFile := ctx.GetString("llm_response_file")

	runCmd := ctx.Bin("plan", "run", "--local", "--all", "--yes")
	runCmd.Dir(projectDir)
	runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))

	result := runCmd.Run()
	if err := result.AssertSuccess(); err != nil {
		return fmt.Errorf("plan run failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
	}
	return nil
}

func verifyRulesArchiving(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")
	jobPath := ctx.GetString("job_path")

	// Load the job to get its ID
	job, err := orchestration.LoadJob(jobPath)
	if err != nil {
		return fmt.Errorf("failed to load job: %w", err)
	}

	// Verify job completed
	if job.Status != orchestration.JobStatusCompleted {
		return fmt.Errorf("expected job status completed, got %s", job.Status)
	}

	// Verify archived rules file exists
	artifactRulesPath := filepath.Join(planPath, ".artifacts", job.ID, "context.rules")

	// Check artifact file exists
	if err := ctx.Check("archived context.rules exists", fs.AssertExists(artifactRulesPath)); err != nil {
		return err
	}

	// Verify content matches original custom.rules
	archivedContent, err := os.ReadFile(artifactRulesPath)
	if err != nil {
		return fmt.Errorf("failed to read archived rules: %w", err)
	}

	// Read job file for frontmatter check
	jobContent, err := os.ReadFile(jobPath)
	if err != nil {
		return fmt.Errorf("failed to read job file: %w", err)
	}
	contentStr := string(jobContent)
	expectedRef := filepath.Join(".artifacts", job.ID, "context.rules")

	return ctx.Verify(func(v *verify.Collector) {
		expectedContent := ctx.GetString("custom_rules_content")
		v.Equal("archived content matches original", expectedContent, string(archivedContent))
		v.Contains("frontmatter contains used_rules_file", contentStr, "used_rules_file: "+expectedRef)
	})
}
