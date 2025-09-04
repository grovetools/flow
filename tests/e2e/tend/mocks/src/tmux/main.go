package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "mock tmux: missing command\n")
		os.Exit(1)
	}

	command := os.Args[1]
	switch command {
	case "has-session":
		if len(os.Args) < 4 {
			os.Exit(1) // No session specified
		}
		sessionName := os.Args[3]
		stateFile := filepath.Join(os.TempDir(), "tmux_session_"+sanitize(sessionName))
		if _, err := os.Stat(stateFile); err == nil {
			os.Exit(0) // Session exists
		}
		os.Exit(1) // Session does not exist

	case "new-session":
		var sessionName string
		for i, arg := range os.Args {
			if arg == "-s" && i+1 < len(os.Args) {
				sessionName = os.Args[i+1]
				break
			}
		}
		if sessionName != "" {
			stateFile := filepath.Join(os.TempDir(), "tmux_session_"+sanitize(sessionName))
			os.WriteFile(stateFile, []byte("active"), 0644)
			fmt.Printf("Created session %s\n", sessionName)
		}

	case "kill-session":
		if len(os.Args) < 4 {
			os.Exit(1)
		}
		sessionName := os.Args[3]
		stateFile := filepath.Join(os.TempDir(), "tmux_session_"+sanitize(sessionName))
		os.Remove(stateFile)
		fmt.Printf("Killed session %s\n", sessionName)

	default:
		// Do nothing for other commands like send-keys, select-window
	}
}

func sanitize(name string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(name)
}