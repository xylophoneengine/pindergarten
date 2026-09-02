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
// grids, or lipgloss's own Width-wrap for prose). h > 0 pads the box (via
// lipgloss's own Height) to exactly h content lines when body is naturally
// shorter, so a panel with less content than its budget still visually
// fills it (blank interior) instead of leaving the terminal looking
// squished/empty below; h <= 0 leaves it at body's own natural height --
// dialogs (a small popup) use that: they should clamp to, never stretch
// to, the space available.
func panelInner(title, body string, w, h int) string {
	if w < 4 {
		w = 4
	}
	inner := w - 2
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(inner)
	if h > 0 {
		style = style.Height(h)
	}
	box := style.Render(body)
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

// panelWrap renders body (prose: sentences, messages, diffs) inside a
// titled rounded-border box of total width w, word-wrapping via lipgloss
// instead of truncating. Natural height, same convention as panel.
func panelWrap(title, body string, w int) string {
	if w < 4 {
		w = 4
	}
	inner := w - 2
	wrapped := lipgloss.NewStyle().Width(inner).Render(body)
	return panelInner(title, wrapped, w, 0)
}

// panelH renders body (tabular/grid content, truncated -- never wrapped --
// to fit) inside a titled rounded-border box of total width w and height
// clip h: body is truncated (top-down, no scroll tracking -- callers
// whose content has a natural "keep this visible" target should pre-slice
// it, e.g. via scrollWindow, before calling this) to at most h-2 content
// lines. When fill is true, content shorter than h-2 is padded (blank
// interior) up to it instead of leaving the panel at its own shorter
// natural height -- for a tab's body panels, which must occupy their
// whole allotted budget (see panelInner); dialogs pass fill=false,
// clamping but never stretching. Returns the panel plus how many content
// lines are real (not padding), so the caller can drop any hits recorded
// past that point via clipHitsToWindow.
func panelH(title, body string, w, h int, fill bool) (string, int) {
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
	kept := len(lines)
	if kept > budget {
		lines = lines[:budget]
		kept = budget
	}
	padTo := 0
	if fill {
		padTo = budget
	}
	return panelInner(title, strings.Join(lines, "\n"), w, padTo), kept
}

// panelWrapH renders body (prose, word-wrapped) inside a titled
// rounded-border box, with the same height clip (and fill option) as
// panelH: word-wrap first, then truncate top-down to at most h-2 lines,
// padding to fill when fill is true. Returns the panel plus how many
// content lines are real (not padding).
func panelWrapH(title, body string, w, h int, fill bool) (string, int) {
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
	kept := len(lines)
	if kept > budget {
		lines = lines[:budget]
		kept = budget
	}
	padTo := 0
	if fill {
		padTo = budget
	}
	return panelInner(title, strings.Join(lines, "\n"), w, padTo), kept
}

// fitStackedCount returns how many of n stacked panels (heights[i] lines
// each, borders included) fit within budget lines total, always at least
// 1 (so something renders even when the very first panel alone exceeds
// budget). Used wherever panels stack vertically with no "selected" one to
// keep visible (Overview's per-node cards, the CPU Map's per-node panels
// when they don't fit side by side): dividing the budget evenly across
// every panel instead would force each one so short that panelH's own
// height floor (a border needs at least 3 lines) makes their sum overflow
// budget again -- showing fewer full-height panels reads better anyway
// than many truncated slivers.
func fitStackedCount(heights []int, budget int) int {
	used, n := 0, 0
	for _, h := range heights {
		if n > 0 && used+h > budget {
			break
		}
		used += h
		n++
	}
	return n
}

// fitStackedWindow returns the [start, start+count) range of stacked
// panels (heights[i] lines each, borders included) to show so panel
// `keep` stays visible within budget lines total -- mirrors
// scrollWindow's "keep visible, pinned to the trailing edge" rule, but
// for variously-sized items instead of fixed-size rows. Always includes
// keep, even if it alone exceeds budget (matching fitStackedCount's own
// "always at least 1" guarantee).
func fitStackedWindow(heights []int, budget, keep int) (start, count int) {
	if len(heights) == 0 {
		return 0, 0
	}
	if keep < 0 {
		keep = 0
	}
	if keep > len(heights)-1 {
		keep = len(heights) - 1
	}
	// Walk backward from keep, accumulating heights, to find how many
	// trailing panels (ending at keep) fit budget.
	used, fitting := 0, 0
	for i := keep; i >= 0; i-- {
		if fitting > 0 && used+heights[i] > budget {
			break
		}
		used += heights[i]
		fitting++
	}
	start = keep - fitting + 1
	if start < 0 {
		start = 0
	}
	// There may be room for more panels *after* keep too, once budget
	// isn't the tight constraint that fixed fitting above -- fill forward
	// from start the same way fitStackedCount would on its own.
	count = fitStackedCount(heights[start:], budget)
	return start, count
}

// maxStackedScroll returns the largest start index worth scrolling
// stacked panels (heights[i] lines each) to: once the panels from index s
// to the end already all fit within budget, scrolling any further would
// only hide earlier ones without revealing anything new, so the first
// such s caps it -- mirrors scrollWindow's "pinned to the trailing edge"
// rule for fixed-size items, but for variously-sized panels. 0 for no
// panels or when everything already fits from the start.
func maxStackedScroll(heights []int, budget int) int {
	if len(heights) == 0 {
		return 0
	}
	sum := 0
	for _, h := range heights {
		sum += h
	}
	for s := 0; s < len(heights); s++ {
		if sum <= budget {
			return s
		}
		sum -= heights[s]
	}
	return len(heights) - 1
}

// clipLinesTo truncates s (top-down) to at most n lines, or "" if n <= 0.
// Used to enforce a computed body budget exactly, regardless of whether
// the render function that produced s respected it internally (e.g. a
// bordered panel's own minimum height floor) -- so chrome rendered after
// the body is never pushed past a.height by the body's own overflow.
func clipLinesTo(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// minSecondaryBudget is the smallest height budget worth giving a stacked
// tab's secondary (detail) panel; below that it's dropped entirely rather
// than rendered as an unreadable sliver.
const minSecondaryBudget = 4

// splitStackedBudget divides a stacked tab's total body-height budget
// between its primary (table/grid) panel and its secondary (detail) one:
// the primary gets only what it naturally needs (primaryNaturalLines + 2
// for its borders), capped at ~60% of budget so a long list never starves
// the detail panel down to nothing, and the secondary gets the rest --
// down to a minimum reserve, below which it's dropped entirely (budget 0)
// rather than rendered as an unreadable sliver. Both returned budgets are
// meant to be filled exactly (via panelH/panelWrapH's fill option), not
// merely treated as a ceiling, so together they always sum to budget.
func splitStackedBudget(budget, primaryNaturalLines int) (primary, secondary int) {
	if budget <= minSecondaryBudget+3 {
		return budget, 0
	}
	need := primaryNaturalLines + 2
	if cap := budget * 3 / 5; need > cap { // ~60% of budget
		need = cap
	}
	if max := budget - minSecondaryBudget; need > max {
		need = max
	}
	if need < 3 {
		need = 3
	}
	return need, budget - need
}

// splitStackedFill divides budget evenly across n same-priority panels
// (Overview's node cards, the CPU Map's per-node panels) that all stretch
// to fill their share via panelH's fill option, once fitStackedCount/
// fitStackedWindow has already picked which (and how many) of them to
// show -- any remainder goes to the last panel, mirroring equalSplit's own
// convention for dividing a width. Panics-free for n <= 0 (returns nil).
func splitStackedFill(budget, n int) []int {
	if n <= 0 {
		return nil
	}
	base, extra := budget/n, budget%n
	out := make([]int, n)
	for i := range out {
		out[i] = base
		if i >= n-extra {
			out[i]++
		}
	}
	return out
}

// scrollWindow slices items (already-built content, one row per visual
// line/entry -- a []string of rendered lines, a []backup.Entry, etc.) down
// to at most budget of them, keeping index `keep` visible by pinning it to
// the bottom edge once the content overflows. The offset is always
// recomputed fresh from (len(items), keep, budget) -- callers don't need
// to persist any scroll state for this. budget <= 0 means there's no room
// at all: the window is empty (not the full, unclipped input -- a caller
// that then unconditionally prepends its own header/border lines still
// gets just those, not the whole table). offset/total are both 0 when
// nothing was trimmed.
func scrollWindow[T any](items []T, budget, keep int) (window []T, offset, total int) {
	total = len(items)
	if budget <= 0 {
		return nil, 0, 0
	}
	if total <= budget {
		return items, 0, 0
	}
	offset = keep - budget + 1
	if offset < 0 {
		offset = 0
	}
	if max := total - budget; offset > max {
		offset = max
	}
	return items[offset : offset+budget], offset, total
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
// offset (clamped into range here). budget <= 0 means there's no room at
// all: the window is empty, matching scrollWindow's own convention.
// offset/total are both 0 when nothing was trimmed.
func windowAt(lines []string, budget, offset int) (window []string, usedOffset, total int) {
	total = len(lines)
	if budget <= 0 {
		return nil, 0, 0
	}
	if total <= budget {
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

// clipHitsToWindow drops hits whose row falls outside [0, hBudget), or
// whose x1 exceeds wBudget (pass 0 to skip the column check) -- for
// content that was simply truncated (top-down for height via panelH/
// panelWrapH, or right-edge for width via panel/panelH's own
// truncateLines) to fit a budget, e.g. a CPU Map node panel or a wizard/
// mem-node-picker panel. Without the column check, a hit recorded for a
// cell past a panel's truncated right edge would survive with its
// original (too-wide) x-range, which -- once offset by a neighboring
// panel's own x position -- can overlap that neighbor's hits entirely.
func clipHitsToWindow(hits []hit, hBudget, wBudget int) []hit {
	if hBudget <= 0 {
		return nil
	}
	out := make([]hit, 0, len(hits))
	for _, h := range hits {
		if h.y0 < hBudget && (wBudget <= 0 || h.x1 <= wBudget) {
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
