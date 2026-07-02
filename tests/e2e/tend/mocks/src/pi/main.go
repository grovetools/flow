package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grovetools/agentlogs/pkg/transcript"
)

// Mock pi command (github.com/earendil-works/pi) to prevent actual agent
// launches during tests. Mirrors the codex mock's conventions:
//
//  1. Logs its invocation for test verification.
//  2. Accepts the CLI surface flow exercises: a positional prompt
//     (interactive first message), `-p`/`--print` (headless print mode with
//     the prompt piped via stdin), `--mode <m>` (e.g. json), and
//     `--model <pattern>` (pass-through; recorded in the session file).
//  3. Writes a realistic v3 session JSONL — header
//     {type:"session",version:3,id,timestamp,cwd} plus tree entries whose
//     assistant messages carry per-message usage incl. cost — into pi's real
//     per-cwd layout: ~/.pi/agent/sessions/--<munged-cwd>--/<ts>_<uuidv7>.jsonl.
//     The directory munging and filename encoding are taken from
//     agentlogs/pkg/transcript (PiSessionsDir), the SAME helper flow's
//     discovery compiles against, so mock layout and discovery glob can never
//     drift apart. The entry shape is shared with
//     agentlogs/pkg/transcript/testdata/pi/ (do not fork it).
//  4. Env-assert hooks mirroring GROVE_MOCK_CODEX_DUMP_ENV:
//     GROVE_MOCK_PI_DUMP_ENV=<path>  — dump the sorted environment to <path>
//     GROVE_MOCK_PI_DUMP_ARGS=<path> — dump raw argv (one arg per line)
//  5. In print mode, sleeps briefly AFTER writing all files (default 3000ms,
//     override GROVE_MOCK_PI_HEADLESS_SLEEP_MS) so flow's detached headless
//     lifecycle is deterministic in e2e runs: the flow CLI always exits while
//     the "agent" is still alive, leaving the job in `running` for the
//     scenario to complete explicitly.
//  6. Exits successfully.
func main() {
	args := os.Args[1:]
	fmt.Fprintf(os.Stderr, "[MOCK PI] Called with args: %s\n", strings.Join(args, " "))

	// Test hooks: env + argv dumps for assertions.
	if dumpPath := os.Getenv("GROVE_MOCK_PI_DUMP_ENV"); dumpPath != "" {
		env := os.Environ()
		sort.Strings(env)
		_ = os.WriteFile(dumpPath, []byte(strings.Join(env, "\n")), 0o600)
	}
	if dumpPath := os.Getenv("GROVE_MOCK_PI_DUMP_ARGS"); dumpPath != "" {
		_ = os.WriteFile(dumpPath, []byte(strings.Join(args, "\n")), 0o600)
	}

	printMode := false
	model := "claude-sonnet-4-5" // pi's per-message model field; overridden by --model
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--print":
			printMode = true
		case "--mode", "--model", "--thinking", "--provider":
			// Value-taking flags flow may pass through. pi's args.ts only
			// consumes the next token when it doesn't start with "-".
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				if args[i] == "--model" {
					model = args[i+1]
				}
				i++
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				// Unknown boolean-ish flag (e.g. --pi-test-arg): ignore.
				continue
			}
			positional = append(positional, args[i])
		}
	}

	prompt := strings.Join(positional, " ")
	if printMode {
		// pi reads all of piped stdin as the initial prompt in print mode
		// (readPipedStdin in the pi source); flow pipes the briefing
		// instruction this way.
		if data, err := io.ReadAll(os.Stdin); err == nil && len(data) > 0 {
			stdinPrompt := strings.TrimSpace(string(data))
			if prompt == "" {
				prompt = stdinPrompt
			} else {
				prompt = prompt + "\n" + stdinPrompt
			}
		}
	}
	if prompt == "" {
		prompt = "Mock pi session"
	}

	if err := writeSessionFile(prompt, model); err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK PI] Warning: could not write session file: %v\n", err)
	}

	if printMode {
		sleepMS := 3000
		if v := os.Getenv("GROVE_MOCK_PI_HEADLESS_SLEEP_MS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				sleepMS = n
			}
		}
		time.Sleep(time.Duration(sleepMS) * time.Millisecond)
	}

	os.Exit(0)
}

// writeSessionFile creates the fake pi session transcript in pi's real
// on-disk layout for the current working directory.
func writeSessionFile(prompt, model string) error {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return err
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// pi resolves the cwd before encoding it into the session dir name; flow
	// canonicalizes agent workdirs the same way (see PiSessionDirName docs).
	if resolved, rerr := filepath.EvalSymlinks(cwd); rerr == nil {
		cwd = resolved
	}

	sessionsDir := transcript.PiSessionsDir(homeDir, cwd)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		return err
	}

	now := time.Now().UTC()
	sessionID := newUUIDv7(now)
	sessionFile := filepath.Join(sessionsDir, piSessionFilename(now, sessionID))

	content, err := renderSessionJSONL(sessionID, cwd, prompt, model, now)
	if err != nil {
		return err
	}
	if err := os.WriteFile(sessionFile, content, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[MOCK PI] Created session file: %s\n", sessionFile)
	return nil
}

// piSessionFilename encodes pi's session file name:
// <ISO timestamp with ":" and "." replaced by "-">_<uuidv7>.jsonl
// (SessionManager newSession in packages/coding-agent/src/core/
// session-manager.ts of the pi source). Example:
// 2026-07-01T10-00-00-000Z_0198c2f4-9a51-7abc-8def-0123456789ab.jsonl
func piSessionFilename(t time.Time, sessionID string) string {
	iso := t.Format("2006-01-02T15:04:05.000Z")
	munged := strings.NewReplacer(":", "-", ".", "-").Replace(iso)
	return munged + "_" + sessionID + ".jsonl"
}

// newUUIDv7 builds a UUIDv7 (unix-ms timestamp prefix + random tail) — the id
// shape pi uses for native session ids. flow extracts the native id as the
// segment after the filename's last "_", so the exact bits only need to be
// UUID-shaped, but we keep v7 semantics to stay faithful.
func newUUIDv7(t time.Time) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(t.UnixMilli()) //nolint:gosec // test mock; timestamps are positive
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms<<16) // top 48 bits carry the ms value
	copy(b[0:6], ts[0:6])
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// renderSessionJSONL emits a v3 session: header line + a linear message tree
// (user -> assistant) whose entry/field shape is shared with the agentlogs pi
// fixtures (pkg/transcript/testdata/pi/). Assistant messages carry usage with
// per-message cost, like real pi transcripts.
func renderSessionJSONL(sessionID, cwd, prompt, model string, now time.Time) ([]byte, error) {
	iso := func(t time.Time) string { return t.Format("2006-01-02T15:04:05.000Z") }

	header := map[string]interface{}{
		"type":      "session",
		"version":   3,
		"id":        sessionID,
		"timestamp": iso(now),
		"cwd":       cwd,
	}

	userEntry := map[string]interface{}{
		"type":      "message",
		"id":        "aa000001",
		"parentId":  nil,
		"timestamp": iso(now.Add(1 * time.Second)),
		"message": map[string]interface{}{
			"role":      "user",
			"content":   prompt,
			"timestamp": now.Add(1 * time.Second).UnixMilli(),
		},
	}

	assistantEntry := map[string]interface{}{
		"type":      "message",
		"id":        "aa000002",
		"parentId":  "aa000001",
		"timestamp": iso(now.Add(2 * time.Second)),
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []map[string]interface{}{
				{"type": "text", "text": "Mock response from pi"},
			},
			"api":      "anthropic-messages",
			"provider": "anthropic",
			"model":    model,
			"usage": map[string]interface{}{
				"input":       1200,
				"output":      150,
				"cacheRead":   1000,
				"cacheWrite":  50,
				"reasoning":   40,
				"totalTokens": 2400,
				"cost": map[string]interface{}{
					"input":      0.0036,
					"output":     0.00225,
					"cacheRead":  0.0003,
					"cacheWrite": 0.0001875,
					"total":      0.0062375,
				},
			},
			"stopReason": "stop",
			"timestamp":  now.Add(2 * time.Second).UnixMilli(),
		},
	}

	var sb strings.Builder
	for _, entry := range []map[string]interface{}{header, userEntry, assistantEntry} {
		line, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return []byte(sb.String()), nil
}
