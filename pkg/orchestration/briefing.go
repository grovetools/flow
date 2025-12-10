package orchestration

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// countLines efficiently counts the number of lines in a file.
func countLines(filePath string) (int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		count++
	}

	if err := scanner.Err(); err != nil {
		return 0, err
	}

	return count, nil
}

// WriteBriefingFile saves the provided content to a uniquely named .xml file
// in the plan's .artifacts directory for auditing.
// For chat jobs, turnID should be the unique turn identifier. For other jobs, pass empty string.
func WriteBriefingFile(plan *Plan, job *Job, content string, turnID string) (string, error) {
	artifactsDir := filepath.Join(plan.Directory, ".artifacts")
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return "", fmt.Errorf("creating .artifacts directory: %w", err)
	}

	// Generate a unique filename for the briefing with an .xml extension
	var briefingFilename string
	if turnID != "" {
		// For chat jobs, use the turn UUID for deterministic naming
		briefingFilename = fmt.Sprintf("briefing-%s-%s.xml", job.ID, turnID)
	} else {
		// For oneshot/interactive jobs, use timestamp
		briefingFilename = fmt.Sprintf("briefing-%s-%d.xml", job.ID, time.Now().Unix())
	}
	briefingFilePath := filepath.Join(artifactsDir, briefingFilename)

	// Write the file
	if err := os.WriteFile(briefingFilePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing briefing file: %w", err)
	}

	return briefingFilePath, nil
}

// BuildXMLPrompt assembles a structured XML prompt for oneshot and interactive_agent jobs.
// It returns the final XML string and a list of file paths that should be uploaded separately.
// contextFiles should include paths to .grove/context, CLAUDE.md, and other project context files.
func BuildXMLPrompt(job *Job, plan *Plan, workDir string, contextFiles []string) (promptXML string, filesToUpload []string, err error) {
	var b strings.Builder
	filesToUpload = []string{}

	b.WriteString("<prompt>\n")

	// 1. Add system instructions from the job's template, if available.
	if job.Template != "" {
		templateManager := NewTemplateManager()
		template, err := templateManager.FindTemplate(job.Template)
		if err != nil {
			return "", nil, fmt.Errorf("resolving template %s: %w", job.Template, err)
		}
		b.WriteString(fmt.Sprintf("    <system_instructions template=\"%s\">\n", job.Template))
		b.WriteString(template.Prompt)
		b.WriteString("\n    </system_instructions>\n")
	}

	b.WriteString("\n    <context>\n")

	// 2. Handle git_changes if enabled for the job.
	if job.GitChanges {
		gitChangesXML, err := GenerateGitChangesXML(workDir)
		if err != nil {
			// Log a warning but don't fail the job. The agent can still proceed.
			fmt.Fprintf(os.Stderr, "Warning: failed to generate git changes for job %s: %v\n", job.ID, err)
		} else if gitChangesXML != "" {
			b.WriteString(gitChangesXML)
		}
	}

	// 3. Handle dependencies: inline or reference.
	// For interactive_agent jobs, use local_dependency tags since files are always read locally.
	// For oneshot jobs, use inlined_dependency tags since files are provided elsewhere in the prompt.
	if len(job.Dependencies) > 0 {
		for _, dep := range job.Dependencies {
			if dep == nil {
				continue
			}
			if job.PrependDependencies {
				// Inline dependency content directly in the XML (prepend mode)
				depContent, err := os.ReadFile(dep.FilePath)
				if err != nil {
					return "", nil, fmt.Errorf("reading dependency file %s: %w", dep.FilePath, err)
				}
				_, depBody, _ := ParseFrontmatter(depContent)
				b.WriteString(fmt.Sprintf("        <prepended_dependency file=\"%s\">\n", dep.Filename))
				b.WriteString(string(depBody))
				b.WriteString("\n        </prepended_dependency>\n")
			} else {
				// Use different tags based on job type
				if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent {
					// Interactive and headless agents read files directly from the local filesystem
					lineCount, err := countLines(dep.FilePath)
					description := "Dependency file available on the local filesystem."
					if err == nil && lineCount > 5000 {
						description = fmt.Sprintf("Large dependency file with %d lines. Use grep/search tools rather than reading directly.", lineCount)
					}

					if err != nil {
						b.WriteString(fmt.Sprintf("        <local_dependency file=\"%s\" path=\"%s\" description=\"%s\"/>\n", dep.Filename, dep.FilePath, description))
					} else {
						b.WriteString(fmt.Sprintf("        <local_dependency file=\"%s\" path=\"%s\" n_lines=\"%d\" description=\"%s\"/>\n", dep.Filename, dep.FilePath, lineCount, description))
					}
				} else {
					// Oneshot jobs: files are inlined elsewhere in the prompt by grove llm, or uploaded by Gemini
					b.WriteString(fmt.Sprintf("        <inlined_dependency file=\"%s\" description=\"This file's content is provided elsewhere in the prompt context.\"/>\n", dep.Filename))
				}
				filesToUpload = append(filesToUpload, dep.FilePath)
			}
		}
	}

	// 4. Handle prompt_source files.
	// For interactive_agent jobs, use local_source_file tags since files are always read locally.
	// For oneshot jobs, use inlined_source_file tags since files are provided elsewhere in the prompt.
	for _, source := range job.PromptSource {
		sourcePath, err := ResolvePromptSource(source, plan)
		if err != nil {
			return "", nil, fmt.Errorf("resolving prompt source %s: %w", source, err)
		}
		// Use different tags based on job type
		if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent {
			// Interactive and headless agents read files directly from the local filesystem
			b.WriteString(fmt.Sprintf("        <local_source_file file=\"%s\" path=\"%s\" description=\"This file was provided as a source for your task.\"/>\n", source, sourcePath))
		} else {
			// Oneshot jobs: files are inlined elsewhere in the prompt by grove llm, or uploaded by Gemini
			b.WriteString(fmt.Sprintf("        <inlined_source_file file=\"%s\" description=\"This file's content is provided elsewhere in the prompt context.\"/>\n", source))
		}
		filesToUpload = append(filesToUpload, sourcePath)
	}

	// 5. Handle source_block content: always inline.
	if job.SourceBlock != "" {
		extractedContent, err := resolveSourceBlock(job.SourceBlock, plan)
		if err != nil {
			return "", nil, fmt.Errorf("resolving source_block: %w", err)
		}
		// Extract file and block IDs for the XML attributes
		parts := strings.SplitN(job.SourceBlock, "#", 2)
		fromFile := parts[0]
		blocks := ""
		if len(parts) > 1 {
			blocks = parts[1]
		}
		b.WriteString(fmt.Sprintf("        <inlined_source_block from_file=\"%s\" blocks=\"%s\">\n", fromFile, blocks))
		b.WriteString(extractedContent)
		b.WriteString("\n        </inlined_source_block>\n")
	}

	// 6. Handle context files (.grove/context, CLAUDE.md, etc.)
	// For interactive_agent and headless_agent jobs, use local_context_file tags since files are read locally.
	// For oneshot jobs, use inlined_context_file tags since files are provided elsewhere in the prompt.
	for _, contextFile := range contextFiles {
		if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent {
			// Interactive and headless agents read files directly from the local filesystem
			lineCount, err := countLines(contextFile)
			description := "Large context file with project information. DO NOT try to read this file directly - it may be very large. Use grep/search tools to find specific content if needed. This file contains information the user thinks you might need."
			if err == nil {
				description = fmt.Sprintf("Large context file with %d lines. DO NOT read this file directly - it is very large. Use grep/search tools to find specific content if needed.", lineCount)
			}

			if err != nil {
				b.WriteString(fmt.Sprintf("        <local_context_file file=\"%s\" path=\"%s\" description=\"%s\"/>\n", filepath.Base(contextFile), contextFile, description))
			} else {
				b.WriteString(fmt.Sprintf("        <local_context_file file=\"%s\" path=\"%s\" n_lines=\"%d\" description=\"%s\"/>\n", filepath.Base(contextFile), contextFile, lineCount, description))
			}
		} else {
			// Oneshot jobs: files are inlined elsewhere in the prompt by grove llm, or uploaded by Gemini
			b.WriteString(fmt.Sprintf("        <inlined_context_file file=\"%s\" description=\"Project context file provided elsewhere in the prompt.\"/>\n", filepath.Base(contextFile)))
		}
		filesToUpload = append(filesToUpload, contextFile)
	}

	b.WriteString("    </context>\n")

	// 6. Add the main task from the job's prompt body.
	if strings.TrimSpace(job.PromptBody) != "" {
		b.WriteString("\n    <user_request priority=\"high\">\n")
		b.WriteString(job.PromptBody)
		b.WriteString("\n    </user_request>\n")
	}

	b.WriteString("</prompt>\n")

	return b.String(), filesToUpload, nil
}

// resolveSourceBlock reads and extracts content from a source_block reference
func resolveSourceBlock(sourceBlock string, plan *Plan) (string, error) {
	parts := strings.SplitN(sourceBlock, "#", 2)
	filePath := parts[0]

	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(plan.Directory, filePath)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("reading source file %s: %w", filePath, err)
	}

	if len(parts) == 1 {
		_, bodyContent, err := ParseFrontmatter(content)
		if err != nil {
			return "", fmt.Errorf("parsing frontmatter: %w", err)
		}
		return string(bodyContent), nil
	}

	blockIDs := strings.Split(parts[1], ",")
	turns, err := ParseChatFile(content)
	if err != nil {
		return "", fmt.Errorf("parsing chat file: %w", err)
	}

	blockMap := make(map[string]*ChatTurn)
	for _, turn := range turns {
		if turn.Directive != nil && turn.Directive.ID != "" {
			blockMap[turn.Directive.ID] = turn
		}
	}

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
