package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

const coresPerRow = 32

// fmtKiB formats a KiB quantity as a human size with one decimal place and
// a K/M/G/T suffix, e.g. 196485734 -> "187.4G".
func fmtKiB(kib uint64) string {
	const unit = 1024
	f := float64(kib)
	switch {
	case kib >= unit*unit*unit:
		return fmt.Sprintf("%.1fT", f/(unit*unit*unit))
	case kib >= unit*unit:
		return fmt.Sprintf("%.1fG", f/(unit*unit))
	case kib >= unit:
		return fmt.Sprintf("%.1fM", f/unit)
	default:
		return fmt.Sprintf("%.1fK", f)
	}
}

// overviewBarWidth is the fixed width (brackets included) of the memory and
// threads progress bars on the Overview tab.
const overviewBarWidth = 22

// nodeThreadStats returns node's pinned thread count (at least one VM
// claims it), pending-only count (no VM claim, but a pending op does), and
// its total thread count.
func nodeThreadStats(s *model.Snapshot, node hostinfo.Node) (pinned, pending, total int) {
	total = len(node.Threads)
	for _, t := range node.Threads {
		use := s.Use[t]
		switch {
		case len(use.VMs) > 0:
			pinned++
		case len(use.Pending) > 0:
			pending++
		}
	}
	return pinned, pending, total
}

// overviewNodeCard renders one NUMA node's card body: the original
// mem/threads/bound-vm-mem summary line (kept verbatim so existing
// assertions on it still hold), a memory bar, a threads bar (pinned solid,
// pending a second color), and the gpus:/vms: lists.
func overviewNodeCard(s *model.Snapshot, node hostinfo.Node) string {
	pinned := 0
	for _, t := range node.Threads {
		if len(s.Use[t].VMs) > 0 {
			pinned++
		}
	}
	bound := s.BoundMemKiB[node.ID]

	var b strings.Builder
	line := fmt.Sprintf("node %d  mem %s free %s  threads %d (%d pinned)  bound-vm-mem %s",
		node.ID, fmtKiB(node.MemTotalKiB), fmtKiB(node.MemFreeKiB), len(node.Threads), pinned, fmtKiB(bound))
	if bound > node.MemTotalKiB {
		line += " " + overStyle.Render("OVER")
	}
	b.WriteString(line)
	b.WriteString("\n")

	memFrac := 0.0
	if node.MemTotalKiB > 0 {
		memFrac = float64(bound) / float64(node.MemTotalKiB)
	}
	pct := int(memFrac*100 + 0.5)
	fmt.Fprintf(&b, "memory %s %d%%  %s/%s\n", bar(overviewBarWidth, memFrac, barFilledStyle), pct, fmtKiB(bound), fmtKiB(node.MemTotalKiB))

	pinnedN, pendingN, total := nodeThreadStats(s, node)
	pinnedFrac, pendingFrac := 0.0, 0.0
	if total > 0 {
		pinnedFrac = float64(pinnedN) / float64(total)
		pendingFrac = float64(pendingN) / float64(total)
	}
	fmt.Fprintf(&b, "threads %s %d/%d pinned", dualBar(overviewBarWidth, pinnedFrac, pendingFrac, barFilledStyle, barPendingStyle), pinnedN, total)
	if pendingN > 0 {
		fmt.Fprintf(&b, " (%d pending)", pendingN)
	}
	b.WriteString("\n")

	if gpus := gpusOnNode(s, node.ID); gpus != "" {
		b.WriteString("gpus: " + gpus + "\n")
	}
	if vms := vmsOnNode(s, node.ID); vms != "" {
		b.WriteString("vms: " + vms + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderOverviewTab renders the Overview tab: one bordered panel per NUMA
// node (side by side when each would get at least sideCardMinWidth
// columns, else stacked), followed by a one-line-per-VM list of any
// domains flagged Unsupported.
func renderOverviewTab(s *model.Snapshot, w int) string {
	cardW, sideBySide := equalSplit(w, len(s.Topo.Nodes), sideCardMinWidth)
	panels := make([]string, len(s.Topo.Nodes))
	for i, node := range s.Topo.Nodes {
		panels[i] = panel(fmt.Sprintf("node %d", node.ID), overviewNodeCard(s, node), cardW)
	}
	out := joinPanels(panels, sideBySide)

	var unsupported strings.Builder
	for _, v := range s.VMs {
		if v.Unsupported {
			fmt.Fprintf(&unsupported, "%s: unsupported config, view only\n", v.Name)
		}
	}
	if unsupported.Len() > 0 {
		out += "\n" + strings.TrimRight(unsupported.String(), "\n")
	}
	return out
}

// gpusOnNode returns the comma-separated PCI addresses of devices, across
// all VMs, whose Node matches node, or "" when there are none.
func gpusOnNode(s *model.Snapshot, node int) string {
	var addrs []string
	for _, v := range s.VMs {
		for _, d := range v.Devices {
			if d.Node == node {
				addrs = append(addrs, d.Addr)
			}
		}
	}
	return strings.Join(addrs, ", ")
}

// vmsOnNode returns the comma-separated names of VMs whose MemNodes include
// node, or "" when there are none. s.VMs is sorted by name, so the result
// is too.
func vmsOnNode(s *model.Snapshot, node int) string {
	var names []string
	for _, v := range s.VMs {
		for _, n := range v.MemNodes {
			if n == node {
				names = append(names, v.Name)
				break
			}
		}
	}
	return strings.Join(names, ", ")
}

// cpuMapLegend is the CPU Map panel's bottom legend line.
func cpuMapLegend() string {
	return "\u25cf pinned  \u25cb free  \u25d0 shared  (" + pendingGlyphStyle.Render("yellow") + " = pending)"
}

// renderCPUMapTab renders the CPU Map tab: one bordered panel holding
// every node's cell grid plus a legend, and a detail panel for the
// cursor's core (below it, or beside it when wide).
func renderCPUMapTab(s *model.Snapshot, cursor, w int) (string, []hit) {
	primaryW, secondaryW, sideBySide := splitBodyWidth(w)

	grid, hits := renderCPUMap(s, cursor, primaryW)
	body := grid + "\n" + cpuMapLegend()
	mapPanel := panel("CPU Map", body, primaryW)
	hits = offsetHits(hits, 1, 1)

	detailPanel := panelWrap("core detail", cpuMapDetail(s, cursor), secondaryW)

	if sideBySide {
		return lipgloss.JoinHorizontal(lipgloss.Top, mapPanel, " ", detailPanel), hits
	}
	return mapPanel + "\n" + detailPanel, hits
}

// renderCPUMap renders every node's cores as a grid of two-glyph cells (one
// glyph per sibling thread), 32 cells per row, with a "node N" heading
// before each node's cores. cursor is a position in s.Topo.Cores; that cell
// renders reverse-video. Alongside the string it returns one "core" hit per
// cell, 0-based relative to the grid's own top-left corner (x0 = the
// cell's column * 3, since each cell is 2 glyphs wide plus a 1-column
// separator). w is unused for now: an overlong row is truncated, not
// wrapped, by the caller's panel().
func renderCPUMap(s *model.Snapshot, cursor, w int) (string, []hit) {
	var b strings.Builder
	var hits []hit
	curNode := -1
	col := 0
	row := 0
	for i, core := range s.Topo.Cores {
		if i == 0 || core.Node != curNode {
			if i != 0 {
				b.WriteString("\n\n")
				row += 2
			}
			fmt.Fprintf(&b, "node %d\n", core.Node)
			row++
			curNode = core.Node
			col = 0
		} else if col == coresPerRow {
			b.WriteString("\n")
			row++
			col = 0
		}
		if col > 0 {
			b.WriteString(" ")
		}
		x0 := col * 3
		hits = append(hits, hit{y0: row, y1: row + 1, x0: x0, x1: x0 + 2, kind: "core", index: i})
		b.WriteString(renderCell(s, core, i == cursor))
		col++
	}
	b.WriteString("\n")
	return b.String(), hits
}

// renderCell renders one core's two-glyph cell. A single-thread core (no
// SMT sibling) renders its one glyph followed by a space.
func renderCell(s *model.Snapshot, core hostinfo.Core, isCursor bool) string {
	var glyphs strings.Builder
	for _, t := range core.Threads {
		glyphs.WriteString(threadGlyph(s, t, isCursor))
	}
	if len(core.Threads) == 1 {
		glyphs.WriteString(" ")
	}
	return glyphs.String()
}

// threadGlyph returns the styled glyph for one thread: pinned (solid),
// free, or shared (2+ claimants counting VMs+Pending); a pending-only claim
// keeps the pinned glyph but in a distinct color. The cursor cell instead
// renders its plain glyph reverse-video, ignoring the pending color.
func threadGlyph(s *model.Snapshot, t int, isCursor bool) string {
	glyph := glyphChar(s, t)
	use := s.Use[t]
	total := len(use.VMs) + len(use.Pending)

	if isCursor {
		return cursorStyle.Render(glyph)
	}
	if total == 1 && len(use.Pending) == 1 {
		return pendingGlyphStyle.Render(glyph)
	}
	return glyph
}

// glyphChar returns thread t's plain (unstyled) glyph: pinned (solid), free,
// or shared (2+ claimants counting VMs+Pending). Factored out of
// threadGlyph so the wizard's node map can apply its own highlight style on
// top of the same base glyph.
func glyphChar(s *model.Snapshot, t int) string {
	use := s.Use[t]
	switch len(use.VMs) + len(use.Pending) {
	case 0:
		return "\u25cb" // white circle: free
	case 1:
		return "\u25cf" // black circle: pinned
	default:
		return "\u25d0" // circle, left half black: shared
	}
}

// cpuMapDetail renders the detail panel for the core at coreIdx: its id,
// socket, node and thread list, then one line per thread naming its
// pinning VM(s) and pending claimant(s). Returns "" for an out-of-range
// index.
func cpuMapDetail(s *model.Snapshot, coreIdx int) string {
	if coreIdx < 0 || coreIdx >= len(s.Topo.Cores) {
		return ""
	}
	core := s.Topo.Cores[coreIdx]

	var b strings.Builder
	fmt.Fprintf(&b, "core %d (socket %d, node %d)  threads %s\n",
		core.ID, core.Socket, core.Node, hostinfo.FormatCPUList(core.Threads))
	for _, t := range core.Threads {
		b.WriteString(threadDetailLine(s, t))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// threadDetailLine formats one thread's detail line. It naturally produces
// all four documented shapes: "free" (no claimants), the bare VM name (one
// pinning VM), "pending: name" (one pending-only claimant), or the
// comma-joined list of names/"pending: name" entries suffixed "(shared)"
// (2+ claimants).
func threadDetailLine(s *model.Snapshot, t int) string {
	use := s.Use[t]
	var parts []string
	parts = append(parts, use.VMs...)
	for _, p := range use.Pending {
		parts = append(parts, "pending: "+p)
	}

	switch len(parts) {
	case 0:
		return fmt.Sprintf("thread %d: free", t)
	case 1:
		return fmt.Sprintf("thread %d: %s", t, parts[0])
	default:
		return fmt.Sprintf("thread %d: %s (shared)", t, strings.Join(parts, ", "))
	}
}

// stateName maps a domain's run state to its display word.
func stateName(st libvirtio.DomState) string {
	switch st {
	case libvirtio.StateRunning:
		return "running"
	case libvirtio.StateShutoff:
		return "shut off"
	default:
		return "other"
	}
}

// pinsSummary renders a VM's pin-state column: "unpinned" (no pins),
// "cross-node" (pinned threads span more than one node), "partial" (some
// vcpus pinned, some not), or "N pinned -> node X" (every vcpu pinned, all
// on the same node).
func pinsSummary(topo *hostinfo.Topology, v *model.VM) string {
	if len(v.Pins) == 0 {
		return "unpinned"
	}

	nodes := map[int]bool{}
	for _, threads := range v.Pins {
		for _, t := range threads {
			if th, ok := topo.Threads[t]; ok {
				nodes[th.Node] = true
			}
		}
	}
	if len(nodes) == 0 {
		return "unknown"
	}
	if len(nodes) > 1 {
		return "cross-node"
	}
	if len(v.Pins) < v.VCPUs {
		return "partial"
	}
	node := 0
	for n := range nodes {
		node = n
	}
	return fmt.Sprintf("%d pinned -> node %d", len(v.Pins), node)
}

// intListOrDash comma-joins ids, or returns "-" when ids is empty.
func intListOrDash(ids []int) string {
	if len(ids) == 0 {
		return "-"
	}
	return hostinfo.FormatCPUList(ids)
}

// gpuNodeCol renders a VM's gpu-node column: "-" with no passthrough
// devices, "?" when it has one but the node could not be resolved, else the
// node number.
func gpuNodeCol(v *model.VM) string {
	if len(v.Devices) == 0 {
		return "-"
	}
	if n := v.GPUNode(); n != -1 {
		return strconv.Itoa(n)
	}
	return "?"
}

// flagBadges renders one "[!]" per flag on v, in the warning color.
func flagBadges(v *model.VM) string {
	return strings.Repeat(warningStyle.Render("[!]"), len(v.Flags))
}

// vmNameCap and vmPinsCap are the VMs table's two capped column widths
// (truncated with ".." past this many cells).
const (
	vmNameCap = 20
	vmPinsCap = 24
)

// vmDropOrder lists the VMs table's columns in the priority order they get
// dropped when the table (after NAME/PINS capping) still doesn't fit w.
// NAME, VCPUS, PINS, and FLAGS are never dropped.
var vmDropOrder = []string{"GPUNODE", "MEMNODE", "MEM", "STATE"}

// vmCol is one VMs-table column: its header, per-row values (same order as
// s.VMs), and cap (0 = uncapped).
type vmCol struct {
	name string
	cap  int
	vals []string
}

// vmColWidth returns col's rendered width: the natural max of its header
// and values, clamped to its cap (if any).
func vmColWidth(c vmCol) int {
	w := lipgloss.Width(c.name)
	for _, v := range c.vals {
		if cw := lipgloss.Width(v); cw > w {
			w = cw
		}
	}
	if c.cap > 0 && w > c.cap {
		w = c.cap
	}
	return w
}

// fitCell truncates val to w (with ".." if it had to cut) and pads it to
// exactly w cells.
func fitCell(val string, w int) string {
	if lipgloss.Width(val) > w {
		val = ansiTruncate(val, w)
	}
	return padRight(val, w)
}

// renderVMs renders the VMs tab's table: name, state, vcpus, mem, pins
// summary, mem node, gpu node, and flag badges, one row per VM (s.VMs is
// sorted by name). Rows never wrap: NAME and PINS are capped (truncated
// with ".."), and STATE/MEM/MEMNODE/GPUNODE are dropped -- in that priority
// -- if the table still doesn't fit w. The row at sel gets a background
// highlight instead of full reverse video. Alongside the string it returns
// one "vm" hit per row (whole-width, 0-based relative to the table's own
// top-left corner: row 0 is the header).
func renderVMs(s *model.Snapshot, sel int, w int) (string, []hit) {
	cols := []vmCol{
		{name: "NAME", cap: vmNameCap},
		{name: "STATE"},
		{name: "VCPUS"},
		{name: "MEM"},
		{name: "PINS", cap: vmPinsCap},
		{name: "MEMNODE"},
		{name: "GPUNODE"},
		{name: "FLAGS"},
	}
	for i := range s.VMs {
		v := &s.VMs[i]
		cols[0].vals = append(cols[0].vals, v.Name)
		cols[1].vals = append(cols[1].vals, stateName(v.State))
		cols[2].vals = append(cols[2].vals, strconv.Itoa(v.VCPUs))
		cols[3].vals = append(cols[3].vals, fmtKiB(v.MemoryKiB))
		cols[4].vals = append(cols[4].vals, pinsSummary(s.Topo, v))
		cols[5].vals = append(cols[5].vals, intListOrDash(v.MemNodes))
		cols[6].vals = append(cols[6].vals, gpuNodeCol(v))
		cols[7].vals = append(cols[7].vals, flagBadges(v))
	}

	active := make(map[string]bool, len(cols))
	widths := make(map[string]int, len(cols))
	for _, c := range cols {
		active[c.name] = true
		widths[c.name] = vmColWidth(c)
	}
	tableWidth := func() int {
		total, n := 0, 0
		for _, c := range cols {
			if active[c.name] {
				total += widths[c.name]
				n++
			}
		}
		if n > 1 {
			total += 2 * (n - 1)
		}
		return total
	}
	for _, name := range vmDropOrder {
		if tableWidth() <= w {
			break
		}
		active[name] = false
	}

	rowCells := func(row int) []string {
		var cells []string
		for _, c := range cols {
			if !active[c.name] {
				continue
			}
			val := c.name
			if row >= 0 {
				val = c.vals[row]
			}
			cells = append(cells, fitCell(val, widths[c.name]))
		}
		return cells
	}

	var lines []string
	lines = append(lines, tableHeaderStyle.Render(strings.Join(rowCells(-1), "  ")))
	hits := make([]hit, 0, len(s.VMs))
	for i := range s.VMs {
		line := strings.Join(rowCells(i), "  ")
		if i == sel {
			line = selectedRowStyle.Render(line)
		}
		lines = append(lines, line)
		hits = append(hits, hit{y0: i + 1, y1: i + 2, x0: 0, x1: hitWide, kind: "vm", index: i})
	}
	return strings.Join(lines, "\n"), hits
}

// vmDetail renders the detail panel for the VM at index sel: a key/value
// grid (state, vcpus, mem, pins, mem node, gpu node), then one "[!] <Cause>
// <Consequence>" bullet per flag. Returns "" for an out-of-range index.
func vmDetail(s *model.Snapshot, sel int) string {
	if sel < 0 || sel >= len(s.VMs) {
		return ""
	}
	v := &s.VMs[sel]

	rows := [][2]string{
		{"state", stateName(v.State)},
		{"vcpus", strconv.Itoa(v.VCPUs)},
		{"mem", fmtKiB(v.MemoryKiB)},
		{"pins", pinsSummary(s.Topo, v)},
		{"mem node", intListOrDash(v.MemNodes)},
		{"gpu node", gpuNodeCol(v)},
	}
	keyW := 0
	for _, r := range rows {
		if len(r[0]) > keyW {
			keyW = len(r[0])
		}
	}

	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s  %s\n", padRight(r[0], keyW), r[1])
	}
	for _, f := range v.Flags {
		fmt.Fprintf(&b, "- %s %s %s\n", warningStyle.Render("[!]"), f.Cause, f.Consequence)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderVMsTab renders the VMs tab: the table panel, and a detail panel
// (titled with the selected VM's name) below it, or beside it when wide.
func renderVMsTab(s *model.Snapshot, sel, w int) (string, []hit) {
	primaryW, secondaryW, sideBySide := splitBodyWidth(w)

	table, hits := renderVMs(s, sel, primaryW-2)
	tablePanel := panel("VMs", table, primaryW)
	hits = offsetHits(hits, 1, 1)

	title := "detail"
	if sel >= 0 && sel < len(s.VMs) {
		title = s.VMs[sel].Name
	}
	detailPanel := panelWrap(title, vmDetail(s, sel), secondaryW)

	if sideBySide {
		return lipgloss.JoinHorizontal(lipgloss.Top, tablePanel, " ", detailPanel), hits
	}
	return tablePanel + "\n" + detailPanel, hits
}
