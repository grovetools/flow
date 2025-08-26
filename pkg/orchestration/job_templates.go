package orchestration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/mattsolo1/grove-core/util/sanitize"
)

// JobTemplate represents a predefined job structure.
type JobTemplate struct {
	Name        string                 `json:"name"`
	Path        string                 `json:"path"`
	Source      string                 `json:"source"` // "project", "user", "builtin"
	Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
	Frontmatter map[string]interface{} `json:"frontmatter,omitempty"`
	Prompt      string                 `json:"prompt,omitempty"`
}

// TemplateManager finds and loads job templates.
type TemplateManager struct {
	// Paths can be added here for customization
}

func NewTemplateManager() *TemplateManager {
	return &TemplateManager{}
}

// FindTemplate searches for a template by name, traversing upwards to find it.
func (tm *TemplateManager) FindTemplate(name string) (*JobTemplate, error) {
	// Start from current directory and traverse upwards
	currentDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting current directory: %w", err)
	}

	// 1. Search upwards for .grove/job-templates/
	dir := currentDir
	for {
		templatePath := filepath.Join(dir, ".grove", "job-templates", name+".md")
		if _, err := os.Stat(templatePath); err == nil {
			// Calculate relative path for cleaner display
			relPath, _ := filepath.Rel(currentDir, templatePath)
			if relPath == "" {
				relPath = templatePath
			}
			return tm.LoadTemplate(templatePath, name, "project")
		}

		// Check if we've reached the root
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// 2. Check user-global: ~/.config/grove/job-templates/
	homeDir, err := os.UserHomeDir()
	if err == nil {
		userPath := filepath.Join(homeDir, ".config", "grove", "job-templates", name+".md")
		if _, err := os.Stat(userPath); err == nil {
			return tm.LoadTemplate(userPath, name, "user")
		}
	}

	// 3. Check built-in templates
	if content, ok := BuiltinTemplates[name]; ok {
		fm, body, err := ParseFrontmatter([]byte(content))
		if err != nil {
			return nil, fmt.Errorf("failed to parse built-in template '%s': %w", name, err)
		}

		template := &JobTemplate{
			Name:        name,
			Path:        "builtin:" + name,
			Source:      "builtin",
			Frontmatter: fm,
			Prompt:      sanitize.UTF8(body),
		}

		if desc, ok := fm["description"].(string); ok {
			template.Description = desc
		}

		return template, nil
	}

	return nil, fmt.Errorf("template '%s' not found", name)
}

// ListTemplates lists all discoverable templates by searching upwards.
func (tm *TemplateManager) ListTemplates() ([]*JobTemplate, error) {
	templates := make([]*JobTemplate, 0)
	templateNames := make(map[string]bool) // Track template names to avoid duplicates

	// Start from current directory and traverse upwards
	currentDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting current directory: %w", err)
	}

	// 1. Search upwards for .grove/job-templates/
	dir := currentDir
	for {
		projectDir := filepath.Join(dir, ".grove", "job-templates")
		if entries, err := os.ReadDir(projectDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
					name := strings.TrimSuffix(entry.Name(), ".md")
					// Only add if we haven't seen this template name yet (closer templates take precedence)
					if !templateNames[name] {
						if tmpl, err := tm.LoadTemplate(filepath.Join(projectDir, entry.Name()), name, "project"); err == nil {
							templates = append(templates, tmpl)
							templateNames[name] = true
						}
					}
				}
			}
			break // Stop after finding the first .grove/job-templates directory
		}

		// Check if we've reached the root
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// 2. Check user-global: ~/.config/grove/job-templates/
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		userDir := filepath.Join(homeDir, ".config", "grove", "job-templates")
		if entries, err := os.ReadDir(userDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
					name := strings.TrimSuffix(entry.Name(), ".md")
					// Only add if we haven't seen this template name yet
					if !templateNames[name] {
						if tmpl, err := tm.LoadTemplate(filepath.Join(userDir, entry.Name()), name, "user"); err == nil {
							templates = append(templates, tmpl)
							templateNames[name] = true
						}
					}
				}
			}
		}
	}

	// 3. Add built-in templates
	for name, content := range BuiltinTemplates {
		// Only add if we haven't seen this template name yet
		if !templateNames[name] {
			fm, body, err := ParseFrontmatter([]byte(content))
			if err != nil {
				continue
			}

			template := &JobTemplate{
				Name:        name,
				Path:        "builtin:" + name,
				Source:      "builtin",
				Frontmatter: fm,
				Prompt:      sanitize.UTF8(body),
			}

			if desc, ok := fm["description"].(string); ok {
				template.Description = desc
			}

			templates = append(templates, template)
			templateNames[name] = true
		}
	}

	return templates, nil
}

// LoadTemplate loads a template from a given path.
func (tm *TemplateManager) LoadTemplate(path, name, source string) (*JobTemplate, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fm, body, err := ParseFrontmatter(content)
	if err != nil {
		return nil, err
	}

	template := &JobTemplate{
		Name:        name,
		Path:        path,
		Source:      source,
		Frontmatter: fm,
		Prompt:      string(body),
	}
	
	if desc, ok := fm["description"].(string); ok {
		template.Description = desc
	}

	return template, nil
}

// Render applies data to a template's prompt.
func (t *JobTemplate) Render(data interface{}) (string, error) {
	tmpl, err := template.New(t.Name).Parse(t.Prompt)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}