package orchestration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GenerateGitChangesXML creates an XML block summarizing the git status of a worktree.
func GenerateGitChangesXML(workDir string) (string, error) {
	var xmlBuilder strings.Builder
	hasChanges := false

	// Helper to run a git command and append its output to the XML builder.
	runAndAppend := func(section, description string, args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			// Some diff commands might fail if e.g. 'main' doesn't exist. Don't treat as fatal.
			return nil
		}
		trimmedOutput := strings.TrimSpace(string(output))
		if trimmedOutput != "" {
			hasChanges = true
			xmlBuilder.WriteString(fmt.Sprintf("    <git_change type=\"%s\" description=\"%s\">\n", section, description))
			xmlBuilder.WriteString("      <![CDATA[\n")
			xmlBuilder.WriteString(trimmedOutput)
			xmlBuilder.WriteString("\n      ]]>\n")
			xmlBuilder.WriteString(fmt.Sprintf("    </git_change>\n"))
		}
		return nil
	}

	// 1. Committed changes (diff from main)
	if err := runAndAppend("committed", "Changes committed to the current branch but not in main", "diff", "main...HEAD"); err != nil {
		return "", err
	}

	// 2. Staged changes
	if err := runAndAppend("staged", "Changes staged for the next commit", "diff", "--cached"); err != nil {
		return "", err
	}

	// 3. Uncommitted changes
	if err := runAndAppend("uncommitted", "Changes in the working directory not yet staged", "diff"); err != nil {
		return "", err
	}

	// 4. Untracked files
	cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		hasChanges = true
		untrackedFiles := strings.Split(strings.TrimSpace(string(output)), "\n")
		xmlBuilder.WriteString("    <git_change type=\"untracked_files\" description=\"New files not yet tracked by git\">\n")
		for _, file := range untrackedFiles {
			if file == "" {
				continue
			}
			filePath := filepath.Join(workDir, file)
			content, err := os.ReadFile(filePath)
			if err == nil {
				xmlBuilder.WriteString(fmt.Sprintf("      <file path=\"%s\">\n", file))
				xmlBuilder.WriteString("        <![CDATA[\n")
				xmlBuilder.WriteString(string(content))
				xmlBuilder.WriteString("\n        ]]>\n")
				xmlBuilder.WriteString("      </file>\n")
			}
		}
		xmlBuilder.WriteString("    </git_change>\n")
	}

	if !hasChanges {
		return "", nil // No changes to report
	}

	var finalXML bytes.Buffer
	finalXML.WriteString("  <git_changes>\n")
	finalXML.WriteString(xmlBuilder.String())
	finalXML.WriteString("  </git_changes>\n")

	return finalXML.String(), nil
}
