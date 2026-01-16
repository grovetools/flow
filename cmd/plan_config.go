package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// PlanConfigCmd represents the plan config command
type PlanConfigCmd struct {
	Dir  string
	Set  []string
	Get  string
	JSON bool
}

// NewPlanConfigCmd creates a new plan config command
func NewPlanConfigCmd() *cobra.Command {
	var setFlags []string
	var getFlag string
	var jsonFlag bool

	cmd := &cobra.Command{
		Use:   "config [directory]",
		Short: "Get or set plan configuration values",
		Long: `Get or set configuration values in the plan's .grove-plan.yml file.

Examples:
  # Set a single value
  flow plan config myplan --set model=gemini-2.0-flash
  
  # Set multiple values
  flow plan config myplan --set model=gemini-2.0-flash --set worktree=feature/new
  
  # Get a value
  flow plan config myplan --get model
  
  # Show all configuration
  flow plan config myplan`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dir string
			if len(args) > 0 {
				dir = args[0]
			}

			configCmd := &PlanConfigCmd{
				Dir:  dir,
				Set:  setFlags,
				Get:  getFlag,
				JSON: jsonFlag,
			}
			return RunPlanConfig(configCmd)
		},
	}

	cmd.Flags().StringArrayVar(&setFlags, "set", nil, "Set a configuration value (format: key=value)")
	cmd.Flags().StringVar(&getFlag, "get", "", "Get a configuration value")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "Output in JSON format")

	return cmd
}

// RunPlanConfig executes the plan config command
func RunPlanConfig(cmd *PlanConfigCmd) error {
	// Resolve the plan path
	planPath, err := resolvePlanPathWithActiveJob(cmd.Dir)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// For absolute paths, use them directly
	if filepath.IsAbs(cmd.Dir) {
		planPath = cmd.Dir
	}

	// Check if plan directory exists
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		return fmt.Errorf("plan directory does not exist: %s", planPath)
	}

	configPath := filepath.Join(planPath, ".grove-plan.yml")

	// If no flags, show current configuration
	if len(cmd.Set) == 0 && cmd.Get == "" {
		return showConfig(configPath, cmd.JSON)
	}

	// Handle get operation
	if cmd.Get != "" {
		return getConfigValue(configPath, cmd.Get, cmd.JSON)
	}

	// Handle set operations
	if len(cmd.Set) > 0 {
		return setConfigValues(configPath, cmd.Set)
	}

	return nil
}

// showConfig displays the current configuration
func showConfig(configPath string, jsonOutput bool) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			if jsonOutput {
				fmt.Println("{}")
			} else {
				fmt.Println("No .grove-plan.yml file found")
			}
			return nil
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if jsonOutput {
		// Parse YAML and convert to JSON
		var config map[string]interface{}
		if err := yaml.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to parse config file: %w", err)
		}
		
		// Filter out nil values
		filtered := make(map[string]interface{})
		for k, v := range config {
			if v != nil {
				filtered[k] = v
			}
		}
		
		jsonData, err := json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to convert to JSON: %w", err)
		}
		fmt.Println(string(jsonData))
	} else {
		fmt.Println(string(data))
	}
	return nil
}

// getConfigValue retrieves a specific configuration value
func getConfigValue(configPath string, key string, jsonOutput bool) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no .grove-plan.yml file found")
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	value, exists := config[key]
	if !exists {
		return fmt.Errorf("key '%s' not found in configuration", key)
	}

	if jsonOutput {
		// Output as JSON object with the key
		result := map[string]interface{}{key: value}
		jsonData, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("failed to convert to JSON: %w", err)
		}
		fmt.Println(string(jsonData))
	} else {
		fmt.Println(value)
	}
	return nil
}

// setConfigValues updates configuration values
func setConfigValues(configPath string, pairs []string) error {
	// Read existing config or create new one
	var config map[string]interface{}
	
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read config file: %w", err)
		}
		// File doesn't exist, create new config
		config = make(map[string]interface{})
	} else {
		// Parse existing config
		if err := yaml.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to parse config file: %w", err)
		}
		if config == nil {
			config = make(map[string]interface{})
		}
	}

	// Parse and apply key=value pairs
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid format for set flag: %s (expected key=value)", pair)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Validate key and handle type conversion
		switch key {
		case "model", "worktree", "target_agent_container", "notes", "status":
			config[key] = value
		case "prepend_dependencies":
			// Handle boolean conversion
			boolVal, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean value for prepend_dependencies: %s", value)
			}
			config[key] = boolVal
		case "repos":
			// Handle array - split by comma
			if value == "" {
				config[key] = []string{}
			} else {
				repos := strings.Split(value, ",")
				for i := range repos {
					repos[i] = strings.TrimSpace(repos[i])
				}
				config[key] = repos
			}
		default:
			return fmt.Errorf("unknown configuration key: %s", key)
		}
	}

	// Marshal config to YAML
	yamlData, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write the file
	if err := os.WriteFile(configPath, yamlData, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	fmt.Printf("* Updated %s\n", configPath)
	
	// Propagate non-blank values to job files that don't have them set
	updatesToPropagate := make(map[string]interface{})
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if value != "" {
			updatesToPropagate[key] = value
		}
	}
	
	if len(updatesToPropagate) > 0 {
		planPath := filepath.Dir(configPath)
		plan, err := orchestration.LoadPlan(planPath)
		if err != nil {
			fmt.Printf("Warning: could not load plan to propagate config: %v\n", err)
			return nil
		}
		
		updatedJobs := 0
		for _, job := range plan.Jobs {
			jobContent, err := os.ReadFile(job.FilePath)
			if err != nil {
				fmt.Printf("Warning: could not read job file %s: %v\n", job.Filename, err)
				continue
			}
			
			frontmatter, _, _ := orchestration.ParseFrontmatter(jobContent)
			
			jobUpdates := make(map[string]interface{})
			for key, value := range updatesToPropagate {
				if _, ok := frontmatter[key]; !ok {
					// Check if this field is appropriate for this job type
					jobType, _ := frontmatter["type"].(string)
					if key == "worktree" && jobType == "shell" {
						// Shell jobs don't use worktrees
						continue
					}
					if key == "target_agent_container" && jobType != "agent" && jobType != "interactive_agent" {
						// Only agent jobs use containers
						continue
					}
					jobUpdates[key] = value
				}
			}
			
			if len(jobUpdates) > 0 {
				newContent, err := orchestration.UpdateFrontmatter(jobContent, jobUpdates)
				if err != nil {
					fmt.Printf("Warning: could not update frontmatter for %s: %v\n", job.Filename, err)
					continue
				}
				if err := os.WriteFile(job.FilePath, newContent, 0644); err != nil {
					fmt.Printf("Warning: could not write updated job file %s: %v\n", job.Filename, err)
					continue
				}
				updatedJobs++
			}
		}
		
		if updatedJobs > 0 {
			fmt.Printf("* Propagated config changes to %d job(s)\n", updatedJobs)
		}
	}
	
	return nil
}