package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Mock grove command to simulate `aglogs read` and `llm request`
func main() {
	if len(os.Args) > 2 && os.Args[1] == "aglogs" && os.Args[2] == "read" {
		// Output a dummy transcript for `flow plan complete` to append.
		fmt.Println("This is a mock transcript from the interactive agent session.")
		fmt.Fprintf(os.Stderr, "[MOCK GROVE] Simulated 'aglogs read' for job: %s\n", os.Args[3])
		os.Exit(0)
	}

	if len(os.Args) > 2 && os.Args[1] == "llm" && os.Args[2] == "request" {
		// Simulate LLM request - read from stdin (prompt) and return mock response

		// Read stdin to see what prompt we received
		stdinContent, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MOCK GROVE] Error reading stdin: %v\n", err)
		} else {
			// Log prompt length and preview for debugging
			fmt.Fprintf(os.Stderr, "[MOCK GROVE] Received prompt: %d bytes\n", len(stdinContent))
			if len(stdinContent) > 0 {
				preview := string(stdinContent)
				if len(preview) > 500 {
					preview = preview[:500] + "..."
				}
				fmt.Fprintf(os.Stderr, "[MOCK GROVE] Prompt preview:\n%s\n", preview)
			}
		}

		// Check for mock response file environment variable
		mockResponseFile := os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE")
		if mockResponseFile != "" {
			content, err := os.ReadFile(mockResponseFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[MOCK GROVE] Error reading mock response file: %v\n", err)
				os.Exit(1)
			}
			fmt.Print(string(content))
			fmt.Fprintf(os.Stderr, "[MOCK GROVE] Simulated 'llm request' using mock file: %s\n", mockResponseFile)
		} else {
			// Default mock response
			fmt.Println("This is a mock LLM response.")
			fmt.Fprintf(os.Stderr, "[MOCK GROVE] Simulated 'llm request' with default response\n")
		}

		os.Exit(0)
	}

	// Log the call for debugging purposes.
	fmt.Fprintf(os.Stderr, "[MOCK GROVE] Unhandled command: %s\n", strings.Join(os.Args[1:], " "))
	os.Exit(1)
}
