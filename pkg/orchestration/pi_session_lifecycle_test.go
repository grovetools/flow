package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// piSessionChatFixture writes a pi-session chat job with the given body and
// returns the loaded job plus its plan.
func piSessionChatFixture(t *testing.T, body string, status JobStatus) (*Job, *Plan) {
	t.Helper()
	planDir := t.TempDir()
	plan := &Plan{Name: "test-plan", Directory: planDir, JobsByID: map[string]*Job{}}

	content := "---\nid: pi-chat-1\ntitle: pi chat\nstatus: " + string(status) +
		"\ntype: chat\nresponder: pi-session\ntemplate: chat\n---\n" + body
	path := filepath.Join(planDir, "01-pi-chat.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := LoadJob(path)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = "01-pi-chat.md"
	job.FilePath = path
	plan.Jobs = append(plan.Jobs, job)
	plan.JobsByID[job.ID] = job
	return job, plan
}

// --- Wake protocol ---------------------------------------------------------

// TestNudgePiSessionWake_SequenceAndDigest: the sentinel is the receiver's
// dedup key, so the sequence must advance monotonically and the digest must
// track the chat file's actual bytes.
func TestNudgePiSessionWake_SequenceAndDigest(t *testing.T) {
	job, plan := piSessionChatFixture(t, "\n<!-- grove: {\"template\": \"chat\"} -->\n\nfirst\n", JobStatusRunning)

	if err := NudgePiSessionWake(plan.Directory, job, WakeReasonSay); err != nil {
		t.Fatalf("NudgePiSessionWake() error = %v", err)
	}
	first, err := ReadPiSessionWake(plan.Directory, job.ID)
	if err != nil || first == nil {
		t.Fatalf("ReadPiSessionWake() = (%v, %v)", first, err)
	}
	if first.Seq != 1 {
		t.Errorf("first Seq = %d, want 1", first.Seq)
	}
	if first.ChatSHA256 == "" || first.ChatFile != job.FilePath {
		t.Errorf("wake = %+v, want it bound to the chat file with a digest", first)
	}

	// Nudging again without changing the file must still advance the sequence
	// (the nudge is idempotent-SAFE, not idempotent-silent) while leaving the
	// digest identical, which is what lets a receiver drop it as a duplicate.
	if err := NudgePiSessionWake(plan.Directory, job, WakeReasonSay); err != nil {
		t.Fatal(err)
	}
	second, _ := ReadPiSessionWake(plan.Directory, job.ID)
	if second.Seq != 2 {
		t.Errorf("second Seq = %d, want 2", second.Seq)
	}
	if second.ChatSHA256 != first.ChatSHA256 {
		t.Error("digest changed without the chat file changing; a receiver would re-reconcile needlessly")
	}

	// A real append must move the digest.
	if err := os.WriteFile(job.FilePath, []byte("---\nid: pi-chat-1\n---\n\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NudgePiSessionWake(plan.Directory, job, WakeReasonSay); err != nil {
		t.Fatal(err)
	}
	third, _ := ReadPiSessionWake(plan.Directory, job.ID)
	if third.ChatSHA256 == first.ChatSHA256 {
		t.Error("digest did not change after the chat file changed; deduplication would drop a real turn")
	}
}

// TestNudgePiSessionWake_NoOpForOtherResponders: the nudge sits on the shared
// `say` path, so it must be silently inert for every other chat flavor.
func TestNudgePiSessionWake_NoOpForOtherResponders(t *testing.T) {
	planDir := t.TempDir()
	for _, responder := range []string{"", ResponderOracle, ResponderAgent} {
		job := &Job{ID: "j1", Type: JobTypeChat, Responder: responder, FilePath: filepath.Join(planDir, "x.md")}
		if err := NudgePiSessionWake(planDir, job, WakeReasonSay); err != nil {
			t.Errorf("responder %q: NudgePiSessionWake() error = %v, want a silent no-op", responder, err)
		}
		if wake, _ := ReadPiSessionWake(planDir, job.ID); wake != nil {
			t.Errorf("responder %q: a wake sentinel was written for a non-pi-session chat", responder)
		}
	}
}

// --- Response append -------------------------------------------------------

// TestAppendChatResponseTurn_AppendsAndLeavesCleanMarker: the response cell
// must be shaped exactly like the oracle path's, so no downstream reader can
// tell which engine produced a turn.
func TestAppendChatResponseTurn_AppendsAndLeavesCleanMarker(t *testing.T) {
	job, _ := piSessionChatFixture(t, "\n<!-- grove: {\"template\": \"chat\"} -->\n\nDesign the thing.\n", JobStatusRunning)

	if err := NewStatePersister().AppendChatResponseTurn(job, "Here is the design.", false); err != nil {
		t.Fatalf("AppendChatResponseTurn() error = %v", err)
	}

	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(content)
	if !strings.Contains(body, "## LLM Response (") {
		t.Error("response cell is missing the '## LLM Response' heading the oracle path emits")
	}
	if !strings.Contains(body, "Here is the design.") {
		t.Error("the response body was not written")
	}

	turns, err := ParseChatFile(content)
	if err != nil {
		t.Fatalf("chat no longer parses: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3 (question, response, fresh marker)", len(turns))
	}
	if turns[1].Speaker != "llm" {
		t.Errorf("turn 2 speaker = %q, want llm", turns[1].Speaker)
	}
	// A clean trailing user marker is what makes the chat immediately ready for
	// the next `flow plan say`.
	if turns[2].Speaker != "user" || strings.TrimSpace(turns[2].Content) != "" {
		t.Errorf("trailing turn = {%q, %q}, want an empty user marker", turns[2].Speaker, turns[2].Content)
	}
}

// TestAppendChatResponseTurn_Refusals: every refusal prevents a response from
// landing somewhere it would be silently misread.
func TestAppendChatResponseTurn_Refusals(t *testing.T) {
	t.Run("oracle chat", func(t *testing.T) {
		job, _ := piSessionChatFixture(t, "\n<!-- grove: {\"template\": \"chat\"} -->\n\nq\n", JobStatusRunning)
		job.Responder = ResponderOracle
		err := NewStatePersister().AppendChatResponseTurn(job, "answer", false)
		if err == nil || !strings.Contains(err.Error(), "oracle chat") {
			t.Fatalf("error = %v, want a refusal naming the oracle responder", err)
		}
	})

	t.Run("no pending user turn", func(t *testing.T) {
		job, _ := piSessionChatFixture(t, "\n<!-- grove: {\"template\": \"chat\"} -->\n\nq\n", JobStatusRunning)
		if err := NewStatePersister().AppendChatResponseTurn(job, "first answer", false); err != nil {
			t.Fatal(err)
		}
		// The chat now ends on an empty marker: there is no question to answer.
		err := NewStatePersister().AppendChatResponseTurn(job, "second answer", false)
		if err == nil || !strings.Contains(err.Error(), "nothing to respond to") {
			t.Fatalf("error = %v, want a refusal for a chat with no pending user turn", err)
		}
		// --force is the interrupted-write recovery path.
		if err := NewStatePersister().AppendChatResponseTurn(job, "second answer", true); err != nil {
			t.Fatalf("--force error = %v, want the recovery path to succeed", err)
		}
	})

	t.Run("empty response", func(t *testing.T) {
		job, _ := piSessionChatFixture(t, "\n<!-- grove: {\"template\": \"chat\"} -->\n\nq\n", JobStatusRunning)
		if err := NewStatePersister().AppendChatResponseTurn(job, "   ", false); err == nil {
			t.Fatal("an empty response was accepted")
		}
	})

	t.Run("not a chat", func(t *testing.T) {
		job := &Job{ID: "j", Type: JobTypeInteractiveAgent, Filename: "x.md"}
		if err := NewStatePersister().AppendChatResponseTurn(job, "answer", false); err == nil {
			t.Fatal("a response was accepted for a non-chat job")
		}
	})
}

// TestAppendChatResponseTurn_AgentRespondedAlsoAllowed: the writer serves both
// agent-authored responders, not just pi-session.
func TestAppendChatResponseTurn_AgentRespondedAlsoAllowed(t *testing.T) {
	job, _ := piSessionChatFixture(t, "\n<!-- grove: {\"template\": \"chat\"} -->\n\nq\n", JobStatusRunning)
	job.Responder = ResponderAgent
	if err := NewStatePersister().AppendChatResponseTurn(job, "answer", false); err != nil {
		t.Fatalf("AppendChatResponseTurn() on a responder: agent chat = %v, want success", err)
	}
}

// --- Lifecycle transitions -------------------------------------------------

// TestPiSessionLifecycleTransitions walks the documented status machine end to
// end: run → running, respond → pending_user, say → running. These three are
// the contract Phase 3 codes against, so they are asserted together rather than
// scattered across three tests.
func TestPiSessionLifecycleTransitions(t *testing.T) {
	planDir := t.TempDir()
	job, plan := piSessionLaunchedFixture(t, planDir)

	// respond → pending_user.
	if err := NewStatePersister().AppendChatResponseTurn(job, "The design is X.", false); err != nil {
		t.Fatalf("AppendChatResponseTurn() error = %v", err)
	}
	if err := NewStatePersister().UpdateJobStatus(job, JobStatusPendingUser); err != nil {
		t.Fatal(err)
	}
	if job.Status != JobStatusPendingUser {
		t.Fatalf("status after respond = %q, want pending_user", job.Status)
	}
	if status := frontmatterStatus(t, job.FilePath); status != string(JobStatusPendingUser) {
		t.Errorf("frontmatter status after respond = %q, want pending_user", status)
	}

	// say → the turn appends and the wake advances.
	before, _ := ReadPiSessionWake(planDir, job.ID)
	if err := NewStatePersister().AppendChatUserTurn(job, "Second question.", false); err != nil {
		t.Fatalf("AppendChatUserTurn() error = %v", err)
	}
	if err := NudgePiSessionWake(plan.Directory, job, WakeReasonSay); err != nil {
		t.Fatal(err)
	}
	after, _ := ReadPiSessionWake(planDir, job.ID)
	if before != nil && after.Seq <= before.Seq {
		t.Errorf("wake seq did not advance on say: %d → %d", before.Seq, after.Seq)
	}
	if after.Reason != WakeReasonSay {
		t.Errorf("wake reason = %q, want %q", after.Reason, WakeReasonSay)
	}

	// The chat must still parse and end on the new question.
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := ParseChatFile(content)
	if err != nil {
		t.Fatalf("chat no longer parses after a full round trip: %v", err)
	}
	last := turns[len(turns)-1]
	if last.Speaker != "user" || !strings.Contains(last.Content, "Second question.") {
		t.Errorf("last turn = {%q, %q}, want the new user question", last.Speaker, last.Content)
	}
}

// TestCompleteJob_PiSessionTearsDownAndNudges: the human gate must both close
// the record and tell the session the dialogue is over. Without the final
// nudge a watcher sits forever on a file that will never move again.
func TestCompleteJob_PiSessionTearsDownAndNudges(t *testing.T) {
	planDir := t.TempDir()
	job, plan := piSessionLaunchedFixture(t, planDir)
	job.Status = JobStatusPendingUser

	if err := CompleteJob(job, plan, true); err != nil {
		t.Fatalf("CompleteJob() error = %v", err)
	}
	if job.Status != JobStatusCompleted {
		t.Errorf("status = %q, want completed", job.Status)
	}

	wake, err := ReadPiSessionWake(planDir, job.ID)
	if err != nil || wake == nil {
		t.Fatalf("ReadPiSessionWake() = (%v, %v), want a completion nudge", wake, err)
	}
	if wake.Reason != WakeReasonComplete {
		t.Errorf("wake reason = %q, want %q so the session stops expecting turns", wake.Reason, WakeReasonComplete)
	}

	// The seed and descriptor are the audit record of what the session held;
	// completion must not delete them.
	desc, err := ReadPiSessionDescriptor(planDir, job.ID)
	if err != nil || desc == nil {
		t.Fatal("the launch descriptor was removed at completion; the context audit trail is gone")
	}
	if _, err := os.Stat(desc.SessionFile); err != nil {
		t.Errorf("the session file was removed at completion: %v", err)
	}
}

func frontmatterStatus(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fm, _, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := fm["status"].(string)
	return status
}

// TestPiSessionAppendsWorkWhileSessionIsLive is the regression guard for the
// defect that would have made the whole responder unusable: a live pi-session
// chat sits at status `running` and its lock file holds the SESSION HOST's pid
// for the session's entire life. The generic guards read those two signals as
// "a turn is in flight" and would have refused every `say` and every `respond`
// of a perfectly healthy chat.
func TestPiSessionAppendsWorkWhileSessionIsLive(t *testing.T) {
	job, _ := piSessionChatFixture(t, "\n<!-- grove: {\"template\": \"chat\"} -->\n\nFirst question.\n", JobStatusRunning)

	// The launcher's lock file: our own pid stands in for the live session host.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = RemoveLockFile(job.FilePath) }()

	// respond must land despite running + a live lock.
	if err := NewStatePersister().AppendChatResponseTurn(job, "An answer.", false); err != nil {
		t.Fatalf("AppendChatResponseTurn() on a live pi-session chat = %v, want success", err)
	}
	// say must land too — delivering to a busy session is supported; the turn
	// queues behind whatever it is doing.
	if err := NewStatePersister().AppendChatUserTurn(job, "Second question.", false); err != nil {
		t.Fatalf("AppendChatUserTurn() on a live pi-session chat = %v, want success", err)
	}

	// The same conditions must STILL refuse an ordinary agent-responded chat:
	// this carve-out is about what those signals mean for pi-session, not a
	// general weakening of the guard.
	other, _ := piSessionChatFixture(t, "\n<!-- grove: {\"template\": \"chat\"} -->\n\nQ.\n", JobStatusRunning)
	other.Responder = ResponderAgent
	if err := CreateLockFile(other.FilePath, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = RemoveLockFile(other.FilePath) }()
	if err := NewStatePersister().AppendChatUserTurn(other, "hi", false); err == nil {
		t.Error("AppendChatUserTurn() succeeded on a running, locked responder: agent chat; the guard must still hold there")
	}
}
