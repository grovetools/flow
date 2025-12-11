package cmd

import (
	"fmt"
	"sort"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var (
	planActionList bool
)

var planActionCmd = &cobra.Command{
	Use:   "action [action-name] [plan-name]",
	Short: "Execute or list workspace actions from a recipe",
	Long: `Execute a named workspace action defined in the recipe's workspace_init.yml,
or list all available actions.

Examples:
  # List available actions for the current plan
  flow plan action --list

  # List available actions for a specific plan
  flow plan action --list my-plan

  # Run the "start-dev" action for the current plan
  flow plan action start-dev

  # Run the "start-dev" action for a specific plan
  flow plan action start-dev my-plan

  # Run the init actions (special case)
  flow plan action init

Available actions are defined in the recipe's workspace_init.yml under the 'actions' key.
The special action 'init' runs the actions defined under the 'init' key.`,
	Args: cobra.RangeArgs(0, 2),
	RunE: runPlanAction,
}

func init() {
	planActionCmd.Flags().BoolVar(&planActionList, "list", false, "List available actions for the plan")
}

func runPlanAction(cmd *cobra.Command, args []string) error {
	// Determine plan name
	var planName string

	if planActionList {
		// For --list, plan name is optional first arg
		if len(args) > 0 {
			planName = args[0]
		}
	} else {
		// For execution, need action name
		if len(args) == 0 {
			return fmt.Errorf("action name required (or use --list to see available actions)")
		}

		if len(args) > 1 {
			planName = args[1]
		}
	}

	// If no plan name provided, try to get active plan
	if planName == "" {
		activePlan, err := getActivePlanWithMigration()
		if err != nil {
			return fmt.Errorf("no plan specified and could not determine active plan: %w", err)
		}
		planName = activePlan
	}

	// Resolve the plan directory
	planPath, err := resolvePlanPath(planName)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// Load the plan to get the recipe
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	if plan.Config == nil || plan.Config.Recipe == "" {
		return fmt.Errorf("plan was not created from a recipe (no recipe field in .grove-plan.yml)")
	}

	// Load flow config to get recipe command if needed
	flowCfg, _ := loadFlowConfig()
	var getRecipeCmd string
	if flowCfg != nil {
		_, getRecipeCmd, _ = loadFlowConfigWithDynamicRecipes()
	}

	// Load the recipe
	recipe, err := orchestration.GetRecipe(plan.Config.Recipe, getRecipeCmd)
	if err != nil {
		return fmt.Errorf("failed to load recipe '%s': %w", plan.Config.Recipe, err)
	}

	// If --list flag is set, list available actions
	if planActionList {
		return listAvailableActions(recipe, planName)
	}

	actionName := args[0]

	// Prepare template data
	templateData := struct {
		PlanName string
		Vars     map[string]string
	}{
		PlanName: planName,
		Vars:     make(map[string]string), // Could be extended to support vars from plan config
	}

	// Find and execute the action
	var actionsToRun []orchestration.InitAction

	if actionName == "init" {
		// Special case: run init actions
		if len(recipe.InitActions) == 0 {
			return fmt.Errorf("recipe '%s' has no init actions defined", recipe.Name)
		}
		actionsToRun = recipe.InitActions
		fmt.Printf("▶️  Executing init actions from recipe '%s'...\n", recipe.Name)
	} else {
		// Look up the named action
		if recipe.NamedActions == nil || len(recipe.NamedActions[actionName]) == 0 {
			return fmt.Errorf("action '%s' not found in recipe '%s'", actionName, recipe.Name)
		}
		actionsToRun = recipe.NamedActions[actionName]
		fmt.Printf("▶️  Executing action '%s' from recipe '%s'...\n", actionName, recipe.Name)
	}

	// Execute the actions
	if err := executeInitActions(actionsToRun, plan.Config.Worktree, plan.Config.Worktree, templateData); err != nil {
		return fmt.Errorf("failed to execute action '%s': %w", actionName, err)
	}

	fmt.Println("✓ Action completed successfully.")
	return nil
}

func listAvailableActions(recipe *orchestration.Recipe, planName string) error {
	fmt.Printf("Available actions for plan '%s' (recipe: %s)\n\n", planName, recipe.Name)

	// List init actions
	if len(recipe.InitActions) > 0 {
		fmt.Println("  init")
		for _, action := range recipe.InitActions {
			fmt.Printf("    - %s\n", action.Description)
		}
		fmt.Println()
	}

	// List named actions
	if len(recipe.NamedActions) > 0 {
		// Sort action names for consistent output
		actionNames := make([]string, 0, len(recipe.NamedActions))
		for name := range recipe.NamedActions {
			actionNames = append(actionNames, name)
		}
		sort.Strings(actionNames)

		for _, name := range actionNames {
			actions := recipe.NamedActions[name]
			fmt.Printf("  %s\n", name)
			for _, action := range actions {
				fmt.Printf("    - %s\n", action.Description)
			}
			fmt.Println()
		}
	}

	if len(recipe.InitActions) == 0 && len(recipe.NamedActions) == 0 {
		fmt.Println("  No actions defined in this recipe")
	} else {
		fmt.Println("Run an action with: flow plan action <action-name>")
	}

	return nil
}
