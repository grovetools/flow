package add

import "testing"

// baseSlotCount is the number of original (Phase 4a) form slots
// — title, jobType, templateOrSkill, deps, prompt. Phase 4b appends
// conditionally-visible provider/model slots after these; the parity
// checks below cover only the base slots, and rely on a zero-value
// Model (no job type selected) leaving the 4b slots invisible so the
// wrap/first/last targets are unchanged.
const baseSlotCount = 5

// TestSlotNavParity locks in the Phase 4a invariant: the slot
// descriptor table must classify and wrap exactly like the previous
// hardcoded index logic. Slots 0 and 4 are text entries; 1, 2, and 3
// are lists; the focus cycle wraps 4→0 and 0→4.
func TestSlotNavParity(t *testing.T) {
	if len(addFormSlots) < baseSlotCount {
		t.Fatalf("expected at least %d slots, got %d", baseSlotCount, len(addFormSlots))
	}

	var m Model
	for idx := 0; idx < baseSlotCount; idx++ {
		m.focusIndex = idx

		// Old hardcoded classification from update.go:
		//   inTextInput := focusIndex == 0 || focusIndex == 4
		//   inList      := focusIndex == 1 || focusIndex == 2 || focusIndex == 3
		wantText := idx == 0 || idx == 4
		wantList := idx == 1 || idx == 2 || idx == 3

		gotText := m.currentSlotKind() == slotText
		gotList := m.currentSlotKind() == slotList

		if gotText != wantText {
			t.Errorf("slot %d: currentSlotKind()==slotText = %v, want %v", idx, gotText, wantText)
		}
		if gotList != wantList {
			t.Errorf("slot %d: currentSlotKind()==slotList = %v, want %v", idx, gotList, wantList)
		}
		if !addFormSlots[idx].visible(&m) {
			t.Errorf("slot %d: visible() = false, want true (all slots visible in Phase 4a)", idx)
		}
	}

	// Wrap parity with the old `if focusIndex > 4 { 0 }` /
	// `if focusIndex < 0 { 4 }` arithmetic.
	m.focusIndex = 4
	if got := m.nextVisibleSlot(4); got != 0 {
		t.Errorf("nextVisibleSlot(4) = %d, want 0", got)
	}
	if got := m.prevVisibleSlot(0); got != 4 {
		t.Errorf("prevVisibleSlot(0) = %d, want 4", got)
	}

	// Sequential parity for the interior indices.
	for idx := 0; idx < 4; idx++ {
		if got := m.nextVisibleSlot(idx); got != idx+1 {
			t.Errorf("nextVisibleSlot(%d) = %d, want %d", idx, got, idx+1)
		}
	}
	for idx := 1; idx < 5; idx++ {
		if got := m.prevVisibleSlot(idx); got != idx-1 {
			t.Errorf("prevVisibleSlot(%d) = %d, want %d", idx, got, idx-1)
		}
	}

	// First/last visible slots match the old GoTop/GoBottom targets.
	if got := m.firstVisibleSlot(); got != 0 {
		t.Errorf("firstVisibleSlot() = %d, want 0", got)
	}
	if got := m.lastVisibleSlot(); got != 4 {
		t.Errorf("lastVisibleSlot() = %d, want 4", got)
	}
}
