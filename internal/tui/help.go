package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// helpEntry is one row of the single key-binding table this file owns:
// rendered (grouped by section) by the help overlay, and cross-checked by
// TestHelpKeysMatchREADME against every key mentioned in README.md's
// Keybindings section, so the two can never silently drift apart. key is
// compared to README's own Key column text with normalizeKey applied to
// both sides (backticks/backslashes stripped, whitespace trimmed) --
// action does not need to match README's Action column wording, only the
// key itself.
type helpEntry struct {
	section string
	key     string
	action  string
}

// helpKeys is grouped in the order the brief asked for: global, tabs, VMs,
// CPU Map, Pending, Backups, the mem-node picker, the pin wizard, then the
// apply flow. A key appearing in more than one context (e.g. "esc") is
// listed once per context deliberately -- the cross-check only cares about
// the set of unique keys, not row count.
var helpKeys = []helpEntry{
	{"Global", "? / F1", "show/hide this help"},
	{"Global", "1-6", "jump to tab: Overview, Topology, CPU Map, VMs, Pending, Backups"},
	{"Global", "Tab / Shift+Tab", "next / previous tab"},
	{"Global", "mouse click", "tab bar: jump to that tab"},
	{"Global", "e", "toggle edit mode (confirmed)"},
	{"Global", "a", "open the apply review for all staged operations (edit mode)"},
	{"Global", "r", "rescan host topology and libvirt domains"},
	{"Global", "q", "quit (confirms first if operations are pending)"},
	{"Global", "ctrl+c", "quit immediately, no confirmation"},

	{"CPU Map", "arrows / h j k l", "move the selected core"},

	{"VMs", "up/down / j k", "move the selected row"},
	{"VMs", "p", "open the pin wizard for the selected VM (edit mode)"},
	{"VMs", "s", "stage stripping the selected VM's existing pin (edit mode)"},
	{"VMs", "n", "open the set-memory-node picker for the selected VM (edit mode)"},

	{"Pending", "up/down / j k", "move the selected row"},
	{"Pending", "x", "remove a staged operation from the queue (edit mode)"},
	{"Pending", "d", "discard all staged operations (edit mode)"},

	{"Backups", "up/down / j k", "move the selected row"},
	{"Backups", "enter", "show a diff of the selected backup's XML against the domain's current XML"},
	{"Backups", "R", "stage restoring the selected backup (edit mode)"},
	{"Backups", "any key", "(diff shown) close the diff and return to the list"},

	{"Topology", "up/down / j k", "scroll the drawing"},
	{"Topology", "mouse click", "on a core box: jump to that core on the CPU Map tab"},

	{"Set memory node", "digit 0-9", "stage a memory-node-only change to that node (a pick crossing the VM's GPU node opens a yes/no confirm first)"},
	{"Set memory node", "esc", "cancel"},

	{"Pin wizard", "up/down / j k", "move between the node/within/threads/memory node/core-grid fields; inside the grid, up/down / j k move the cursor by row; at the grid's top/bottom edge they leave to the previous/next field"},
	{"Pin wizard", "left/right / h l", "cycle the focused field's value, move the caret within the threads field, or move the core-grid cursor"},
	{"Pin wizard", "backspace", "(threads field) delete the character before the caret"},
	{"Pin wizard", "space", "(core grid) toggle the cursor's core into/out of the threads field"},
	{"Pin wizard", "a", "re-fill the threads field from the current proposal"},
	{"Pin wizard", "A", "stage the current form (or click [A]pply); a placement crossing the VM's GPU node opens a yes/no confirm first"},
	{"Pin wizard", "C / esc", "cancel back out (or click [C]ancel)"},

	{"Apply / drift", "y", "(apply review) confirm and run the apply sequence"},
	{"Apply / drift", "n / esc", "(apply review, or any y/n confirmation) cancel"},
	{"Apply / drift", "up/down / j k", "(drift screen) select a drifted operation"},
	{"Apply / drift", "d", "(drift screen) discard the drifted operation"},
	{"Apply / drift", "w", "(drift screen) reopen the pin wizard for the drifted operation"},
	{"Apply / drift", "esc", "(drift screen) close back to browsing"},
	{"Apply / drift", "any key", "(results screen) dismiss the results screen and rescan"},
}

// normalizeKey strips the markdown emphasis README wraps key names in
// (backticks) and surrounding whitespace, so a README cell like
// "`1`-`5`" and this file's plain "1-5" compare equal.
func normalizeKey(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "`", ""))
}

// helpKeySet returns the set of unique, normalized keys in helpKeys.
func helpKeySet() map[string]bool {
	set := make(map[string]bool, len(helpKeys))
	for _, e := range helpKeys {
		set[normalizeKey(e.key)] = true
	}
	return set
}

// helpLines renders every helpKeys entry (grouped under a section
// heading, key and action separated by at least two spaces) word-wrapped
// to inner columns, one entry per visual line -- shared by helpPanel
// (which windows it to a scroll offset) and App.clampHelpScroll (which
// only needs the total line count).
func helpLines(inner int) []string {
	keyW := 0
	for _, e := range helpKeys {
		if w := len(e.key); w > keyW {
			keyW = w
		}
	}

	var b strings.Builder
	section := ""
	for _, e := range helpKeys {
		if e.section != section {
			if section != "" {
				b.WriteString("\n")
			}
			b.WriteString(tableHeaderStyle.Render(e.section))
			b.WriteString("\n")
			section = e.section
		}
		fmt.Fprintf(&b, "%s  %s\n", padRight(e.key, keyW), e.action)
	}
	text := strings.TrimRight(b.String(), "\n")

	if inner < 1 {
		inner = 1
	}
	return strings.Split(lipgloss.NewStyle().Width(inner).Render(text), "\n")
}

// helpPanel renders the help overlay: helpLines, scrolled to scroll (the
// caller clamps via App.clampHelpScroll) with a "lines N-M of T" footer
// once it doesn't all fit -- up/down/wheel scroll it while it's open (any
// other key closes it instead, see App.handleKey), the same pattern the
// Backups tab's diff view uses. Unlike every other dialog, it fills the
// full body height (budget, via panelInner's height parameter) rather
// than clamping to its own natural size -- the key list is long enough
// (39 rows and growing) to want all the room it can get.
func helpPanel(dw, budget, scroll int) string {
	lines := helpLines(dw - 2)
	contentBudget := budget - 2
	if contentBudget < 1 {
		contentBudget = 1
	}
	visible, offset, total := windowWithFooter(lines, contentBudget, scroll)
	body := strings.Join(visible, "\n")
	if footer := scrollFooter(offset, len(visible), total); footer != "" {
		body += "\n" + keyBarLabelStyle.Render(footer)
	}
	return panelInner("Help", body, dw, contentBudget)
}
