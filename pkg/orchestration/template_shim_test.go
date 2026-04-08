package orchestration

import "testing"

// TestApplyTemplateShim_PromoteSkill verifies that a deleted skill-like
// template is promoted to the corresponding Skill and the Template
// field is cleared.
func TestApplyTemplateShim_PromoteSkill(t *testing.T) {
	job := &Job{ID: "shim-promote", Template: "cx-builder"}
	applyTemplateShim(job)
	if job.Template != "" {
		t.Errorf("expected Template to be cleared, got %q", job.Template)
	}
	if job.Skill != "cx-builder" {
		t.Errorf("expected Skill=cx-builder, got %q", job.Skill)
	}
}

// TestApplyTemplateShim_FallbackToChat verifies that a deleted generic
// oneshot template falls back to the default chat template.
func TestApplyTemplateShim_FallbackToChat(t *testing.T) {
	job := &Job{ID: "shim-fallback", Template: "api-design"}
	applyTemplateShim(job)
	if job.Template != "chat" {
		t.Errorf("expected Template=chat, got %q", job.Template)
	}
	if job.Skill != "" {
		t.Errorf("expected Skill to remain empty, got %q", job.Skill)
	}
}

// TestApplyTemplateShim_Untouched verifies that templates not in the
// shim map are left alone.
func TestApplyTemplateShim_Untouched(t *testing.T) {
	job := &Job{ID: "shim-noop", Template: "chat"}
	applyTemplateShim(job)
	if job.Template != "chat" {
		t.Errorf("expected Template=chat, got %q", job.Template)
	}

	empty := &Job{ID: "shim-empty"}
	applyTemplateShim(empty)
	if empty.Template != "" || empty.Skill != "" {
		t.Errorf("expected no change on empty template, got %+v", empty)
	}
}

// TestApplyTemplateShim_PreservesExplicitSkill verifies that if a job
// already declares a Skill alongside a deprecated Template, the
// explicit Skill wins and is not overwritten by the shim.
func TestApplyTemplateShim_PreservesExplicitSkill(t *testing.T) {
	job := &Job{ID: "shim-explicit", Template: "cx-builder", Skill: "my-custom-skill"}
	applyTemplateShim(job)
	if job.Template != "" {
		t.Errorf("expected Template to be cleared, got %q", job.Template)
	}
	if job.Skill != "my-custom-skill" {
		t.Errorf("expected explicit Skill to be preserved, got %q", job.Skill)
	}
}
