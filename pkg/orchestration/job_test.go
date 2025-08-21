package orchestration

import (
	"testing"
)

func TestJobAgentContinueField(t *testing.T) {
	// Test that Job struct has AgentContinue field
	job := &Job{
		ID:            "test-job",
		Title:         "Test Job",
		Type:          JobTypeInteractiveAgent,
		Status:        JobStatusPending,
		AgentContinue: true,
	}

	if !job.AgentContinue {
		t.Error("AgentContinue field should be true")
	}

	// Test with false value
	job2 := &Job{
		ID:            "test-job-2",
		Title:         "Test Job 2",
		Type:          JobTypeInteractiveAgent,
		Status:        JobStatusPending,
		AgentContinue: false,
	}

	if job2.AgentContinue {
		t.Error("AgentContinue field should be false")
	}
}