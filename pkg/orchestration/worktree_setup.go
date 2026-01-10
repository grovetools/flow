package orchestration

import (
	"context"
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-core/fs"
	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-skills/pkg/skills"
)

var worktreeUlog = grovelogging.NewUnifiedLogger("grove-flow.worktree")

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
		".cx.work",
	}

	ctx := context.Background()
	worktreeUlog.Progress("Copying project configuration to new worktree").
		Pretty("› Copying project configuration to new worktree...").
		Log(ctx)

	// Copy files
	for _, file := range filesToCopy {
		srcPath := filepath.Join(gitRoot, file)
		destPath := filepath.Join(worktreePath, file)

		if _, err := os.Stat(srcPath); err == nil {
			if err := fs.CopyFile(srcPath, destPath); err != nil {
				// Log a warning but don't fail the entire operation
				worktreeUlog.Warn("Failed to copy file").
					Field("file", file).
					Err(err).
					Pretty("  " + theme.IconWarning + "  Warning: failed to copy " + file + ": " + err.Error()).
					Log(ctx)
			} else {
				worktreeUlog.Success("Copied file").
					Field("file", file).
					Pretty("  " + theme.IconSuccess + " Copied " + file).
					Log(ctx)
			}
		}
	}

	// Copy directories
	for _, dir := range dirsToCopy {
		srcPath := filepath.Join(gitRoot, dir)
		destPath := filepath.Join(worktreePath, dir)

		if _, err := os.Stat(srcPath); err == nil {
			if err := fs.CopyDir(srcPath, destPath); err != nil {
				worktreeUlog.Warn("Failed to copy directory").
					Field("directory", dir).
					Err(err).
					Pretty("  " + theme.IconWarning + "  Warning: failed to copy directory " + dir + ": " + err.Error()).
					Log(ctx)
			} else {
				worktreeUlog.Success("Copied directory").
					Field("directory", dir).
					Pretty("  " + theme.IconSuccess + " Copied directory " + dir + "/").
					Log(ctx)
			}
		}
	}

	return nil
}

// SyncSkillsToWorktree copies skills from all sources to a new worktree.
// Skills are collected from multiple sources with the following precedence (higher wins):
//   1. User skills from ~/.config/grove/skills
//   2. Ecosystem skills from the notebook (if project is part of an ecosystem)
//   3. Project skills from the notebook
//
// This delegates to grove-skills for the actual discovery and sync logic.
func SyncSkillsToWorktree(worktreePath string, node *workspace.WorkspaceNode) error {
	ctx := context.Background()

	// Create a minimal service for skill operations
	svc, err := skills.NewServiceForNode(node)
	if err != nil {
		worktreeUlog.Debug("Could not create skill service").
			Err(err).
			Log(ctx)
		return nil // Non-fatal: skills sync is optional
	}

	// Get the destination directory for skills
	destDir := skills.GetSkillsDirectoryForWorktree(worktreePath, "claude")

	worktreeUlog.Progress("Syncing skills to worktree").
		Pretty("› Syncing skills to worktree...").
		Log(ctx)

	// Use grove-skills to sync all discoverable skills
	syncedCount, err := skills.SyncSkillsToDirectory(svc, node, destDir)
	if err != nil {
		worktreeUlog.Warn("Some skills failed to sync").
			Err(err).
			Pretty("  " + theme.IconWarning + " Warning: some skills failed to sync: " + err.Error()).
			Log(ctx)
	}

	if syncedCount > 0 {
		worktreeUlog.Success("Skills sync complete").
			Field("count", syncedCount).
			Pretty("  " + theme.IconSuccess + " Synced " + string(rune('0'+syncedCount)) + " skills").
			Log(ctx)
	} else {
		worktreeUlog.Debug("No skills found to sync").Log(ctx)
	}

	return nil
}
