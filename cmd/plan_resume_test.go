package cmd

import (
	"errors"
	"reflect"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

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
