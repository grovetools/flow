package main

import (
	"fmt"
	"os"
	"strings"
)

// Mock claude command to prevent actual agent launches during tests
func main() {
	// Log the call for debugging purposes
	fmt.Fprintf(os.Stderr, "[MOCK CLAUDE] Called with args: %s\n", strings.Join(os.Args[1:], " "))

	// Simulate successful exit
	os.Exit(0)
}
