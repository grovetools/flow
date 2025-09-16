package orchestration

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed builtin_recipes/*/*.md
var builtinRecipeFS embed.FS

type Recipe struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
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
			recipes = append(recipes, recipe)
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

// GetRecipe finds and returns a recipe by name, checking user recipes first, then built-in.
func GetRecipe(name string) (*Recipe, error) {
	// First try user recipes
	recipe, err := GetUserRecipe(name)
	if err == nil {
		return recipe, nil
	}
	
	// Then try built-in recipes
	return GetBuiltinRecipe(name)
}

// ListAllRecipes lists all available recipes (both user and built-in).
func ListAllRecipes() ([]*Recipe, error) {
	recipes := make([]*Recipe, 0)
	
	// First add user recipes
	homeDir, err := os.UserHomeDir()
	if err == nil {
		userRecipeDir := filepath.Join(homeDir, ".config", "grove", "recipes")
		if entries, err := os.ReadDir(userRecipeDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					if recipe, err := GetUserRecipe(entry.Name()); err == nil {
						recipe.Description = "[User] " + recipe.Description
						recipes = append(recipes, recipe)
					}
				}
			}
		}
	}
	
	// Then add built-in recipes
	builtinRecipes, err := ListBuiltinRecipes()
	if err != nil {
		return nil, err
	}
	
	for _, recipe := range builtinRecipes {
		recipe.Description = "[Built-in] " + recipe.Description
		recipes = append(recipes, recipe)
	}
	
	return recipes, nil
}