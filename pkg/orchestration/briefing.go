package orchestration

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/memory/pkg/memory"
	notifications "github.com/grovetools/notify"
	notifyconfig "github.com/grovetools/notify/pkg/config"
	"github.com/grovetools/skills/pkg/skills"
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
// in the plan's .artifacts/<job.ID> directory for auditing.
// For chat jobs, turnID should be the unique turn identifier. For other jobs, pass empty string.
func WriteBriefingFile(plan *Plan, job *Job, content, turnID string) (string, error) {
	jobArtifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	if err := os.MkdirAll(jobArtifactDir, 0o755); err != nil {
		return "", fmt.Errorf("creating job artifact directory: %w", err)
	}

	// Generate a unique filename for the briefing with an .xml extension
	var briefingFilename string
	if turnID != "" {
		// For chat jobs, use the turn UUID for deterministic naming
		briefingFilename = fmt.Sprintf("briefing-%s.xml", turnID)
	} else {
		// For oneshot/interactive jobs, use timestamp
		briefingFilename = fmt.Sprintf("briefing-%d.xml", time.Now().Unix())
	}
	briefingFilePath := filepath.Join(jobArtifactDir, briefingFilename)

	// Write the file
	if err := os.WriteFile(briefingFilePath, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("writing briefing file: %w", err)
	}

	return briefingFilePath, nil
}

// BuildXMLPrompt assembles a structured XML prompt for oneshot and interactive_agent jobs.
// It returns the final XML string and a list of file paths that should be uploaded separately.
// contextFiles should include paths to .grove/context, CLAUDE.md, and other project context files.
func BuildXMLPrompt(job *Job, plan *Plan, workDir string, contextFiles []string, memories []memory.SearchResult) (promptXML string, filesToUpload []string, err error) {
	var b strings.Builder
	filesToUpload = []string{}

	b.WriteString("<prompt>\n")

	// 0. If this job belongs to a playbook-scoped plan, render a
	// <playbook_overview> block summarizing the available skills,
	// prompts, and recipes the agent can use. This is the "JIT
	// inventory" so the agent doesn't have to grep the filesystem.
	if overview := renderPlaybookOverview(job, plan); overview != "" {
		b.WriteString(overview)
	}

	// 1. Add system instructions from the job's template or skill, if available.
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

	// 1b. Add skill instructions (inlined, same as template).
	if job.Skill != "" {
		skillContent, err := ResolveJobSkillContent(job, workDir)
		if err != nil {
			return "", nil, fmt.Errorf("resolving skill for job %s: %w", job.ID, err)
		}
		if skillContent != "" {
			b.WriteString(fmt.Sprintf("    <system_instructions skill=\"%s\">\n", job.Skill))
			b.WriteString(skillContent)
			b.WriteString("\n    </system_instructions>\n")
		}
	}

	// 1c. Add skill sequence instructions.
	// If a parent skill is set, treat the sequence as implicitly authorized (depth 1).
	if len(job.SkillSequence) > 0 {
		var sequenceNodes []SkillSequenceNode
		var err error
		if job.Skill != "" {
			sequenceNodes, err = ResolveSkillSequenceWithParent(job.SkillSequence, workDir, job.Skill)
		} else {
			sequenceNodes, err = ResolveSkillSequenceMetadata(job.SkillSequence, workDir)
		}
		if err != nil {
			return "", nil, fmt.Errorf("resolving skill sequence: %w", err)
		}

		if len(sequenceNodes) > 0 {
			// Jobs always have artifact dirs; use the plan's artifact directory
			artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
			sequenceXML, err := GenerateSkillSequenceXML(sequenceNodes, artifactDir, false, workDir)
			if err != nil {
				return "", nil, fmt.Errorf("generating skill sequence XML: %w", err)
			}
			b.WriteString("\n    ")
			b.WriteString(strings.ReplaceAll(sequenceXML, "\n", "\n    "))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n    <context>\n")

	// 2. Handle git_changes if enabled for the job.
	if job.GitChanges {
		gitChangesXML, err := GenerateGitChangesXML(workDir)
		if err != nil {
			// Log a warning but don't fail the job. The agent can still proceed.
			ctx := context.Background()
			ulog.Warn("Failed to generate git changes for job").
				Field("job_id", job.ID).
				Err(err).
				Log(ctx)
		} else if gitChangesXML != "" {
			b.WriteString(gitChangesXML)
		}
	}

	// 3. Handle dependencies: inline or reference.
	// Uses ShouldInline to support both new inline field and legacy prepend_dependencies.
	// For interactive_agent jobs, use local_dependency tags since files are always read locally.
	// For oneshot jobs, use inlined_dependency tags since files are provided elsewhere in the prompt.
	if len(job.Dependencies) > 0 {
		for _, dep := range job.Dependencies {
			if dep == nil {
				continue
			}
			if job.ShouldInline(InlineDependencies) {
				// Inline dependency content directly in the XML
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
					// Oneshot jobs: files are uploaded as separate attachments
					b.WriteString(fmt.Sprintf("        <uploaded_context_file file=\"%s\" type=\"dependency\" importance=\"high\" description=\"Context from upstream jobs in this LLM pipeline.\"/>\n", dep.Filename))
				}
				filesToUpload = append(filesToUpload, dep.FilePath)
			}
		}
	}

	// 4. Handle include files.
	// For interactive_agent jobs, use local_include_file tags since files are always read locally.
	// For oneshot jobs, files are uploaded as separate attachments.
	for _, source := range job.Include {
		sourcePath, err := ResolvePromptSource(source, plan)
		if err != nil {
			return "", nil, fmt.Errorf("resolving include file %s: %w", source, err)
		}
		// Use different tags based on job type
		if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent {
			// Interactive and headless agents read files directly from the local filesystem
			b.WriteString(fmt.Sprintf("        <local_include_file file=\"%s\" path=\"%s\" description=\"This file was explicitly included for your task.\"/>\n", source, sourcePath))
		} else {
			// Oneshot jobs: files are uploaded as separate attachments
			b.WriteString(fmt.Sprintf("        <uploaded_context_file file=\"%s\" type=\"include\" importance=\"high\" description=\"File explicitly included for this task.\"/>\n", source))
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

	// 6. Inject related memories from hybrid semantic search.
	if len(memories) > 0 {
		b.WriteString("\n        <related_memories>\n")
		for _, mem := range memories {
			b.WriteString(fmt.Sprintf("            <memory path=\"%s\">\n%s\n            </memory>\n", mem.Path, mem.Content))
		}
		b.WriteString("        </related_memories>\n")
	}

	// 7. Handle context files (.grove/context, CLAUDE.md, etc.)
	// For interactive_agent and headless_agent jobs, use local_context_file tags since files are read locally.
	// For oneshot jobs, files are uploaded as separate attachments.
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
			// Oneshot jobs: files are uploaded as separate attachments
			b.WriteString(fmt.Sprintf("        <uploaded_context_file file=\"%s\" type=\"repository\" importance=\"medium\" description=\"Concatenated project/source code files from the current repository.\"/>\n", filepath.Base(contextFile)))
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

	// 7. Add channel instructions if the job has channels enabled.
	if len(job.Channels) > 0 {
		notifyCfg := notifyconfig.Load()
		channelInstructions := notifications.AgentInstructions(notifyCfg, job.Channels)
		if channelInstructions != "" {
			b.WriteString("\n    <channel_instructions>\n")
			b.WriteString(channelInstructions)
			b.WriteString("\n    </channel_instructions>\n")
		}
	}

	// 8. Add autonomous instructions if configured.
	if job.Autonomous != nil && job.Autonomous.Enabled {
		b.WriteString("\n    <autonomous_mode>\n")
		b.WriteString("You are running in autonomous mode. When you have been idle and receive an idle ping,\n")
		b.WriteString("check for new work, report your status, or continue any in-progress tasks.\n")
		if job.Autonomous.Prompt != "" {
			b.WriteString(fmt.Sprintf("Custom idle instruction: %s\n", job.Autonomous.Prompt))
		}
		b.WriteString("    </autonomous_mode>\n")
	}

	b.WriteString("</prompt>\n")

	return b.String(), filesToUpload, nil
}

// GenerateSkillSequenceXML produces the <skill_sequence> and optional <skill_content>
// XML blocks for a resolved sequence of skills.
//
// If artifactDir is non-empty, the output includes `flow artifact` CLI protocols for
// reading/writing artifacts and marking completion. If empty, a plain TODO-list style
// protocol is emitted instead.
//
// If includeSkillContent is true, each leaf skill's SKILL.md body is appended inside
// a <skill_content> block so the consuming agent doesn't need secondary lookups.
func GenerateSkillSequenceXML(nodes []SkillSequenceNode, artifactDir string, includeSkillContent bool, workDir string) (string, error) {
	useArtifacts := artifactDir != ""

	var b strings.Builder
	b.WriteString("<skill_sequence>\n")
	b.WriteString("    You have a sequence of skills to work through in order.\n")
	b.WriteString("    Before starting, create a TODO list with these exact items:\n\n")

	renderSequenceNodes(&b, nodes, "    ", false, useArtifacts)

	if useArtifacts {
		b.WriteString("\n    Work through the list in order. For each pair: first invoke the skill using the Skill tool, then follow its instructions to completion. Mark each item done as you go.\n")
	} else {
		b.WriteString("\n    Work through the list in order. For each skill: invoke it using the Skill tool, follow its instructions to completion, then mark the item done on your TODO list.\n")
	}
	b.WriteString(fmt.Sprintf("\n    Start by invoking Skill(\"%s\") now.\n", nodes[0].Metadata.Name))
	b.WriteString("</skill_sequence>\n")

	if useArtifacts {
		b.WriteString("\n<feedback_protocol>\n")
		b.WriteString("    When completing each skill, provide brief feedback via the --feedback flag:\n")
		b.WriteString("    flow artifact complete <skill> --status completed --feedback \"your feedback\"\n\n")
		b.WriteString("    Your feedback helps improve the system. Examples of useful feedback:\n\n")
		b.WriteString("    Prompt improvements:\n")
		b.WriteString("    - \"The cx alias format examples were unclear, had to guess @a: syntax\"\n")
		b.WriteString("    - \"Step 3 says run make test but should be make build first\"\n")
		b.WriteString("    - \"Missing context about which directory to run commands from\"\n\n")
		b.WriteString("    Tool/system issues:\n")
		b.WriteString("    - \"flow artifact write silently overwrites without warning\"\n")
		b.WriteString("    - \"cx stats --json returned non-JSON when no rules file exists\"\n")
		b.WriteString("    - \"tend run hangs if the binary wasnt built first\"\n\n")
		b.WriteString("    Process observations:\n")
		b.WriteString("    - \"This skill would work better split into two separate skills\"\n")
		b.WriteString("    - \"The artifact from the previous skill was missing info I needed\"\n")
		b.WriteString("    - \"This step took 5 minutes but could be automated\"\n")
		b.WriteString("</feedback_protocol>\n")
	}

	if includeSkillContent {
		skillContentXML, err := generateSkillContentXML(nodes, workDir)
		if err != nil {
			return "", fmt.Errorf("generating skill content: %w", err)
		}
		if skillContentXML != "" {
			b.WriteString("\n")
			b.WriteString(skillContentXML)
		}
	}

	return b.String(), nil
}

// generateSkillContentXML recursively collects SKILL.md bodies and wraps them in XML.
func generateSkillContentXML(nodes []SkillSequenceNode, workDir string) (string, error) {
	var b strings.Builder
	b.WriteString("<skill_content>\n")

	hasContent := false
	if err := collectSkillContent(&b, nodes, workDir, &hasContent); err != nil {
		return "", err
	}

	if !hasContent {
		return "", nil
	}

	b.WriteString("</skill_content>\n")
	return b.String(), nil
}

func collectSkillContent(b *strings.Builder, nodes []SkillSequenceNode, workDir string, hasContent *bool) error {
	for _, node := range nodes {
		if len(node.Children) > 0 {
			if err := collectSkillContent(b, node.Children, workDir, hasContent); err != nil {
				return err
			}
			continue
		}

		// Load the skill content for leaf nodes
		loadedSkill, err := skills.LoadSkillBypassingAccess(workDir, node.Metadata.Name)
		if err != nil {
			// Non-fatal: skip skills we can't load
			continue
		}
		content, ok := loadedSkill.Files["SKILL.md"]
		if !ok {
			continue
		}
		body := stripSkillFrontmatter(content)
		if body == "" {
			continue
		}

		b.WriteString(fmt.Sprintf("    <system_instructions skill=\"%s\">\n", node.Metadata.Name))
		b.WriteString(body)
		b.WriteString("\n    </system_instructions>\n")
		*hasContent = true
	}
	return nil
}

// renderSequenceNodes recursively renders a skill sequence tree as a TODO list.
// When useArtifacts is true, it includes `flow artifact` CLI protocols.
// When false, it uses plain TODO-list completion instructions.
func renderSequenceNodes(b *strings.Builder, nodes []SkillSequenceNode, indent string, isSubstep, useArtifacts bool) {
	var previousArtifacts []string

	for _, node := range nodes {
		if len(node.Children) > 0 {
			// Parent skill with sub-sequence
			b.WriteString(fmt.Sprintf("%s- Invoke Skill(%s)\n", indent, node.Metadata.Name))
			renderSequenceNodes(b, node.Children, indent+"  ", true, useArtifacts)
			if useArtifacts {
				b.WriteString(fmt.Sprintf("%s- Mark complete: `flow artifact complete %s --status completed`\n", indent, node.Metadata.Name))
			} else {
				b.WriteString(fmt.Sprintf("%s- Mark complete: %s done\n", indent, node.Metadata.Name))
			}
		} else {
			// Leaf skill: render invoke + execute pair
			b.WriteString(fmt.Sprintf("%s- Invoke Skill(%s)\n", indent, node.Metadata.Name))

			var actions []string

			if useArtifacts {
				// Downstream skills read previous artifacts using the CLI
				if len(previousArtifacts) > 0 {
					actions = append(actions, fmt.Sprintf("read prior context using `flow artifact read <file>` for: %s", strings.Join(previousArtifacts, ", ")))
				}

				if len(node.Metadata.Produces) > 0 {
					actions = append(actions, fmt.Sprintf("write output using `flow artifact write <filename>` for: %s", strings.Join(node.Metadata.Produces, ", ")))
				}
			}

			// Build the execute line
			if len(actions) > 0 {
				b.WriteString(fmt.Sprintf("%s- Execute %s — %s\n", indent, node.Metadata.Name, strings.Join(actions, " AND ")))
			} else {
				b.WriteString(fmt.Sprintf("%s- Execute %s — %s\n", indent, node.Metadata.Name, node.Metadata.Description))
			}

			// Completion protocol
			if useArtifacts {
				b.WriteString(fmt.Sprintf("%s  When finished successfully: `flow artifact complete %s --status completed`\n", indent, node.Metadata.Name))
				b.WriteString(fmt.Sprintf("%s  If you get stuck or fail: write a diagnostic via `flow artifact write %s-diag.md`, then run `flow artifact complete %s --status failed --error \"<reason>\" --diagnostic-file %s-diag.md`\n",
					indent, node.Metadata.Name, node.Metadata.Name, node.Metadata.Name))
			} else {
				b.WriteString(fmt.Sprintf("%s  When finished successfully: mark %s as done on your TODO list\n", indent, node.Metadata.Name))
				b.WriteString(fmt.Sprintf("%s  If you get stuck or fail: note the failure reason and proceed to the next skill\n", indent))
			}

			// Accumulate current artifacts for the next skill's "read" action
			previousArtifacts = append(previousArtifacts, node.Metadata.Produces...)
		}
	}
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
