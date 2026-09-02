package tui

import (
	"fmt"
	"strings"
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
	{"Global", "1-5", "jump to tab: Overview, CPU Map, VMs, Pending, Backups"},
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

	{"Set memory node", "digit 0-9", "stage a memory-node-only change to that node"},
	{"Set memory node", "esc", "cancel"},

	{"Pin wizard", "enter", "(proposal) accept the proposed pin placement"},
	{"Pin wizard", "m", "(proposal) switch to manual thread selection"},
	{"Pin wizard", "esc", "(proposal / manual) cancel back out"},
	{"Pin wizard", "h/l/j/k, up/down", "(manual) move the cursor across the node's cores"},
	{"Pin wizard", "n", "(manual) cycle the target node"},
	{"Pin wizard", "space", "(manual) toggle the thread pair under the cursor"},
	{"Pin wizard", "enter", "(manual) accept the manual selection"},

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

// helpPanel renders the help overlay: every helpKeys entry, grouped under
// a section heading, key and action separated by at least two spaces.
// Like every dialog (see renderDialog), it clamps to dw x budget rather
// than stretching to fill -- a long key list simply truncates on a short
// terminal rather than scrolling; there is no key input while it's open
// beyond the "any key closes it" contract, so nothing to scroll it with.
func helpPanel(dw, budget int) string {
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
	body := strings.TrimRight(b.String(), "\n")
	panel, _ := panelWrapH("Help", body, dw, budget, false)
	return panel
}
