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
				m.items[m.cursor].IsEnabled = !m.items[m.cursor].IsEnabled
			}

		case key.Matches(msg, m.keys.SelectAll):
			for _, item := range m.items {
				if item != nil && item.IsAvailable {
					item.IsEnabled = true
				}
			}

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
