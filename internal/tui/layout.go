// Package tui: shared layout helpers -- bordered/titled panels, table-cell
// fitting, progress bars, and side-by-side/stacked panel arrangement. Every
// render function in this package is width-aware: tables and grids never
// wrap (they truncate, via these helpers), only prose does (via lipgloss's
// own word-wrap). See docs/superpowers/specs -- TUI section.
package tui

import (
	"fmt"
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

// fallbackHeight mirrors fallbackWidth for a.height: generous enough that
// nothing scrolls/truncates before the first WindowSizeMsg lands.
const fallbackHeight = 200

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

// equalSplit returns each of n cards' width across total width w (the last
// few a column wider than the rest when w - (n-1) doesn't divide evenly by
// n, so the row's right edge is used rather than left as a gap), and
// whether they fit side by side (the smallest share >= minW) rather than
// needing to stack at full width (every card then gets w).
func equalSplit(w, n, minW int) (widths []int, sideBySide bool) {
	if n <= 1 {
		return []int{w}, false
	}
	share := w - (n - 1) // reserve the n-1 1-column gaps between cards
	base, extra := share/n, share%n
	if base < minW {
		widths = make([]int, n)
		for i := range widths {
			widths[i] = w
		}
		return widths, false
	}
	widths = make([]int, n)
	for i := range widths {
		widths[i] = base
		if i >= n-extra { // the last `extra` cards get the +1 remainder
			widths[i]++
		}
	}
	return widths, true
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

// panelH is panel with an additional height clip: body is truncated
// (top-down, no scroll tracking -- callers whose content has a natural
// "keep this visible" target should pre-slice it, e.g. via scrollWindow,
// before calling this) to at most h-2 content lines. Returns the panel
// plus how many content lines actually survived, so the caller can drop
// any hits recorded past that point via clipHitsToWindow.
func panelH(title, body string, w, h int) (string, int) {
	if w < 4 {
		w = 4
	}
	if h < 3 {
		h = 3
	}
	lines := strings.Split(truncateLines(body, w-2), "\n")
	budget := h - 2
	if budget < 1 {
		budget = 1
	}
	if len(lines) > budget {
		lines = lines[:budget]
	} else {
		budget = len(lines)
	}
	return panelInner(title, strings.Join(lines, "\n"), w), budget
}

// panelWrapH is panelWrap with the same height clip as panelH (word-wrap
// first, then truncate top-down to at most h-2 lines). Returns the panel
// plus how many content lines survived.
func panelWrapH(title, body string, w, h int) (string, int) {
	if w < 4 {
		w = 4
	}
	if h < 3 {
		h = 3
	}
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	wrapped := lipgloss.NewStyle().Width(inner).Render(body)
	lines := strings.Split(wrapped, "\n")
	budget := h - 2
	if budget < 1 {
		budget = 1
	}
	if len(lines) > budget {
		lines = lines[:budget]
	} else {
		budget = len(lines)
	}
	return panelInner(title, strings.Join(lines, "\n"), w), budget
}

// minSecondaryBudget is the smallest height budget worth giving a stacked
// tab's secondary (detail) panel; below that it's dropped entirely rather
// than rendered as an unreadable sliver.
const minSecondaryBudget = 4

// splitStackedBudget divides a stacked tab's total body-height budget
// between its primary (table/grid) panel and its secondary (detail) one:
// the primary gets only what it naturally needs (primaryNaturalLines + 2
// for its borders), so a short list doesn't starve the detail panel, and
// the secondary gets the rest -- down to a minimum reserve, below which
// it's dropped entirely (budget 0) rather than rendered as an unreadable
// sliver.
func splitStackedBudget(budget, primaryNaturalLines int) (primary, secondary int) {
	if budget <= minSecondaryBudget+3 {
		return budget, 0
	}
	need := primaryNaturalLines + 2
	if max := budget - minSecondaryBudget; need > max {
		need = max
	}
	if need < 3 {
		need = 3
	}
	return need, budget - need
}

// scrollWindow slices lines (already-built content, one row per visual
// line) down to at most budget lines, keeping index `keep` visible by
// pinning it to the bottom edge once the content overflows. The offset is
// always recomputed fresh from (len(lines), keep, budget) -- callers don't
// need to persist any scroll state for this. offset/total are both 0 when
// nothing was trimmed.
func scrollWindow(lines []string, budget, keep int) (window []string, offset, total int) {
	total = len(lines)
	if budget <= 0 || total <= budget {
		return lines, 0, 0
	}
	offset = keep - budget + 1
	if offset < 0 {
		offset = 0
	}
	if max := total - budget; offset > max {
		offset = max
	}
	return lines[offset : offset+budget], offset, total
}

// clampScroll clamps an explicit (user-driven) scroll offset into
// [0, max(0, total-budget)].
func clampScroll(offset, budget, total int) int {
	if offset < 0 {
		offset = 0
	}
	if budget <= 0 || total <= budget {
		return 0
	}
	if max := total - budget; offset > max {
		offset = max
	}
	return offset
}

// windowAt slices lines to at most budget of them, starting at an explicit
// (already-clamped-by-the-caller-via-clampScroll) offset. offset/total are
// both 0 when nothing was trimmed, matching scrollWindow's convention.
func windowAt(lines []string, budget, offset int) (window []string, usedOffset, total int) {
	total = len(lines)
	if budget <= 0 || total <= budget {
		return lines, 0, 0
	}
	offset = clampScroll(offset, budget, total)
	return lines[offset : offset+budget], offset, total
}

// scrollFooter renders a "lines N-M of T" footer for a scrolled view, or ""
// when nothing was trimmed (total == 0, scrollWindow/windowAt's shared
// convention for "it all fit already").
func scrollFooter(offset, shown, total int) string {
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("lines %d-%d of %d", offset+1, offset+shown, total)
}

// clipHitsToWindow drops hits whose row falls outside [0, budget) --
// for content that was simply top-truncated (no scroll offset) to fit a
// height budget, e.g. a CPU Map node panel or a wizard/mem-node-picker
// panel.
func clipHitsToWindow(hits []hit, budget int) []hit {
	if budget <= 0 {
		return nil
	}
	out := make([]hit, 0, len(hits))
	for _, h := range hits {
		if h.y0 < budget {
			out = append(out, h)
		}
	}
	return out
}

// dialogMaxWidth is the widest a centered dialog (wizard, mem-node picker,
// confirm, apply-flow screen) ever renders at, regardless of how wide the
// terminal is.
const dialogMaxWidth = 90

// dialogWidth returns the width to render a centered dialog at: narrower
// than the terminal (w-4, so it visibly floats over the body) but never
// wider than dialogMaxWidth, so a very wide terminal doesn't stretch it
// edge to edge.
func dialogWidth(w int) int {
	limit := w - 4
	if limit > dialogMaxWidth {
		limit = dialogMaxWidth
	}
	if limit < 4 {
		limit = 4
	}
	return limit
}

// centerDialog horizontally centers body (already rendered at some width
// <= w) within a total width of w. Returns the placed string plus the
// x-offset (columns of left padding) the placement added, since a caller
// with recorded hit regions needs to shift their x by that same amount.
func centerDialog(body string, w int) (string, int) {
	x := (w - lipgloss.Width(body)) / 2
	if x < 0 {
		x = 0
	}
	return lipgloss.PlaceHorizontal(w, lipgloss.Center, body), x
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

// pluralize renders "N <noun>" with an "s" suffix unless n is exactly 1,
// e.g. pluralize(1, "pending op") -> "1 pending op", pluralize(0, ...) and
// pluralize(2, ...) -> "0 pending ops"/"2 pending ops".
func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
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
