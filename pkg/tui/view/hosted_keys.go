package view

import (
	"sort"

	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/flow/pkg/tui/status"
)

// HostedKeyReference is the machine-readable key contract exported to hosts
// such as treemux. SchemaVersion changes only for incompatible shape changes.
type HostedKeyReference struct {
	SchemaVersion int                `json:"schema_version"`
	App           string             `json:"app"`
	Bindings      []HostedKeyBinding `json:"bindings"`
}

// HostedKeyBinding describes either an embedded status binding or a key
// intercepted by flow's view host before the embedded model/outer host sees it.
type HostedKeyBinding struct {
	Scope          string   `json:"scope"`
	Action         string   `json:"action"`
	Keys           []string `json:"keys"`
	Description    string   `json:"description"`
	ConfigKey      string   `json:"config_key,omitempty"`
	HostSwallowed  bool     `json:"host_swallowed"`
	CollisionHints []string `json:"collision_hints,omitempty"`
}

// HostedKeys returns Flow's hosted-app key declaration. Outer hosts can join
// this list with their own key registry by normalized chord; collision hints
// are advisory names, while Keys is the stable comparison surface.
func HostedKeys() HostedKeyReference {
	ref := HostedKeyReference{SchemaVersion: 1, App: "flow"}
	for _, section := range status.KeymapInfo().Sections {
		for _, binding := range section.Bindings {
			if !binding.Enabled || len(binding.Keys) == 0 {
				continue
			}
			ref.Bindings = append(ref.Bindings, hostedStatusBinding(section.Name, binding))
		}
	}
	ref.Bindings = append(ref.Bindings,
		HostedKeyBinding{Scope: "view-host", Action: "finish_plan", Keys: []string{"ctrl+f"}, Description: "open Finish Plan", HostSwallowed: true, CollisionHints: []string{"treemux.nav_workspaces"}},
		HostedKeyBinding{Scope: "view-host", Action: "add_plan", Keys: []string{"n"}, Description: "add plan (browser mode)", HostSwallowed: true},
		HostedKeyBinding{Scope: "view-host", Action: "add_job", Keys: []string{"a"}, Description: "add job (status mode)", HostSwallowed: true},
	)
	return ref
}

func hostedStatusBinding(section string, binding keymap.BindingInfo) HostedKeyBinding {
	keys := append([]string(nil), binding.Keys...)
	sort.Strings(keys)
	return HostedKeyBinding{
		Scope:       "status/" + section,
		Action:      binding.Name,
		Keys:        keys,
		Description: binding.Description,
		ConfigKey:   binding.ConfigKey,
	}
}
