package main

import (
	"fmt"
	"os"
	"strings"
)

// Mock git command that logs all commands to stderr and succeeds
func main() {
	// Log the full command to stderr for verification in tests
	fmt.Fprintf(os.Stderr, "[MOCK GIT] %s\n", strings.Join(os.Args[1:], " "))

	if len(os.Args) < 2 {
		os.Exit(0)
	}

	command := os.Args[1]

	switch command {
	case "worktree":
		if len(os.Args) > 2 && os.Args[2] == "add" {
			// Simulate worktree add
			os.Exit(0)
		} else if len(os.Args) > 2 && os.Args[2] == "remove" {
			// Simulate worktree remove
			os.Exit(0)
		}
	case "branch":
		if len(os.Args) > 2 && os.Args[2] == "-D" {
			// Simulate branch deletion
			os.Exit(0)
		}
		// Default branch command
		os.Exit(0)
	case "init":
		// Initialize git repo
		os.Exit(0)
	case "add":
		// Stage files
		os.Exit(0)
	case "commit":
		// Create commit
		os.Exit(0)
	case "rev-parse":
		// Return dummy values for rev-parse
		if len(os.Args) > 2 && os.Args[2] == "--show-toplevel" {
			fmt.Println(os.Getenv("PWD"))
		} else if len(os.Args) > 2 && os.Args[2] == "--abbrev-ref" {
			fmt.Println("main")
		} else if len(os.Args) > 2 && os.Args[2] == "HEAD" {
			fmt.Println("abc123")
		}
		os.Exit(0)
	case "status":
		// Return clean status
		os.Exit(0)
	case "config":
		// Handle config commands
		os.Exit(0)
	default:
		// For any other git command, just succeed
		os.Exit(0)
	}
}
