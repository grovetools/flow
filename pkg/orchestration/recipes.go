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
)

//go:embed builtin_recipes/*/*.md
var builtinRecipeFS embed.FS

type Recipe struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source,omitempty"` // [Built-in], [User], or [Dynamic]
	Jobs        map[string][]byte `json:"-"` // Filename -> Content
}

// GetBuiltinRecipe finds and returns a built-in recipe.
func GetBuiltinRecipe(name string) (*Recipe, error) {
	recipeDir := filepath.Join("builtin_recipes", name)
	entries, err := builtinRecipeFS.ReadDir(recipeDir)
	if err != nil {
		return nil, fmt.Errorf("recipe '%s' not found", name)
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

	if len(recipe.Jobs) == 0 {
		return nil, fmt.Errorf("recipe '%s' contains no job files", name)
	}

	return recipe, nil
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
	recipeDirs, err := builtinRecipeFS.ReadDir("builtin_recipes")
	if err != nil {
		return nil, fmt.Errorf("could not read builtin recipes: %w", err)
	}

	for _, entry := range recipeDirs {
		if entry.IsDir() {
			recipe, err := GetBuiltinRecipe(entry.Name())
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
			recipes = append(recipes, recipe)
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

	if len(recipe.Jobs) == 0 {
		return nil, fmt.Errorf("recipe '%s' contains no job files", name)
	}

	return recipe, nil
}

// GetRecipe finds and returns a recipe by name with precedence: User > Dynamic > Built-in.
func GetRecipe(name string, getRecipeCmd string) (*Recipe, error) {
	// Precedence: User > Dynamic > Built-in

	// 1. Try user recipes
	recipe, err := GetUserRecipe(name)
	if err == nil {
		recipe.Source = "[User]"
		return recipe, nil
	}

	// 2. Try dynamic recipes
	dynamicRecipes, _ := ListDynamicRecipes(getRecipeCmd)
	for _, r := range dynamicRecipes {
		if r.Name == name {
			return r, nil
		}
	}

	// 3. Try built-in recipes
	recipe, err = GetBuiltinRecipe(name)
	if err == nil {
		recipe.Source = "[Built-in]"
		return recipe, nil
	}

	return nil, fmt.Errorf("recipe '%s' not found", name)
}

// ListAllRecipes lists all available recipes with precedence: User > Dynamic > Built-in.
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

	// 3. Load user recipes, overriding all others
	userRecipes, _ := ListUserRecipes()
	for _, recipe := range userRecipes {
		recipe.Source = "[User]"
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