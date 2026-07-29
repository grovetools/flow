package view

import (
	"sort"

	"github.com/grovetools/core/tui/hostedkeys"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/status"
	"github.com/grovetools/flow/pkg/tui/wizards/add"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// HostedKeyReference is the machine-readable key contract exported to hosts
// such as treemux, and HostedKeyBinding is one row of it.
//
// Both are aliases of the shared shapes in core/tui/hostedkeys. They used to
// be declared here, which made flow's struct the de-facto contract every other
// publisher had to mirror by hand — treemux's sidecar protocol carried a
// field-for-field copy so the JSON would line up. One declaration now backs all
// of them, and a host filters flow's claims and a sidecar's through the same
// code path. The names stay for flow's own callers; the shape is no longer
// flow's to change alone.
type (
	HostedKeyReference = hostedkeys.Reference
	HostedKeyBinding   = hostedkeys.Binding
)

// hostedCollisionHints names outer-host bindings that flow keys are known to
// collide with, keyed by the flow key. Advisory only — Keys is the stable
// comparison surface a host joins on — but hints make `treemux keys` readable.
// Keep the mechanism even when the current keymap has no known collisions.
var hostedCollisionHints = map[string][]string{}

// hostedKeymaps are the embedded sub-application keymaps whose bindings flow
// exposes to an outer host, each with the scope prefix it is published under.
//
// Deriving these instead of hand-listing them is the point. The declaration
// used to enumerate status.KeymapInfo() plus three hand-written rows, so it
// silently omitted every browser-mode and wizard-mode key — including
// ctrl+x (finish plan from the browser) and ctrl+s (wizard submit) — and a
// host joining against it under-reported the collision set. Adding a binding
// to any of these keymaps now publishes it automatically.
func hostedKeymaps() []struct {
	scope string
	info  keymap.TUIInfo
} {
	return []struct {
		scope string
		info  keymap.TUIInfo
	}{
		{"status", status.KeymapInfo()},
		{"browser", keymap.MakeTUIInfo(
			"flow-plan-browser", "flow",
			"Flow plan browser", browser.NewKeyMap(nil),
		)},
		{"wizard-add", keymap.MakeTUIInfo(
			"flow-plan-add", "flow",
			"Flow add-job wizard", add.NewKeyMap(nil),
		)},
		{"wizard-finish", keymap.MakeTUIInfo(
			"flow-plan-finish", "flow",
			"Flow finish-plan wizard", finish.NewKeyMap(nil),
		)},
	}
}

// HostedKeys returns Flow's hosted-app key declaration. Outer hosts can join
// this list with their own key registry by normalized chord; collision hints
// are advisory names, while Keys is the stable comparison surface.
//
// This is a real contract, not documentation: treemux's plan panel filters it
// against treemux's own key reference to decide which host globals to hand
// back to flow while the panel holds focus. A key that is not declared here
// cannot be won back.
func HostedKeys() HostedKeyReference {
	ref := HostedKeyReference{SchemaVersion: hostedkeys.SchemaVersion, App: "flow"}
	for _, source := range hostedKeymaps() {
		for _, section := range source.info.Sections {
			for _, binding := range section.Bindings {
				if !binding.Enabled || len(binding.Keys) == 0 {
					continue
				}
				ref.Bindings = append(ref.Bindings, hostedBinding(source.scope+"/"+section.Name, binding))
			}
		}
	}
	ref.Bindings = append(ref.Bindings,
		hostedViewHostBinding("add_plan", []string{"n"}, "add plan (browser mode)"),
		hostedViewHostBinding("add_job", []string{"a"}, "add job (status mode)"),
	)
	return ref
}

// hostedBinding converts one embedded sub-application binding. HostSwallowed
// is false: these keys are handled by the embedded model, not intercepted by
// flow's view host on the way down.
func hostedBinding(scope string, binding keymap.BindingInfo) HostedKeyBinding {
	keys := append([]string(nil), binding.Keys...)
	sort.Strings(keys)
	return HostedKeyBinding{
		Scope:          scope,
		Action:         binding.Name,
		Keys:           keys,
		Description:    binding.Description,
		ConfigKey:      binding.ConfigKey,
		CollisionHints: collisionHintsFor(keys),
	}
}

// hostedViewHostBinding builds a row for a key flow's view host consumes
// itself, before the embedded model (and therefore before any outer host's
// panel forwarding can reach the sub-model).
func hostedViewHostBinding(action string, keys []string, description string) HostedKeyBinding {
	return HostedKeyBinding{
		Scope:          "view-host",
		Action:         action,
		Keys:           keys,
		Description:    description,
		HostSwallowed:  true,
		CollisionHints: collisionHintsFor(keys),
	}
}

// collisionHintsFor returns the union of known host-collision hints for keys,
// in the order the keys appear. Returns nil when there are none, so the field
// stays omitted from JSON.
func collisionHintsFor(keys []string) []string {
	var hints []string
	seen := make(map[string]bool)
	for _, k := range keys {
		for _, hint := range hostedCollisionHints[k] {
			if seen[hint] {
				continue
			}
			seen[hint] = true
			hints = append(hints, hint)
		}
	}
	return hints
}
