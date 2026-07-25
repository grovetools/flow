package view

import "testing"

func TestHostedKeysReportsCtrlFCollision(t *testing.T) {
	ref := HostedKeys()
	if ref.SchemaVersion != 1 || ref.App != "flow" {
		t.Fatalf("unexpected reference header: %#v", ref)
	}
	for _, binding := range ref.Bindings {
		if binding.Action != "finish_plan" {
			continue
		}
		if len(binding.Keys) != 1 || binding.Keys[0] != "ctrl+f" || !binding.HostSwallowed {
			t.Fatalf("finish_plan binding = %#v", binding)
		}
		if len(binding.CollisionHints) != 1 || binding.CollisionHints[0] != "treemux.nav_workspaces" {
			t.Fatalf("ctrl+f collision hint missing: %#v", binding)
		}
		return
	}
	t.Fatal("finish_plan hosted binding not exported")
}

func TestHostedKeysIncludesEmbeddedStatusBindings(t *testing.T) {
	for _, binding := range HostedKeys().Bindings {
		if binding.Scope == "status/Navigation" && binding.ConfigKey == "up" && !binding.HostSwallowed {
			return
		}
	}
	t.Fatal("embedded status bindings missing from hosted key reference")
}
