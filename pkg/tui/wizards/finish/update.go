package finish

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/embed"
)

// doneWithItems returns a tea.Cmd that emits embed.DoneMsg carrying
// the (possibly mutated) item slice as its Result. Hosts intercept
// the DoneMsg to execute any enabled Action closures.
func doneWithItems(items []*Item) tea.Cmd {
	return func() tea.Msg {
		return embed.DoneMsg{Result: items}
	}
}

// doneCancelled returns a tea.Cmd that emits embed.DoneMsg with a nil
// Result, signaling that the user dismissed the wizard without
// confirming.
func doneCancelled() tea.Cmd {
	return func() tea.Msg {
		return embed.DoneMsg{Result: nil}
	}
}

// clearExclusivePeers disables every OTHER item sharing keep's exclusive
// group, so enabling one member of a group is always a swap rather than an
// addition. Items with no group are untouched.
func clearExclusivePeers(items []*Item, keep *Item) {
	if keep == nil || keep.ExclusiveGroup == "" {
		return
	}
	for _, item := range items {
		if item == nil || item == keep {
			continue
		}
		if item.ExclusiveGroup == keep.ExclusiveGroup {
			item.IsEnabled = false
		}
	}
}

// resolveExclusiveGroups reduces every exclusive group to at most one enabled
// item, keeping the FIRST one in list order and disabling the rest. Hosts order
// each group best-first for exactly this reason: the finish factory emits
// archive_worktree ahead of prune_worktree so a bulk selection retires the
// worktree recoverably instead of deleting it.
func resolveExclusiveGroups(items []*Item) {
	claimed := make(map[string]bool)
	for _, item := range items {
		if item == nil || item.ExclusiveGroup == "" || !item.IsEnabled {
			continue
		}
		if claimed[item.ExclusiveGroup] {
			item.IsEnabled = false
			continue
		}
		claimed[item.ExclusiveGroup] = true
	}
}

// Update handles user input and lifecycle messages. It does not
// forward messages to any child component — the wizard owns all its
// own state.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case embed.SetWorkspaceMsg:
		// Workspace changed under us. The wizard's items are plan-
		// scoped and now stale — close the wizard by emitting a
		// cancel DoneMsg so the host returns to its previous view in
		// the new workspace context.
		return m, doneCancelled()

	case embed.FocusMsg, embed.BlurMsg:
		// The wizard has nothing async to start/stop on focus, so
		// these are no-ops today.
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, doneCancelled()

		case key.Matches(msg, m.keys.Help):
			m.helpModel.ShowAll = !m.helpModel.ShowAll

		case key.Matches(msg, m.keys.Down):
			// Move to next available item.
			for i := m.cursor + 1; i < len(m.items); i++ {
				if m.items[i] != nil && m.items[i].IsAvailable {
					m.cursor = i
					break
				}
			}

		case key.Matches(msg, m.keys.Up):
			// Move to previous available item.
			for i := m.cursor - 1; i >= 0; i-- {
				if m.items[i] != nil && m.items[i].IsAvailable {
					m.cursor = i
					break
				}
			}

		case key.Matches(msg, m.keys.Toggle):
			if m.cursor < len(m.items) && m.items[m.cursor] != nil && m.items[m.cursor].IsAvailable {
				picked := m.items[m.cursor]
				picked.IsEnabled = !picked.IsEnabled
				if picked.IsEnabled {
					clearExclusivePeers(m.items, picked)
				}
			}

		case key.Matches(msg, m.keys.SelectAll):
			for _, item := range m.items {
				if item != nil && item.IsAvailable {
					item.IsEnabled = true
				}
			}
			// Some items cannot coexist (archive vs prune of the same
			// worktree container). Without this, one "select all" keystroke
			// produced a selection the actions can never satisfy.
			resolveExclusiveGroups(m.items)

		case key.Matches(msg, m.keys.SelectNone):
			for _, item := range m.items {
				if item != nil {
					item.IsEnabled = false
				}
			}

		case m.showForce && key.Matches(msg, m.keys.ToggleForce):
			// Force is a run-scoped modifier, not an item: it changes HOW the
			// selected destructive items behave. Default off, and rendered
			// loudly by View when on.
			m.force = !m.force

		case key.Matches(msg, m.keys.Confirm):
			return m, doneWithItems(m.items)
		}
	}

	return m, nil
}
