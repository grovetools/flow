package orchestration

import (
	"fmt"
	"testing"
	"time"
)

func TestHookPayloads(t *testing.T) {
	// Test OneshotHookInput serialization
	t.Run("OneshotHookInput", func(t *testing.T) {
		input := OneshotHookInput{
			JobID:         "test-123",
			PlanName:      "my-plan",
			PlanDirectory: "/path/to/plan",
			JobTitle:      "Test Job",
			JobFilePath:   "/path/to/job.md",
		}

		// This would be marshaled to JSON when calling grove-hooks
		// Just ensure the struct is properly defined
		if input.JobID != "test-123" {
			t.Errorf("JobID mismatch")
		}
	})

	// Test OneshotHookStopInput serialization
	t.Run("OneshotHookStopInput", func(t *testing.T) {
		input := OneshotHookStopInput{
			JobID:  "test-123",
			Status: "completed",
		}

		if input.Status != "completed" {
			t.Errorf("Status mismatch")
		}

		// Test with error
		inputWithError := OneshotHookStopInput{
			JobID:  "test-456",
			Status: "failed",
			Error:  "Something went wrong",
		}

		if inputWithError.Error != "Something went wrong" {
			t.Errorf("Error message mismatch")
		}
	})
}

func TestNotifyJobStart(t *testing.T) {
	job := &Job{
		ID:       "test-job-123",
		Title:    "Test Job",
		FilePath: "/path/to/test.md",
	}

	plan := &Plan{
		Name:      "test-plan",
		Directory: "/path/to/plan",
	}

	// This will attempt to call grove-hooks if installed
	// In a test environment, it will likely skip silently
	notifyJobStart(job, plan)

	// Test with nil job
	notifyJobStart(nil, plan)

	// Test with nil plan
	notifyJobStart(job, nil)

	// Test with both nil
	notifyJobStart(nil, nil)
}

func TestNotifyJobComplete(t *testing.T) {
	job := &Job{
		ID:     "test-job-123",
		Title:  "Test Job",
		Status: JobStatusCompleted,
	}

	// Test successful completion
	notifyJobComplete(job, nil)

	// Test with error
	err := fmt.Errorf("test error")
	notifyJobComplete(job, err)

	// Test with failed job status
	job.Status = JobStatusFailed
	notifyJobComplete(job, nil)

	// Test with nil job
	notifyJobComplete(nil, nil)
}

func TestCallGroveHook(t *testing.T) {
	// Test that callGroveHook doesn't panic with various inputs
	t.Run("ValidPayload", func(t *testing.T) {
		payload := OneshotHookInput{
			JobID:    "test-123",
			PlanName: "test",
		}
		callGroveHook("start", payload)
		// Give goroutine time to execute
		time.Sleep(10 * time.Millisecond)
	})

	t.Run("InvalidPayload", func(t *testing.T) {
		// Test with a payload that can't be marshaled
		payload := make(chan int) // channels can't be marshaled to JSON
		callGroveHook("start", payload)
		// Give goroutine time to execute
		time.Sleep(10 * time.Millisecond)
	})

	t.Run("NilPayload", func(t *testing.T) {
		callGroveHook("start", nil)
		// Give goroutine time to execute
		time.Sleep(10 * time.Millisecond)
	})
}