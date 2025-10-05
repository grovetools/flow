package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	grovecontext "github.com/mattsolo1/grove-context/pkg/context"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// NewPlanContextCmd creates the plan context command.
func NewPlanContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage job-specific context rules",
		Long:  "Manage job-specific context rules for plan jobs",
	}

	cmd.AddCommand(newPlanContextSetCmd())
	return cmd
}

// newPlanContextSetCmd creates the plan context set command.
func newPlanContextSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <job-file>",
		Short: "Save current context rules for a job",
		Long: `Save the current active .grove/rules file as job-specific context rules.

The rules will be saved in the plan's rules/ subdirectory and referenced
in the job's frontmatter. When the job runs, it will automatically use
these saved rules to generate context.`,
		Args: cobra.ExactArgs(1),
		RunE: runPlanContextSet,
	}

	return cmd
}

func runPlanContextSet(cmd *cobra.Command, args []string) error {
	jobFilePath := args[0]

	// Get absolute path for job file
	absJobPath, err := filepath.Abs(jobFilePath)
	if err != nil {
		return fmt.Errorf("failed to resolve job file path: %w", err)
	}

	// Verify job file exists
	if _, err := os.Stat(absJobPath); os.IsNotExist(err) {
		return fmt.Errorf("job file not found: %s", jobFilePath)
	}

	// Find the active rules file using grove-context
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	ctxMgr := grovecontext.NewManager(cwd)

	// Find active rules - we need to check manually since findActiveRulesFile is private
	activeRulesPath := filepath.Join(cwd, ".grove", "rules")
	if _, err := os.Stat(activeRulesPath); os.IsNotExist(err) {
		// Try old location for backward compatibility
		activeRulesPath = filepath.Join(cwd, ".grovectx")
		if _, err := os.Stat(activeRulesPath); os.IsNotExist(err) {
			return fmt.Errorf("no active rules file found. Please create .grove/rules first")
		}
	}

	// Read the active rules content
	rulesContent, err := os.ReadFile(activeRulesPath)
	if err != nil {
		return fmt.Errorf("failed to read active rules file: %w", err)
	}

	// Determine plan directory from job file path
	planDir := filepath.Dir(absJobPath)

	// Create rules subdirectory in plan directory
	rulesDir := filepath.Join(planDir, "rules")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return fmt.Errorf("failed to create rules directory: %w", err)
	}

	// Generate destination filename: rules/<job-filename>.rules
	jobFileName := filepath.Base(absJobPath)
	destFileName := fmt.Sprintf("%s.rules", jobFileName)
	destPath := filepath.Join(rulesDir, destFileName)

	// Write rules content to destination
	if err := os.WriteFile(destPath, rulesContent, 0644); err != nil {
		return fmt.Errorf("failed to write rules file: %w", err)
	}

	// Update job frontmatter with rules_file field
	relativePath := filepath.Join("rules", destFileName)

	// Read job file content
	jobContent, err := os.ReadFile(absJobPath)
	if err != nil {
		return fmt.Errorf("failed to read job file: %w", err)
	}

	// Update frontmatter
	updates := map[string]interface{}{
		"rules_file": relativePath,
	}

	newContent, err := orchestration.UpdateFrontmatter(jobContent, updates)
	if err != nil {
		return fmt.Errorf("failed to update job frontmatter: %w", err)
	}

	// Write updated content back
	if err := os.WriteFile(absJobPath, newContent, 0644); err != nil {
		return fmt.Errorf("failed to write updated job file: %w", err)
	}

	// Display success message
	fmt.Printf("✅ Context rules saved for job: %s\n", jobFileName)
	fmt.Printf("   Rules file: %s\n", destPath)
	fmt.Printf("   Frontmatter updated with: rules_file: %s\n", relativePath)

	// Display a preview of the rules
	rulesStr := strings.TrimSpace(string(rulesContent))
	if rulesStr != "" {
		lines := strings.Split(rulesStr, "\n")
		fmt.Printf("\n📋 Saved rules (%d lines):\n", len(lines))

		maxLines := 5
		displayLines := lines
		if len(lines) > maxLines {
			displayLines = lines[:maxLines]
		}

		for _, line := range displayLines {
			fmt.Printf("   %s\n", line)
		}

		if len(lines) > maxLines {
			fmt.Printf("   ... (%d more lines)\n", len(lines)-maxLines)
		}
	}

	// Suppress ctxMgr to avoid unused variable error
	_ = ctxMgr

	return nil
}
