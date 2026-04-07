package cmd

import (
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/wizards/add"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

// PlanListKeymapInfo returns the keymap metadata for the flow plan list TUI.
// Used by the grove keys registry generator to aggregate all TUI keybindings.
func PlanListKeymapInfo() keymap.TUIInfo {
	return keymap.MakeTUIInfo(
		"flow-plan-list",
		"flow",
		"Plan browser and manager",
		browser.NewKeyMap(nil),
	)
}

// PlanAddKeymapInfo returns the keymap metadata for the flow plan add job TUI.
// Used by the grove keys registry generator to aggregate all TUI keybindings.
func PlanAddKeymapInfo() keymap.TUIInfo {
	return keymap.MakeTUIInfo(
		"flow-plan-add",
		"flow",
		"Add new job to a plan",
		add.NewKeyMap(nil),
	)
}

// PlanInitKeymapInfo returns the keymap metadata for the flow plan init TUI.
// Used by the grove keys registry generator to aggregate all TUI keybindings.
func PlanInitKeymapInfo() keymap.TUIInfo {
	return keymap.MakeTUIInfo(
		"flow-plan-init",
		"flow",
		"Create a new plan",
		planinit.NewKeyMap(nil),
	)
}

// PlanFinishKeymapInfo returns the keymap metadata for the flow plan finish TUI.
// Used by the grove keys registry generator to aggregate all TUI keybindings.
func PlanFinishKeymapInfo() keymap.TUIInfo {
	km := finish.NewKeyMap(nil)
	return keymap.MakeTUIInfo(
		"flow-plan-finish",
		"flow",
		"Finish and clean up a plan",
		km,
	)
}
