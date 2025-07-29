package orchestration

import (
	"embed"
	"fmt"
	"path/filepath"
	"strings"
)

//go:embed builtin_templates/*.md
var builtinTemplateFS embed.FS

// BuiltinTemplates stores templates loaded from embedded files.
var BuiltinTemplates map[string]string

func init() {
	BuiltinTemplates = make(map[string]string)
	
	// Read all template files from the embedded filesystem
	entries, err := builtinTemplateFS.ReadDir("builtin_templates")
	if err != nil {
		// This should never happen with embed
		panic(fmt.Sprintf("failed to read builtin templates directory: %v", err))
	}
	
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		
		// Extract template name from filename (remove .md extension)
		templateName := strings.TrimSuffix(entry.Name(), ".md")
		
		// Read the template content
		content, err := builtinTemplateFS.ReadFile(filepath.Join("builtin_templates", entry.Name()))
		if err != nil {
			// Log error but continue loading other templates
			fmt.Printf("Warning: failed to load template %s: %v\n", templateName, err)
			continue
		}
		
		BuiltinTemplates[templateName] = string(content)
	}
}