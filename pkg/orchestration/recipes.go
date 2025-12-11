package orchestration

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"gopkg.in/yaml.v3"
)

//go:embed all:builtin_recipes
var builtinRecipeFS embed.FS

// InitAction defines a single action to be performed during plan initialization.
// These are defined in a recipe's `workspace_init.yml`.
type InitAction struct {
	Type        string                 `yaml:"type"` // "shell" or "docker_compose"
	Description string                 `yaml:"description,omitempty"`
	Repo        string                 `yaml:"repo,omitempty"`         // Optional sub-repo for ecosystem worktrees
	Command     string                 `yaml:"command,omitempty"`      // For 'shell' type
	Files       []string               `yaml:"files,omitempty"`        // For 'docker_compose': list of user's compose files
	ProjectName string                 `yaml:"project_name,omitempty"` // For 'docker_compose' --project-name flag
	Overlay     map[string]interface{} `yaml:"overlay,omitempty"`      // For 'docker_compose': the generic overlay
}

// loadInitActions parses a workspace_init.yml file and populates the Actions and Description fields of a Recipe.
func loadInitActions(recipe *Recipe, recipeDir string, fs embed.FS) error {
	var initData []byte
	var err error

	initFilePath := filepath.Join(recipeDir, "workspace_init.yml")

	if (fs != embed.FS{}) { // A non-zero embed.FS indicates we are reading from embedded assets
		initData, err = fs.ReadFile(initFilePath)
	} else {
		initData, err = os.ReadFile(initFilePath)
	}

	if err != nil {
		if os.IsNotExist(err) {
			return nil // It's okay if the file doesn't exist
		}
		return err
	}

	var initConfig struct {
		Description string       `yaml:"description"`
		Actions     []InitAction `yaml:"actions"`
	}

	if err := yaml.Unmarshal(initData, &initConfig); err != nil {
		return fmt.Errorf("parsing %s: %w", initFilePath, err)
	}

	recipe.Description = initConfig.Description
	recipe.Actions = initConfig.Actions
	return nil
}

type Recipe struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source,omitempty"`  // [Built-in], [User], [Dynamic], or [Project]
	Domain      string            `json:"domain,omitempty"`  // "generic" or "grove"
	Jobs        map[string][]byte `json:"-"`                 // Filename -> Content
	Actions     []InitAction      `yaml:"actions,omitempty"` // Loaded from workspace_init.yml
}

// GetBuiltinRecipe finds and returns a built-in recipe.
func GetBuiltinRecipe(name string) (*Recipe, error) {
	// If name contains a slash, use it directly as a path
	if strings.Contains(name, "/") {
		recipeDir := filepath.Join("builtin_recipes", name)
		entries, err := builtinRecipeFS.ReadDir(recipeDir)
		if err != nil {
			return nil, fmt.Errorf("recipe '%s' not found", name)
		}

		recipe := &Recipe{
			Name: filepath.Base(name),
			Jobs: make(map[string][]byte),
		}

		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
				content, err := builtinRecipeFS.ReadFile(filepath.Join(recipeDir, entry.Name()))
				if err != nil {
					return nil, fmt.Errorf("could not read recipe file %s: %w", entry.Name(), err)
				}
				recipe.Jobs[entry.Name()] = content
			}
		}

		if len(recipe.Jobs) == 0 {
			return nil, fmt.Errorf("recipe '%s' contains no job files", name)
		}
		// Load init actions if present
		if err := loadInitActions(recipe, recipeDir, builtinRecipeFS); err != nil {
			return nil, fmt.Errorf("loading init actions for recipe '%s': %w", name, err)
		}

		return recipe, nil
	}

	// Otherwise, search for the recipe by name across all domains
	domainDirs, err := builtinRecipeFS.ReadDir("builtin_recipes")
	if err != nil {
		return nil, fmt.Errorf("could not read builtin recipes: %w", err)
	}

	for _, domainEntry := range domainDirs {
		if domainEntry.IsDir() {
			domain := domainEntry.Name()
			recipeDir := filepath.Join("builtin_recipes", domain, name)
			entries, err := builtinRecipeFS.ReadDir(recipeDir)
			if err != nil {
				continue // Try next domain
			}

			recipe := &Recipe{
				Name: name,
				Jobs: make(map[string][]byte),
			}

			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
					content, err := builtinRecipeFS.ReadFile(filepath.Join(recipeDir, entry.Name()))
					if err != nil {
						return nil, fmt.Errorf("could not read recipe file %s: %w", entry.Name(), err)
					}
					recipe.Jobs[entry.Name()] = content
				}
			}

			if len(recipe.Jobs) > 0 {
				recipe.Domain = domain

				// Load init actions if present
				if err := loadInitActions(recipe, recipeDir, builtinRecipeFS); err != nil {
					return nil, fmt.Errorf("loading init actions for recipe '%s': %w", name, err)
				}
				return recipe, nil
			}
		}
	}

	return nil, fmt.Errorf("recipe '%s' not found", name)
}

// RenderJob renders a single job template from a recipe.
func (r *Recipe) RenderJob(filename string, data interface{}) ([]byte, error) {
	content, ok := r.Jobs[filename]
	if !ok {
		return nil, fmt.Errorf("job template '%s' not found in recipe '%s'", filename, r.Name)
	}

	tmpl, err := template.New(filename).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parsing template %s: %w", filename, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("executing template %s: %w", filename, err)
	}

	return buf.Bytes(), nil
}

// ListBuiltinRecipes lists all available built-in recipes.
func ListBuiltinRecipes() ([]*Recipe, error) {
	var recipes []*Recipe
	domainDirs, err := builtinRecipeFS.ReadDir("builtin_recipes")
	if err != nil {
		return nil, fmt.Errorf("could not read builtin recipes: %w", err)
	}

	for _, domainEntry := range domainDirs {
		if domainEntry.IsDir() {
			domain := domainEntry.Name()
			recipeDirs, _ := builtinRecipeFS.ReadDir(filepath.Join("builtin_recipes", domain))
			for _, recipeEntry := range recipeDirs {
				if recipeEntry.IsDir() {
					recipeName := recipeEntry.Name()
					recipe, err := GetBuiltinRecipe(filepath.Join(domain, recipeName))
					if err != nil {
						// Log or skip? For now, skip.
						continue
					}
					// Attempt to find a description. For now, it's hardcoded.
					// Later, this could come from a recipe.yml.
					if recipe.Name == "standard-feature" {
						recipe.Description = "A standard workflow: spec -> implement -> review."
					}
					if recipe.Name == "chat" {
						recipe.Description = "A single chat job for discussion and planning."
					}
					if recipe.Name == "chat-workflow" {
						recipe.Description = "A chat-driven workflow: chat -> implement -> review."
					}
					if recipe.Name == "docgen-customize" {
						recipe.Description = "Customize and generate documentation: plan -> generate."
					}
					recipe.Domain = domain
					recipes = append(recipes, recipe)
				}
			}
		}
	}
	return recipes, nil
}

// DynamicRecipe represents the structure of a recipe from a dynamic source.
type DynamicRecipe struct {
	Description string            `json:"description"`
	Jobs        map[string]string `json:"jobs"` // filename -> content
}

// ListDynamicRecipes loads recipes by executing an external command.
func ListDynamicRecipes(getRecipeCmd string) ([]*Recipe, error) {
	if getRecipeCmd == "" {
		return nil, nil
	}

	var dynamicRecipes []*Recipe
	parts := strings.Fields(getRecipeCmd)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty get_recipe_cmd")
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.Output()
	if err != nil {
		// Log a warning instead of failing hard. A provider might be broken.
		fmt.Fprintf(os.Stderr, "Warning: recipe provider command failed: %v\n", err)
		return nil, nil // Return empty list, not an error
	}

	var recipesFromProvider map[string]DynamicRecipe
	if err := json.Unmarshal(output, &recipesFromProvider); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to parse JSON from recipe provider: %v\n", err)
		return nil, nil // Return empty list
	}

	// Sort keys for deterministic order
	var recipeNames []string
	for recipeName := range recipesFromProvider {
		recipeNames = append(recipeNames, recipeName)
	}
	sort.Strings(recipeNames)

	for _, recipeName := range recipeNames {
		dynamicRecipe := recipesFromProvider[recipeName]
		jobs := make(map[string][]byte)
		for filename, content := range dynamicRecipe.Jobs {
			jobs[filename] = []byte(content)
		}

		recipe := &Recipe{
			Name:        recipeName,
			Description: dynamicRecipe.Description,
			Source:      "[Dynamic]",
			Jobs:        jobs,
		}
		dynamicRecipes = append(dynamicRecipes, recipe)
	}
	return dynamicRecipes, nil
}

// ListProjectRecipes lists all project-local recipes from .grove/recipes, searching upwards.
func ListProjectRecipes() ([]*Recipe, error) {
	var recipes []*Recipe
	currentDir, err := os.Getwd()
	if err != nil {
		return nil, nil
	}

	// Search upwards for .grove/recipes/
	dir := currentDir
	for {
		projectRecipeDir := filepath.Join(dir, ".grove", "recipes")
		if entries, err := os.ReadDir(projectRecipeDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					if recipe, err := GetProjectRecipe(entry.Name()); err == nil {
						recipes = append(recipes, recipe)
					}
				}
			}
			break // Stop after finding the first .grove/recipes directory
		}

		// Check if we've reached the root
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return recipes, nil
}

// ListUserRecipes lists all user-defined recipes from ~/.config/grove/recipes.
func ListUserRecipes() ([]*Recipe, error) {
	var recipes []*Recipe
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, nil // Not an error if home dir doesn't exist
	}

	userRecipeDir := filepath.Join(homeDir, ".config", "grove", "recipes")
	if entries, err := os.ReadDir(userRecipeDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				if recipe, err := GetUserRecipe(entry.Name()); err == nil {
					recipes = append(recipes, recipe)
				}
			}
		}
	}
	return recipes, nil
}

// ListNotebookRecipes lists all recipes from the current notebook context.
func ListNotebookRecipes() ([]*Recipe, error) {
	var recipes []*Recipe
	notebookRecipesDir, err := getNotebookRecipesDir()
	if err != nil {
		return nil, nil // Not an error if the dir doesn't resolve
	}

	if entries, err := os.ReadDir(notebookRecipesDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				if recipe, err := GetNotebookRecipe(entry.Name()); err == nil {
					recipes = append(recipes, recipe)
				}
			}
		}
	}
	return recipes, nil
}

// GetProjectRecipe finds and returns a project-local recipe by searching upwards.
func GetProjectRecipe(name string) (*Recipe, error) {
	currentDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting current directory: %w", err)
	}

	// Search upwards for .grove/recipes/
	dir := currentDir
	for {
		recipeDir := filepath.Join(dir, ".grove", "recipes", name)
		entries, err := os.ReadDir(recipeDir)
		if err == nil {
			recipe := &Recipe{
				Name: name,
				Jobs: make(map[string][]byte),
			}

			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
					content, err := os.ReadFile(filepath.Join(recipeDir, entry.Name()))
					if err != nil {
						return nil, fmt.Errorf("could not read recipe file %s: %w", entry.Name(), err)
					}
					recipe.Jobs[entry.Name()] = content
				}
			}

			if err := loadInitActions(recipe, recipeDir, embed.FS{}); err != nil {
				return nil, fmt.Errorf("loading init actions for project recipe '%s': %w", name, err)
			}
			if len(recipe.Jobs) == 0 {
				return nil, fmt.Errorf("recipe '%s' contains no job files", name)
			}

			return recipe, nil
		}

		// Check if we've reached the root
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return nil, fmt.Errorf("recipe '%s' not found", name)
}

// GetUserRecipe finds and returns a user-defined recipe.
func GetUserRecipe(name string) (*Recipe, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home directory: %w", err)
	}

	recipeDir := filepath.Join(homeDir, ".config", "grove", "recipes", name)
	entries, err := os.ReadDir(recipeDir)
	if err != nil {
		return nil, fmt.Errorf("recipe '%s' not found", name)
	}

	recipe := &Recipe{
		Name: name,
		Jobs: make(map[string][]byte),
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			content, err := os.ReadFile(filepath.Join(recipeDir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("could not read recipe file %s: %w", entry.Name(), err)
			}
			recipe.Jobs[entry.Name()] = content
		}
	}

	if err := loadInitActions(recipe, recipeDir, embed.FS{}); err != nil {
		return nil, fmt.Errorf("loading init actions for user recipe '%s': %w", name, err)
	}

	if len(recipe.Jobs) == 0 {
		return nil, fmt.Errorf("recipe '%s' contains no job files", name)
	}

	return recipe, nil
}

// GetNotebookRecipe finds and returns a recipe from the current notebook context.
func GetNotebookRecipe(name string) (*Recipe, error) {
	notebookRecipesDir, err := getNotebookRecipesDir()
	if err != nil {
		return nil, err
	}

	recipeDir := filepath.Join(notebookRecipesDir, name)
	entries, err := os.ReadDir(recipeDir)
	if err != nil {
		return nil, fmt.Errorf("notebook recipe '%s' not found", name)
	}

	recipe := &Recipe{
		Name: name,
		Jobs: make(map[string][]byte),
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			content, err := os.ReadFile(filepath.Join(recipeDir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("could not read recipe file %s: %w", entry.Name(), err)
			}
			recipe.Jobs[entry.Name()] = content
		}
	}

	if err := loadInitActions(recipe, recipeDir, embed.FS{}); err != nil {
		return nil, fmt.Errorf("loading init actions for notebook recipe '%s': %w", name, err)
	}

	if len(recipe.Jobs) == 0 {
		return nil, fmt.Errorf("notebook recipe '%s' contains no job files", name)
	}

	return recipe, nil
}

// GetRecipe finds and returns a recipe by name with precedence: Project > Notebook > User > Dynamic > Built-in.
func GetRecipe(name string, getRecipeCmd string) (*Recipe, error) {
	// Precedence: Project > Notebook > User > Dynamic > Built-in

	// 1. Try project recipes
	recipe, err := GetProjectRecipe(name)
	if err == nil {
		recipe.Source = "[Project]"
		return recipe, nil
	}

	// 2. Try notebook recipes
	recipe, err = GetNotebookRecipe(name)
	if err == nil {
		recipe.Source = "[Notebook]"
		return recipe, nil
	}

	// 3. Try user recipes
	recipe, err = GetUserRecipe(name)
	if err == nil {
		recipe.Source = "[User]"
		return recipe, nil
	}

	// 4. Try dynamic recipes
	dynamicRecipes, _ := ListDynamicRecipes(getRecipeCmd)
	for _, r := range dynamicRecipes {
		if r.Name == name {
			return r, nil
		}
	}

	// 5. Try built-in recipes
	recipe, err = GetBuiltinRecipe(name)
	if err == nil {
		recipe.Source = "[Built-in]"
		return recipe, nil
	}

	return nil, fmt.Errorf("recipe '%s' not found", name)
}

// ListAllRecipes lists all available recipes with precedence: Project > User > Dynamic > Built-in.
func ListAllRecipes(getRecipeCmd string) ([]*Recipe, error) {
	recipeMap := make(map[string]*Recipe)

	// 1. Load built-in recipes first
	builtinRecipes, err := ListBuiltinRecipes()
	if err != nil {
		return nil, err
	}
	for _, recipe := range builtinRecipes {
		recipe.Source = "[Built-in]"
		recipeMap[recipe.Name] = recipe
	}

	// 2. Load dynamic recipes, overriding built-in
	dynamicRecipes, _ := ListDynamicRecipes(getRecipeCmd)
	for _, recipe := range dynamicRecipes {
		recipeMap[recipe.Name] = recipe
	}

	// 3. Load notebook recipes, overriding dynamic and built-in
	notebookRecipes, _ := ListNotebookRecipes()
	for _, recipe := range notebookRecipes {
		recipe.Source = "[Notebook]"
		recipeMap[recipe.Name] = recipe
	}

	// 4. Load user recipes, overriding notebook, dynamic and built-in
	userRecipes, _ := ListUserRecipes()
	for _, recipe := range userRecipes {
		recipe.Source = "[User]"
		recipeMap[recipe.Name] = recipe
	}

	// 5. Load project recipes, overriding all others
	projectRecipes, _ := ListProjectRecipes()
	for _, recipe := range projectRecipes {
		recipe.Source = "[Project]"
		recipeMap[recipe.Name] = recipe
	}

	// Convert map back to slice
	var allRecipes []*Recipe
	for _, recipe := range recipeMap {
		allRecipes = append(allRecipes, recipe)
	}

	// Sort for consistent output
	sort.Slice(allRecipes, func(i, j int) bool {
		return allRecipes[i].Name < allRecipes[j].Name
	})

	return allRecipes, nil
}

// getNotebookRecipesDir resolves the path to the current notebook's recipes directory.
func getNotebookRecipesDir() (string, error) {
	node, err := workspace.GetProjectByPath(".")
	if err != nil {
		return "", fmt.Errorf("could not determine current workspace: %w", err)
	}

	cfg, err := config.LoadDefault()
	if err != nil {
		cfg = &config.Config{} // Proceed with default locator logic
	}

	locator := workspace.NewNotebookLocator(cfg)
	return locator.GetRecipesDir(node)
}
