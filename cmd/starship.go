package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-flow/pkg/state"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// NewStarshipCmd creates the `flow starship` command and its subcommands.
func NewStarshipCmd() *cobra.Command {
	starshipCmd := &cobra.Command{
		Use:   "starship",
		Short: "Manage Starship prompt integration",
		Long:  `Provides commands to integrate Grove Flow status with the Starship prompt.`,
	}

	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install the Grove Flow module to your starship.toml",
		Long: `Appends a custom module to your starship.toml configuration file to display
the current active plan, model, and worktree in your shell prompt. It will
also attempt to add the module to your main prompt format.`,
		RunE: runStarshipInstall,
	}

	statusCmd := &cobra.Command{
		Use:    "status",
		Short:  "Print status for Starship prompt (for internal use)",
		Hidden: true,
		RunE:   runStarshipStatus,
	}

	starshipCmd.AddCommand(installCmd)
	starshipCmd.AddCommand(statusCmd)

	return starshipCmd
}

func runStarshipInstall(cmd *cobra.Command, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not get home directory: %w", err)
	}

	configPath := filepath.Join(home, ".config", "starship.toml")

	contentBytes, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("starship config not found at %s. Please ensure starship is installed and configured", configPath)
		}
		return fmt.Errorf("could not read starship config: %w", err)
	}
	content := string(contentBytes)

	// --- 1. Add or replace the custom module definition ---
	moduleConfig := `
# Added by 'flow starship install'
[custom.grove]
description = "Shows Grove Flow plan status"
command = "flow starship status"
when = "test -f .grove/state.yml || test -f grove.yml"
format = " [$output](bold green) "
`

	// Check if [custom.grove] already exists and replace it
	if strings.Contains(content, "[custom.grove]") {
		// Find the start of [custom.grove] section
		startIdx := strings.Index(content, "[custom.grove]")
		if startIdx != -1 {
			// Find the next section (starts with '[') or end of file
			afterGrove := content[startIdx:]
			nextSectionIdx := strings.Index(afterGrove[1:], "\n[")
			
			var endIdx int
			if nextSectionIdx != -1 {
				endIdx = startIdx + nextSectionIdx + 1
			} else {
				endIdx = len(content)
			}
			
			// Replace the entire [custom.grove] section
			content = content[:startIdx] + moduleConfig + content[endIdx:]
			fmt.Println("✓ Updated existing Grove Flow starship module configuration.")
		}
	} else {
		content += moduleConfig
		fmt.Println("✓ Added [custom.grove] module to starship config.")
	}

	// --- 2. Add the module to the prompt format ---
	if strings.Contains(content, "$custom.grove") {
		fmt.Println("✓ Grove Flow module already in starship format.")
	} else {
		// Try to insert it after git_metrics, which is a common element.
		target := "$git_metrics\\"
		if strings.Contains(content, target) {
			replacement := target + "\n$custom.grove\\"
			content = strings.Replace(content, target, replacement, 1)
			fmt.Println("✓ Added Grove Flow module to starship format.")
		} else {
			fmt.Printf("⚠️  Could not automatically add '$custom.grove' to your starship format.\n")
			fmt.Printf("   Please add it manually to the 'format' string in %s\n", configPath)
		}
	}

	// --- 3. Write the updated config back ---
	err = os.WriteFile(configPath, []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("failed to write updated starship config: %w", err)
	}

	fmt.Printf("\nSuccessfully updated %s. Please restart your shell to see the changes.\n", configPath)
	return nil
}

func runStarshipStatus(cmd *cobra.Command, args []string) error {
	// This command must be fast and should not print errors to stderr.
	activePlan, err := state.GetActiveJob()
	if err != nil || activePlan == "" {
		return nil // Print nothing if no active job or error
	}

	// Now try to get model and worktree from the plan's config
	planPath, err := resolvePlanPathWithActiveJob(activePlan)
	if err != nil {
		// Can't resolve path, just show the plan name
		fmt.Printf("🌳 Plan: %s", activePlan)
		return nil
	}

	configPath := filepath.Join(planPath, ".grove-plan.yml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// Config file not found, just show the plan name
		fmt.Printf("🌳 Plan: %s", activePlan)
		return nil
	}

	var config struct {
		Model    string `yaml:"model"`
		Worktree string `yaml:"worktree"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		// Invalid config, just show the plan name
		fmt.Printf("🌳 Plan: %s", activePlan)
		return nil
	}

	// Format: 🌳 Plan: plan-name 🤖 model-name 🌲 in worktree (or worktree name)
	output := fmt.Sprintf("🌳 Plan: %s", activePlan)

	if config.Model != "" {
		output += fmt.Sprintf(" 🤖 %s", config.Model)
	}

	if config.Worktree != "" {
		if config.Worktree == activePlan {
			output += " 🌲 in worktree"
		} else {
			output += fmt.Sprintf(" 🌲 %s", config.Worktree)
		}
	}

	fmt.Print(output)
	return nil
}