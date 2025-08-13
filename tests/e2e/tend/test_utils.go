package main

import (
	"fmt"
	"os"

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