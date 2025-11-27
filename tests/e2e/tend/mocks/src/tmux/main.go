package main

import (
	"fmt"
	"os"
	"strings"
)

// Mock tmux command to prevent real tmux sessions during tests
func main() {
	args := os.Args[1:]

	// Handle common tmux commands
	if len(args) > 0 {
		switch args[0] {
		case "new-session":
			// Simulate successful session creation
			fmt.Fprintf(os.Stderr, "[MOCK TMUX] Created session\n")
			os.Exit(0)
		case "new-window":
			// Simulate successful window creation
			fmt.Fprintf(os.Stderr, "[MOCK TMUX] Created window\n")
			os.Exit(0)
		case "send-keys":
			// Simulate successful key send
			fmt.Fprintf(os.Stderr, "[MOCK TMUX] Sent keys\n")
			os.Exit(0)
		case "display-message":
			// Return a mock PID for pane_pid queries
			if len(args) > 2 && strings.Contains(strings.Join(args, " "), "pane_pid") {
				fmt.Println("99999")
				os.Exit(0)
			}
			// Return a mock session name
			if len(args) > 2 && strings.Contains(strings.Join(args, " "), "#S") {
				fmt.Println("mock-session")
				os.Exit(0)
			}
			os.Exit(0)
		case "list-sessions":
			// Return empty list (no sessions)
			os.Exit(0)
		case "has-session":
			// Pretend session doesn't exist
			os.Exit(1)
		case "select-window":
			// Simulate successful window selection
			fmt.Fprintf(os.Stderr, "[MOCK TMUX] Selected window\n")
			os.Exit(0)
		}
	}

	// Log unhandled commands for debugging
	fmt.Fprintf(os.Stderr, "[MOCK TMUX] Unhandled command: %s\n", strings.Join(args, " "))
	os.Exit(0)
}
