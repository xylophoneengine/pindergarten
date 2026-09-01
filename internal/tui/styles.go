package tui

import "github.com/charmbracelet/lipgloss"

// Badge styles: READ ONLY renders on a red background, EDIT on an
// orange/yellow one. Text stays plain ASCII ("READ ONLY" / "EDIT").
var (
	badgeReadOnlyStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#ffffff"}).
				Background(lipgloss.AdaptiveColor{Light: "#c0392b", Dark: "#902020"}).
				Padding(0, 1)

	badgeEditStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#000000", Dark: "#000000"}).
			Background(lipgloss.AdaptiveColor{Light: "#f0a020", Dark: "#c98a10"}).
			Padding(0, 1)

	tabActiveStyle   = lipgloss.NewStyle().Bold(true).Underline(true)
	tabInactiveStyle = lipgloss.NewStyle().Faint(true)

	statusBarStyle = lipgloss.NewStyle().Faint(true)

	// modalStyle boxes the confirm-prompt line rendered above the status bar.
	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			Padding(0, 1)
)
