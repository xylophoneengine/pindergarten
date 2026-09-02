package tui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// keybindingsSection returns the portion of readme from "## Keybindings"
// up to (not including) the next top-level "## " heading, or "" if the
// section isn't found.
func keybindingsSection(readme string) string {
	start := strings.Index(readme, "## Keybindings")
	if start < 0 {
		return ""
	}
	rest := readme[start:]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return rest
	}
	if end := strings.Index(rest[nl:], "\n## "); end >= 0 {
		return rest[:nl+end]
	}
	return rest
}

// readmeKeySet parses every pipe-table row in section and returns the set
// of unique, normalized first-column ("Key") values -- skipping header and
// separator rows.
func readmeKeySet(section string) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 2 {
			continue
		}
		key := strings.TrimSpace(cells[1])
		if key == "" || key == "Key" || strings.Trim(key, "-") == "" {
			continue // header or "|---|---|" separator row
		}
		set[normalizeKey(key)] = true
	}
	return set
}

// TestHelpKeysMatchREADME cross-checks help.go's single key-binding table
// against README.md's Keybindings section: every key mentioned in one must
// appear in the other, so the help overlay and the docs can't silently
// drift apart.
func TestHelpKeysMatchREADME(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	section := keybindingsSection(string(b))
	if section == "" {
		t.Fatal("README.md has no ## Keybindings section")
	}
	readmeKeys := readmeKeySet(section)
	if len(readmeKeys) == 0 {
		t.Fatal("parsed zero keys out of README's Keybindings section")
	}
	ownKeys := helpKeySet()

	for k := range readmeKeys {
		if !ownKeys[k] {
			t.Errorf("README mentions key %q, not present in help.go's helpKeys", k)
		}
	}
	for k := range ownKeys {
		if !readmeKeys[k] {
			t.Errorf("help.go's helpKeys has key %q, not mentioned anywhere in README's Keybindings section", k)
		}
	}
}

// keyHandlerFiles are scanned by TestKeyHandlersDocumentedInHelp for
// isRune(msg, 'x')/tea.Key* literals used in key handling.
var keyHandlerFiles = []string{
	"app.go", "wizard.go", "memnode.go", "pending.go", "backups.go",
}

var (
	isRuneRe = regexp.MustCompile(`isRune\(msg,\s*'(.)'\)`)
	teaKeyRe = regexp.MustCompile(`tea\.Key(\w+)`)
)

// teaKeyMarkers maps a tea.Key* identifier (as it appears in source, e.g.
// "Up" for tea.KeyUp) to the substrings -- any one is enough -- that must
// appear somewhere across helpKeys' own key+action text. Multiple
// physical keys are often documented together there (e.g. "arrows / h j
// k l" covers KeyUp/KeyDown/KeyLeft/KeyRight on the CPU Map tab, "up/down
// / j k" covers KeyUp/KeyDown everywhere else), so this checks for
// whichever shared description applies rather than requiring an exact
// per-key row. An identifier that names an actual key but has no entry
// here (a newly wired tea.KeyHome, say) fails the suite -- see
// teaKeyNonKeyIdents just below for the identifiers this regex matches
// that aren't a key at all and so are deliberately exempt.
var teaKeyMarkers = map[string][]string{
	"Up":        {"up/down", "arrows"},
	"Down":      {"up/down", "arrows"},
	"Left":      {"arrows"},
	"Right":     {"arrows"},
	"Enter":     {"enter"},
	"Esc":       {"esc"},
	"Tab":       {"Tab"},
	"ShiftTab":  {"Shift+Tab"},
	"Space":     {"space"},
	"F1":        {"F1"},
	"CtrlC":     {"ctrl+c"},
	"Backspace": {"backspace"},
}

// teaKeyNonKeyIdents are tea.Key* identifiers the `tea\.Key(\w+)` regex
// matches that don't actually name a specific keypress, so they have no
// business appearing in teaKeyMarkers: "Msg" (tea.KeyMsg, the message
// type itself, not a key) and "Runes" (tea.KeyRunes, the Type value
// meaning "one or more printable runes" -- the actual rune is checked via
// isRuneRe elsewhere, not here). Anything else found in a scanned file
// must have a teaKeyMarkers entry; unlike the old "unknown -> skip"
// behavior, an identifier in neither this set nor teaKeyMarkers (e.g. a
// newly wired tea.KeyHome) now fails the suite instead of silently
// passing uncaught.
var teaKeyNonKeyIdents = map[string]bool{
	"Msg":   true,
	"Runes": true,
}

// stripLineComments blanks out any "// ..." trailing each line -- a cheap
// stand-in for full AST parsing, just enough to keep a key mentioned only
// in a comment from being mistaken for a real handler.
func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if idx := strings.Index(l, "//"); idx >= 0 {
			lines[i] = l[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// helpKeyTokens splits every helpEntry's key text into individual tokens
// (on spaces, '/', ',') -- so a combined description like "arrows / h j
// k l" or "up/down / j k" is recognized as covering each of the
// individual "h"/"j"/"k"/"l" (or "up"/"down"... though isRune only ever
// sees single runes, never those words) keys it lists, not matched only
// as one long, unsplit string.
func helpKeyTokens() map[string]bool {
	isSep := func(r rune) bool { return r == ' ' || r == '/' || r == ',' }
	tokens := map[string]bool{}
	for _, e := range helpKeys {
		for _, tok := range strings.FieldsFunc(e.key, isSep) {
			tokens[tok] = true
		}
	}
	return tokens
}

// TestKeyHandlersDocumentedInHelp scans every key-handling file for
// isRune(msg, 'X') and tea.Key* literals and checks each is actually
// documented somewhere in help.go's helpKeys table -- so a key wired up
// only in a handler (never added to helpKeys/README) fails the suite
// instead of silently going undocumented. This is a canary, not an exact
// verifier: adding a fake `case isRune(msg, 'Z'):` to any scanned file, or
// a fake `case msg.Type == tea.KeyHome:` (an identifier with neither a
// teaKeyMarkers entry nor a teaKeyNonKeyIdents exemption), each makes it
// fail (confirmed during development, then removed).
func TestKeyHandlersDocumentedInHelp(t *testing.T) {
	tokens := helpKeyTokens()
	var allText strings.Builder
	for _, e := range helpKeys {
		allText.WriteString(e.key)
		allText.WriteString(" ")
		allText.WriteString(e.action)
		allText.WriteString("\n")
	}
	combined := allText.String()

	for _, name := range keyHandlerFiles {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		src := stripLineComments(string(b))

		for _, m := range isRuneRe.FindAllStringSubmatch(src, -1) {
			key := m[1]
			if !tokens[key] {
				t.Errorf("%s: isRune(msg, '%s') has no matching key/token in help.go's helpKeys", name, key)
			}
		}
		for _, m := range teaKeyRe.FindAllStringSubmatch(src, -1) {
			if teaKeyNonKeyIdents[m[1]] {
				continue
			}
			markers, known := teaKeyMarkers[m[1]]
			if !known {
				t.Errorf("%s: tea.Key%s is not in teaKeyMarkers or teaKeyNonKeyIdents -- add a marker (or a non-key skip entry) for it", name, m[1])
				continue
			}
			found := false
			for _, marker := range markers {
				if strings.Contains(combined, marker) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: tea.Key%s has no matching entry in helpKeys (want one of %q)", name, m[1], markers)
			}
		}
	}
}
