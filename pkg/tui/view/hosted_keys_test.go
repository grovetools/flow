package view

import (
	"strings"
	"testing"

	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/status"
	"github.com/grovetools/flow/pkg/tui/wizards/add"
)

func hostedKeySet() map[string]bool {
	keys := make(map[string]bool)
	for _, binding := range HostedKeys().Bindings {
		for _, k := range binding.Keys {
			keys[k] = true
		}
	}
	return keys
}

// TestHostedKeysPublishesFinishWizardBindings guards the gap that the finish
// wizard's keymap was never a source for HostedKeys(): the declaration derived
// status, browser and the add wizard only, so the finish wizard's force toggle
// and its esc-to-quit binding were absent from the contract treemux joins
// against. A key that is not declared here cannot be won back from the host,
// so an undeclared binding is not merely undocumented — it is unreachable
// whenever the host claims the same chord.
func TestHostedKeysPublishesFinishWizardBindings(t *testing.T) {
	var scopes []string
	byAction := make(map[string][]string)
	for _, binding := range HostedKeys().Bindings {
		if !strings.HasPrefix(binding.Scope, "wizard-finish/") {
			continue
		}
		scopes = append(scopes, binding.Scope)
		byAction[binding.Action] = binding.Keys
	}
	if len(scopes) == 0 {
		t.Fatal("no wizard-finish bindings exported; the finish keymap is not a HostedKeys source")
	}

	force, ok := byAction["Toggle Force"]
	if !ok {
		for action, keys := range byAction {
			if len(keys) == 1 && keys[0] == "f" {
				force, ok = keys, true
				t.Logf("force toggle published under action %q", action)
				break
			}
		}
	}
	if !ok || len(force) != 1 || force[0] != "f" {
		t.Fatalf("finish force toggle not published as \"f\"; wizard-finish actions = %#v", byAction)
	}

	var quit []string
	for _, keys := range byAction {
		for _, k := range keys {
			if k == "esc" {
				quit = keys
			}
		}
	}
	if quit == nil {
		t.Fatalf("finish wizard esc binding not published; wizard-finish actions = %#v", byAction)
	}
}

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

// TestHostedKeysCoversEveryEmbeddedKeymap is the completeness guard. The
// declaration used to enumerate status.KeymapInfo() plus three hand-written
// rows, so it silently omitted every browser-mode and wizard-mode key — a host
// joining against it under-reported the collision set, and treemux's plan
// panel can only hand back keys that appear here. Every enabled binding of
// every embedded keymap must be published.
func TestHostedKeysCoversEveryEmbeddedKeymap(t *testing.T) {
	published := hostedKeySet()
	sources := map[string]keymap.TUIInfo{
		"status":  status.KeymapInfo(),
		"browser": keymap.MakeTUIInfo("flow-plan-browser", "flow", "", browser.NewKeyMap(nil)),
		"add":     keymap.MakeTUIInfo("flow-plan-add", "flow", "", add.NewKeyMap(nil)),
	}
	for name, info := range sources {
		for _, section := range info.Sections {
			for _, binding := range section.Bindings {
				if !binding.Enabled {
					continue
				}
				for _, k := range binding.Keys {
					if !published[k] {
						t.Errorf("%s keymap binds %q (%s/%s) but HostedKeys() never publishes it", name, k, section.Name, binding.Name)
					}
				}
			}
		}
	}
}

// TestHostedKeysDeclaresPreviouslyMissingKeys names the three keys the
// declaration was actually missing, so a regression reads as the concrete gap
// rather than as a coverage-loop failure.
func TestHostedKeysDeclaresPreviouslyMissingKeys(t *testing.T) {
	published := hostedKeySet()
	for key, why := range map[string]string{
		"ctrl+x": "browser finish-plan",
		"ctrl+s": "add-wizard submit",
		"ctrl+t": "add-wizard toggle claw",
	} {
		if !published[key] {
			t.Errorf("HostedKeys() omits %q (%s)", key, why)
		}
	}
}

// TestHostedKeysDoesNotDeclareCtrlG guards the rebind. ctrl+g is treemux's
// action-chord arm — permanently non-deferrable, because it is how the user
// reaches quit/reload/help/rail. flow's add wizard moved toggle-claw to ctrl+t
// rather than have a binding that is dead under the host and undeclarable as a
// winnable collision.
func TestHostedKeysDoesNotDeclareCtrlG(t *testing.T) {
	for _, binding := range HostedKeys().Bindings {
		for _, k := range binding.Keys {
			if k == "ctrl+g" {
				t.Fatalf("flow still binds ctrl+g (%s/%s) — the host's action-chord arm can never be handed over", binding.Scope, binding.Action)
			}
		}
	}
}

// TestHostedKeysAttachesResumeCollisionHint pins the ctrl+e hint. Resume was
// declared (via status.KeymapInfo) but carried no hint, unlike ctrl+f, so the
// second real collision was invisible in `treemux keys`.
func TestHostedKeysAttachesResumeCollisionHint(t *testing.T) {
	found := false
	for _, binding := range HostedKeys().Bindings {
		hasCtrlE := false
		for _, k := range binding.Keys {
			if k == "ctrl+e" {
				hasCtrlE = true
			}
		}
		if !hasCtrlE {
			continue
		}
		found = true
		if len(binding.CollisionHints) == 0 || binding.CollisionHints[0] != "treemux.jump_editor" {
			t.Errorf("ctrl+e binding %s/%s hints = %v, want [treemux.jump_editor]", binding.Scope, binding.Action, binding.CollisionHints)
		}
	}
	if !found {
		t.Fatal("no ctrl+e binding declared")
	}
}

// TestHostedKeysScopesAreQualified keeps the scope strings joinable: a host
// grouping by scope must be able to tell a browser binding from a status one.
func TestHostedKeysScopesAreQualified(t *testing.T) {
	seen := map[string]bool{}
	for _, binding := range HostedKeys().Bindings {
		if binding.Scope != "view-host" && !strings.Contains(binding.Scope, "/") {
			t.Errorf("unqualified scope %q on %s", binding.Scope, binding.Action)
		}
		seen[strings.SplitN(binding.Scope, "/", 2)[0]] = true
	}
	for _, want := range []string{"status", "browser", "wizard-add", "view-host"} {
		if !seen[want] {
			t.Errorf("no bindings published under scope prefix %q", want)
		}
	}
}
