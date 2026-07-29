package status

import "testing"

// TestTitleDropsBeforeInformationColumns: TITLE restates the filename already
// in JOB, so it is given up before columns that carry information found
// nowhere else in the row.
func TestTitleDropsBeforeInformationColumns(t *testing.T) {
	m := newWideContentModel(t, 4)
	rank := map[string]int{}
	for i, col := range m.columnDropOrder() {
		if _, seen := rank[col]; !seen {
			rank[col] = i
		}
	}
	for _, col := range []string{"TOKENS", "MODEL", "TYPE", "STATUS", "UPDATED", "DURATION", "WORKTREE", "SESSION", "SKILL", "TEMPLATE"} {
		if rank["TITLE"] > rank[col] {
			t.Errorf("TITLE (rank %d) should drop before %s (rank %d)", rank["TITLE"], col, rank[col])
		}
	}
}
