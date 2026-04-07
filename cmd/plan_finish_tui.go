package cmd

import (
	"fmt"

	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// runFinishTUI launches the embeddable plan-finish wizard via
// embed.RunStandalone. The cleanupItems passed in are the CLI's own
// per-action state (with Check and Action closures bound to
// plan_finish.go's local variables); this function mirrors them into
// finish.Item values for the wizard, lets the wizard mutate the
// IsEnabled flags, and then copies them back onto the originals so
// the calling CLI code can execute the enabled Action closures.
func runFinishTUI(planName string, items []*cleanupItem, branchIsMerged bool, branchExists bool) error {
	// Mirror cmd-local cleanupItems into wizard items. The wizard
	// never calls Action/Check, so we leave those nil on the mirror.
	wItems := make([]*finish.Item, len(items))
	for i, it := range items {
		if it == nil {
			continue
		}
		details := make([]finish.RepoStatus, len(it.Details))
		for j, d := range it.Details {
			details[j] = finish.RepoStatus{Name: d.Name, Status: d.Status}
		}
		wItems[i] = &finish.Item{
			Name:        it.Name,
			Status:      it.Status,
			IsAvailable: it.IsAvailable,
			IsEnabled:   it.IsEnabled,
			Details:     details,
		}
	}

	model := finish.New(finish.Config{
		PlanName:       planName,
		Items:          wItems,
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

	// Propagate IsEnabled toggles back onto the CLI's cleanupItems
	// so the existing execution loop in runPlanFinish picks them up.
	for i, wi := range returned {
		if i >= len(items) || items[i] == nil || wi == nil {
			continue
		}
		items[i].IsEnabled = wi.IsEnabled
	}

	return nil
}
