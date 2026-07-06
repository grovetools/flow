package orchestration

import (
	"fmt"
	"os"
	"strings"

	"github.com/grovetools/core/pkg/process"
)

// chat_append.go implements the safe programmatic chat-turn append (oracle-plays
// J2 — `flow plan say`). It is the in-package, in-persister guard the mech
// contract (chat 33, A4/§3-am) requires: the daemon path bypasses runPlanRun's
// pre-submit InspectTrailingChatTurn check, so validation that must always hold
// lives here, not in the CLI. Two pieces:
//
//   - buildChatAppendBytes: a pure function that classifies the trailing state
//     directly from the ParseChatFile turn list (NOT InspectTrailingChatTurn —
//     its OrphanBeforeMarker flag encodes fire-time semantics and is always
//     true for the mint case, per J2 addendum R1) and computes the full new
//     file bytes to write.
//   - (*StatePersister).AppendChatUserTurn: the sanctioned writer, modeled on
//     AppendJobOutput/updateJobSection in state.go — read-only liveness guard,
//     running-status guard, lock + atomic write, and a post-write turn-count
//     tripwire (addendum R3).

// buildChatAppendBytes computes the new whole-file bytes for appending a user
// turn `text` to a chat file's `content`. It is pure (no I/O) so the classifier
// can be unit-tested directly.
//
// Classification is driven ENTIRELY by the ParseChatFile turn list, keyed on the
// last turn (addendum R1/R2):
//
//   - parse error                          → returned verbatim, wrapped with
//     jobPath, BEFORE any trailing-state logic (InspectTrailingChatTurn would
//     otherwise mask a parse error as a spurious pending-turn refusal).
//   - no turns                             → case (c): mint a fresh marker so the
//     appended text becomes a clean, dispatchable user turn.
//   - last Speaker=="llm"                  → case (b): mint
//     `\n\n<!-- grove: {"template":"<T>"} -->\n` + text. Never orphan-refuse.
//   - last Speaker=="user", empty content  → bare trailing marker (or empty
//     body). If turns[len-2] is a content-bearing user turn → orphan refusal
//     (text stranded above the marker). Otherwise case (a): append `\n` + text
//     after the marker.
//   - last Speaker=="user", non-empty      → a user turn is already pending:
//     refuse unless `force`, in which case extend it with `\n` + text.
//
// The minted-marker template (case b/c) follows the executor's newCell
// precedence (addendum R4): most recent user turn's Directive.Template →
// `fallbackTemplate` (the job's frontmatter template) → "chat".
//
// It returns the new content, `minted` (true when a marker was added, i.e. the
// post-write turn count grows by one — cases b/c), and any refusal error.
func buildChatAppendBytes(content []byte, text, fallbackTemplate, jobPath string, force bool) (newContent []byte, minted bool, err error) {
	turns, perr := ParseChatFile(content)
	if perr != nil {
		return nil, false, fmt.Errorf("could not parse chat file %s: %w", jobPath, perr)
	}

	mint := func() ([]byte, bool, error) {
		tmpl := mintTemplate(turns, fallbackTemplate)
		suffix := fmt.Sprintf("\n\n<!-- grove: {\"template\": \"%s\"} -->\n%s", tmpl, text)
		return append(append([]byte{}, content...), []byte(suffix)...), true, nil
	}
	extend := func() ([]byte, bool, error) {
		suffix := "\n" + text
		return append(append([]byte{}, content...), []byte(suffix)...), false, nil
	}

	// No parsed turns at all (e.g. a body of only empty LLM markers): mint a
	// fresh marker so the text lands as an unambiguous user turn.
	if len(turns) == 0 {
		return mint()
	}

	last := turns[len(turns)-1]
	switch last.Speaker {
	case "llm":
		// Case (b): body ends with an LLM response and no trailing user marker.
		return mint()
	case "user":
		if strings.TrimSpace(last.Content) == "" {
			// Bare trailing marker (or empty body). Refuse only when a
			// content-bearing user turn is stranded immediately above it — the
			// classic "typed the question, pasted a fresh marker below it"
			// orphan. Otherwise append after the marker (case a / empty body).
			if len(turns) >= 2 {
				prev := turns[len(turns)-2]
				if prev.Speaker == "user" && strings.TrimSpace(prev.Content) != "" {
					return nil, false, NoUserTurnError(jobPath)
				}
			}
			return extend()
		}
		// Case: a content-bearing user turn is already pending.
		if !force {
			return nil, false, fmt.Errorf("a user turn is already pending in %s — run it with 'flow plan run', or pass --force to extend it", jobPath)
		}
		return extend()
	default:
		return nil, false, fmt.Errorf("unexpected trailing turn speaker %q in %s", last.Speaker, jobPath)
	}
}

// mintTemplate resolves the template for a freshly minted trailing marker,
// mirroring executeChatJob's newCell precedence: the most recent user turn's
// directive template, then the fallback (the job's frontmatter template), then
// the "chat" default. Only user turns carry a template in ParseChatFile's model,
// so LLM directives are skipped by the Speaker filter.
func mintTemplate(turns []*ChatTurn, fallback string) string {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.Speaker == "user" && t.Directive != nil && t.Directive.Template != "" {
			return t.Directive.Template
		}
	}
	if fallback != "" {
		return fallback
	}
	return "chat"
}

// AppendChatUserTurn is the sanctioned writer for programmatically appending a
// user turn to a chat job body (mech contract §3-am). It composes the read-only
// liveness guard, the running-status guard, an exclusive lock, an atomic
// whole-file write, and a post-write tripwire. It never mutates job status —
// runPlanRun owns reopen/dispatch and the append_delta stamp for completed
// chats (interaction table, §3 of the J2 design).
func (sp *StatePersister) AppendChatUserTurn(job *Job, text string, force bool) error {
	if job.Type != JobTypeChat {
		return fmt.Errorf("cannot append a chat turn to %s: job is type %q, not chat", chatJobRef(job), job.Type)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("refusing to append an empty turn to %s", chatJobRef(job))
	}

	// Refuse loudly when the job is executing: a live turn may append an LLM
	// response concurrently. Status guard first (cheap), then the lock file.
	if job.Status == JobStatusRunning {
		return fmt.Errorf("job %s is running — refusing to append a turn while it executes", chatJobRef(job))
	}
	// Read-only liveness probe. Never CreateLockFile here: writing the lock is
	// the executor's ownership assertion (§3-am). A dead PID / absent lock is
	// fine to proceed over; sp.lockFile below will steal or create as needed.
	if pid, err := ReadLockFile(job.FilePath); err == nil && pid > 0 && process.IsProcessAlive(pid) {
		return fmt.Errorf("job %s has a turn in flight (locked by live pid %d) — wait for it to finish before appending", chatJobRef(job), pid)
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

	// Turn-count baseline for the post-write tripwire (R3). Errors here are
	// surfaced authoritatively by buildChatAppendBytes below, so ignore.
	preTurns, _ := ParseChatFile(content)

	newContent, minted, err := buildChatAppendBytes(content, text, job.Template, job.FilePath, force)
	if err != nil {
		return err
	}

	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("writing chat file %s: %w", job.FilePath, err)
	}

	// Post-write tripwire (R3): re-read from disk and assert the append produced
	// exactly the intended shape. The write is already durable — a failure here
	// is a should-never-fire internal invariant, not a recoverable condition.
	after, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("re-reading chat file %s after append: %w", job.FilePath, err)
	}
	postTurns, err := ParseChatFile(after)
	if err != nil {
		return fmt.Errorf("internal invariant: chat file %s no longer parses after append: %w", job.FilePath, err)
	}
	if err := verifyAppendedTurn(preTurns, postTurns, minted, text); err != nil {
		return fmt.Errorf("internal invariant while appending to %s: %w", job.FilePath, err)
	}
	return nil
}

// verifyAppendedTurn is the R3 post-write assertion. A minted turn (marker
// added, cases b/c) must grow the turn count by exactly one and end in a
// content-bearing user turn; an extended turn (case a / empty body / --force)
// must keep the count and end in a user turn whose content carries the appended
// text as a suffix.
func verifyAppendedTurn(pre, post []*ChatTurn, minted bool, text string) error {
	if len(post) == 0 {
		return fmt.Errorf("append produced no turns")
	}
	last := post[len(post)-1]
	if last.Speaker != "user" || strings.TrimSpace(last.Content) == "" {
		return fmt.Errorf("append did not end in a content-bearing user turn (got speaker=%q)", last.Speaker)
	}
	if minted {
		if len(post) != len(pre)+1 {
			return fmt.Errorf("minted append changed turn count by %d, want +1", len(post)-len(pre))
		}
		return nil
	}
	if len(post) != len(pre) {
		return fmt.Errorf("extended append changed turn count by %d, want 0", len(post)-len(pre))
	}
	if !strings.HasSuffix(strings.TrimSpace(last.Content), strings.TrimSpace(text)) {
		return fmt.Errorf("extended turn does not end with the appended text")
	}
	return nil
}

// chatJobRef renders a short, human-facing reference to a job for error
// messages: its filename, else its ID, else its file path.
func chatJobRef(job *Job) string {
	if job.Filename != "" {
		return job.Filename
	}
	if job.ID != "" {
		return job.ID
	}
	return job.FilePath
}
