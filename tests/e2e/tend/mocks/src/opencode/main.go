package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Mock opencode command to simulate the opencode agent during tests.
//
// Refreshed for P4/P6: the previous version wrote a fake session file into
// ~/.config/opencode/sessions/, a layout modern opencode does NOT use (it
// stores fragmented message/part records under
// ~/.local/share/opencode/storage/ keyed by session id, and grove learns the
// native session id via the grove-opencode plugin v2 shelling out to
// `grove hooks session-start/...`). Writing the stale layout gave scenarios a
// false sense of coverage, so it is gone.
//
// What this mock does now:
//  1. Logs its invocation for test verification (both the interactive
//     `opencode --prompt "..."` shape and the headless `opencode run ...`
//     shape flow builds).
//  2. Env-assert hook mirroring GROVE_MOCK_CODEX_DUMP_ENV:
//     GROVE_MOCK_OPENCODE_DUMP_ENV=<path> dumps the sorted environment so
//     scenarios can assert injected vars (e.g. GROVE_AGENT_PROVIDER=opencode).
//  3. Touches a minimal marker under the MODERN storage root
//     (~/.local/share/opencode/storage/session/) so scenarios can verify the
//     mock ran, without pretending to reproduce opencode's fragmented
//     message/part store.
//  4. Exits successfully.
//
// Deliberately NOT covered (deferred, see P6 report): the plugin path itself.
// Faithful e2e coverage of session enrichment would need the mock to host the
// grove-opencode plugin runtime (JS) and invoke the real `grove hooks` binary
// — both out of scope for a hermetic flow suite whose `grove` is a mock.
func main() {
	args := os.Args[1:]

	// Log the call for debugging purposes
	fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Called with args: %s\n", strings.Join(args, " "))

	// Test hook: dump environment for assertions.
	if dumpPath := os.Getenv("GROVE_MOCK_OPENCODE_DUMP_ENV"); dumpPath != "" {
		env := os.Environ()
		sort.Strings(env)
		_ = os.WriteFile(dumpPath, []byte(strings.Join(env, "\n")), 0o600)
	}

	// Recognize the two invocation shapes flow builds (see the opencode
	// entry in flow's agent provider registry):
	//   interactive: opencode <args> --prompt "<instruction>"
	//   headless:    opencode run <args> <prompt>
	mode := "interactive"
	if len(args) > 0 && args[0] == "run" {
		mode = "headless"
	}
	for i, arg := range args {
		if arg == "--prompt" && i+1 < len(args) {
			fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Prompt: %s\n", args[i+1])
		}
		if strings.Contains(arg, "briefing") || strings.Contains(arg, ".xml") {
			fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Briefing file reference found in arg %d: %s\n", i, arg)
		}
	}
	fmt.Fprintf(os.Stderr, "[MOCK OPENCODE] Mode: %s\n", mode)

	// Touch a marker in the modern storage root so tests can assert the mock
	// executed. XDG_DATA_HOME is set by the tend sandbox.
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		if home := os.Getenv("HOME"); home != "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
	}
	if dataHome != "" {
		storageSessionDir := filepath.Join(dataHome, "opencode", "storage", "session")
		if err := os.MkdirAll(storageSessionDir, 0o755); err == nil {
			marker := filepath.Join(storageSessionDir, fmt.Sprintf("mock-invocation-%d", time.Now().UnixNano()))
			_ = os.WriteFile(marker, []byte(mode+"\n"), 0o600)
		}
	}

	// Simulate successful exit
	os.Exit(0)
}
