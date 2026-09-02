package tui

import "github.com/charmbracelet/lipgloss"

// Shared adaptive colors, named by role so every style below picks from the
// same small palette (readable in both light and dark terminals).
var (
	accentColor  = lipgloss.AdaptiveColor{Light: "#1f6feb", Dark: "#58a6ff"}
	dimColor     = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#999999"}
	okColor      = lipgloss.AdaptiveColor{Light: "#1f7a3f", Dark: "#4fd07a"}
	errColor     = lipgloss.AdaptiveColor{Light: "#c0392b", Dark: "#ff5f5f"}
	pendingColor = lipgloss.AdaptiveColor{Light: "#f0a020", Dark: "#c98a10"}
)

// Badge styles: READ ONLY renders on a red background, EDIT on an
// orange/yellow one. Text stays plain ASCII ("READ ONLY" / "EDIT").
var (
	badgeReadOnlyStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#ffffff"}).
				Background(errColor).
				Padding(0, 1)

	badgeEditStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#000000", Dark: "#000000"}).
			Background(pendingColor).
			Padding(0, 1)

	// tabActiveStyle renders the active tab as a filled, colored pill;
	// tabInactiveStyle dims the rest. Both pad identically (0, 1) so the
	// style -- not extra bracket/space characters -- is what marks the
	// active tab (see renderTabs); active/inactive is queried directly
	// (App.tab) in tests rather than by grepping for a plain-text marker.
	tabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#ffffff"}).
			Background(accentColor).
			Padding(0, 1)
	tabInactiveStyle = lipgloss.NewStyle().Foreground(dimColor).Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().Faint(true)

	// keyBarKeyStyle/keyBarLabelStyle style the bottom key bar's
	// "[key] label" hints: bold accent-colored key, dim label.
	keyBarKeyStyle   = lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	keyBarLabelStyle = lipgloss.NewStyle().Foreground(dimColor)

	// statusErrStyle/statusOKStyle color the status line by outcome kind
	// (red for FAILED/errors, green for staged/OK).
	statusErrStyle = lipgloss.NewStyle().Foreground(errColor)
	statusOKStyle  = lipgloss.NewStyle().Foreground(okColor)

	// panelTitleStyle styles a panel's spliced-in border title.
	panelTitleStyle = lipgloss.NewStyle().Bold(true)

	// tableHeaderStyle marks a table's header row: bold, underlined,
	// accent-colored.
	tableHeaderStyle = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(accentColor)

	// selectedRowStyle highlights a selected table row with a background
	// tint, rather than full reverse video.
	selectedRowStyle = lipgloss.NewStyle().
				Background(lipgloss.AdaptiveColor{Light: "#d8e0f0", Dark: "#2b3340"})

	// overStyle marks an overcommitted node's "OVER" marker in red.
	overStyle = lipgloss.NewStyle().Bold(true).Foreground(errColor)

	// pendingGlyphStyle distinguishes a pending-only claim on the CPU map
	// from an actually-pinned one, without changing its glyph.
	pendingGlyphStyle = lipgloss.NewStyle().Foreground(pendingColor)

	// cursorStyle marks the selected CPU map cell (and other single-item
	// list cursors) reverse-video.
	cursorStyle = lipgloss.NewStyle().Reverse(true)

	// wizardHighlightStyle marks a proposed/selected thread on the pin
	// wizard's node map, distinct from the cursor and from pendingGlyphStyle.
	wizardHighlightStyle = lipgloss.NewStyle().Bold(true).Foreground(okColor)

	// warningStyle marks a wizard proposal's warning sentences and "[!]"
	// flag badges.
	warningStyle = lipgloss.NewStyle().Foreground(errColor)

	// gpuWarningStyle is warningStyle's "loud" variant: bold on top of the
	// same red, for the one warning in this app that gates a confirm
	// (crossing a VM's GPU node -- the pin wizard form, the mem-node
	// picker, and the Pending tab's row for a staged op that did) rather
	// than being purely informational.
	gpuWarningStyle = lipgloss.NewStyle().Bold(true).Foreground(errColor)

	// barEmptyStyle renders the unfilled portion of a progress bar.
	barEmptyStyle = lipgloss.NewStyle().Foreground(dimColor)

	// barFilledStyle renders a bar's solid (pinned/used) portion.
	barFilledStyle = lipgloss.NewStyle().Foreground(okColor)

	// barPendingStyle renders a dual-tone bar's pending-only portion.
	barPendingStyle = lipgloss.NewStyle().Foreground(pendingColor)
)
