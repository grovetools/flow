package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Mock codex command to prevent actual agent launches during tests.
// This mock:
//  1. Logs its invocation for test verification
//  2. Creates a fake rollout session log in the sandboxed
//     ~/.codex/sessions/YYYY/MM/DD/ — the DATE-NESTED layout the real codex
//     CLI writes (codex-rs/rollout/src/recorder.rs) and the only layout
//     flow's discovery matches (agentlogs transcript.CodexSessionsGlob:
//     ~/.codex/sessions/*/*/*/*.jsonl). A flat file under ~/.codex/sessions/
//     would be invisible to discovery — that was the P2 glob bug this mock
//     now guards against (see the codex-nested-session-discovery scenario).
//  3. Exits successfully
func main() {
	// Log the call for debugging purposes
	fmt.Fprintf(os.Stderr, "[MOCK CODEX] Called with args: %s\n", strings.Join(os.Args[1:], " "))

	// Test hook: when GROVE_MOCK_CODEX_DUMP_ENV is set, write the
	// full environment to that path so tests can assert on injected
	// variables (e.g. PLAYBOOK_ROOT).
	if dumpPath := os.Getenv("GROVE_MOCK_CODEX_DUMP_ENV"); dumpPath != "" {
		env := os.Environ()
		sort.Strings(env)
		_ = os.WriteFile(dumpPath, []byte(strings.Join(env, "\n")), 0o600)
	}

	// Create a fake session log like the real codex CLI would. The flow
	// codex provider discovers sessions via the shared
	// ~/.codex/sessions/YYYY/MM/DD/*.jsonl glob, expecting filenames whose
	// last five dash-separated segments form a UUID.
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	if homeDir != "" {
		now := time.Now()
		sessionsDir := filepath.Join(homeDir, ".codex", "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
		if err := os.MkdirAll(sessionsDir, 0o755); err == nil {
			// Pseudo-UUID with the standard 8-4-4-4-12 shape (the last
			// segment is masked to 48 bits so it stays exactly 12 hex chars —
			// flow parses the native id as the trailing UUID of the filename).
			uuid := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
				now.UnixNano()&0xffffffff, 0x4d0c, 0x4b1d, 0x8e2f, now.UnixNano()&0xffffffffffff)
			sessionFile := filepath.Join(sessionsDir,
				fmt.Sprintf("rollout-%s-%s.jsonl", now.Format("2006-01-02T15-04-05"), uuid))
			mockContent := fmt.Sprintf(`{"type":"session_meta","timestamp":"%s","id":"%s"}
{"type":"message","role":"user","content":"Mock codex session"}
{"type":"message","role":"assistant","content":"Mock response from codex"}
`, now.Format(time.RFC3339), uuid)
			if err := os.WriteFile(sessionFile, []byte(mockContent), 0o600); err == nil {
				fmt.Fprintf(os.Stderr, "[MOCK CODEX] Created session file: %s\n", sessionFile)
			}
		}
	}

	// Simulate successful exit
	os.Exit(0)
}
