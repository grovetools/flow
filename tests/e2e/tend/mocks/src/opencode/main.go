package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Mock opencode command to simulate the opencode agent during tests.
// This mock:
// 1. Logs its invocation for test verification
// 2. Creates a fake session file in the sandboxed ~/.config/opencode/sessions/
// 3. Exits successfully
func main() {
	args := os.Args[1:]

	// Log the call for debugging purposes
	fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Called with args: %s\n", strings.Join(args, " "))

	// Handle the 'run' subcommand which is what grove-flow uses
	if len(args) > 0 && args[0] == "run" {
		// Create a fake session file in the sandboxed home directory
		// The HOME env var is set by grove-tend to the sandboxed home
		homeDir := os.Getenv("HOME")
		if homeDir == "" {
			var err error
			homeDir, err = os.UserHomeDir()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Warning: Could not get home directory: %v\n", err)
				os.Exit(0)
			}
		}

		// Create the sessions directory structure
		opencodeSessionsDir := filepath.Join(homeDir, ".config", "opencode", "sessions")
		if err := os.MkdirAll(opencodeSessionsDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Warning: Could not create sessions directory: %v\n", err)
			os.Exit(0)
		}

		// Create a fake session file with a unique timestamp-based name
		sessionID := fmt.Sprintf("mock-session-%d", time.Now().UnixNano())
		sessionFile := filepath.Join(opencodeSessionsDir, sessionID+".jsonl")

		// Write some mock session content
		mockContent := fmt.Sprintf(`{"type":"init","timestamp":"%s","session_id":"%s"}
{"type":"message","role":"user","content":"Mock opencode session"}
{"type":"message","role":"assistant","content":"Mock response from opencode"}
`, time.Now().Format(time.RFC3339), sessionID)

		if err := os.WriteFile(sessionFile, []byte(mockContent), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Warning: Could not write session file: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Created session file: %s\n", sessionFile)
		}

		// Log the prompt/briefing file if provided
		for i, arg := range args {
			if strings.Contains(arg, "briefing") || strings.Contains(arg, ".xml") {
				fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Briefing file reference found in arg %d: %s\n", i, arg)
			}
		}
	}

	// Simulate successful exit
	os.Exit(0)
}
