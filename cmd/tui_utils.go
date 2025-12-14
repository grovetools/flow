package cmd

import (
	"regexp"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// stripANSI removes ANSI escape codes from a string
func stripANSI(str string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansiRegex.ReplaceAllString(str, "")
}

type refreshTickMsg time.Time

const refreshInterval = 2 * time.Second

// refreshTick returns a command that sends a tick message for periodic refresh
func refreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return refreshTickMsg(t)
	})
}
