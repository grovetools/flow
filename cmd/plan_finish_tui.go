package cmd

import (
	"fmt"

	"github.com/grovetools/core/tui/embed"

	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// runFinishTUI launches the embeddable plan-finish wizard via
// embed.RunStandalone. The wizard toggles IsEnabled on the passed-in
// items in-place; callers then execute the enabled Action closures.
func runFinishTUI(planName string, items []*finish.Item, branchIsMerged, branchExists bool) error {
	model := finish.New(finish.Config{
		PlanName:       planName,
		Items:          items,
		BranchIsMerged: branchIsMerged,
		BranchExists:   branchExists,
	})

	result, err := embed.RunStandalone(model)
	if err != nil {
		return fmt.Errorf("error running TUI: %w", err)
	}
	if result == nil {
		return fmt.Errorf("user aborted")
	}
	returned, ok := result.([]*finish.Item)
	if !ok || returned == nil {
		return fmt.Errorf("user aborted")
	}
	// The wizard mutates items in-place, but also ensure any
	// returned slice is reflected onto the caller's copy for safety.
	for i, wi := range returned {
		if i >= len(items) || items[i] == nil || wi == nil {
			continue
		}
		items[i].IsEnabled = wi.IsEnabled
	}
	return nil
}
