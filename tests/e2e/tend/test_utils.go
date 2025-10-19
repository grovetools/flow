package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/project"
)

// FindProjectBinary finds the project's main binary path by reading grove.yml.
// This provides a single source of truth for locating the binary under test.
func FindProjectBinary() (string, error) {
	// The test runner is executed from the project root, so we start the search here.
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get working directory: %w", err)
	}

	binaryPath, err := project.GetBinaryPath(wd)
	if err != nil {
		return "", fmt.Errorf("failed to find project binary via grove.yml: %w", err)
	}

	return binaryPath, nil
}

// CreateMockLockFile creates a .lock file for a given job file path with the current PID.
// It simulates a job being in a "running" state for grove-hooks to discover.
// It returns a cleanup function that should be deferred by the caller.
func CreateMockLockFile(ctx *harness.Context, jobFilePath string) (cleanupFunc func(), err error) {
	pid := os.Getpid()
	lockFilePath := jobFilePath + ".lock"

	if err := os.WriteFile(lockFilePath, []byte(fmt.Sprintf("%d", pid)), 0644); err != nil {
		return nil, fmt.Errorf("failed to write mock lock file at %s: %w", lockFilePath, err)
	}

	cleanupFunc = func() {
		os.Remove(lockFilePath)
	}

	return cleanupFunc, nil
}

// FindJobFileByTitle searches a plan directory for a job markdown file with a specific title.
// This is more robust than relying on filename conventions.
func FindJobFileByTitle(planDir, title string) (string, error) {
	files, err := filepath.Glob(filepath.Join(planDir, "*.md"))
	if err != nil {
		return "", err
	}

	// Define search patterns for the title in YAML frontmatter.
	searchPattern1 := fmt.Sprintf("title: %s", title)
	searchPattern2 := fmt.Sprintf("title: \"%s\"", title) // With quotes

	for _, file := range files {
		// Skip spec and README files
		if strings.HasSuffix(file, "spec.md") || strings.HasSuffix(file, "README.md") {
			continue
		}

		content, err := os.ReadFile(file)
		if err != nil {
			continue // Skip files that can't be read
		}
		contentStr := string(content)

		if strings.Contains(contentStr, searchPattern1) || strings.Contains(contentStr, searchPattern2) {
			return file, nil
		}
	}

	return "", fmt.Errorf("job file with title '%s' not found in directory %s", title, planDir)
}