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

func GetRegisterCodexSessionCmd() *cobra.Command {
	return &cobra.Command{
	Use:    "register-session-codex [job-id] [plan-name] [pane] [workdir] [job-title] [job-filepath]",
	Short:  "Internal: Register a Codex session",
	Hidden: true,
	Args:   cobra.ExactArgs(6),
	RunE: func(cmd *cobra.Command, args []string) error {
		jobID := args[0]
		planName := args[1]
		targetPane := args[2]
		workDir := args[3]
		jobTitle := args[4]
		jobFilePath := args[5]

		// Find the most recent Codex log file
		codexSessionsDir := filepath.Join(os.Getenv("HOME"), ".codex", "sessions")
		latestFile, err := findMostRecentCodexLog(codexSessionsDir)
		if err != nil {
			return fmt.Errorf("failed to find codex log: %w", err)
		}

		// Parse session ID from filename
		base := filepath.Base(latestFile)
		parts := strings.Split(strings.TrimSuffix(base, ".jsonl"), "-")
		if len(parts) < 6 {
			return fmt.Errorf("invalid codex log filename format")
		}
		codexSessionID := strings.Join(parts[len(parts)-5:], "-")

		// Find the Codex PID
		pid, err := orchestration.FindCodexPIDForPane(targetPane)
		if err != nil {
			return fmt.Errorf("failed to find codex PID: %w", err)
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
			ClaudeSessionID:  codexSessionID,
			Provider:         "codex",
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

		fmt.Printf("✅ Registered Codex session %s (PID %d)\n", codexSessionID, pid)
		return nil
	},
	}
}

func findMostRecentCodexLog(dir string) (string, error) {
	var latestFile string
	var latestModTime time.Time

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".jsonl") {
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
		return "", fmt.Errorf("no jsonl files found in %s", dir)
	}
	return latestFile, nil
}
