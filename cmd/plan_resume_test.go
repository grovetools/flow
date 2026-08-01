package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TestAwaitResumeConfirmation covers the foreground half of the orphaned
// confirmation fix: `flow plan resume` must outlive the provider's background
// session discovery, but it must do so visibly and with a ceiling. A silent,
// unbounded block would just trade a pending session for a terminal that looks
// wedged — and a Ctrl-C out of that block recreates the original orphaning.
func TestAwaitResumeConfirmation(t *testing.T) {
	t.Run("blocks until the confirmation settles", func(t *testing.T) {
		var out bytes.Buffer
		settled := false
		err := awaitResumeConfirmation(context.Background(), &out, time.Minute, time.Minute,
			func(context.Context) error {
				time.Sleep(20 * time.Millisecond)
				settled = true
				return nil
			})
		if err != nil {
			t.Fatalf("awaitResumeConfirmation() error = %v", err)
		}
		if !settled {
			t.Fatal("awaitResumeConfirmation() returned before the confirmation finished")
		}
		if out.Len() != 0 {
			t.Fatalf("a confirmation that settled promptly printed progress noise: %q", out.String())
		}
	})

	t.Run("surfaces a confirmation failure without hiding it", func(t *testing.T) {
		want := errors.New("daemon unreachable")
		err := awaitResumeConfirmation(context.Background(), &bytes.Buffer{}, time.Minute, time.Minute,
			func(context.Context) error { return want })
		if !errors.Is(err, want) {
			t.Fatalf("awaitResumeConfirmation() error = %v, want %v", err, want)
		}
	})

	t.Run("gives up at the bound and says so while waiting", func(t *testing.T) {
		var out bytes.Buffer
		release := make(chan struct{})
		defer close(release)

		err := awaitResumeConfirmation(context.Background(), &out, 60*time.Millisecond, 10*time.Millisecond,
			func(ctx context.Context) error {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return ctx.Err()
			})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("awaitResumeConfirmation() error = %v, want context.DeadlineExceeded", err)
		}
		if !strings.Contains(out.String(), "still waiting") {
			t.Fatalf("no progress reported during a slow confirmation: %q", out.String())
		}
	})
}

func TestRunResumeWithRollback(t *testing.T) {
	t.Run("launch success leaves job running", func(t *testing.T) {
		var statuses []orchestration.JobStatus
		job := &orchestration.Job{}
		err := runResumeWithRollback(job, func(_ *orchestration.Job) (func() error, error) {
			statuses = append(statuses, orchestration.JobStatusRunning)
			return func() error {
				statuses = append(statuses, orchestration.JobStatusCompleted)
				return nil
			}, nil
		}, func() error { return nil })
		if err != nil {
			t.Fatalf("runResumeWithRollback() error = %v", err)
		}
		want := []orchestration.JobStatus{orchestration.JobStatusRunning}
		if !reflect.DeepEqual(statuses, want) {
			t.Fatalf("statuses = %v, want %v", statuses, want)
		}
	})

	t.Run("launch failure restores completed", func(t *testing.T) {
		launchErr := errors.New("window creation failed")
		var statuses []orchestration.JobStatus
		err := runResumeWithRollback(&orchestration.Job{}, func(_ *orchestration.Job) (func() error, error) {
			statuses = append(statuses, orchestration.JobStatusRunning)
			return func() error {
				statuses = append(statuses, orchestration.JobStatusCompleted)
				return nil
			}, nil
		}, func() error { return launchErr })
		if !errors.Is(err, launchErr) {
			t.Fatalf("runResumeWithRollback() error = %v, want wrapping launch error", err)
		}
		want := []orchestration.JobStatus{orchestration.JobStatusRunning, orchestration.JobStatusCompleted}
		if !reflect.DeepEqual(statuses, want) {
			t.Fatalf("statuses = %v, want %v", statuses, want)
		}
	})

	t.Run("rollback failure reports both failures", func(t *testing.T) {
		launchErr := errors.New("launch failed")
		rollbackErr := errors.New("rollback failed")
		err := runResumeWithRollback(&orchestration.Job{}, func(_ *orchestration.Job) (func() error, error) {
			return func() error { return rollbackErr }, nil
		}, func() error { return launchErr })
		if !errors.Is(err, launchErr) || !errors.Is(err, rollbackErr) {
			t.Fatalf("runResumeWithRollback() error = %v, want both launch and rollback failures", err)
		}
	})

	t.Run("initial status failure prevents launch", func(t *testing.T) {
		statusErr := errors.New("status failed")
		launched := false
		err := runResumeWithRollback(&orchestration.Job{}, func(_ *orchestration.Job) (func() error, error) {
			return nil, statusErr
		}, func() error {
			launched = true
			return nil
		})
		if !errors.Is(err, statusErr) {
			t.Fatalf("runResumeWithRollback() error = %v, want status failure", err)
		}
		if launched {
			t.Fatal("launch called after status update failure")
		}
	})
}
