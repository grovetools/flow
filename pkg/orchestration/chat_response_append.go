package orchestration

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/grovetools/core/pkg/process"
)

// chat_response_append.go is the sanctioned writer for the RESPONSE half of an
// agent-authored chat — the mirror of chat_append.go's AppendChatUserTurn.
//
// The oracle path writes its response inline in executeChatJob because it
// already holds the file, the lock, and the turn's identity. An agent-authored
// chat has none of that: the author is a different process (a fresh agent
// session, or a persistent Pi session), possibly on a different machine's view
// of the same filesystem. Letting it splice bytes into the .md by hand would
// put marker grammar, lock discipline, and the status transition in the hands
// of whichever agent wrote the integration that day.
//
// So the response append is one Go function with the same guards as the user
// append — parse before and after, refuse a live writer, atomic whole-file
// write — plus the one thing the user append deliberately does NOT do: the
// status transition. A response is the event that hands the conversation back
// to the human, so it is also the event that stamps `pending_user`.

// buildChatResponseBytes computes the new whole-file bytes for appending an
// agent-authored response to a chat file's content. Pure, for the same reason
// buildChatAppendBytes is: the classifier is the interesting part.
//
// The emitted cell is byte-identical in shape to the oracle path's newCell
// (oneshot_executor.go): a grove marker carrying the turn id, an
// "## LLM Response (<timestamp>)" heading, the response body, and a fresh
// trailing user marker so the chat is immediately ready for the next turn.
// Identical shape is the point — ParseChatFile, the formatter, dependency
// inlining, and every reader downstream must not be able to tell which engine
// produced a turn.
//
// Classification, keyed on the trailing turn:
//
//   - parse error                → returned verbatim (never masked as a refusal)
//   - last turn is a user turn
//     with content               → the normal case: append the response cell
//   - last turn is an LLM turn   → refuse: responding twice in a row would
//     silently drop the second response into a
//     conversation nobody asked a question in
//   - bare trailing marker / no
//     turns                      → refuse: there is no question to answer
//
// `force` overrides the two refusals, for the recovery case where an agent
// crashed after writing its response but before the status landed.
func buildChatResponseBytes(content []byte, response, template, jobPath, turnID string, at time.Time, force bool) ([]byte, error) {
	turns, err := ParseChatFile(content)
	if err != nil {
		return nil, fmt.Errorf("could not parse chat file %s: %w", jobPath, err)
	}

	respondable := false
	if len(turns) > 0 {
		last := turns[len(turns)-1]
		respondable = last.Speaker == "user" && strings.TrimSpace(last.Content) != ""
	}
	if !respondable && !force {
		return nil, fmt.Errorf("refusing to append a response to %s: it does not end with a content-bearing user turn, so there is nothing to respond to. Append the turn first with 'flow plan say', or pass --force if you are recovering an interrupted response", jobPath)
	}

	if template == "" {
		template = mintTemplate(turns, "chat")
	}
	cell := fmt.Sprintf("\n<!-- grove: {\"id\": \"%s\"} -->\n## LLM Response (%s)\n\n%s\n\n<!-- grove: {\"template\": \"%s\"} -->\n",
		turnID, at.Format("2006-01-02 15:04:05"), strings.TrimSpace(response), template)
	return append(append([]byte{}, content...), []byte(cell)...), nil
}

// AppendChatResponseTurn appends an agent-authored response to a chat job body
// and transitions the job to pending_user.
//
// It is the mechanism behind `flow plan respond`, and it is the ONLY sanctioned
// way for a Pi session (Phase 3) or any other out-of-process responder to put a
// turn into a chat .md. Callers that write the file themselves are outside the
// contract and will eventually corrupt marker grammar.
//
// The status transition is deliberately part of the same operation rather than
// a separate call: a response that landed without moving the job to
// pending_user leaves a finished turn looking like an in-flight one, which is
// the state that makes a coordinator wait forever.
func (sp *StatePersister) AppendChatResponseTurn(job *Job, response string, force bool) error {
	if job.Type != JobTypeChat {
		return fmt.Errorf("cannot append a chat response to %s: job is type %q, not chat", chatJobRef(job), job.Type)
	}
	if !job.IsAPIDispatchVetoed() {
		return fmt.Errorf("refusing to append an agent-authored response to %s: it is an oracle chat (responder: %s), whose responses are produced by 'flow plan run'. Only responder: agent and responder: pi-session chats are authored this way",
			chatJobRef(job), defaultResponderLabel(job.Responder))
	}
	response = strings.TrimSpace(response)
	if response == "" {
		return fmt.Errorf("refusing to append an empty response to %s", chatJobRef(job))
	}

	// Liveness guard, mirroring AppendChatUserTurn — and skipped for the same
	// reason on a pi-session chat: there the lock file holds the SESSION HOST's
	// pid for the whole life of the session, so it is alive precisely when the
	// responder is trying to record its answer. Enforcing it would reject every
	// response the feature exists to produce. Mutual exclusion still comes from
	// the persister lock and the atomic write below.
	if !job.IsPiSessionResponded() && !force {
		if pid, err := ReadLockFile(job.FilePath); err == nil && pid > 0 && process.IsProcessAlive(pid) {
			return fmt.Errorf("job %s has a turn in flight (locked by live pid %d) — wait for it to finish before appending a response", chatJobRef(job), pid)
		}
	}

	sp.mu.Lock()
	defer sp.mu.Unlock()

	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock on %s: %w", chatJobRef(job), err)
	}
	defer func() { _ = lock.Unlock() }()

	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading chat file %s: %w", job.FilePath, err)
	}
	preTurns, _ := ParseChatFile(content)

	turnID := sha256Hex([]byte(fmt.Sprintf("%s:%d:%d", job.ID, len(preTurns), time.Now().UnixNano())))[:6]
	newContent, err := buildChatResponseBytes(content, response, job.Template, job.FilePath, turnID, time.Now(), force)
	if err != nil {
		return err
	}

	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("writing chat file %s: %w", job.FilePath, err)
	}

	// Post-write tripwire, mirroring verifyAppendedTurn: re-read from disk and
	// assert the shape. A response cell adds exactly two turns (the LLM turn and
	// the fresh trailing user marker) and must leave the file parseable.
	after, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("re-reading chat file %s after response append: %w", job.FilePath, err)
	}
	postTurns, err := ParseChatFile(after)
	if err != nil {
		return fmt.Errorf("internal invariant: chat file %s no longer parses after response append: %w", job.FilePath, err)
	}
	if err := verifyAppendedResponse(preTurns, postTurns); err != nil {
		return fmt.Errorf("internal invariant while appending a response to %s: %w", job.FilePath, err)
	}
	return nil
}

// verifyAppendedResponse is the post-write assertion for a response cell: the
// turn count grows by exactly two (the response, then the fresh trailing user
// marker), the response turn is an LLM turn, and the file ends on an empty user
// marker ready for the next question.
func verifyAppendedResponse(pre, post []*ChatTurn) error {
	if len(post) != len(pre)+2 {
		return fmt.Errorf("response append changed turn count by %d, want +2", len(post)-len(pre))
	}
	if post[len(post)-2].Speaker != "llm" {
		return fmt.Errorf("response append did not produce an llm turn (got speaker=%q)", post[len(post)-2].Speaker)
	}
	last := post[len(post)-1]
	if last.Speaker != "user" || strings.TrimSpace(last.Content) != "" {
		return fmt.Errorf("response append did not leave a clean trailing user marker (speaker=%q, content=%d bytes)", last.Speaker, len(strings.TrimSpace(last.Content)))
	}
	return nil
}

// defaultResponderLabel renders an absent responder as its effective value, so
// an error about an oracle chat says "oracle" rather than "".
func defaultResponderLabel(responder string) string {
	if responder == "" {
		return ResponderOracle
	}
	return responder
}
