package orchestration

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/util/sanitize"
)

//go:embed all:builtin_templates
var builtinTemplateFS embed.FS

// BuiltinTemplates stores templates loaded from embedded files.
var BuiltinTemplates map[string]*JobTemplate

func init() {
	BuiltinTemplates = make(map[string]*JobTemplate)

	// Walk the new nested directory structure
	if err := fs.WalkDir(builtinTemplateFS, "builtin_templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}

		// Extract template name from filename
		templateName := strings.TrimSuffix(d.Name(), ".md")

		// Read content
		content, err := builtinTemplateFS.ReadFile(path)
		if err != nil {
			fmt.Printf("Warning: failed to load template %s: %v\n", templateName, err)
			return nil
		}

		fm, body, err := ParseFrontmatter(content)
		if err != nil {
			fmt.Printf("Warning: failed to parse template %s: %v\n", templateName, err)
			return nil
		}

		template := &JobTemplate{
			Name:        templateName,
			Path:        "builtin:" + templateName,
			Source:      "builtin",
			Frontmatter: fm,
			Prompt:      sanitize.UTF8(body),
		}

		if desc, ok := fm["description"].(string); ok {
			template.Description = desc
		}

		// Parse domain and type from path
		parts := strings.Split(path, string(filepath.Separator))
		if len(parts) == 4 { // builtin_templates/{domain}/{type}/{file}.md
			template.Domain = parts[1]
			template.Type = parts[2]
		}

		BuiltinTemplates[templateName] = template
		return nil
	}); err != nil {
		panic(fmt.Sprintf("failed to walk builtin templates: %v", err))
	}
}