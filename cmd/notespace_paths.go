package cmd

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/notespace"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/util/pathutil"
)

// notespaceAtNotesPath resolves a notes-plane path without interpreting path
// positions as identity. Centralized paths are accepted only beneath a
// configured notebook root; the immutable identity comes from the root stamp.
func notespaceAtNotesPath(path string, cfg *config.Config) (string, string, string, error) {
	roots := configuredNotebookRoots(cfg)
	// ParseNotespaceRoot recognizes local <repo>/.notebook layouts before it
	// consults notebookRoot. Give it one configured root when possible.
	if len(roots) == 0 {
		roots = []string{""}
	}
	for _, notebookRoot := range roots {
		centralMatch := notebookRoot != "" && pathWithin(filepath.Join(notebookRoot, workspace.NotespaceDirectory), path)
		localMatch := hasNotebookAncestor(path)
		if !centralMatch && !localMatch {
			continue
		}
		_, root, _, err := workspace.ParseNotespaceRoot(notebookRoot, path)
		if err != nil {
			return "", "", "", err
		}
		stamp, err := notespace.LoadNotespace(root)
		if err != nil {
			return "", "", "", err
		}
		if stamp == nil {
			return "", "", "", fmt.Errorf("notespace root %s has no identity stamp", root)
		}
		return stamp.ID, stamp.Name, root, nil
	}
	return "", "", "", fmt.Errorf("path %s is not inside a configured notespace root", path)
}

func configuredNotebookRoots(cfg *config.Config) []string {
	set := map[string]bool{}
	if cfg != nil {
		for _, grove := range cfg.Groves {
			if grove.NotebookRoot != "" {
				set[grove.NotebookRoot] = true
			}
		}
		if cfg.Notebooks != nil {
			for _, notebook := range cfg.Notebooks.Definitions {
				if notebook != nil && notebook.RootDir != "" {
					set[notebook.RootDir] = true
				}
			}
		}
	}
	roots := make([]string, 0, len(set))
	for root := range set {
		expanded, err := pathutil.Expand(root)
		if err == nil {
			roots = append(roots, expanded)
		}
	}
	sort.Strings(roots)
	return roots
}

func pathWithin(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && rel != ".." && rel != "." && !startsWithParent(rel)
}

func startsWithParent(rel string) bool {
	return len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator)
}

func hasNotebookAncestor(path string) bool {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if filepath.Base(p) == ".notebook" {
			return true
		}
		if next := filepath.Dir(p); next == p {
			return false
		}
	}
}
