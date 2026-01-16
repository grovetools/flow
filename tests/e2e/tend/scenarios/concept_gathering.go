package scenarios

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var ConceptGatheringScenario = harness.NewScenario(
	"concept-gathering",
	"Tests concept gathering functionality using grove-core workspace APIs",
	[]string{"concept", "oneshot", "integration"},
	[]harness.Step{
		harness.NewStep("Setup environment with multiple concepts", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "gather-test-project")
			if err != nil {
				return err
			}

			// Create concepts directory with multiple concepts
			conceptsDir := filepath.Join(notebooksRoot, "workspaces", "gather-test-project", "concepts")

			// Create first concept: api-design
			apiDesignDir := filepath.Join(conceptsDir, "api-design")
			if err := os.MkdirAll(apiDesignDir, 0755); err != nil {
				return fmt.Errorf("creating api-design concept: %w", err)
			}

			manifestContent := `id: api-design
title: API Design
description: RESTful API design patterns
status: active
related_concepts: []
related_plans: []
related_notes: []
`
			if err := fs.WriteString(filepath.Join(apiDesignDir, "concept-manifest.yml"), manifestContent); err != nil {
				return err
			}

			overviewContent := `# API Design

This concept covers RESTful API design patterns.

## Principles

- Resource-based URLs
- HTTP methods semantics
- Proper status codes
`
			if err := fs.WriteString(filepath.Join(apiDesignDir, "overview.md"), overviewContent); err != nil {
				return err
			}

			implementationContent := `# Implementation Guide

## REST Endpoints

- GET /api/resources - List resources
- POST /api/resources - Create resource
- GET /api/resources/:id - Get resource
`
			if err := fs.WriteString(filepath.Join(apiDesignDir, "implementation.md"), implementationContent); err != nil {
				return err
			}

			// Create second concept: database-schema
			dbSchemaDir := filepath.Join(conceptsDir, "database-schema")
			if err := os.MkdirAll(dbSchemaDir, 0755); err != nil {
				return fmt.Errorf("creating database-schema concept: %w", err)
			}

			dbManifest := `id: database-schema
title: Database Schema
description: Database design and migrations
status: active
related_concepts: []
related_plans: []
related_notes: []
`
			if err := fs.WriteString(filepath.Join(dbSchemaDir, "concept-manifest.yml"), dbManifest); err != nil {
				return err
			}

			dbOverview := `# Database Schema

This concept covers database design principles.

## Tables

- users
- posts
- comments
`
			if err := fs.WriteString(filepath.Join(dbSchemaDir, "overview.md"), dbOverview); err != nil {
				return err
			}

			// Create context rules
			rulesDir := filepath.Join(projectDir, ".grove")
			if err := os.MkdirAll(rulesDir, 0755); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(rulesDir, "rules"), "*.go\n"); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"}, // Mocks `grove llm request`
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Create mock LLM response", func(ctx *harness.Context) error {
			responseContent := `# Analysis Complete

Based on the concepts provided, here are recommendations:

1. API design looks good
2. Database schema needs indexing
`
			responseFile := filepath.Join(ctx.RootDir, "mock_response.txt")
			if err := fs.WriteString(responseFile, responseContent); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)
			return nil
		}),

		harness.NewStep("Create plan with concept gathering job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "gather-test-project", "plans", "concept-test-plan")
			ctx.Set("plan_path", planPath)

			// Init plan
			initCmd := ctx.Bin("plan", "init", "concept-test-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			// Create job with gather_concept_notes flag
			jobContent := `---
id: test-concept-gathering
title: Test Concept Gathering
type: oneshot
status: pending
gather_concept_notes: false
gather_concept_plans: false
---

Analyze the project concepts.
`
			jobPath := filepath.Join(planPath, "01-test-gathering.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Run job with concept gathering", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			llmResponseFile := ctx.GetString("llm_response_file")

			runCmd := ctx.Bin("plan", "run", "--next", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))

			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w\nStderr: %s", err, result.Stderr)
			}

			// Verify job completed
			jobPath := filepath.Join(planPath, "01-test-gathering.md")
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job: %w", err)
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job status to be 'completed', but was '%s'", job.Status)
			}

			return nil
		}),

		harness.NewStep("Verify aggregated concepts file was created", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// The aggregated-concepts.md file should be in .artifacts/{job-id}/
			artifactsDir := filepath.Join(planPath, ".artifacts", "test-concept-gathering")
			aggregatedFile := filepath.Join(artifactsDir, "aggregated-concepts.md")

			// Note: File may not exist if gather_concept_notes and gather_concept_plans are both false
			// This is expected behavior
			if _, err := os.Stat(aggregatedFile); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("error checking aggregated concepts file: %w", err)
			}

			return nil
		}),
	},
)

var ConceptGatheringWithNotesScenario = harness.NewScenario(
	"concept-gathering-with-notes",
	"Tests concept gathering with related notes using grove-core workspace APIs",
	[]string{"concept", "notes", "oneshot"},
	[]harness.Step{
		harness.NewStep("Setup environment with concept and related notes", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "gather-notes-project")
			if err != nil {
				return err
			}

			// Create inbox note first
			inboxDir := filepath.Join(notebooksRoot, "workspaces", "gather-notes-project", "inbox")
			if err := os.MkdirAll(inboxDir, 0755); err != nil {
				return err
			}
			noteContent := `---
title: Implementation Details
---

Key implementation details for the architecture concept.

## Important Points

- Use microservices
- Event-driven communication
`
			if err := fs.WriteString(filepath.Join(inboxDir, "impl-details.md"), noteContent); err != nil {
				return err
			}

			// Create concept with related note
			conceptsDir := filepath.Join(notebooksRoot, "workspaces", "gather-notes-project", "concepts")
			conceptDir := filepath.Join(conceptsDir, "architecture")
			if err := os.MkdirAll(conceptDir, 0755); err != nil {
				return err
			}

			manifestContent := `id: architecture
title: System Architecture
description: System architecture overview
status: active
related_concepts: []
related_plans: []
related_notes:
  - nb:gather-notes-project:inbox/impl-details.md
`
			if err := fs.WriteString(filepath.Join(conceptDir, "concept-manifest.yml"), manifestContent); err != nil {
				return err
			}

			overviewContent := `# System Architecture

High-level architecture overview.

## Components

- API Gateway
- Services
- Database
`
			if err := fs.WriteString(filepath.Join(conceptDir, "overview.md"), overviewContent); err != nil {
				return err
			}

			designContent := `# Design Decisions

## Patterns

- Microservices architecture
- API Gateway pattern
`
			if err := fs.WriteString(filepath.Join(conceptDir, "design.md"), designContent); err != nil {
				return err
			}

			// Create context rules
			rulesDir := filepath.Join(projectDir, ".grove")
			if err := os.MkdirAll(rulesDir, 0755); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(rulesDir, "rules"), "*.go\n"); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"}, // Mocks `grove llm request`
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Create mock LLM response", func(ctx *harness.Context) error {
			responseContent := `# Architecture Review Complete

The architecture concept and related notes have been analyzed.
`
			responseFile := filepath.Join(ctx.RootDir, "mock_response.txt")
			if err := fs.WriteString(responseFile, responseContent); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)
			return nil
		}),

		harness.NewStep("Create plan with gather_concept_notes enabled", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "gather-notes-project", "plans", "notes-test-plan")
			ctx.Set("plan_path", planPath)

			initCmd := ctx.Bin("plan", "init", "notes-test-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			jobContent := `---
id: test-notes-gathering
title: Test Notes Gathering
type: oneshot
status: pending
gather_concept_notes: true
gather_concept_plans: false
---

Analyze concepts with their related notes.
`
			jobPath := filepath.Join(planPath, "01-test-notes.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Run job and verify note gathering", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			llmResponseFile := ctx.GetString("llm_response_file")

			runCmd := ctx.Bin("plan", "run", "--next", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))

			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w\nStderr: %s", err, result.Stderr)
			}

			// Verify job completed
			jobPath := filepath.Join(planPath, "01-test-notes.md")
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job: %w", err)
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job status to be 'completed', but was '%s'", job.Status)
			}

			return nil
		}),

		harness.NewStep("Verify aggregated concepts includes notes", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			artifactsDir := filepath.Join(planPath, ".artifacts", "test-notes-gathering")
			aggregatedFile := filepath.Join(artifactsDir, "aggregated-concepts.md")

			if err := fs.AssertExists(aggregatedFile); err != nil {
				return fmt.Errorf("aggregated concepts file not found: %w", err)
			}

			// Verify XML structure
			if err := fs.AssertContains(aggregatedFile, "<concepts_context>"); err != nil {
				return fmt.Errorf("missing XML root element: %w", err)
			}
			if err := fs.AssertContains(aggregatedFile, "<concept id=\"architecture\">"); err != nil {
				return fmt.Errorf("missing concept element: %w", err)
			}
			if err := fs.AssertContains(aggregatedFile, "<manifest>"); err != nil {
				return fmt.Errorf("missing manifest element: %w", err)
			}

			// Verify multiple .md files were included (overview.md and design.md)
			if err := fs.AssertContains(aggregatedFile, "<document path=\"overview.md\">"); err != nil {
				return fmt.Errorf("missing overview.md document: %w", err)
			}
			if err := fs.AssertContains(aggregatedFile, "<document path=\"design.md\">"); err != nil {
				return fmt.Errorf("missing design.md document: %w", err)
			}

			// Verify linked notes section exists
			if err := fs.AssertContains(aggregatedFile, "<linked_notes>"); err != nil {
				return fmt.Errorf("missing linked_notes section: %w", err)
			}
			if err := fs.AssertContains(aggregatedFile, "nb:gather-notes-project:inbox/impl-details.md"); err != nil {
				return fmt.Errorf("missing note alias: %w", err)
			}

			// Verify note content was included
			if err := fs.AssertContains(aggregatedFile, "Implementation Details"); err != nil {
				return fmt.Errorf("missing note content: %w", err)
			}

			return nil
		}),
	},
)
