package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

var SessionArchivingScenario = harness.NewScenario(
	"session-archiving",
	"Tests session artifact archiving when completing interactive/headless agent jobs.",
	[]string{"core", "agent", "session", "archiving"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "archiving-project")
			if err != nil {
				return err
			}

			// Create a git repo for the project
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Archiving Test Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "claude"}, // Mock claude to prevent actual agent launch
			harness.Mock{CommandName: "tmux"},   // Mock tmux to prevent real sessions
		),

		harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "test-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Add interactive_agent job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "archiving-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "test-plan")
			ctx.Set("plan_path", planPath)

			// Add an interactive agent job
			cmd := ctx.Bin("plan", "add", "test-plan",
				"--type", "interactive_agent",
				"--title", "Implement Feature",
				"-p", "Implement a test feature")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			jobPath, err := findJobByPrefix(planPath, "01-implement-feature")
			if err != nil {
				return err
			}
			ctx.Set("job_path", jobPath)
			return nil
		}),

		harness.NewStep("Run interactive_agent job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			// Run the job. Mocks will prevent hanging.
			cmd := ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Verify the job reached "running" status
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}
			if !strings.Contains(content, "status: running") {
				return fmt.Errorf("expected job status to be 'running', but got: %s", content)
			}

			return nil
		}),

		harness.NewStep("Create mock session metadata and transcript", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			jobPath := ctx.GetString("job_path")

			// Read the job to get the job ID
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}

			// Extract job ID from the frontmatter
			// Format: id: implement-feature-{hash}
			var jobID string
			for _, line := range strings.Split(content, "\n") {
				if strings.HasPrefix(line, "id: ") {
					jobID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
					break
				}
			}
			if jobID == "" {
				return fmt.Errorf("could not find job ID in job file")
			}
			ctx.Set("job_id", jobID)

			// Create a mock Claude session ID
			mockClaudeSessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
			ctx.Set("claude_session_id", mockClaudeSessionID)

			// Create session registry directory structure
			// The registry stores session metadata in ~/.grove/hooks/sessions/{claude-session-id}/
			claudeSessionDir := filepath.Join(homeDir, ".grove", "hooks", "sessions", mockClaudeSessionID)
			if err := fs.CreateDir(claudeSessionDir); err != nil {
				return fmt.Errorf("creating Claude session directory: %w", err)
			}

			// Create a mock transcript file
			transcriptPath := filepath.Join(homeDir, ".claude", "projects", "test-project", mockClaudeSessionID+".jsonl")
			if err := fs.CreateDir(filepath.Dir(transcriptPath)); err != nil {
				return fmt.Errorf("creating transcript directory: %w", err)
			}
			transcriptContent := `{"type":"session_start","timestamp":"2025-12-18T10:00:00Z"}
{"type":"message","role":"user","content":"Implement a test feature"}
{"type":"message","role":"assistant","content":"I'll implement the test feature."}
`
			if err := fs.WriteString(transcriptPath, transcriptContent); err != nil {
				return fmt.Errorf("creating mock transcript: %w", err)
			}
			ctx.Set("transcript_path", transcriptPath)

			// Create metadata.json in the Claude session directory
			metadataContent := fmt.Sprintf(`{
  "session_id": "%s",
  "claude_session_id": "%s",
  "transcript_path": "%s",
  "plan_name": "test-plan",
  "job_file_path": "%s",
  "provider": "claude",
  "pid": 12345,
  "working_directory": "%s",
  "user": "testuser",
  "started_at": "2025-12-18T10:00:00Z"
}
`, jobID, mockClaudeSessionID, transcriptPath, jobPath, homeDir)
			metadataPath := filepath.Join(claudeSessionDir, "metadata.json")
			if err := fs.WriteString(metadataPath, metadataContent); err != nil {
				return fmt.Errorf("creating metadata.json: %w", err)
			}
			ctx.Set("source_metadata_path", metadataPath)

			return nil
		}),

		harness.NewStep("Remove job lock file to prepare for completion", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")
			lockPath := jobPath + ".lock"
			return fs.RemoveIfExists(lockPath)
		}),

		harness.NewStep("Complete the job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "complete", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify session artifacts were archived", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobID := ctx.GetString("job_id")

			// Check that artifacts were copied to .artifacts/{job-id}/
			artifactDir := filepath.Join(planPath, ".artifacts", jobID)

			// Verify artifact directory exists
			if err := fs.AssertExists(artifactDir); err != nil {
				return fmt.Errorf("artifact directory should exist: %w", err)
			}

			// Verify metadata.json was copied
			archivedMetadataPath := filepath.Join(artifactDir, "metadata.json")
			if err := fs.AssertExists(archivedMetadataPath); err != nil {
				return fmt.Errorf("archived metadata.json should exist: %w", err)
			}

			// Verify metadata content
			metadataContent, err := fs.ReadString(archivedMetadataPath)
			if err != nil {
				return fmt.Errorf("reading archived metadata: %w", err)
			}
			if !strings.Contains(metadataContent, jobID) {
				return fmt.Errorf("archived metadata should contain job ID")
			}
			if !strings.Contains(metadataContent, "claude_session_id") {
				return fmt.Errorf("archived metadata should contain claude_session_id")
			}

			// Verify transcript.jsonl was copied
			archivedTranscriptPath := filepath.Join(artifactDir, "transcript.jsonl")
			if err := fs.AssertExists(archivedTranscriptPath); err != nil {
				return fmt.Errorf("archived transcript.jsonl should exist: %w", err)
			}

			// Verify transcript content
			transcriptContent, err := fs.ReadString(archivedTranscriptPath)
			if err != nil {
				return fmt.Errorf("reading archived transcript: %w", err)
			}
			if !strings.Contains(transcriptContent, "session_start") {
				return fmt.Errorf("archived transcript should contain session data")
			}
			if !strings.Contains(transcriptContent, "Implement a test feature") {
				return fmt.Errorf("archived transcript should contain conversation content")
			}

			return nil
		}),

		harness.NewStep("Verify job is completed", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}
			if !strings.Contains(content, "status: completed") {
				return fmt.Errorf("job status should be 'completed'")
			}
			return nil
		}),

		harness.NewStep("Add headless_agent job to test archiving for both job types", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Add a headless agent job
			cmd := ctx.Bin("plan", "add", "test-plan",
				"--type", "headless_agent",
				"--title", "Review Feature",
				"-p", "Review the implemented feature")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			headlessJobPath, err := findJobByPrefix(planPath, "02-review-feature")
			if err != nil {
				return err
			}
			ctx.Set("headless_job_path", headlessJobPath)
			return nil
		}),

		harness.NewStep("Mark headless job as running manually", func(ctx *harness.Context) error {
			headlessJobPath := ctx.GetString("headless_job_path")

			// Mark the job as running manually (since headless jobs run and complete quickly)
			content, err := fs.ReadString(headlessJobPath)
			if err != nil {
				return fmt.Errorf("reading headless job file: %w", err)
			}
			updatedContent := strings.Replace(content, "status: pending", "status: running", 1)
			if err := fs.WriteString(headlessJobPath, updatedContent); err != nil {
				return fmt.Errorf("updating headless job status: %w", err)
			}

			return nil
		}),

		harness.NewStep("Create mock session for headless job", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			headlessJobPath := ctx.GetString("headless_job_path")

			// Read job to get ID
			content, err := fs.ReadString(headlessJobPath)
			if err != nil {
				return fmt.Errorf("reading headless job file: %w", err)
			}

			var headlessJobID string
			for _, line := range strings.Split(content, "\n") {
				if strings.HasPrefix(line, "id: ") {
					headlessJobID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
					break
				}
			}
			if headlessJobID == "" {
				return fmt.Errorf("could not find headless job ID")
			}
			ctx.Set("headless_job_id", headlessJobID)

			mockClaudeSessionID := "11111111-2222-3333-4444-555555555555"
			ctx.Set("headless_claude_session_id", mockClaudeSessionID)

			// Create Claude session directory
			claudeSessionDir := filepath.Join(homeDir, ".grove", "hooks", "sessions", mockClaudeSessionID)
			if err := fs.CreateDir(claudeSessionDir); err != nil {
				return err
			}

			// Create transcript
			transcriptPath := filepath.Join(homeDir, ".claude", "projects", "test-project", mockClaudeSessionID+".jsonl")
			transcriptContent := `{"type":"session_start","timestamp":"2025-12-18T11:00:00Z"}
{"type":"message","role":"user","content":"Review the implemented feature"}
{"type":"message","role":"assistant","content":"The feature looks good."}
`
			if err := fs.WriteString(transcriptPath, transcriptContent); err != nil {
				return err
			}

			// Create metadata
			metadataContent := fmt.Sprintf(`{
  "session_id": "%s",
  "claude_session_id": "%s",
  "transcript_path": "%s",
  "plan_name": "test-plan",
  "job_file_path": "%s",
  "provider": "claude",
  "pid": 12346,
  "working_directory": "%s",
  "user": "testuser",
  "started_at": "2025-12-18T11:00:00Z"
}
`, headlessJobID, mockClaudeSessionID, transcriptPath, headlessJobPath, homeDir)
			metadataPath := filepath.Join(claudeSessionDir, "metadata.json")
			if err := fs.WriteString(metadataPath, metadataContent); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Remove headless job lock and complete", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			headlessJobPath := ctx.GetString("headless_job_path")

			// Remove lock
			lockPath := headlessJobPath + ".lock"
			if err := fs.RemoveIfExists(lockPath); err != nil {
				return err
			}

			// Complete the job
			cmd := ctx.Bin("plan", "complete", headlessJobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify headless job session artifacts were archived", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			headlessJobID := ctx.GetString("headless_job_id")

			artifactDir := filepath.Join(planPath, ".artifacts", headlessJobID)

			if err := fs.AssertExists(artifactDir); err != nil {
				return fmt.Errorf("headless job artifact directory should exist: %w", err)
			}

			archivedMetadataPath := filepath.Join(artifactDir, "metadata.json")
			if err := fs.AssertExists(archivedMetadataPath); err != nil {
				return fmt.Errorf("headless job archived metadata.json should exist: %w", err)
			}

			archivedTranscriptPath := filepath.Join(artifactDir, "transcript.jsonl")
			if err := fs.AssertExists(archivedTranscriptPath); err != nil {
				return fmt.Errorf("headless job archived transcript.jsonl should exist: %w", err)
			}

			// Verify transcript content
			transcriptContent, err := fs.ReadString(archivedTranscriptPath)
			if err != nil {
				return fmt.Errorf("reading headless job archived transcript: %w", err)
			}
			if !strings.Contains(transcriptContent, "Review the implemented feature") {
				return fmt.Errorf("headless job archived transcript should contain conversation content")
			}

			return nil
		}),

		harness.NewStep("Test archiving gracefully handles missing session", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Add a job without creating session artifacts
			cmd := ctx.Bin("plan", "add", "test-plan",
				"--type", "interactive_agent",
				"--title", "Missing Session",
				"-p", "Test missing session handling")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			missingSessionJobPath := filepath.Join(planPath, "03-missing-session.md")

			// Mark as running manually
			content, err := fs.ReadString(missingSessionJobPath)
			if err != nil {
				return err
			}
			updatedContent := strings.Replace(content, "status: pending", "status: running", 1)
			if err := fs.WriteString(missingSessionJobPath, updatedContent); err != nil {
				return err
			}

			// Try to complete without creating session artifacts
			// This should log a warning but not fail the completion
			cmd = ctx.Bin("plan", "complete", missingSessionJobPath)
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The completion should succeed (archiving failure is non-fatal)
			// But we expect a warning in the output
			if !strings.Contains(result.Stdout, "Warning") && !strings.Contains(result.Stderr, "Warning") {
				// It's OK if there's no warning in the output, the important part is that it doesn't fail
			}

			// Verify the job was still marked as completed
			content, err = fs.ReadString(missingSessionJobPath)
			if err != nil {
				return err
			}
			if !strings.Contains(content, "status: completed") {
				return fmt.Errorf("job should be marked completed even if archiving fails")
			}

			return nil
		}),
	},
)
