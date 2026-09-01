// Package tui: shared layout helpers -- bordered/titled panels, table-cell
// fitting, progress bars, and side-by-side/stacked panel arrangement. Every
// render function in this package is width-aware: tables and grids never
// wrap (they truncate, via these helpers), only prose does (via lipgloss's
// own word-wrap). See docs/superpowers/specs -- TUI section.
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// twoColThreshold is the total body width at or above which a tab's
// primary/detail panels sit side by side instead of stacked.
const twoColThreshold = 120

// sideCardMinWidth is the minimum per-card width (Overview's per-node
// panels) below which cards stack instead of sitting side by side.
const sideCardMinWidth = 50

// fallbackWidth is used wherever a.width is 0 (no WindowSizeMsg has landed
// yet): generous enough that no table column gets dropped and nothing
// truncates, matching the pre-panel behavior tests rely on.
const fallbackWidth = 300

// effectiveWidth returns w, or fallbackWidth when w is unset (<= 0).
func effectiveWidth(w int) int {
	if w <= 0 {
		return fallbackWidth
	}
	return w
}

// splitBodyWidth returns the primary (table/grid) and secondary (detail)
// panel widths for a tab body of total width w: side by side (roughly
// half each, with a 1-column gap) at or above twoColThreshold, else both
// get the full width (stacked).
func splitBodyWidth(w int) (primary, secondary int, sideBySide bool) {
	if w >= twoColThreshold {
		left := w / 2
		return left, w - left - 1, true
	}
	return w, w, false
}

// equalSplit returns the per-card width for n equal cards across total
// width w, and whether they fit side by side (each >= minW) rather than
// needing to stack at full width.
func equalSplit(w, n, minW int) (cardW int, sideBySide bool) {
	if n <= 1 {
		return w, false
	}
	each := (w - (n - 1)) / n
	if each >= minW {
		return each, true
	}
	return w, false
}

// joinPanels arranges pre-rendered equal-height-ish panels side by side
// (with 1-column gaps) when sideBySide, else stacks them one per line.
func joinPanels(panels []string, sideBySide bool) string {
	if len(panels) == 0 {
		return ""
	}
	if !sideBySide {
		return strings.Join(panels, "\n")
	}
	args := make([]string, 0, len(panels)*2-1)
	for i, p := range panels {
		if i > 0 {
			args = append(args, " ")
		}
		args = append(args, p)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, args...)
}

// ansiTruncate truncates s to width w, ANSI-aware, appending ".." when it
// actually had to cut (and there's room for the marker).
func ansiTruncate(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	if w <= 2 {
		return ansi.Truncate(s, w, "")
	}
	return ansi.Truncate(s, w, "..")
}

// truncateLines truncates (never wraps) every line of body to at most w
// cells wide.
func truncateLines(body string, w int) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = ansiTruncate(l, w)
	}
	return strings.Join(lines, "\n")
}

// padRight right-pads s with spaces to width w (a no-op if s is already >=
// w wide).
func padRight(s string, w int) string {
	pad := w - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// spliceTitle overlays " title " onto a rendered top-border line (plain
// box-drawing runes, no ANSI -- safe to slice by rune), just after its
// left corner. lipgloss v1 has no native bordered-title support, so panels
// splice their own.
func spliceTitle(border, title string) string {
	if title == "" {
		return border
	}
	r := []rune(border)
	if len(r) < 3 {
		return border
	}
	label := []rune(" " + title + " ")
	avail := len(r) - 2 // room between the two corners
	if avail <= 0 {
		return border
	}
	if len(label) > avail {
		label = []rune(ansiTruncate(string(label), avail))
	}
	copy(r[1:1+len(label)], label)
	return string(r)
}

// panelInner assembles the titled border box around body, which the
// caller has already fit to w-2 columns (via truncateLines for tables/
// grids, or lipgloss's own Width-wrap for prose).
func panelInner(title, body string, w int) string {
	if w < 4 {
		w = 4
	}
	inner := w - 2
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(inner).Render(body)
	if title == "" {
		return box
	}
	lines := strings.SplitN(box, "\n", 2)
	lines[0] = panelTitleStyle.Render(spliceTitle(lines[0], title))
	if len(lines) == 1 {
		return lines[0]
	}
	return lines[0] + "\n" + lines[1]
}

// panel renders body (tabular/grid content) inside a titled rounded-border
// box of total width w (borders count toward w): every line is truncated,
// never wrapped, to fit.
func panel(title, body string, w int) string {
	if w < 4 {
		w = 4
	}
	return panelInner(title, truncateLines(body, w-2), w)
}

// panelWrap renders body (prose: sentences, messages, diffs) inside a
// titled rounded-border box of total width w, word-wrapping via lipgloss
// instead of truncating.
func panelWrap(title, body string, w int) string {
	if w < 4 {
		w = 4
	}
	inner := w - 2
	wrapped := lipgloss.NewStyle().Width(inner).Render(body)
	return panelInner(title, wrapped, w)
}

// bar renders a fixed-width single-tone ASCII progress bar, e.g.
// "[||||......]", filled cells in fillStyle and the rest dim.
func bar(width int, frac float64, fillStyle lipgloss.Style) string {
	return dualBar(width, frac, 0, fillStyle, fillStyle)
}

// dualBar renders a fixed-width two-tone ASCII progress bar: aFrac cells
// filled in aStyle, then bFrac cells in bStyle, the remainder dim.
func dualBar(width int, aFrac, bFrac float64, aStyle, bStyle lipgloss.Style) string {
	if width < 3 {
		width = 3
	}
	inner := width - 2
	aN := barCells(inner, aFrac)
	bN := barCells(inner, bFrac)
	if aN+bN > inner {
		bN = inner - aN
	}
	if bN < 0 {
		bN = 0
	}
	rest := inner - aN - bN
	return "[" + aStyle.Render(strings.Repeat("|", aN)) +
		bStyle.Render(strings.Repeat("|", bN)) +
		barEmptyStyle.Render(strings.Repeat(".", rest)) + "]"
}

func barCells(inner int, frac float64) int {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	n := int(float64(inner)*frac + 0.5)
	if n > inner {
		n = inner
	}
	return n
}

// keyHint is one "[key] label" token in the bottom key bar.
type keyHint struct{ key, label string }

// renderKeyHint styles one key hint: bold key, dim label.
func renderKeyHint(h keyHint) string {
	return keyBarKeyStyle.Render("["+h.key+"]") + " " + keyBarLabelStyle.Render(h.label)
}

// styleStatus colors a status/result line by what it reports: red for an
// error/FAILED line, green for a staged/OK one, else unstyled.
func styleStatus(s string) string {
	switch {
	case strings.Contains(s, "FAILED"), strings.Contains(s, "error"):
		return statusErrStyle.Render(s)
	case strings.HasPrefix(s, "staged:"), strings.Contains(s, "OK "):
		return statusOKStyle.Render(s)
	default:
		return s
	}
}
