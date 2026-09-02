package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// boxChild is one child box ready for wrapBoxesInto: its already-rendered
// rectangular block (as panelInner produces: a titled rounded-border box,
// every line the same width), and any hits recorded within it, 0-based
// relative to its own top-left corner.
type boxChild struct {
	block string
	hits  []hit
}

// wrapBoxesInto lays out children left to right (a 1-column gap between
// adjacent boxes on the same row), wrapping to a new row once the
// current row's accumulated width would exceed maxWidth -- always
// placing at least one child per row, even one alone wider than maxWidth
// (mirrors fitStackedCount's own "never zero" rule elsewhere in this
// package). Rows are joined vertically, each as tall as its tallest
// child. Returns the assembled block plus every child's hits, offset
// into the block's own coordinate space. "" for no children.
func wrapBoxesInto(children []boxChild, maxWidth int) (string, []hit) {
	if len(children) == 0 {
		return "", nil
	}
	if maxWidth < 1 {
		maxWidth = 1
	}

	var rows [][]boxChild
	var row []boxChild
	rowWidth := 0
	for _, c := range children {
		cw := lipgloss.Width(strings.SplitN(c.block, "\n", 2)[0])
		add := cw
		if len(row) > 0 {
			add++ // the 1-column gap
		}
		if len(row) > 0 && rowWidth+add > maxWidth {
			rows = append(rows, row)
			row = nil
			rowWidth = 0
			add = cw
		}
		row = append(row, c)
		rowWidth += add
	}
	rows = append(rows, row)

	outRows := make([]string, 0, len(rows))
	var hits []hit
	y := 0
	for _, r := range rows {
		blocks := make([]string, 0, len(r)*2-1)
		x, rowHeight := 0, 0
		for i, c := range r {
			if i > 0 {
				blocks = append(blocks, " ")
				x++
			}
			blocks = append(blocks, c.block)
			hits = append(hits, offsetHits(c.hits, y, x)...)
			x += lipgloss.Width(strings.SplitN(c.block, "\n", 2)[0])
			if h := lineCount(c.block); h > rowHeight {
				rowHeight = h
			}
		}
		outRows = append(outRows, lipgloss.JoinHorizontal(lipgloss.Top, blocks...))
		y += rowHeight
	}
	return strings.Join(outRows, "\n"), hits
}

// coreCellWidth returns the glyph-cell width shared by every core in
// cores: each thread gets one glyph column, plus one trailing space (so
// SMT2 cores get width 3, no-SMT cores width 2, per the brief) -- taken
// as the widest core's own thread count so a hybrid core set (unusual,
// but not disallowed by hostinfo.Core) still lines every cell up evenly
// rather than drifting mid-row.
func coreCellWidth(cores []hostinfo.Core) int {
	w := 1
	for _, c := range cores {
		if n := len(c.Threads); n > w {
			w = n
		}
	}
	return w + 1
}

// rulerStride returns how many cells apart the ruler row's core-id labels
// land: every second cell by default, unless the widest id in cores would
// spill past the room two cells of width cellW actually give it, in which
// case every third cell instead -- the brief's own escape hatch for a
// host with enough cores that ids grow past 2 digits.
func rulerStride(cores []hostinfo.Core, cellW int) int {
	maxLen := 1
	for _, c := range cores {
		if l := len(strconv.Itoa(c.ID)); l > maxLen {
			maxLen = l
		}
	}
	if maxLen+1 > 2*cellW {
		return 3
	}
	return 2
}

// coreGlyphCell renders one core's thread glyphs, one column per thread
// (pinned/free/shared, same glyphChar the CPU Map/wizard node maps use;
// a pending-only claim gets pendingGlyphStyle, a reserved-for-the-host
// thread (-reserve N) gets a dim style, mirroring nodeMapCell -- this
// drawing has no cursor/highlight concept, so it only needs the two cases
// nodeMapCell itself doesn't already cover for a plain cell).
func coreGlyphCell(s *model.Snapshot, core hostinfo.Core) string {
	var b strings.Builder
	for _, t := range core.Threads {
		glyph := glyphChar(s, t)
		use := s.Use[t]
		switch {
		case len(use.VMs) == 0 && len(use.Pending) == 1:
			glyph = pendingGlyphStyle.Render(glyph)
		case s.Reserved[t]:
			glyph = keyBarLabelStyle.Render(glyph)
		}
		b.WriteString(glyph)
	}
	return b.String()
}

// renderTopoCoreGrid renders cores as ruler/glyph row pairs: a ruler row
// naming every rulerStride'th core's sysfs core id (left-aligned at its
// cell's column), then a glyph row of one coreGlyphCell per core --
// wrapping to another ruler/glyph pair once a row would exceed availWidth
// (always at least one core per row). idxs is parallel to cores, giving
// each core's position in s.Topo.Cores -- the same global index
// renderTopoNodeBox/renderTopoL3Box need for their own "topocore" hits,
// used here directly since this is the one place cores actually turn into
// glyph cells and hits together. Used both for an L3 domain's own box
// content and, when the topology has no L3 data at all, directly as a
// node's own content (no L3 fence -- see renderTopoNodeBox).
func renderTopoCoreGrid(s *model.Snapshot, cores []hostinfo.Core, idxs []int, availWidth int) (string, []hit) {
	if len(cores) == 0 {
		return "", nil
	}
	cellW := coreCellWidth(cores)
	rowCores := availWidth / cellW
	if rowCores < 1 {
		rowCores = 1
	}
	stride := rulerStride(cores, cellW)

	var rows []string
	var hits []hit
	y := 0
	for start := 0; start < len(cores); start += rowCores {
		end := start + rowCores
		if end > len(cores) {
			end = len(cores)
		}
		rowCoresList := cores[start:end]
		rowIdxs := idxs[start:end]

		var ruler strings.Builder
		for col, c := range rowCoresList {
			if col%stride != 0 {
				continue
			}
			x := col * cellW
			idStr := strconv.Itoa(c.ID)
			if x+len(idStr) > availWidth {
				continue // label would spill past the glyph row it labels -- a later, shorter one (ids need not be ascending) may still fit
			}
			for ruler.Len() < x {
				ruler.WriteString(" ")
			}
			ruler.WriteString(idStr)
		}

		var glyph strings.Builder
		for col, c := range rowCoresList {
			x0 := col * cellW
			hits = append(hits, hit{y0: y + 1, y1: y + 2, x0: x0, x1: x0 + len(c.Threads), kind: "topocore", index: rowIdxs[col]})
			glyph.WriteString(coreGlyphCell(s, c))
			if pad := cellW - len(c.Threads); pad > 0 {
				glyph.WriteString(strings.Repeat(" ", pad))
			}
		}

		rows = append(rows, ruler.String(), glyph.String())
		y += 2
	}
	return strings.Join(rows, "\n"), hits
}

// renderTopoL3Box renders one L3-domain box: title "L3 #k", body
// renderTopoCoreGrid over that domain's own cores (hostinfo.L3Domain.
// Threads is a thread list, not a core list, so the cores that actually
// share this domain are found by Core.L3 == l3.ID instead -- L3Domain.ID
// is unique across the whole host, not per-node, so this alone is enough,
// no extra node filter needed). maxWidth is the outer width budget this
// box may not exceed -- the node's own inner width, per the brief's "capped
// at the node inner width" -- not how much room is left on its current
// flow row, so every L3 box in a node wraps its own rows at the same
// width regardless of how wrapBoxesInto ends up packing the boxes
// themselves.
func renderTopoL3Box(s *model.Snapshot, l3 hostinfo.L3Domain, maxWidth int) boxChild {
	var cores []hostinfo.Core
	var idxs []int
	for i, core := range s.Topo.Cores {
		if core.L3 != l3.ID {
			continue
		}
		cores = append(cores, core)
		idxs = append(idxs, i)
	}
	body, hits := renderTopoCoreGrid(s, cores, idxs, maxWidth-2)
	w := maxLineWidth(body) + 2
	if w > maxWidth {
		w = maxWidth
	}
	// Safety net: renderTopoCoreGrid's own ruler row is bounded to its
	// availWidth, but truncate here too so a too-wide body can never reach
	// panelInner's lipgloss Width().Render(), which word-wraps (not clips)
	// -- an orphan wrapped row would shift every line below it, and every
	// hit's y0 along with it.
	body = truncateLines(body, w-2)
	block := panelInner(fmt.Sprintf("L3 #%d", l3.ID), body, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// renderTopoGPULine renders one display-class PCI device as a single line
// (no box, per the brief): "gpu <addr, domain prefix dropped>  <vendor/
// device name>  host (<driver>)", or "...  vm: <name>" once some VM
// actually passes it through (vmUsingDevice, shared with gpuLinesOnNode in
// views.go) -- the caller truncates it to the node's own inner width.
func renderTopoGPULine(s *model.Snapshot, dev hostinfo.PCIDevice) string {
	addr := strings.TrimPrefix(dev.Addr, "0000:")
	status := "host (" + pciDriverOrNone(dev.Driver) + ")"
	if vm := vmUsingDevice(s, dev.Addr); vm != "" {
		status = "vm: " + vm
	}
	return fmt.Sprintf("gpu %s  %s  %s", addr, pciDisplayName(dev), status)
}

// renderTopoNodeBox renders one NUMA node box: title "node N  <mem>",
// body its L3 domains flowing left to right and wrapping (renderTopoL3Box
// boxes via wrapBoxesInto), or -- when the topology has no L3 data at all,
// the same "skip a level with no data" gating every level here uses --
// its cores rendered directly as one ruler/glyph grid, no L3 fence
// (renderTopoCoreGrid). Any display-class PCI device on this node gets one
// more line each (renderTopoGPULine), below the L3/core content.
func renderTopoNodeBox(s *model.Snapshot, node hostinfo.Node, maxWidth int) boxChild {
	innerWidth := maxWidth - 2
	if innerWidth < 1 {
		innerWidth = 1
	}

	var body string
	var hits []hit
	var l3s []hostinfo.L3Domain
	for _, l3 := range s.Topo.L3Domains {
		if l3.Node == node.ID {
			l3s = append(l3s, l3)
		}
	}
	if len(l3s) > 0 {
		children := make([]boxChild, len(l3s))
		for i, l3 := range l3s {
			children[i] = renderTopoL3Box(s, l3, innerWidth)
		}
		body, hits = wrapBoxesInto(children, innerWidth)
	} else {
		var cores []hostinfo.Core
		var idxs []int
		for i, core := range s.Topo.Cores {
			if core.Node == node.ID {
				cores = append(cores, core)
				idxs = append(idxs, i)
			}
		}
		body, hits = renderTopoCoreGrid(s, cores, idxs, innerWidth)
	}

	var gpuLines []string
	for _, dev := range s.Topo.PCIDevices {
		if dev.Node != node.ID || !isDisplayDevice(dev.Class) {
			continue
		}
		gpuLines = append(gpuLines, ansiTruncate(renderTopoGPULine(s, dev), innerWidth))
	}
	if len(gpuLines) > 0 {
		if body != "" {
			body += "\n"
		}
		body += strings.Join(gpuLines, "\n")
	}

	title := fmt.Sprintf("node %d  %s", node.ID, fmtKiB(node.MemTotalKiB))
	w := maxLineWidth(body) + 2
	if w > maxWidth {
		w = maxWidth
	}
	// Safety net: see renderTopoL3Box's own comment -- a too-wide body must
	// never reach panelInner's word-wrapping Render() call.
	body = truncateLines(body, w-2)
	block := panelInner(title, body, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// renderTopoUnknownBox renders a box for display-class PCI devices whose
// Node is still -1 -- a real multi-node host only, since hostinfo.Read
// resolves a single-node host's -1 to that sole node itself (there's
// nothing to guess on a multi-node host, so it's left alone there).
// Grouped directly under the machine box, as a sibling of the socket/node
// boxes, since there's no NUMA node to nest an unresolvable device under.
// One line per device (renderTopoGPULine), same as inside a node box.
func renderTopoUnknownBox(s *model.Snapshot, devs []hostinfo.PCIDevice, maxWidth int) boxChild {
	innerWidth := maxWidth - 2
	if innerWidth < 1 {
		innerWidth = 1
	}
	lines := make([]string, len(devs))
	for i, dev := range devs {
		lines[i] = ansiTruncate(renderTopoGPULine(s, dev), innerWidth)
	}
	body := strings.Join(lines, "\n")
	w := maxLineWidth(body) + 2
	if w > maxWidth {
		w = maxWidth
	}
	block := panelInner("unknown locality", body, w, 0)
	return boxChild{block, nil}
}

// renderTopoSocketBox renders one socket box: title "socket N  <CPU
// model>", containing one box per NUMA node the socket's threads belong
// to.
func renderTopoSocketBox(s *model.Snapshot, sock hostinfo.Socket, maxWidth int) boxChild {
	var children []boxChild
	for _, nodeID := range sock.Nodes {
		node := nodeByID(s.Topo.Nodes, nodeID)
		if node == nil {
			continue
		}
		children = append(children, renderTopoNodeBox(s, *node, maxWidth-2))
	}
	inner, hits := wrapBoxesInto(children, maxWidth-2)
	title := fmt.Sprintf("socket %d", sock.ID)
	if sock.Model != "" {
		title += "  " + sock.Model
	}
	w := maxLineWidth(inner) + 2
	if w > maxWidth {
		w = maxWidth
	}
	block := panelInner(title, inner, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// topologyChildren builds the machine box's top-level children: one box
// per socket, or -- when the topology has no socket data at all, the
// same "skip a level with no data" gating every level here uses -- one
// box per node directly, plus a trailing "unknown locality" box for any
// display-class PCI device hostinfo.Read couldn't place on a real node
// (see renderTopoUnknownBox). Shared by buildTopologyTab and
// renderTopologyTab (via topologyInner).
func topologyChildren(s *model.Snapshot, w int) []boxChild {
	var children []boxChild
	if len(s.Topo.Sockets) > 0 {
		for _, sock := range s.Topo.Sockets {
			children = append(children, renderTopoSocketBox(s, sock, w-2))
		}
	} else {
		for _, node := range s.Topo.Nodes {
			children = append(children, renderTopoNodeBox(s, node, w-2))
		}
	}
	var unknown []hostinfo.PCIDevice
	for _, dev := range s.Topo.PCIDevices {
		if dev.Node == -1 && isDisplayDevice(dev.Class) {
			unknown = append(unknown, dev)
		}
	}
	if len(unknown) > 0 {
		children = append(children, renderTopoUnknownBox(s, unknown, w-2))
	}
	return children
}

// topologyMachineTitle renders the machine box's title: "machine  <total
// mem>", the sum of every node's own MemTotalKiB.
func topologyMachineTitle(s *model.Snapshot) string {
	var totalMem uint64
	for _, n := range s.Topo.Nodes {
		totalMem += n.MemTotalKiB
	}
	return "machine  " + fmtKiB(totalMem)
}

// machineBoxWidth returns the machine box's own width: shrink-wrapped to
// its widest child (plus borders), the same as every level below it
// already does, rather than always claiming the full body width w --
// but never wider than w itself.
func machineBoxWidth(inner string, w int) int {
	mw := maxLineWidth(inner) + 2
	if mw > w {
		mw = w
	}
	return mw
}

// topologyInner returns the Topology tab's raw, unbordered content
// (topologyChildren wrapped via wrapBoxesInto -- the machine box's own
// border/fill isn't added yet) at width w. Shared by renderTopologyTab
// and App.clampTopologyScroll, which needs the total line count.
func topologyInner(s *model.Snapshot, w int) (string, []hit) {
	return wrapBoxesInto(topologyChildren(s, w), w-2)
}

// renderTopologyTab renders the Topology tab: the nested-box drawing
// topologyInner builds, windowed vertically to budget lines
// starting at scroll (the caller clamps scroll via App.clampTopologyScroll)
// *before* the machine box's own border is added -- so a short drawing
// fills the border down to budget (panelInner's height parameter, exactly
// like every other tab's body panel) instead of leaving bare blank rows
// below it, and a long one keeps its own full top/bottom border around
// whatever page is visible, the same pattern renderDiffView (and every
// other scrollable panel in this package) already uses. Alongside the
// string it returns one "topocore" hit per visible glyph cell, 0-based
// relative to the visible window -- a click switches to the CPU Map tab
// and moves its cursor there (see App.handleBodyClick).
//
// A "reserved (-reserve N)" legend line is appended below the machine box
// when reserve is actually on (reservedCoreCount(s) > 0), its own line
// reserved out of budget first -- so the default (off) rendering, and
// every budget/scroll computation for it, is unchanged.
func renderTopologyTab(s *model.Snapshot, w, budget, scroll int) (string, []hit) {
	var legendLine string
	if n := reservedCoreCount(s); n > 0 {
		legendLine = lipgloss.NewStyle().Width(w).Render(keyBarLabelStyle.Render(fmt.Sprintf("reserved (-reserve %d)", n)))
	}
	legendLines := 0
	if legendLine != "" {
		legendLines = lineCount(legendLine)
	}

	inner, hits := topologyInner(s, w)
	lines := strings.Split(inner, "\n")
	contentBudget := budget - 2 - legendLines
	if contentBudget < 1 {
		contentBudget = 1
	}
	visible, offset, _ := windowAt(lines, contentBudget, scroll)
	body := strings.Join(visible, "\n")

	mw := machineBoxWidth(body, w)
	block := truncateLines(panelInner(topologyMachineTitle(s), body, mw, contentBudget), w)
	if legendLine != "" {
		block += "\n" + legendLine
	}

	visibleHits := make([]hit, 0, len(hits))
	for _, h := range hits {
		if h.y0 < offset || h.y0 >= offset+len(visible) {
			continue
		}
		visibleHits = append(visibleHits, hit{y0: h.y0 - offset, y1: h.y1 - offset, x0: h.x0, x1: h.x1, kind: h.kind, index: h.index})
	}
	return block, offsetHits(visibleHits, 1, 1)
}
