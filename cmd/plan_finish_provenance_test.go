package cmd

import (
	"testing"

	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// provenanceItems is the checklist shape applyFinishSelection receives when the
// two provenance items are available.
func provenanceItems() []*finish.Item {
	return []*finish.Item{
		{ID: plan_finish.ItemEnvTeardown, Name: "Tear down environment", IsAvailable: true},
		{ID: plan_finish.ItemLedgerNote, Name: "Write plan ledger note", IsAvailable: true},
		{ID: plan_finish.ItemTombstoneRegistry, Name: "Tombstone worktree registry entry", IsAvailable: true},
		{ID: plan_finish.ItemArchiveWorktree, Name: "Archive git worktree", IsAvailable: true},
		{ID: plan_finish.ItemPruneWorktree, Name: "Prune git worktree", IsAvailable: true},
		{ID: plan_finish.ItemMarkFinished, Name: "Mark plan as finished", IsAvailable: true},
	}
}

// Provenance promotion is not one of the destructive actions the explicit
// flags choose between — it is the record of what the finish is about to
// retire. A finish that names only --prune-worktree must still record it.
func TestExplicitFlagFinishStillRecordsProvenance(t *testing.T) {
	items := provenanceItems()
	applyFinishSelection(items, plan_finish.Options{PruneWorktree: true})

	if !enabledByID(t, items, plan_finish.ItemLedgerNote) {
		t.Error("explicit-flag finish must still write the plan ledger note")
	}
	if !enabledByID(t, items, plan_finish.ItemTombstoneRegistry) {
		t.Error("explicit-flag finish must still tombstone the registry entry")
	}
}

func TestYesFinishRecordsProvenance(t *testing.T) {
	items := provenanceItems()
	applyFinishSelection(items, plan_finish.Options{Yes: true})

	if !enabledByID(t, items, plan_finish.ItemLedgerNote) {
		t.Error("--yes must write the plan ledger note")
	}
	if !enabledByID(t, items, plan_finish.ItemTombstoneRegistry) {
		t.Error("--yes must tombstone the registry entry")
	}
}

// An unavailable item is never force-enabled. The ledger reports itself
// unavailable under --no-ledger and when nb is absent, and the tombstone does
// the same when there is no registry entry — so "always enable" can never mean
// "run something that said it had nothing to do".
func TestUnavailableProvenanceItemsStayDisabled(t *testing.T) {
	for _, opts := range []plan_finish.Options{{}, {Yes: true}, {PruneWorktree: true}} {
		items := provenanceItems()
		plan_finish.ItemsByID(items, plan_finish.ItemLedgerNote).IsAvailable = false
		plan_finish.ItemsByID(items, plan_finish.ItemTombstoneRegistry).IsAvailable = false
		applyFinishSelection(items, opts)

		if enabledByID(t, items, plan_finish.ItemLedgerNote) {
			t.Errorf("opts %+v enabled an unavailable ledger item", opts)
		}
		if enabledByID(t, items, plan_finish.ItemTombstoneRegistry) {
			t.Errorf("opts %+v enabled an unavailable tombstone item", opts)
		}
	}
}
