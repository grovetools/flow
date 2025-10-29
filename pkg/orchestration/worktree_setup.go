package orchestration

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-core/fs"
)

// CopyProjectFilesToWorktree is a setup handler for workspace.Prepare that copies
// key project-level configuration files (like grove.yml and .cx) into a new worktree.
// This is necessary for files that are typically gitignored but are required for
// the Grove tooling to function correctly within the isolated worktree.
func CopyProjectFilesToWorktree(worktreePath, gitRoot string) error {
	filesToCopy := []string{
		"grove.yml",
		".grove.yml",
		"grove.yaml",
		".grove.yaml",
	}
	dirsToCopy := []string{
		".cx",
	}

	fmt.Println("› Copying project configuration to new worktree...")

	// Copy files
	for _, file := range filesToCopy {
		srcPath := filepath.Join(gitRoot, file)
		destPath := filepath.Join(worktreePath, file)

		if _, err := os.Stat(srcPath); err == nil {
			if err := fs.CopyFile(srcPath, destPath); err != nil {
				// Log a warning but don't fail the entire operation
				fmt.Printf("  ⚠️  Warning: failed to copy %s: %v\n", file, err)
			} else {
				fmt.Printf("  ✓ Copied %s\n", file)
			}
		}
	}

	// Copy directories
	for _, dir := range dirsToCopy {
		srcPath := filepath.Join(gitRoot, dir)
		destPath := filepath.Join(worktreePath, dir)

		if _, err := os.Stat(srcPath); err == nil {
			if err := fs.CopyDir(srcPath, destPath); err != nil {
				fmt.Printf("  ⚠️  Warning: failed to copy directory %s: %v\n", dir, err)
			} else {
				fmt.Printf("  ✓ Copied directory %s/\n", dir)
			}
		}
	}

	return nil
}
