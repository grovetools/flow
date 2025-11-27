package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BuildBriefingFile consolidates all job context into a single markdown file.
// It returns the path to the generated file.
func BuildBriefingFile(job *Job, plan *Plan, workDir string) (string, error) {
	artifactsDir := filepath.Join(plan.Directory, ".artifacts")
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return "", fmt.Errorf("creating .artifacts directory: %w", err)
	}

	// Generate a unique filename for the briefing
	briefingFilename := fmt.Sprintf("briefing-%s-%d.md", job.ID, time.Now().Unix())
	briefingFilePath := filepath.Join(artifactsDir, briefingFilename)

	var b strings.Builder

	b.WriteString(fmt.Sprintf("# Agent Briefing: %s\n\n", job.Title))
	b.WriteString(fmt.Sprintf("**Job ID:** %s  \n", job.ID))
	b.WriteString(fmt.Sprintf("**Work Directory:** `%s`\n\n", workDir))
	b.WriteString("---\n\n")

	// 1. Handle dependencies based on PrependDependencies flag
	if len(job.Dependencies) > 0 {
		if job.PrependDependencies {
			// Inline dependency content into the briefing
			b.WriteString("## Context from Dependencies\n\n")
			for _, dep := range job.Dependencies {
				if dep == nil {
					continue
				}
				depContent, err := os.ReadFile(dep.FilePath)
				if err != nil {
					return "", fmt.Errorf("reading dependency file %s: %w", dep.FilePath, err)
				}
				_, depBody, _ := ParseFrontmatter(depContent)
				b.WriteString(fmt.Sprintf("### From: `%s`\n\n", dep.Filename))
				b.Write(depBody)
				b.WriteString("\n\n")
			}
		} else {
			// Just provide file paths for the agent to read
			b.WriteString("## Dependency Files\n\n")
			b.WriteString("Read the following dependency files for context:\n\n")
			for _, dep := range job.Dependencies {
				if dep == nil {
					continue
				}
				b.WriteString(fmt.Sprintf("- `%s`\n", dep.FilePath))
			}
			b.WriteString("\n")
		}
	}

	// 2. Append source_block content
	if job.SourceBlock != "" {
		extractedContent, err := resolveSourceBlock(job.SourceBlock, plan)
		if err != nil {
			return "", fmt.Errorf("resolving source_block: %w", err)
		}
		b.WriteString("## Context from Source Blocks\n\n")
		b.WriteString(extractedContent)
		b.WriteString("\n\n")
	}

	// 3. Append source file content
	if len(job.PromptSource) > 0 {
		b.WriteString("## Context from Source Files\n\n")
		for _, source := range job.PromptSource {
			sourcePath, err := ResolvePromptSource(source, plan)
			if err != nil {
				return "", fmt.Errorf("resolving prompt source %s: %w", source, err)
			}
			sourceContent, err := os.ReadFile(sourcePath)
			if err != nil {
				return "", fmt.Errorf("reading prompt source file %s: %w", sourcePath, err)
			}
			b.WriteString(fmt.Sprintf("### From: `%s`\n\n", source))
			b.Write(sourceContent)
			b.WriteString("\n\n")
		}
	}

	// 4. Append the primary task
	b.WriteString("---\n\n## Primary Task\n\n")
	b.WriteString(job.PromptBody)

	// Write the file
	if err := os.WriteFile(briefingFilePath, []byte(b.String()), 0644); err != nil {
		return "", fmt.Errorf("writing briefing file: %w", err)
	}

	return briefingFilePath, nil
}

// resolveSourceBlock reads and extracts content from a source_block reference
// Format: path/to/file.md#block-id1,block-id2 or path/to/file.md (for entire file)
func resolveSourceBlock(sourceBlock string, plan *Plan) (string, error) {
	// Parse the source block reference
	parts := strings.SplitN(sourceBlock, "#", 2)
	filePath := parts[0]

	// Resolve file path relative to plan directory
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(plan.Directory, filePath)
	}

	// Read the source file
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("reading source file %s: %w", filePath, err)
	}

	// If no block IDs specified, return entire file content (without frontmatter)
	if len(parts) == 1 {
		_, bodyContent, err := ParseFrontmatter(content)
		if err != nil {
			return "", fmt.Errorf("parsing frontmatter: %w", err)
		}
		return string(bodyContent), nil
	}

	// Extract specific blocks
	blockIDs := strings.Split(parts[1], ",")

	// Parse the chat file to find blocks
	turns, err := ParseChatFile(content)
	if err != nil {
		return "", fmt.Errorf("parsing chat file: %w", err)
	}

	// Create a map of block IDs to content
	blockMap := make(map[string]*ChatTurn)
	for _, turn := range turns {
		if turn.Directive != nil && turn.Directive.ID != "" {
			blockMap[turn.Directive.ID] = turn
		}
	}

	// Extract requested blocks
	var extractedContent strings.Builder
	foundCount := 0
	for _, blockID := range blockIDs {
		if turn, ok := blockMap[blockID]; ok {
			if foundCount > 0 {
				extractedContent.WriteString("\n\n---\n\n")
			}
			extractedContent.WriteString(turn.Content)
			foundCount++
		} else {
			return "", fmt.Errorf("block ID '%s' not found in source file", blockID)
		}
	}

	if foundCount == 0 {
		return "", fmt.Errorf("no valid blocks found")
	}

	return extractedContent.String(), nil
}
