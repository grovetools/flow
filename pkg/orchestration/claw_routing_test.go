package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/grovetools/core/pkg/models"
)

type recordingClient struct {
	target string
	calls  int
	err    error
}

func (r *recordingClient) UpdateSessionTmuxTarget(_ context.Context, _, tmuxTarget string) error {
	r.calls++
	r.target = tmuxTarget
	return r.err
}

func TestClassifyTmuxStamp(t *testing.T) {
	tests := []struct {
		name    string
		session *models.Session
		want    TmuxStampVerdict
	}{
		{"treemux-hosted", &models.Session{Mux: models.MuxTreemux}, TmuxStampSkip},
		{"tuimux-hosted", &models.Session{Mux: models.MuxTuimux}, TmuxStampSkip},
		{"live pty outranks a stale tmux label", &models.Session{Mux: models.MuxTmux, PtyID: "pty-1"}, TmuxStampSkip},
		{"pty with no mux label", &models.Session{PtyID: "pty-1"}, TmuxStampSkip},
		{"genuinely tmux-hosted", &models.Session{Mux: models.MuxTmux}, TmuxStampRecord},
		{"no mux recorded", &models.Session{}, TmuxStampVerify},
		{"no session record at all", nil, TmuxStampVerify},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ClassifyTmuxStamp(tc.session)
			if got != tc.want {
				t.Fatalf("ClassifyTmuxStamp = %q (%s), want %q", got, reason, tc.want)
			}
			if reason == "" {
				t.Fatal("verdict came with no reason")
			}
		})
	}
}

// usePaneLiveness stubs the tmux pane probe for one test.
func usePaneLiveness(t *testing.T, live bool) {
	t.Helper()
	prev := tmuxPaneIsLive
	tmuxPaneIsLive = func(context.Context, string) bool { return live }
	t.Cleanup(func() { tmuxPaneIsLive = prev })
}

// The bug this guards: claw recorded a SYNTHESIZED tmux pane name on every
// agent, so a treemux-hosted session's persisted delivery route pointed at a
// pane that had never existed and inbound Signal messages died on send-keys.
func TestRecordTmuxTargetSkipsPtyHostedSessions(t *testing.T) {
	usePaneLiveness(t, true) // even a live pane must not tempt it

	for _, session := range []*models.Session{
		{Mux: models.MuxTreemux},
		{Mux: models.MuxTuimux, PtyID: "pty-1"},
		{PtyID: "pty-1"},
	} {
		client := &recordingClient{}
		recorded, reason := RecordTmuxTargetIfTmuxHosted(context.Background(), client, "job-1", "proj:job-1", session)
		if recorded || client.calls != 0 {
			t.Fatalf("recorded a tmux target for %+v (%s)", session, reason)
		}
	}
}

func TestRecordTmuxTargetRecordsForTmuxHostedSessions(t *testing.T) {
	usePaneLiveness(t, false) // an explicit tmux mux needs no probe
	client := &recordingClient{}

	recorded, reason := RecordTmuxTargetIfTmuxHosted(context.Background(), client, "job-1", "proj:job-1", &models.Session{Mux: models.MuxTmux})
	if !recorded {
		t.Fatalf("did not record a tmux target for a tmux-hosted session: %s", reason)
	}
	if client.target != "proj:job-1" {
		t.Fatalf("recorded target = %q, want proj:job-1", client.target)
	}
}

// With no mux recorded, the synthesized name has to prove itself.
func TestRecordTmuxTargetVerifiesUnknownHosts(t *testing.T) {
	t.Run("live pane is recorded", func(t *testing.T) {
		usePaneLiveness(t, true)
		client := &recordingClient{}
		recorded, reason := RecordTmuxTargetIfTmuxHosted(context.Background(), client, "job-1", "proj:job-1", &models.Session{})
		if !recorded {
			t.Fatalf("a live pane was not recorded: %s", reason)
		}
	})

	t.Run("dead pane is not recorded", func(t *testing.T) {
		usePaneLiveness(t, false)
		client := &recordingClient{}
		recorded, reason := RecordTmuxTargetIfTmuxHosted(context.Background(), client, "job-1", "proj:job-1", &models.Session{})
		if recorded || client.calls != 0 {
			t.Fatalf("recorded a synthesized target with no live pane (%s)", reason)
		}
	})

	t.Run("missing session record is not trusted", func(t *testing.T) {
		usePaneLiveness(t, false)
		client := &recordingClient{}
		recorded, _ := RecordTmuxTargetIfTmuxHosted(context.Background(), client, "job-1", "proj:job-1", nil)
		if recorded || client.calls != 0 {
			t.Fatal("recorded a target for a session the daemon does not know about")
		}
	})
}

func TestRecordTmuxTargetReportsClientFailure(t *testing.T) {
	client := &recordingClient{err: errors.New("daemon unreachable")}

	recorded, reason := RecordTmuxTargetIfTmuxHosted(context.Background(), client, "job-1", "proj:job-1", &models.Session{Mux: models.MuxTmux})
	if recorded {
		t.Fatal("reported success after the daemon call failed")
	}
	if !strings.Contains(reason, "daemon unreachable") {
		t.Fatalf("reason = %q, want the underlying failure", reason)
	}
}

func TestRecordTmuxTargetIgnoresAnEmptyTarget(t *testing.T) {
	client := &recordingClient{}
	if recorded, _ := RecordTmuxTargetIfTmuxHosted(context.Background(), client, "job-1", "", &models.Session{Mux: models.MuxTmux}); recorded {
		t.Fatal("recorded an empty tmux target")
	}
	if client.calls != 0 {
		t.Fatal("called the daemon with an empty target")
	}
}
