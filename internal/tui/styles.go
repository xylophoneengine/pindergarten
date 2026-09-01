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

	// overStyle marks an overcommitted node's "OVER" marker in red.
	overStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#c0392b", Dark: "#ff5f5f"})

	// pendingGlyphStyle distinguishes a pending-only claim on the CPU map
	// from an actually-pinned one, without changing its glyph.
	pendingGlyphStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#f0a020", Dark: "#c98a10"})

	// cursorStyle marks the selected CPU map cell reverse-video.
	cursorStyle = lipgloss.NewStyle().Reverse(true)

	// wizardHighlightStyle marks a proposed/selected thread on the pin
	// wizard's node map, distinct from the cursor and from pendingGlyphStyle.
	wizardHighlightStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#1f7a3f", Dark: "#4fd07a"})

	// warningStyle marks a wizard proposal's warning sentences.
	warningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#c0392b", Dark: "#ff5f5f"})
)
