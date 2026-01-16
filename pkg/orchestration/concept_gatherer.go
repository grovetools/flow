package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/alias"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// ConceptInfo represents basic information about a concept
type ConceptInfo struct {
	ID    string
	Title string
	Path  string
}

// gatherConcepts aggregates all project concepts and their linked notes/plans into a single context file.
func gatherConcepts(ctx context.Context, job *Job, plan *Plan, workDir string) (string, error) {
	requestID, _ := ctx.Value("request_id").(string)
	logger := logging.NewLogger("concept-gatherer")

	// 1. Initialize workspace discovery to find the current workspace
	baseLogger := logrus.New()
	baseLogger.SetLevel(logrus.WarnLevel)
	discoveryService := workspace.NewDiscoveryService(baseLogger)
	result, err := discoveryService.DiscoverAll()
	if err != nil {
		return "", fmt.Errorf("failed to discover workspaces for concept gathering: %w", err)
	}
	provider := workspace.NewProvider(result)

	// 2. Get the current workspace node
	wsNode := provider.FindByPath(workDir)
	if wsNode == nil {
		return "", fmt.Errorf("workspace not found for path: %s", workDir)
	}

	// 3. Get the concepts directory using NotebookLocator
	coreCfg, _ := config.LoadDefault()
	locator := workspace.NewNotebookLocator(coreCfg)
	conceptsDir, err := locator.GetNotesDir(wsNode, "concepts")
	if err != nil {
		return "", fmt.Errorf("failed to get concepts directory: %w", err)
	}

	// 4. List all concepts by reading the concepts directory
	concepts, err := listConcepts(conceptsDir)
	if err != nil {
		return "", fmt.Errorf("failed to list concepts: %w", err)
	}

	logger.WithFields(logrus.Fields{
		"job_id":        job.ID,
		"request_id":    requestID,
		"concept_count": len(concepts),
		"workspace":     wsNode.Name,
	}).Info("Gathering concepts")

	// 4. Build XML content and create alias resolver for workspace resolution
	var conceptBuilder strings.Builder
	conceptBuilder.WriteString("<concepts_context>\n")

	// Create an alias resolver to resolve workspace names
	wsResolver := alias.NewAliasResolverWithWorkDir(workDir)
	wsResolver.InitProvider() // Initialize to use the same provider we already have

	for _, concept := range concepts {
		conceptBuilder.WriteString(fmt.Sprintf("  <concept id=\"%s\">\n", concept.ID))

		// Append manifest
		manifestPath := filepath.Join(concept.Path, "concept-manifest.yml")
		manifestContent, err := os.ReadFile(manifestPath)
		if err == nil {
			conceptBuilder.WriteString("    <manifest><![CDATA[\n")
			conceptBuilder.Write(manifestContent)
			conceptBuilder.WriteString("\n    ]]></manifest>\n")
		}

		// Append all .md files, with overview.md first
		mdFiles, _ := filepath.Glob(filepath.Join(concept.Path, "*.md"))
		sort.Slice(mdFiles, func(i, j int) bool {
			if filepath.Base(mdFiles[i]) == "overview.md" {
				return true
			}
			if filepath.Base(mdFiles[j]) == "overview.md" {
				return false
			}
			return mdFiles[i] < mdFiles[j]
		})

		for _, docPath := range mdFiles {
			docContent, err := os.ReadFile(docPath)
			if err == nil {
				conceptBuilder.WriteString(fmt.Sprintf("    <document path=\"%s\"><![CDATA[\n", filepath.Base(docPath)))
				conceptBuilder.Write(docContent)
				conceptBuilder.WriteString("\n    ]]></document>\n")
			}
		}

		// Conditionally append linked notes and plans from manifest
		var manifestData struct {
			RelatedNotes []string `yaml:"related_notes"`
			RelatedPlans []string `yaml:"related_plans"`
		}
		yaml.Unmarshal(manifestContent, &manifestData)

		if job.GatherConceptNotes && len(manifestData.RelatedNotes) > 0 {
			conceptBuilder.WriteString("    <linked_notes>\n")
			for _, noteAlias := range manifestData.RelatedNotes {
				fullNotePath, err := wsResolver.Resolve(noteAlias)
				if err != nil {
					logger.WithError(err).WithFields(logrus.Fields{
						"note_alias": noteAlias,
						"concept_id": concept.ID,
					}).Warn("Could not resolve workspace for note alias")
					continue
				}

				// Read the note content
				noteContent, err := os.ReadFile(fullNotePath)
				if err != nil {
					logger.WithError(err).WithFields(logrus.Fields{
						"note_alias": noteAlias,
						"note_path":  fullNotePath,
						"concept_id": concept.ID,
					}).Warn("Failed to read note file")
					continue
				}

				conceptBuilder.WriteString(fmt.Sprintf("      <note alias=\"%s\"><![CDATA[\n", noteAlias))
				conceptBuilder.Write(noteContent)
				conceptBuilder.WriteString("\n      ]]></note>\n")
			}
			conceptBuilder.WriteString("    </linked_notes>\n")
		}

		if job.GatherConceptPlans && len(manifestData.RelatedPlans) > 0 {
			conceptBuilder.WriteString("    <linked_plans>\n")
			for _, planAlias := range manifestData.RelatedPlans {
				fullPlanPath, err := wsResolver.Resolve(planAlias)
				if err != nil {
					logger.WithError(err).WithFields(logrus.Fields{
						"plan_alias": planAlias,
						"concept_id": concept.ID,
					}).Warn("Could not resolve workspace for plan alias")
					continue
				}

				conceptBuilder.WriteString(fmt.Sprintf("      <plan alias=\"%s\">\n", planAlias))
				filepath.Walk(fullPlanPath, func(path string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
						planContent, err := os.ReadFile(path)
						if err == nil {
							relPath, _ := filepath.Rel(fullPlanPath, path)
							conceptBuilder.WriteString(fmt.Sprintf("        <document path=\"%s\"><![CDATA[\n", relPath))
							conceptBuilder.Write(planContent)
							conceptBuilder.WriteString("\n        ]]></document>\n")
						}
					}
					return nil
				})
				conceptBuilder.WriteString("      </plan>\n")
			}
			conceptBuilder.WriteString("    </linked_plans>\n")
		}

		conceptBuilder.WriteString("  </concept>\n")
	}

	conceptBuilder.WriteString("</concepts_context>\n")

	// 5. Write to artifact file
	artifactsDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create artifacts directory: %w", err)
	}

	conceptContextFile := filepath.Join(artifactsDir, "aggregated-concepts.md")
	if err := os.WriteFile(conceptContextFile, []byte(conceptBuilder.String()), 0644); err != nil {
		return "", fmt.Errorf("failed to write aggregated concepts file: %w", err)
	}

	return conceptContextFile, nil
}

// listConcepts reads the concepts directory and returns a list of concepts
func listConcepts(conceptsDir string) ([]ConceptInfo, error) {
	entries, err := os.ReadDir(conceptsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read concepts directory: %w", err)
	}

	var concepts []ConceptInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		conceptID := entry.Name()
		conceptPath := filepath.Join(conceptsDir, conceptID)
		manifestPath := filepath.Join(conceptPath, "concept-manifest.yml")

		// Read manifest to get title
		manifestData, err := os.ReadFile(manifestPath)
		if err != nil {
			// Skip if no manifest
			continue
		}

		var manifest struct {
			Title string `yaml:"title"`
		}
		if err := yaml.Unmarshal(manifestData, &manifest); err != nil {
			// Skip if invalid manifest
			continue
		}

		concepts = append(concepts, ConceptInfo{
			ID:    conceptID,
			Title: manifest.Title,
			Path:  conceptPath,
		})
	}

	return concepts, nil
}
