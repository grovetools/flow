package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

// writePiSessionChatJob writes a `responder: pi-session` chat job into a plan
// dir, mirroring writeChatJob but for the seeded-session responder.
func writePiSessionChatJob(t *testing.T, status, body string) (planDir, jobFile string) {
	t.Helper()
	planDir = t.TempDir()
	plan := &orchestration.Plan{
		Name: "test-plan",
		Jobs: []*orchestration.Job{
			{ID: "pi-chat", Title: "pi-chat", Filename: "01-pi-chat.md", Type: "chat", Status: orchestration.JobStatus(status)},
		},
	}
	if err := orchestration.SavePlan(planDir, plan); err != nil {
		t.Fatal(err)
	}
	jobFile = "01-pi-chat.md"
	content := "---\nid: pi-chat\ntitle: pi-chat\nstatus: " + status +
		"\ntype: chat\nresponder: pi-session\ntemplate: chat\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(planDir, jobFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return planDir, jobFile
}

// TestRunPlanSay_PiSessionAppendsAndWakes: for a pi-session chat, `say` is the
// whole delivery verb — the append is the durable truth and the wake sentinel
// is what actually gets the live session to look at it.
func TestRunPlanSay_PiSessionAppendsAndWakes(t *testing.T) {
	planDir, jobFile := writePiSessionChatJob(t, "pending_user",
		"Q\n\n<!-- grove: {\"id\": \"t1\"} -->\n## LLM Response (x)\n\nAn answer\n\n<!-- grove: {\"template\": \"chat\"} -->")

	c := &PlanSayCmd{JobFile: filepath.Join(planDir, jobFile), Text: "Please refine the phase cut."}
	if err := RunPlanSay(c); err != nil {
		t.Fatalf("RunPlanSay: %v", err)
	}

	after, _ := os.ReadFile(filepath.Join(planDir, jobFile))
	if !strings.Contains(string(after), "Please refine the phase cut.") {
		t.Errorf("turn not appended:\n%s", string(after))
	}

	wake, err := orchestration.ReadPiSessionWake(planDir, "pi-chat")
	if err != nil || wake == nil {
		t.Fatalf("ReadPiSessionWake() = (%v, %v), want a wake sentinel written by say", wake, err)
	}
	if wake.Reason != orchestration.WakeReasonSay {
		t.Errorf("wake reason = %q, want %q", wake.Reason, orchestration.WakeReasonSay)
	}
	if wake.ChatSHA256 == "" {
		t.Error("wake carries no chat digest; the receiver cannot deduplicate")
	}

	// pending_user → running: the turn is now in flight, not waiting on a human.
	content, _ := os.ReadFile(filepath.Join(planDir, jobFile))
	fm, _, err := orchestration.ParseFrontmatter(content)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := fm["status"].(string); status != string(orchestration.JobStatusRunning) {
		t.Errorf("status after say = %q, want running", status)
	}
}

// TestRunPlanSay_NonPiSessionWritesNoWake: the nudge lives on the shared say
// path, so an ordinary oracle chat must come away with no pi-session artifacts
// at all.
func TestRunPlanSay_NonPiSessionWritesNoWake(t *testing.T) {
	planDir, jobFile := writeChatJob(t, "Q\n\n<!-- grove: {\"id\": \"t1\"} -->\nAn answer\n\n<!-- grove: {\"template\": \"chat\"} -->")

	if err := RunPlanSay(&PlanSayCmd{JobFile: filepath.Join(planDir, jobFile), Text: "hello"}); err != nil {
		t.Fatalf("RunPlanSay: %v", err)
	}
	if wake, _ := orchestration.ReadPiSessionWake(planDir, "chat-job"); wake != nil {
		t.Error("a wake sentinel was written for an oracle chat")
	}
	// An oracle chat's status must stay untouched by say: runPlanRun owns its
	// dispatch, and moving it to running here would misreport a chat that has
	// not been run.
	content, _ := os.ReadFile(filepath.Join(planDir, jobFile))
	fm, _, _ := orchestration.ParseFrontmatter(content)
	if status, _ := fm["status"].(string); status != "pending_user" {
		t.Errorf("oracle chat status after say = %q, want it untouched at pending_user", status)
	}
}

// TestRunPlanRespond: the response lands in the record and the job moves to
// the human gate.
func TestRunPlanRespond(t *testing.T) {
	t.Run("appends and moves to pending_user", func(t *testing.T) {
		planDir, jobFile := writePiSessionChatJob(t, "running", "<!-- grove: {\"template\": \"chat\"} -->\n\nDesign the phase cut.")

		c := &PlanRespondCmd{JobFile: filepath.Join(planDir, jobFile), Text: "Three phases: A, B, C."}
		if err := RunPlanRespond(c); err != nil {
			t.Fatalf("RunPlanRespond: %v", err)
		}

		content, _ := os.ReadFile(filepath.Join(planDir, jobFile))
		if !strings.Contains(string(content), "Three phases: A, B, C.") {
			t.Errorf("response not appended:\n%s", string(content))
		}
		fm, _, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			t.Fatal(err)
		}
		if status, _ := fm["status"].(string); status != string(orchestration.JobStatusPendingUser) {
			t.Errorf("status after respond = %q, want pending_user", status)
		}
	})

	t.Run("refuses an oracle chat", func(t *testing.T) {
		planDir, jobFile := writeChatJob(t, "<!-- grove: {\"template\": \"chat\"} -->\n\nQuestion?")
		err := RunPlanRespond(&PlanRespondCmd{JobFile: filepath.Join(planDir, jobFile), Text: "answer"})
		if err == nil || !strings.Contains(err.Error(), "oracle chat") {
			t.Fatalf("RunPlanRespond on an oracle chat = %v, want a refusal", err)
		}
	})

	t.Run("job not found errors", func(t *testing.T) {
		planDir, _ := writePiSessionChatJob(t, "running", "<!-- grove: {\"template\": \"chat\"} -->\n\nQ")
		err := RunPlanRespond(&PlanRespondCmd{JobFile: filepath.Join(planDir, "99-missing.md"), Text: "hi"})
		if err == nil || !strings.Contains(err.Error(), "job not found") {
			t.Fatalf("RunPlanRespond on a missing job = %v, want a not-found error", err)
		}
	})
}

// TestValidateResponderFlag_PiSession: the CLI must accept the new value on
// chat jobs and reject it everywhere else, with the same shape as `agent`.
func TestValidateResponderFlag_PiSession(t *testing.T) {
	if err := validateResponderFlag("pi-session", "chat"); err != nil {
		t.Errorf("validateResponderFlag(pi-session, chat) = %v, want nil", err)
	}
	err := validateResponderFlag("pi-session", "interactive_agent")
	if err == nil || !strings.Contains(err.Error(), "--type chat") {
		t.Errorf("validateResponderFlag(pi-session, interactive_agent) = %v, want a --type chat requirement", err)
	}
	err = validateResponderFlag("nonsense", "chat")
	if err == nil || !strings.Contains(err.Error(), "pi-session") {
		t.Errorf("validateResponderFlag(nonsense) = %v, want the error to list pi-session as valid", err)
	}
}
