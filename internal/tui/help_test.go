package tui

import (
	"os"
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
