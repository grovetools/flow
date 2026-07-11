package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/plan"
)

// buildPlanBundle reads a local plan directory into a PlanBundle for satellite
// dispatch (M2 C11). It ships the top-level job `.md` files, `.grove-plan.yml`,
// and everything under `rules/`, keyed by plan-dir-relative slash paths. It
// EXCLUDES `.artifacts/` (large chat context; C11) and `.grove-lease.yml` (the
// Record plane carries the lease; shipping it is pointless).
//
// Workspace and PlanName are derived from the centralized notebook layout
// (<root>/workspaces/<workspace>/plans/<plan>), symmetric with how the
// satellite's NotebookLocator renders the replica plans dir.
func buildPlanBundle(planDir string) (*models.PlanBundle, error) {
	absPlanDir, err := filepath.Abs(planDir)
	if err != nil {
		return nil, err
	}
	planName := filepath.Base(absPlanDir)
	workspaceName := filepath.Base(filepath.Dir(filepath.Dir(absPlanDir)))

	files := map[string][]byte{}
	walkErr := filepath.WalkDir(absPlanDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != absPlanDir && d.Name() == ".artifacts" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(absPlanDir, path)
		if err != nil {
			return err
		}
		if !bundleIncludes(rel) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = content
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("reading plan dir %q: %w", planDir, walkErr)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("plan dir %q has no bundlable files", planDir)
	}

	return &models.PlanBundle{
		Workspace: workspaceName,
		PlanName:  planName,
		Files:     files,
	}, nil
}

// bundleIncludes reports whether a plan-dir-relative path belongs in the bundle:
// top-level *.md job files, .grove-plan.yml, and rules/**. Never the lease.
func bundleIncludes(rel string) bool {
	if rel == plan.LeaseFileName {
		return false
	}
	if rel == ".grove-plan.yml" {
		return true
	}
	if strings.HasPrefix(rel, "rules"+string(os.PathSeparator)) {
		return true
	}
	// Top-level markdown job files only (no separator in the relative path).
	if !strings.ContainsRune(rel, os.PathSeparator) && strings.HasSuffix(rel, ".md") {
		return true
	}
	return false
}
