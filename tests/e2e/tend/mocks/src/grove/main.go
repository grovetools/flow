package main

import (
	"fmt"
	"os"
	"strings"
)

// Mock grove command to simulate `aglogs read`
func main() {
	if len(os.Args) > 2 && os.Args[1] == "aglogs" && os.Args[2] == "read" {
		// Output a dummy transcript for `flow plan complete` to append.
		fmt.Println("This is a mock transcript from the interactive agent session.")
		fmt.Fprintf(os.Stderr, "[MOCK GROVE] Simulated 'aglogs read' for job: %s\n", os.Args[3])
		os.Exit(0)
	}

	// Log the call for debugging purposes.
	fmt.Fprintf(os.Stderr, "[MOCK GROVE] Unhandled command: %s\n", strings.Join(os.Args[1:], " "))
	os.Exit(1)
}
