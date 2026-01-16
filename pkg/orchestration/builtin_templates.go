package orchestration

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/util/sanitize"
)

var templateUlog = grovelogging.NewUnifiedLogger("grove-flow.templates")

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
			ctx := context.Background()
			templateUlog.Warn("Failed to load builtin template").
				Field("template_name", templateName).
				Field("path", path).
				Err(err).
				Log(ctx)
			return nil
		}

		fm, body, err := ParseFrontmatter(content)
		if err != nil {
			ctx := context.Background()
			templateUlog.Warn("Failed to parse builtin template").
				Field("template_name", templateName).
				Field("path", path).
				Err(err).
				Log(ctx)
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