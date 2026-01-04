package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/pkg/sessions"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

func GetRegisterOpencodeSessionCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "register-session-opencode [job-id] [plan-name] [pane] [workdir] [job-title] [job-filepath]",
		Short:  "Internal: Register an opencode session",
		Hidden: true,
		Args:   cobra.ExactArgs(6),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID := args[0]
			planName := args[1]
			targetPane := args[2]
			workDir := args[3]
			jobTitle := args[4]
			jobFilePath := args[5]

			// Find the most recent opencode log file
			homeDir, _ := os.UserHomeDir()
			opencodeSessionsDir := filepath.Join(homeDir, ".config", "opencode", "sessions")
			latestFile, err := findMostRecentOpencodeLog(opencodeSessionsDir)
			if err != nil {
				return fmt.Errorf("failed to find opencode log: %w", err)
			}

			// Parse session ID from filename
			opencodeSessionID := strings.TrimSuffix(filepath.Base(latestFile), filepath.Ext(latestFile))

			// Find the opencode PID
			pid, err := orchestration.FindOpencodePIDForPane(targetPane)
			if err != nil {
				return fmt.Errorf("failed to find opencode PID: %w", err)
			}

			// Register the session
			registry, err := sessions.NewFileSystemRegistry()
			if err != nil {
				return fmt.Errorf("failed to create registry: %w", err)
			}

			user := os.Getenv("USER")
			if user == "" {
				user = "unknown"
			}

			metadata := sessions.SessionMetadata{
				SessionID:        jobID,
				ClaudeSessionID:  opencodeSessionID,
				Provider:         "opencode",
				PID:              pid,
				WorkingDirectory: workDir,
				User:             user,
				StartedAt:        time.Now(),
				JobTitle:         jobTitle,
				PlanName:         planName,
				JobFilePath:      jobFilePath,
				TranscriptPath:   latestFile,
			}

			if err := registry.Register(metadata); err != nil {
				return fmt.Errorf("failed to register session: %w", err)
			}

			fmt.Printf("✅ Registered opencode session %s (PID %d)\n", opencodeSessionID, pid)
			return nil
		},
	}
}

func findMostRecentOpencodeLog(dir string) (string, error) {
	var latestFile string
	var latestModTime time.Time

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if info.ModTime().After(latestModTime) {
				latestModTime = info.ModTime()
				latestFile = path
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}
	if latestFile == "" {
		return "", fmt.Errorf("no files found in %s", dir)
	}
	return latestFile, nil
}
