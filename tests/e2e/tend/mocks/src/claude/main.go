package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Mock claude command to prevent actual agent launches during tests
func main() {
	// Log the call for debugging purposes
	fmt.Fprintf(os.Stderr, "[MOCK CLAUDE] Called with args: %s\n", strings.Join(os.Args[1:], " "))

	// Test hook: when GROVE_MOCK_CLAUDE_DUMP_ENV is set, write the
	// full environment to that path so tests can assert on injected
	// variables (e.g. PLAYBOOK_ROOT).
	if dumpPath := os.Getenv("GROVE_MOCK_CLAUDE_DUMP_ENV"); dumpPath != "" {
		env := os.Environ()
		sort.Strings(env)
		_ = os.WriteFile(dumpPath, []byte(strings.Join(env, "\n")), 0o600)
	}

	// Simulate successful exit
	os.Exit(0)
}
