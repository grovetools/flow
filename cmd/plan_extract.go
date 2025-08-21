package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// NewPlanExtractCmd creates the jobs extract command.
func NewPlanExtractCmd() *cobra.Command {
	var title string
	var file string
	var dependsOn []string
	var worktree string
	var model string
	var outputType string

	cmd := &cobra.Command{
		Use:   "extract <block-id-1> [block-id-2...] | all",
		Short: "Extract chat blocks into a new chat job",
		Long: `Extract specific LLM responses from a chat into a new chat job for further refinement.

This command creates a new chat job containing only the selected LLM responses,
allowing you to continue working with specific parts of a larger conversation.

The special argument "all" extracts all content below the frontmatter.

Examples:
  flow plan extract --title "Database Schema Refinement" f3b9a2 a1c2d4
  flow plan extract --file chat-session.md --title "API Design" d4e5f6
  flow plan extract all --file doc.md --title "Full Document"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJobsExtract(title, file, args, dependsOn, worktree, model, outputType)
		},
	}

	cmd.Flags().StringVar(&title, "title", "", "Title for the new chat job (required)")
	cmd.Flags().StringVar(&file, "file", "plan.md", "Chat file to extract from (default: plan.md)")
	cmd.Flags().StringSliceVarP(&dependsOn, "depends-on", "d", nil, "Dependencies (job filenames)")
	cmd.Flags().StringVar(&worktree, "worktree", "", "Explicitly set the worktree name (overrides automatic inference)")
	cmd.Flags().StringVar(&model, "model", "", "LLM model to use for this job")
	cmd.Flags().StringVar(&outputType, "output", "file", "Output type: file, commit, none, or generate_jobs")
	cmd.MarkFlagRequired("title")

	return cmd
}

func runJobsExtract(title string, file string, blockIDs []string, dependsOn []string, worktree string, model string, outputType string) error {
	// Get the current plan directory
	currentPlanPath, err := resolvePlanPathWithActiveJob("")
	if err != nil {
		// If we can't resolve from active job, check if we're in a plan directory
		if _, err := os.Stat(".grove-plan.yml"); err == nil {
			// We're in a plan directory
			currentPlanPath, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("could not get current directory: %w", err)
			}
		} else {
			return fmt.Errorf("could not resolve current plan path: %w", err)
		}
	}

	// Load the current plan
	currentPlan, err := orchestration.LoadPlan(currentPlanPath)
	if err != nil {
		return fmt.Errorf("load current plan: %w", err)
	}

	// Find the specified chat file
	var chatFilePath string
	if filepath.IsAbs(file) {
		// If the file path is absolute, use it directly
		chatFilePath = file
	} else {
		// Otherwise, look for it in the current plan
		chatFilePath = filepath.Join(currentPlanPath, file)
	}
	
	if _, err := os.Stat(chatFilePath); err != nil {
		return fmt.Errorf("file %s not found: %w", file, err)
	}

	// Read and parse the chat file
	content, err := os.ReadFile(chatFilePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}

	var extractedContent strings.Builder
	foundBlocks := 0

	// Check if "all" argument is specified
	if len(blockIDs) == 1 && blockIDs[0] == "all" {
		// Extract all content below the frontmatter
		_, bodyContent, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			return fmt.Errorf("parse frontmatter: %w", err)
		}
		extractedContent.Write(bodyContent)
		foundBlocks = 1
	} else {
		// Original behavior: extract specific blocks
		turns, err := orchestration.ParseChatFile(content)
		if err != nil {
			return fmt.Errorf("parse chat file: %w", err)
		}

		// Create a map of block IDs to content
		blockMap := make(map[string]*orchestration.ChatTurn)
		for _, turn := range turns {
			if turn.Directive != nil && turn.Directive.ID != "" {
				blockMap[turn.Directive.ID] = turn
			}
		}

		// Extract the requested blocks
		for _, blockID := range blockIDs {
			if turn, ok := blockMap[blockID]; ok {
				if foundBlocks > 0 {
					extractedContent.WriteString("\n\n---\n\n")
				}
				extractedContent.WriteString(turn.Content)
				foundBlocks++
			} else {
				fmt.Printf("Warning: block ID '%s' not found\n", blockID)
			}
		}
	}

	if foundBlocks == 0 {
		return fmt.Errorf("no valid blocks found to extract")
	}

	// Validate dependencies if provided
	for _, dep := range dependsOn {
		found := false
		for _, job := range currentPlan.Jobs {
			if job.Filename == dep {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("dependency not found: %s", dep)
		}
	}

	// Create a new chat job
	job := &orchestration.Job{
		Title:      title,
		Type:       orchestration.JobTypeChat,
		Status:     orchestration.JobStatusPending,
		ID:         sanitizeForFilename(title),
		PromptBody: extractedContent.String(),
		DependsOn:  dependsOn,
		Output: orchestration.OutputConfig{
			Type: outputType,
		},
	}

	// Apply explicit parameters (override plan defaults)
	if worktree != "" {
		job.Worktree = worktree
	}
	if model != "" {
		job.Model = model
	}

	// Inherit values from plan config (only if not explicitly set)
	if currentPlan.Config != nil {
		if job.Model == "" && currentPlan.Config.Model != "" {
			job.Model = currentPlan.Config.Model
		}
		if job.Worktree == "" && currentPlan.Config.Worktree != "" {
			job.Worktree = currentPlan.Config.Worktree
		}
		if currentPlan.Config.TargetAgentContainer != "" {
			job.TargetAgentContainer = currentPlan.Config.TargetAgentContainer
		}
	}

	// Add the job to the current plan
	filename, err := orchestration.AddJob(currentPlan, job)
	if err != nil {
		return fmt.Errorf("failed to add job: %w", err)
	}

	fmt.Printf("✓ Extracted %d blocks to new chat job: %s\n", foundBlocks, filename)
	fmt.Printf("✓ You can now run: flow plan run %s\n", filename)

	return nil
}

// sanitizeForFilename converts a string into a safe filename
func sanitizeForFilename(s string) string {
	// Convert to lowercase and replace spaces with hyphens
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")

	// Remove any characters that aren't alphanumeric, hyphens, or underscores
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			result.WriteRune(r)
		}
	}

	// Ensure we don't have multiple consecutive hyphens
	cleaned := result.String()
	for strings.Contains(cleaned, "--") {
		cleaned = strings.ReplaceAll(cleaned, "--", "-")
	}

	// Trim hyphens from start and end
	cleaned = strings.Trim(cleaned, "-")

	// If empty, use a default
	if cleaned == "" {
		cleaned = "extracted-chat"
	}

	return cleaned
}
