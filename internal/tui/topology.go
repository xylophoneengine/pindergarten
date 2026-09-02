package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// topoZoom is the Topology tab's box-detail mode: topoZoomAuto (the
// zero value, so a fresh App starts here) picks whichever of the other
// two actually fits the body height, preferring detailed since it's the
// more informative view; 'z' cycles the override auto -> detailed ->
// compact -> auto (see App.topoZoom, App.handleKey's tab==topology 'z'
// case).
type topoZoom int

const (
	topoZoomAuto topoZoom = iota
	topoZoomDetailed
	topoZoomCompact
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

// renderTopoCoreBox renders one leaf box for a core: title "core N
// (T1,T2)" -- sysfs core_id (N) skips numbers on some hosts (AMD: core
// IDs 8-13 for threads 6-11, say), so the thread ids are named too
// rather than leaving them unrecoverable from the drawing alone; adding
// them to the title (not a second content line under the glyphs) is the
// cheaper of the brief's two options and keeps the box the same height --
// content its threads' glyph cells -- the same nodeMapCell the CPU Map
// tab uses, so pinning/pending claims are visible here too (no cursor/
// highlight in v1, per the brief). globalIdx is the core's position in
// s.Topo.Cores, the same index the CPU Map tab's own cursor is expressed
// in; the returned "topocore" hit lets a click jump straight there.
func renderTopoCoreBox(s *model.Snapshot, globalIdx int, core hostinfo.Core) boxChild {
	cells := nodeMapCell(s, core, nil, false)
	cw := lipgloss.Width(cells)
	title := fmt.Sprintf("core %d (%s)", core.ID, hostinfo.FormatCPUList(core.Threads))
	// Wide enough for the title to actually show (spliceTitle silently
	// truncates it otherwise -- fine for a long sentence, useless for a
	// two-word label like this one), not just the (much narrower) thread
	// glyph content.
	w := cw + 2
	if minW := len(title) + 4; w < minW {
		w = minW
	}
	block := panelInner(title, cells, w, 0)
	return boxChild{block, []hit{{y0: 1, y1: 2, x0: 1, x1: 1 + cw, kind: "topocore", index: globalIdx}}}
}

// clampBoxWidth returns inner clamped to fit within maxWidth (a box's own
// total-width budget, border included): if inner's natural width
// (maxLineWidth(inner)+2) already fits, inner is returned unchanged
// alongside that natural width; otherwise inner is truncated -- never
// wrapped -- to maxWidth-2 and the width returned is maxWidth itself.
// Every box-building function below calls this right after assembling
// its own inner/body content, so a too-wide child (or, before this fix,
// the compact grid's own hardcoded-32-cores-per-row content) can never
// propagate an over-width body up to its parent: panelInner's own
// lipgloss.Width().Render() call WORD-WRAPS a body wider than the width
// it's given rather than truncating it, which is what corrupted the
// whole nested drawing's borders once a too-wide body finally reached
// the outermost machine box (the only place this clamp used to be
// applied at all, as machineBoxWidth, width-only -- it clamped the
// chosen width but never the body handed to panelInner alongside it).
func clampBoxWidth(inner string, maxWidth int) (string, int) {
	w := maxLineWidth(inner) + 2
	if w > maxWidth {
		inner = truncateLines(inner, maxWidth-2)
		w = maxWidth
	}
	return inner, w
}

// renderTopoL3Box renders one L3-domain box: title "L3 #k", containing
// one core box per core in that domain -- hostinfo.L3Domain.Threads is a
// thread list, not a core list, so the cores that actually share this
// domain are found by Core.L3 == l3.ID instead (L3Domain.ID is unique
// across the whole host, not per-node, so this alone is enough -- no
// extra node filter needed).
func renderTopoL3Box(s *model.Snapshot, l3 hostinfo.L3Domain, maxWidth int) boxChild {
	var children []boxChild
	for i, core := range s.Topo.Cores {
		if core.L3 != l3.ID {
			continue
		}
		children = append(children, renderTopoCoreBox(s, i, core))
	}
	inner, hits := wrapBoxesInto(children, maxWidth-2)
	inner, w := clampBoxWidth(inner, maxWidth)
	block := panelInner(fmt.Sprintf("L3 #%d", l3.ID), inner, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// renderTopoCompactGrid renders cores as a dense glyph grid: nodeMapCell's
// plain two-glyph cell per core (no cursor -- compact mode has none, per
// the brief), one space between cores -- the same density renderNodeMap
// already uses for the CPU Map tab's own per-node grid, but restricted to
// an arbitrary core subset (an L3 domain's cores, or a whole node's when
// there's no L3 data) and hit-indexed by each core's GLOBAL s.Topo.Cores
// position (idxs[i], parallel to cores[i]) rather than a per-node-local
// one -- renderTopoCoreBox/renderTopoL3Box need that same global index
// for their own "topocore" hits, so this does too, to jump to the right
// core on the CPU Map tab. Cores per row is min(coresPerRow, (maxWidth-1)
// /3) -- each cell is 2 columns plus a 1-column separator (3N-1 for N
// cells) -- rather than always coresPerRow (32, 95 columns): a fixed
// row width regardless of maxWidth is exactly what let the compact grid
// ignore its parent's own width budget and, once nested boxes shrink-
// wrapped around it, corrupt the whole drawing's borders once it was
// finally forced to fit (see clampBoxWidth). x0 = col*3 is unchanged --
// hit columns still follow directly from the column index, whatever the
// row width actually wrapped at.
func renderTopoCompactGrid(s *model.Snapshot, cores []hostinfo.Core, idxs []int, maxWidth int) (string, []hit) {
	perRow := (maxWidth - 1) / 3
	if perRow > coresPerRow {
		perRow = coresPerRow
	}
	if perRow < 1 {
		perRow = 1
	}
	var b strings.Builder
	var hits []hit
	col, row := 0, 0
	for i, core := range cores {
		if col == perRow {
			b.WriteString("\n")
			row++
			col = 0
		}
		if col > 0 {
			b.WriteString(" ")
		}
		x0 := col * 3
		hits = append(hits, hit{y0: row, y1: row + 1, x0: x0, x1: x0 + 2, kind: "topocore", index: idxs[i]})
		b.WriteString(nodeMapCell(s, core, nil, false))
		col++
	}
	return b.String(), hits
}

// renderTopoCompactBox renders one compact-mode box for a group of cores:
// title "<label>  cores <range>  threads <range>" (formatCPURanges, the
// same compact range notation the Overview hardware panel's L3 lines
// use) -- or, when label is "" (a whole node with no L3 data, see
// renderTopoNodeCoresCompact), just "cores <range>  threads <range>"
// with no leading label at all (a non-empty label used to be prefixed
// unconditionally, producing a doubled "cores  cores <range>" for this
// case). Body is a dense glyph grid (renderTopoCompactGrid) instead of
// one bordered box per core -- per-core boxes (3 rows + borders each)
// can't scale to hundreds of cores, where this stays a handful of rows.
// The grid (and so the box itself, via clampBoxWidth) is wrapped to fit
// maxWidth, this box's own total-width budget.
func renderTopoCompactBox(s *model.Snapshot, label string, cores []hostinfo.Core, idxs []int, maxWidth int) boxChild {
	coreIDs := make([]int, len(cores))
	var threadIDs []int
	for i, c := range cores {
		coreIDs[i] = c.ID
		threadIDs = append(threadIDs, c.Threads...)
	}
	sort.Ints(threadIDs)
	body, hits := renderTopoCompactGrid(s, cores, idxs, maxWidth-2)
	body, w := clampBoxWidth(body, maxWidth)
	title := fmt.Sprintf("cores %s  threads %s", formatCPURanges(coreIDs), formatCPURanges(threadIDs))
	if label != "" {
		title = label + "  " + title
	}
	if minW := len(title) + 4; w < minW {
		w = minW
	}
	block := panelInner(title, body, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// renderTopoL3BoxCompact is renderTopoL3Box's compact-mode counterpart:
// same core selection (Core.L3 == l3.ID), rendered as one dense grid box
// (renderTopoCompactBox) instead of one bordered box per core.
func renderTopoL3BoxCompact(s *model.Snapshot, l3 hostinfo.L3Domain, maxWidth int) boxChild {
	var cores []hostinfo.Core
	var idxs []int
	for i, core := range s.Topo.Cores {
		if core.L3 != l3.ID {
			continue
		}
		cores = append(cores, core)
		idxs = append(idxs, i)
	}
	return renderTopoCompactBox(s, fmt.Sprintf("L3 #%d", l3.ID), cores, idxs, maxWidth)
}

// renderTopoNodeCoresCompact is compact mode's fallback for a topology
// with no L3 domain data at all: the same "skip a level with no data"
// gating renderTopoNodeBox's detailed branch already uses, but grouping
// the whole node's cores into one dense grid box directly (there's no L3
// level to nest under) instead of one core box per core. label "" drops
// renderTopoCompactBox's "L3 #k" prefix, since there's no L3 domain to
// name here.
func renderTopoNodeCoresCompact(s *model.Snapshot, node hostinfo.Node, maxWidth int) boxChild {
	var cores []hostinfo.Core
	var idxs []int
	for i, core := range s.Topo.Cores {
		if core.Node != node.ID {
			continue
		}
		cores = append(cores, core)
		idxs = append(idxs, i)
	}
	return renderTopoCompactBox(s, "", cores, idxs, maxWidth)
}

// renderTopoGPUBox renders one leaf box for a display-class PCI device:
// title "gpu <addr, domain prefix dropped>  <vendor/device name>
// (<driver>)", content a colored "in use"/"free" word (see
// vmUsingDevice, views.go -- also used by the Overview node card's own
// GPU lines, which need the VM's name too, not just whether one exists)
// -- ponytail: the box's title/border can't safely carry ANSI color of
// its own (panelInner's title-splicing treats it as plain runes), so the
// "colored by whether a VM passes it through" requirement is satisfied
// via this content line instead; upgrade to a styled title if
// panelInner ever grows ANSI-aware title splicing.
func renderTopoGPUBox(s *model.Snapshot, dev hostinfo.PCIDevice) boxChild {
	addr := strings.TrimPrefix(dev.Addr, "0000:")
	title := fmt.Sprintf("gpu %s  %s  (%s)", addr, pciDisplayName(dev), pciDriverOrNone(dev.Driver))
	content := barEmptyStyle.Render("free")
	if vmUsingDevice(s, dev.Addr) != "" {
		content = barFilledStyle.Render("in use")
	}
	w := lipgloss.Width(content) + 2
	if minW := len(title) + 4; w < minW {
		w = minW
	}
	block := panelInner(title, content, w, 0)
	return boxChild{block, nil}
}

// renderTopoNodeBox renders one NUMA node box: title "node N  <mem>",
// containing one box per L3 domain in that node (or, when the topology
// has no L3 data at all, one core box per core directly -- the same
// "skip a level with no data" gating as everywhere else this round) plus
// one box per display-class PCI device attached to it. compact swaps the
// L3/core boxes for their dense-grid counterparts (renderTopoL3BoxCompact/
// renderTopoNodeCoresCompact); the node/GPU boxes themselves are
// identical either way, per the brief.
func renderTopoNodeBox(s *model.Snapshot, node hostinfo.Node, maxWidth int, compact bool) boxChild {
	var children []boxChild
	switch {
	case len(s.Topo.L3Domains) > 0 && compact:
		for _, l3 := range s.Topo.L3Domains {
			if l3.Node != node.ID {
				continue
			}
			children = append(children, renderTopoL3BoxCompact(s, l3, maxWidth-2))
		}
	case len(s.Topo.L3Domains) > 0:
		for _, l3 := range s.Topo.L3Domains {
			if l3.Node != node.ID {
				continue
			}
			children = append(children, renderTopoL3Box(s, l3, maxWidth-2))
		}
	case compact:
		children = append(children, renderTopoNodeCoresCompact(s, node, maxWidth-2))
	default:
		for i, core := range s.Topo.Cores {
			if core.Node != node.ID {
				continue
			}
			children = append(children, renderTopoCoreBox(s, i, core))
		}
	}
	for _, dev := range s.Topo.PCIDevices {
		if dev.Node != node.ID || !isDisplayDevice(dev.Class) {
			continue
		}
		children = append(children, renderTopoGPUBox(s, dev))
	}

	inner, hits := wrapBoxesInto(children, maxWidth-2)
	inner, w := clampBoxWidth(inner, maxWidth)
	title := fmt.Sprintf("node %d  %s", node.ID, fmtKiB(node.MemTotalKiB))
	block := panelInner(title, inner, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// renderTopoUnknownBox renders a box for display-class PCI devices whose
// Node is still -1 -- a real multi-node host only, since hostinfo.Read
// resolves a single-node host's -1 to that sole node itself (there's
// nothing to guess on a multi-node host, so it's left alone there).
// Grouped directly under the machine box, as a sibling of the socket/node
// boxes, since there's no NUMA node to nest an unresolvable device under.
func renderTopoUnknownBox(s *model.Snapshot, devs []hostinfo.PCIDevice, maxWidth int) boxChild {
	var children []boxChild
	for _, dev := range devs {
		children = append(children, renderTopoGPUBox(s, dev))
	}
	inner, hits := wrapBoxesInto(children, maxWidth-2)
	inner, w := clampBoxWidth(inner, maxWidth)
	block := panelInner("unknown locality", inner, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// renderTopoSocketBox renders one socket box: title "socket N  <CPU
// model>", containing one box per NUMA node the socket's threads belong
// to.
func renderTopoSocketBox(s *model.Snapshot, sock hostinfo.Socket, maxWidth int, compact bool) boxChild {
	var children []boxChild
	for _, nodeID := range sock.Nodes {
		node := nodeByID(s.Topo.Nodes, nodeID)
		if node == nil {
			continue
		}
		children = append(children, renderTopoNodeBox(s, *node, maxWidth-2, compact))
	}
	inner, hits := wrapBoxesInto(children, maxWidth-2)
	inner, w := clampBoxWidth(inner, maxWidth)
	title := fmt.Sprintf("socket %d", sock.ID)
	if sock.Model != "" {
		title += "  " + sock.Model
	}
	block := panelInner(title, inner, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// buildTopologyTab renders the full, unwindowed (every row, no scroll
// applied) DETAILED topology drawing at width w: an lstopo-style nesting
// of boxes, machine > socket > node > L3 domain > core, each level
// skipped entirely when the topology has no data for it (no sockets at
// all -- a hand-built fixture, most likely -- puts nodes directly under
// the machine box; no L3 domains puts cores directly under their node,
// see renderTopoNodeBox), plus GPU boxes attached under their node. Boxes
// lay out left to right inside their parent, wrapping to a new row once
// the parent's own width budget is exhausted (wrapBoxesInto); a final
// truncateLines pass guarantees no line exceeds w even so (boxes only
// ever shrink-wrap their own content, never grow past what's asked of
// them, so this is a safety net, not the primary mechanism). See
// buildTopologyTabCompact for the dense-grid alternative, and
// topologyInnerForZoom for how the Topology tab actually picks between
// them at render time.
func buildTopologyTab(s *model.Snapshot, w int) (string, []hit) {
	return buildTopologyTabMode(s, w, false)
}

// buildTopologyTabCompact is buildTopologyTab's compact-mode counterpart:
// same machine/socket/node nesting and GPU/unknown-locality boxes, but
// each L3 domain (or, with no L3 data, each node directly) collapses to
// one dense glyph-grid box (renderTopoCompactBox) instead of one bordered
// box per core -- the fix for hundreds of cores otherwise not fitting
// any terminal at all.
func buildTopologyTabCompact(s *model.Snapshot, w int) (string, []hit) {
	return buildTopologyTabMode(s, w, true)
}

// buildTopologyTabMode is buildTopologyTab/buildTopologyTabCompact's
// shared implementation.
func buildTopologyTabMode(s *model.Snapshot, w int, compact bool) (string, []hit) {
	inner, hits := wrapBoxesInto(topologyChildren(s, w, compact), w-2)
	inner, mw := clampBoxWidth(inner, w)
	block := panelInner(topologyMachineTitle(s), inner, mw, 0)
	return truncateLines(block, w), offsetHits(hits, 1, 1)
}

// topologyChildren builds the machine box's top-level children: one box
// per socket, or -- when the topology has no socket data at all, the
// same "skip a level with no data" gating every level here uses -- one
// box per node directly, plus a trailing "unknown locality" box for any
// display-class PCI device hostinfo.Read couldn't place on a real node
// (see renderTopoUnknownBox). compact is threaded down to every node/
// socket box to select detailed vs dense-grid L3/core rendering; the
// unknown-locality box is unaffected by it (GPU boxes never scale with
// core count). Shared by buildTopologyTabMode and renderTopologyTab
// (via topologyInnerForZoom).
func topologyChildren(s *model.Snapshot, w int, compact bool) []boxChild {
	var children []boxChild
	if len(s.Topo.Sockets) > 0 {
		for _, sock := range s.Topo.Sockets {
			children = append(children, renderTopoSocketBox(s, sock, w-2, compact))
		}
	} else {
		for _, node := range s.Topo.Nodes {
			children = append(children, renderTopoNodeBox(s, node, w-2, compact))
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

// topologyInnerForZoom returns the Topology tab's raw, unbordered content
// (topologyChildren wrapped via wrapBoxesInto -- the machine box's own
// border/fill isn't added yet) for override at width w: a non-auto
// override always wins outright, without ever building the mode it isn't
// using; topoZoomAuto builds the DETAILED drawing first -- there's no way
// to know whether it fits without actually measuring it -- and keeps it
// if its height (+2 for the machine box's own border rows) fits budget,
// falling back to the compact drawing only when it doesn't. Shared by
// App.clampTopologyScroll (total line count only) and renderTopologyTab
// (which windows/borders whichever content this returns), so both always
// agree on which mode is actually showing.
func topologyInnerForZoom(s *model.Snapshot, w, budget int, override topoZoom) (string, []hit) {
	if override == topoZoomCompact {
		return wrapBoxesInto(topologyChildren(s, w, true), w-2)
	}
	inner, hits := wrapBoxesInto(topologyChildren(s, w, false), w-2)
	if override == topoZoomDetailed || lineCount(inner)+2 <= budget {
		return inner, hits
	}
	return wrapBoxesInto(topologyChildren(s, w, true), w-2)
}

// renderTopologyTab renders the Topology tab: the nested-box drawing
// topologyInnerForZoom picks for zoom (detailed, compact, or whichever
// auto resolves to), windowed vertically to budget lines starting at
// scroll (the caller clamps scroll via App.clampTopologyScroll) *before*
// the machine box's own border is added -- so a short drawing fills the
// border down to budget (panelInner's height parameter, exactly like
// every other tab's body panel) instead of leaving bare blank rows below
// it, and a long one keeps its own full top/bottom border around
// whatever page is visible, the same pattern renderDiffView (and every
// other scrollable panel in this package) already uses. Alongside the
// string it returns one "topocore" hit per visible core box, 0-based
// relative to the visible window -- a click switches to the CPU Map tab
// and moves its cursor there (see App.handleBodyClick).
func renderTopologyTab(s *model.Snapshot, w, budget, scroll int, zoom topoZoom) (string, []hit) {
	inner, hits := topologyInnerForZoom(s, w, budget, zoom)
	lines := strings.Split(inner, "\n")
	contentBudget := budget - 2
	if contentBudget < 1 {
		contentBudget = 1
	}
	visible, offset, _ := windowAt(lines, contentBudget, scroll)
	body := strings.Join(visible, "\n")

	body, mw := clampBoxWidth(body, w)
	block := truncateLines(panelInner(topologyMachineTitle(s), body, mw, contentBudget), w)

	visibleHits := make([]hit, 0, len(hits))
	for _, h := range hits {
		if h.y0 < offset || h.y0 >= offset+len(visible) {
			continue
		}
		visibleHits = append(visibleHits, hit{y0: h.y0 - offset, y1: h.y1 - offset, x0: h.x0, x1: h.x1, kind: h.kind, index: h.index})
	}
	return block, offsetHits(visibleHits, 1, 1)
}
