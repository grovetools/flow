package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
)

type fakeAgentInputClient struct {
	daemon.Client
	connected bool
	session   *models.Session
	sendErr   error
	sentJobID string
	sentInput string
}

func (c *fakeAgentInputClient) IsTerminalConnected(context.Context) (bool, error) {
	return c.connected, nil
}

func (c *fakeAgentInputClient) GetSession(context.Context, string) (*models.Session, error) {
	return c.session, nil
}

func (c *fakeAgentInputClient) SendAgentInput(_ context.Context, jobID, input string) error {
	c.sentJobID = jobID
	c.sentInput = input
	return c.sendErr
}

func TestSendDaemonAgentInputWithoutConnectedTerminal(t *testing.T) {
	client := &fakeAgentInputClient{
		session: &models.Session{PtyID: "daemon-pty-123"},
	}

	attempted, err := sendDaemonAgentInput(context.Background(), client, "child-job", "hello\r")
	if err != nil {
		t.Fatalf("sendDaemonAgentInput() error = %v", err)
	}
	if !attempted {
		t.Fatal("sendDaemonAgentInput() did not attempt daemon delivery for a recorded PTY")
	}
	if client.sentJobID != "child-job" || client.sentInput != "hello\r" {
		t.Fatalf("SendAgentInput() got job=%q input=%q", client.sentJobID, client.sentInput)
	}
}

func TestSendDaemonAgentInputFallsBackWithoutNativeTarget(t *testing.T) {
	client := &fakeAgentInputClient{}

	attempted, err := sendDaemonAgentInput(context.Background(), client, "tmux-job", "hello\r")
	if err != nil {
		t.Fatalf("sendDaemonAgentInput() error = %v", err)
	}
	if attempted {
		t.Fatal("sendDaemonAgentInput() attempted daemon delivery without a terminal or PTY")
	}
	if client.sentJobID != "" {
		t.Fatalf("SendAgentInput() unexpectedly called for %q", client.sentJobID)
	}
}

func TestSendDaemonAgentInputReturnsDaemonError(t *testing.T) {
	wantErr := errors.New("input failed")
	client := &fakeAgentInputClient{
		connected: true,
		sendErr:   wantErr,
	}

	attempted, err := sendDaemonAgentInput(context.Background(), client, "relay-job", "hello\r")
	if !attempted {
		t.Fatal("sendDaemonAgentInput() did not attempt connected terminal delivery")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("sendDaemonAgentInput() error = %v, want %v", err, wantErr)
	}
}
