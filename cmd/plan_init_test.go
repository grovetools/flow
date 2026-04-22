package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/core/pkg/workspace"
)

// TestPlanInit_EnvProvisionFailureExitsNonZero verifies the regression fix:
// when environment provisioning fails, executePlanInit returns a non-nil
// error (so the CLI exits non-zero) while still creating the plan directory
// and .grove-plan.yml so the user can retry `grove env up` later.
func TestPlanInit_EnvProvisionFailureExitsNonZero(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "plan-init-envfail-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })

	planPath := filepath.Join(tempDir, "my-plan")

	sentinel := errors.New("stub provisioner exploded")
	prev := provisionEnvironmentFn
	provisionEnvironmentFn = func(worktreeName, planPath string, _ *workspace.Provider, envProfile string) (string, error) {
		return "WARN: provision failed\n", sentinel
	}
	t.Cleanup(func() { provisionEnvironmentFn = prev })

	_, err = executePlanInit(&PlanInitCmd{Dir: planPath})

	if err == nil {
		t.Fatalf("expected non-nil error when provisioning fails, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected returned error to wrap sentinel, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(planPath, ".grove-plan.yml")); statErr != nil {
		t.Fatalf(".grove-plan.yml should exist even after env-provision failure: %v", statErr)
	}
}

// TestPlanInit_EnvProvisionSuccessExitsZero verifies no regression on the
// happy path: a successful stub → executePlanInit returns nil error.
func TestPlanInit_EnvProvisionSuccessExitsZero(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "plan-init-envok-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })

	planPath := filepath.Join(tempDir, "my-plan")

	prev := provisionEnvironmentFn
	provisionEnvironmentFn = func(worktreeName, planPath string, _ *workspace.Provider, envProfile string) (string, error) {
		return "", nil
	}
	t.Cleanup(func() { provisionEnvironmentFn = prev })

	_, err = executePlanInit(&PlanInitCmd{Dir: planPath})
	if err != nil {
		t.Fatalf("unexpected error on happy path: %v", err)
	}
}
