package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// StampTrailingChatDirective merges updates into the JSON payload of the LAST
// grove marker in a chat job file and writes the file back. It is how the
// `flow plan run --append-delta / --rebase-context` flags and the chat-reopen
// auto-refresh reach the layer engine: the verb is stamped into the pending
// user turn's marker, so the semantics are identical whether the turn is then
// executed locally or by the daemon, and the verb is naturally consumed by
// that one turn (the response appends a fresh, clean trailing marker).
//
// Returns false (with no error) when the file has no grove marker to stamp —
// a fresh chat's first turn, where both verbs are no-ops anyway (there is no
// layer store yet).
func StampTrailingChatDirective(jobPath string, updates map[string]interface{}) (bool, error) {
	if len(updates) == 0 {
		return false, nil
	}
	content, err := os.ReadFile(jobPath)
	if err != nil {
		return false, fmt.Errorf("reading chat file: %w", err)
	}

	locs := groveDirectiveRegex.FindAllSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return false, nil
	}
	last := locs[len(locs)-1]
	jsonStart, jsonEnd := last[2], last[3]

	var payload map[string]interface{}
	if err := json.Unmarshal(content[jsonStart:jsonEnd], &payload); err != nil {
		return false, fmt.Errorf("parsing trailing grove marker in %s: %w", jobPath, err)
	}
	for k, v := range updates {
		payload[k] = v
	}
	newJSON, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	var buf bytes.Buffer
	buf.Grow(len(content) + len(newJSON))
	buf.Write(content[:jsonStart])
	buf.Write(newJSON)
	buf.Write(content[jsonEnd:])
	if err := os.WriteFile(jobPath, buf.Bytes(), 0o600); err != nil {
		return false, fmt.Errorf("writing chat file: %w", err)
	}
	return true, nil
}
