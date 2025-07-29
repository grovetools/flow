package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/config"
	"github.com/grovepm/grove-jobs/pkg/orchestration"
	"github.com/spf13/cobra"
)

// NewJobsExtractCmd creates the jobs extract command.
func NewJobsExtractCmd() *cobra.Command {
	var title string
	var file string

	cmd := &cobra.Command{
		Use:   "extract <block-id-1> [block-id-2...]",
		Short: "Extract chat blocks into a new chat job",
		Long: `Extract specific LLM responses from a chat into a new chat job for further refinement.

This command creates a new chat job containing only the selected LLM responses,
allowing you to continue working with specific parts of a larger conversation.

Examples:
  grove jobs extract --title "Database Schema Refinement" f3b9a2 a1c2d4
  grove jobs extract --file chat-session.md --title "API Design" d4e5f6`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJobsExtract(title, file, args)
		},
	}

	cmd.Flags().StringVar(&title, "title", "", "Title for the new chat job (required)")
	cmd.Flags().StringVar(&file, "file", "plan.md", "Chat file to extract from (default: plan.md)")
	cmd.MarkFlagRequired("title")

	return cmd
}

func runJobsExtract(title string, file string, blockIDs []string) error {
	// Load config
	cwd, _ := os.Getwd()
	configFile, err := config.FindConfigFile(cwd)
	var cfg *config.Config
	if err == nil {
		cfg, err = config.LoadWithOverrides(configFile)
		if err != nil {
			cfg = &config.Config{}
		}
	} else {
		cfg = &config.Config{}
	}

	// Get the current plan directory
	currentPlanPath, err := resolvePlanPathWithActiveJob("", cfg)
	if err != nil {
		return fmt.Errorf("could not resolve current plan path: %w", err)
	}

	// Load the current plan
	currentPlan, err := orchestration.LoadPlan(currentPlanPath)
	if err != nil {
		return fmt.Errorf("load current plan: %w", err)
	}

	// Find the specified chat file in the current plan
	chatFilePath := filepath.Join(currentPlanPath, file)
	if _, err := os.Stat(chatFilePath); err != nil {
		return fmt.Errorf("file %s not found in current plan: %w", file, err)
	}

	// Read and parse the chat file
	content, err := os.ReadFile(chatFilePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}

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
	var extractedContent strings.Builder
	foundBlocks := 0
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

	if foundBlocks == 0 {
		return fmt.Errorf("no valid blocks found to extract")
	}

	// Create a new chat job
	job := &orchestration.Job{
		Title:  title,
		Type:   orchestration.JobTypeChat,
		Status: orchestration.JobStatusPending,
		ID:     sanitizeForFilename(title),
		PromptBody: extractedContent.String(),
	}

	// Add the job to the current plan
	filename, err := orchestration.AddJob(currentPlan, job)
	if err != nil {
		return fmt.Errorf("failed to add job: %w", err)
	}

	fmt.Printf("✓ Extracted %d blocks to new chat job: %s\n", foundBlocks, filename)
	fmt.Printf("✓ You can now run: grove jobs run %s\n", filename)

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