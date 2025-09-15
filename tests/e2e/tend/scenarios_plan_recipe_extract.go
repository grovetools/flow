package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// PlanRecipeWithExtractScenario tests combining recipe initialization with content extraction
func PlanRecipeWithExtractScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-recipe-extract",
		Description: "Tests plan init with both --recipe and --extract-all-from flags",
		Tags:        []string{"plan", "recipes", "extract", "init"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository and config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				return nil
			}),

			harness.NewStep("Test recipe with extracted spec replaces recipe spec", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Create a detailed spec file
				specFile := filepath.Join(ctx.RootDir, "auth-spec.md")
				specContent := `# Authentication System Specification

## Overview
We need to implement a comprehensive authentication system for our application.

## Functional Requirements

### User Registration
- Email and password registration
- Email verification required
- Password strength requirements
- Username uniqueness check

### User Login
- Email/password authentication
- Remember me functionality
- Failed login attempt tracking
- Account lockout after 5 failed attempts

### Password Management
- Forgot password flow with email reset
- Password change for logged-in users
- Force password reset capability

### Session Management
- JWT-based authentication
- Refresh token rotation
- Session timeout after 30 minutes of inactivity
- Concurrent session limits

## Technical Requirements

### Security
- Passwords hashed with bcrypt (cost factor 12)
- All auth endpoints rate-limited
- HTTPS-only cookies for tokens
- CSRF protection on state-changing operations

### Database Schema
- users table with email, username, password_hash
- sessions table for active sessions
- password_reset_tokens table
- email_verification_tokens table

### API Endpoints
- POST /auth/register
- POST /auth/login
- POST /auth/logout
- POST /auth/refresh
- POST /auth/forgot-password
- POST /auth/reset-password
- GET /auth/verify-email

## Implementation Notes
- Use existing validation middleware
- Integrate with current logging system
- Follow team's error handling patterns
`
				if err := os.WriteFile(specFile, []byte(specContent), 0644); err != nil {
					return fmt.Errorf("failed to create spec file: %w", err)
				}

				// Initialize with recipe and extraction
				cmd := command.New(flow, "plan", "init", "auth-system",
					"--recipe", "standard-feature",
					"--extract-all-from", specFile,
					"--with-worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}

				// Verify the right messages appear
				if !strings.Contains(result.Stdout, "Using recipe: standard-feature") {
					return fmt.Errorf("expected recipe usage message")
				}
				if !strings.Contains(result.Stdout, "Extracted content from") {
					return fmt.Errorf("expected extraction message")
				}

				planDir := filepath.Join(ctx.RootDir, "plans", "auth-system")
				
				// Verify extracted spec exists
				extractedSpec := filepath.Join(planDir, "01-auth-spec.md")
				content, err := os.ReadFile(extractedSpec)
				if err != nil {
					return fmt.Errorf("failed to read extracted spec: %w", err)
				}

				// Verify content was preserved
				if !strings.Contains(string(content), "Authentication System Specification") {
					return fmt.Errorf("extracted spec missing original content")
				}
				if !strings.Contains(string(content), "JWT-based authentication") {
					return fmt.Errorf("extracted spec missing detailed content")
				}

				// Verify worktree is set
				if !strings.Contains(string(content), "worktree: auth-system") {
					return fmt.Errorf("extracted spec missing worktree")
				}

				// Verify NO duplicate spec from recipe
				files, err := os.ReadDir(planDir)
				if err != nil {
					return fmt.Errorf("failed to read plan dir: %w", err)
				}

				for _, file := range files {
					name := file.Name()
					if strings.Contains(strings.ToLower(name), "spec") && name != "01-auth-spec.md" {
						return fmt.Errorf("found duplicate spec file: %s", name)
					}
				}

				// Verify implementation job exists and depends on the extracted spec
				implPath := filepath.Join(planDir, "02-implement.md")
				implContent, err := os.ReadFile(implPath)
				if err != nil {
					return fmt.Errorf("failed to read implementation job: %w", err)
				}
				if !strings.Contains(string(implContent), "depends_on:") {
					return fmt.Errorf("implementation job missing dependencies")
				}

				return nil
			}),

			harness.NewStep("Test extraction without recipe", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Create a simple doc to extract
				docFile := filepath.Join(ctx.RootDir, "feature-doc.md")
				docContent := `# Feature Documentation

## Current State
The system currently lacks proper error handling.

## Proposed Changes
- Add comprehensive error types
- Implement error recovery mechanisms
- Add error logging and monitoring
`
				if err := os.WriteFile(docFile, []byte(docContent), 0644); err != nil {
					return fmt.Errorf("failed to create doc file: %w", err)
				}

				// Initialize with just extraction (no recipe)
				cmd := command.New(flow, "plan", "init", "error-handling",
					"--extract-all-from", docFile,
					"--with-worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}

				planDir := filepath.Join(ctx.RootDir, "plans", "error-handling")
				
				// Should have exactly 2 files: .grove-plan.yml and extracted job
				files, err := os.ReadDir(planDir)
				if err != nil {
					return fmt.Errorf("failed to read plan dir: %w", err)
				}
				if len(files) != 2 {
					return fmt.Errorf("expected 2 files (config + extracted), got %d", len(files))
				}

				// Verify extracted content
				extractedPath := filepath.Join(planDir, "01-feature-doc.md")
				content, err := os.ReadFile(extractedPath)
				if err != nil {
					return fmt.Errorf("failed to read extracted file: %w", err)
				}
				if !strings.Contains(string(content), "Feature Documentation") {
					return fmt.Errorf("extracted content missing")
				}
				if !strings.Contains(string(content), "worktree: error-handling") {
					return fmt.Errorf("extracted file missing worktree")
				}

				return nil
			}),

			harness.NewStep("Test recipe with multiple extractions uses only first", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Create a comprehensive spec
				specFile := filepath.Join(ctx.RootDir, "api-spec.md")
				specContent := `# API Specification

## Endpoints
- GET /api/users
- POST /api/users
- PUT /api/users/:id
- DELETE /api/users/:id

## Authentication
All endpoints require Bearer token authentication.
`
				if err := os.WriteFile(specFile, []byte(specContent), 0644); err != nil {
					return fmt.Errorf("failed to create spec file: %w", err)
				}

				// Initialize with different recipe
				cmd := command.New(flow, "plan", "init", "api-feature",
					"--recipe", "standard-feature", 
					"--extract-all-from", specFile,
					"--worktree", "api-wt").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}

				planDir := filepath.Join(ctx.RootDir, "plans", "api-feature")
				
				// Verify extracted spec
				extractedPath := filepath.Join(planDir, "01-api-spec.md")
				content, err := os.ReadFile(extractedPath)
				if err != nil {
					return fmt.Errorf("failed to read extracted spec: %w", err)
				}

				// Should use explicit --worktree value
				if !strings.Contains(string(content), "worktree: api-wt") {
					return fmt.Errorf("expected worktree 'api-wt', content: %s", content)
				}

				// Verify plan config also has the explicit worktree
				configPath := filepath.Join(planDir, ".grove-plan.yml")
				configContent, err := os.ReadFile(configPath)
				if err != nil {
					return fmt.Errorf("failed to read config: %w", err)
				}
				if !strings.Contains(string(configContent), "worktree: api-wt") {
					return fmt.Errorf("config should have explicit worktree")
				}

				return nil
			}),

			harness.NewStep("Test extraction with path-based plan name", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Create spec
				specFile := filepath.Join(ctx.RootDir, "ui-spec.md")
				specContent := `# UI Components Specification

## Components to Build
- Button component with multiple variants
- Form input with validation
- Modal dialog system
- Toast notification system
`
				if err := os.WriteFile(specFile, []byte(specContent), 0644); err != nil {
					return fmt.Errorf("failed to create spec file: %w", err)
				}

				// Initialize with path (testing that base name is used)
				cmd := command.New(flow, "plan", "init", "frontend/ui-components",
					"--recipe", "standard-feature",
					"--extract-all-from", specFile,
					"--with-worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}

				// Plan should be created at the path but use base name for worktree
				planDir := filepath.Join(ctx.RootDir, "plans", "frontend", "ui-components")
				
				// Verify extracted spec
				extractedPath := filepath.Join(planDir, "01-ui-spec.md")
				content, err := os.ReadFile(extractedPath)
				if err != nil {
					return fmt.Errorf("failed to read extracted spec: %w", err)
				}

				// Worktree should be base name only
				if !strings.Contains(string(content), "worktree: ui-components") {
					return fmt.Errorf("expected worktree to be base name 'ui-components'")
				}

				// Verify plan was set as active with correct name
				if !strings.Contains(result.Stdout, "Set active plan to: ui-components") {
					return fmt.Errorf("active plan should be set to base name")
				}

				return nil
			}),

			harness.NewStep("Test extraction preserves complex markdown content", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Create a complex markdown file with various elements
				complexFile := filepath.Join(ctx.RootDir, "complex-spec.md")
				complexContent := `# Complex Feature Specification

## Code Examples

### JavaScript Example
` + "```javascript" + `
function authenticate(username, password) {
  // Hash the password
  const hashedPassword = bcrypt.hash(password, 10);
  
  // Check against database
  const user = await db.users.findOne({ 
    username, 
    password: hashedPassword 
  });
  
  if (!user) {
    throw new Error('Invalid credentials');
  }
  
  return generateToken(user);
}
` + "```" + `

### Python Example
` + "```python" + `
def process_data(items):
    """Process a list of items."""
    results = []
    for item in items:
        if item.is_valid():
            results.append(item.transform())
    return results
` + "```" + `

## Tables

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | /api/users | List all users |
| POST | /api/users | Create new user |
| PUT | /api/users/:id | Update user |
| DELETE | /api/users/:id | Delete user |

## Lists and Nested Items

1. First level item
   - Nested item 1
   - Nested item 2
     - Deep nested item
   - Nested item 3
2. Second level item
   * Alternative bullet style
   * Another item

## Special Characters & Formatting

This includes **bold text**, *italic text*, ` + "`inline code`" + `, and [links](https://example.com).

> This is a blockquote with important information
> that spans multiple lines.

---

## Grove Blocks (Should be Preserved)

<!-- grove: {"id": "abc123"} -->
This is a Grove-specific block that should be preserved exactly.

## Final Section

The implementation should preserve all formatting exactly as specified above.
`
				if err := os.WriteFile(complexFile, []byte(complexContent), 0644); err != nil {
					return fmt.Errorf("failed to create complex file: %w", err)
				}

				// Initialize with extraction
				cmd := command.New(flow, "plan", "init", "complex-feature",
					"--extract-all-from", complexFile).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}

				// Read the extracted file
				planDir := filepath.Join(ctx.RootDir, "plans", "complex-feature")
				extractedPath := filepath.Join(planDir, "01-complex-spec.md")
				content, err := os.ReadFile(extractedPath)
				if err != nil {
					return fmt.Errorf("failed to read extracted file: %w", err)
				}

				contentStr := string(content)

				// Verify all complex content is preserved
				checks := []struct {
					name    string
					content string
				}{
					{"JavaScript code block", "```javascript"},
					{"Python code block", "```python"},
					{"Table formatting", "| Method | Endpoint | Description |"},
					{"Nested lists", "- Deep nested item"},
					{"Bold text", "**bold text**"},
					{"Italic text", "*italic text*"},
					{"Inline code", "`inline code`"},
					{"Links", "[links](https://example.com)"},
					{"Blockquote", "> This is a blockquote"},
					{"Grove blocks", "<!-- grove:"},
					{"Special characters", "Special Characters & Formatting"},
				}

				for _, check := range checks {
					if !strings.Contains(contentStr, check.content) {
						return fmt.Errorf("extracted content missing %s", check.name)
					}
				}

				return nil
			}),
		},
	}
}