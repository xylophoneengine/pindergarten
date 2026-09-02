package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTooSmallShowsResizeNotice(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.Update(tea.WindowSizeMsg{Width: 50, Height: 10})

	view := a.View()
	if !strings.Contains(view, "terminal too small: 50x10") {
		t.Fatalf("expected resize notice, got:\n%s", view)
	}
	if !strings.Contains(view, "zoom out") {
		t.Fatalf("expected zoom hint, got:\n%s", view)
	}
	if strings.Contains(view, "Overview") {
		t.Fatalf("tabs must not render while too small:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) > 10 {
		t.Fatalf("view has %d lines, want <= 10", len(lines))
	}
	for i, l := range lines {
		if lipgloss.Width(l) > 50 {
			t.Fatalf("line %d wider than 50: %q", i, l)
		}
	}
	if len(a.hits) != 0 {
		t.Fatalf("no mouse hits expected while too small, got %d", len(a.hits))
	}

	// Mouse input is inert while too small.
	tabBefore := a.tab
	a.Update(tea.MouseMsg{X: 1, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if a.tab != tabBefore {
		t.Fatalf("mouse click changed tab while too small")
	}

	// Growing the window restores the normal UI.
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(a.View(), "Overview") {
		t.Fatalf("expected normal UI after resize")
	}
}

func TestUnknownSizeIsNotTooSmall(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	if a.tooSmall() {
		t.Fatal("zero size (before WindowSizeMsg) must not count as too small")
	}
}
