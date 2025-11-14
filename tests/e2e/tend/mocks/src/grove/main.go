package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Not enough arguments: %v\n", os.Args)
		os.Exit(1)
	}
	command := os.Args[1] + " " + os.Args[2]
	wd, _ := os.Getwd()

	switch command {
	case "dev list":
		// Simplified mock, doesn't distinguish worktrees correctly but is sufficient
		fmt.Println("Binary: flow")
		fmt.Println("  main (/test/repo)")
		fmt.Printf("* finish-test (%s)\n", filepath.Join(wd, ".grove-worktrees", "finish-test"))
	case "dev unlink":
		fmt.Printf("Removed version '%s' of '%s'\n", os.Args[4], os.Args[3])
	case "dev use":
		fmt.Printf("Switched '%s' to version '%s'\n", os.Args[3], os.Args[4])
	case "llm request":
		// Forward to mock-llm binary for LLM requests
		mockDir := filepath.Dir(os.Args[0])
		llmBinary := filepath.Join(mockDir, "mock-llm")
		// Pass all remaining args after "grove llm request"
		cmd := exec.Command(llmBinary, os.Args[3:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Unknown command: %s\n", command)
		os.Exit(1)
	}
}