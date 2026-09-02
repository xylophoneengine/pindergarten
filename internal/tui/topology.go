package tui

import (
	"fmt"
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

// renderTopoCoreBox renders one leaf box for a core: title "core N",
// content its threads' glyph cells -- the same nodeMapCell the CPU Map
// tab uses, so pinning/pending claims are visible here too (no cursor/
// highlight in v1, per the brief). globalIdx is the core's position in
// s.Topo.Cores, the same index the CPU Map tab's own cursor is expressed
// in; the returned "topocore" hit lets a click jump straight there.
func renderTopoCoreBox(s *model.Snapshot, globalIdx int, core hostinfo.Core) boxChild {
	cells := nodeMapCell(s, core, nil, false)
	cw := lipgloss.Width(cells)
	title := fmt.Sprintf("core %d", core.ID)
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
	w := maxLineWidth(inner) + 2
	block := panelInner(fmt.Sprintf("L3 #%d", l3.ID), inner, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// gpuInUse reports whether any VM's passthrough device list references
// addr -- used to color a GPU box by whether it's actually assigned.
func gpuInUse(s *model.Snapshot, addr string) bool {
	for _, v := range s.VMs {
		for _, d := range v.Devices {
			if d.Addr == addr {
				return true
			}
		}
	}
	return false
}

// renderTopoGPUBox renders one leaf box for a display-class PCI device:
// title "gpu <addr, domain prefix dropped>  <vendor/device name>
// (<driver>)", content a colored "in use"/"free" word (see gpuInUse) --
// ponytail: the box's title/border can't safely carry ANSI color of its
// own (panelInner's title-splicing treats it as plain runes), so the
// "colored by whether a VM passes it through" requirement is satisfied
// via this content line instead; upgrade to a styled title if
// panelInner ever grows ANSI-aware title splicing.
func renderTopoGPUBox(s *model.Snapshot, dev hostinfo.PCIDevice) boxChild {
	addr := strings.TrimPrefix(dev.Addr, "0000:")
	title := fmt.Sprintf("gpu %s  %s  (%s)", addr, pciDisplayName(dev), pciDriverOrNone(dev.Driver))
	content := barEmptyStyle.Render("free")
	if gpuInUse(s, dev.Addr) {
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
// one box per display-class PCI device attached to it.
func renderTopoNodeBox(s *model.Snapshot, node hostinfo.Node, maxWidth int) boxChild {
	var children []boxChild
	if len(s.Topo.L3Domains) > 0 {
		for _, l3 := range s.Topo.L3Domains {
			if l3.Node != node.ID {
				continue
			}
			children = append(children, renderTopoL3Box(s, l3, maxWidth-2))
		}
	} else {
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
	title := fmt.Sprintf("node %d  %s", node.ID, fmtKiB(node.MemTotalKiB))
	w := maxLineWidth(inner) + 2
	block := panelInner(title, inner, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
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
	block := panelInner(title, inner, w, 0)
	return boxChild{block, offsetHits(hits, 1, 1)}
}

// buildTopologyTab renders the full, unwindowed (every row, no scroll
// applied) topology drawing at width w: an lstopo-style nesting of boxes,
// machine > socket > node > L3 domain > core, each level skipped
// entirely when the topology has no data for it (no sockets at all -- a
// hand-built fixture, most likely -- puts nodes directly under the
// machine box; no L3 domains puts cores directly under their node, see
// renderTopoNodeBox), plus GPU boxes attached under their node. Boxes lay
// out left to right inside their parent, wrapping to a new row once the
// parent's own width budget is exhausted (wrapBoxesInto); a final
// truncateLines pass guarantees no line exceeds w even so (boxes only
// ever shrink-wrap their own content, never grow past what's asked of
// them, so this is a safety net, not the primary mechanism). Shared by
// renderTopologyTab (which windows it to a budget/scroll) and
// App.clampTopologyScroll (which only needs the total line count).
func buildTopologyTab(s *model.Snapshot, w int) (string, []hit) {
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

	var totalMem uint64
	for _, n := range s.Topo.Nodes {
		totalMem += n.MemTotalKiB
	}
	inner, hits := wrapBoxesInto(children, w-2)
	block := panelInner("machine  "+fmtKiB(totalMem), inner, w, 0)
	hits = offsetHits(hits, 1, 1)

	return truncateLines(block, w), hits
}

// renderTopologyTab renders the Topology tab: buildTopologyTab's full
// drawing, windowed vertically to budget lines starting at scroll (the
// caller clamps scroll via App.clampTopologyScroll), padded to fill
// budget when the drawing is shorter (matching every other tab's body --
// see panelH's fill option) via padLinesTo. Alongside the string it
// returns one "topocore" hit per visible core box, 0-based relative to
// the visible window -- a click switches to the CPU Map tab and moves
// its cursor there (see App.handleBodyClick).
func renderTopologyTab(s *model.Snapshot, w, budget, scroll int) (string, []hit) {
	block, hits := buildTopologyTab(s, w)
	lines := strings.Split(block, "\n")
	visible, offset, _ := windowAt(lines, budget, scroll)
	body := padLinesTo(strings.Join(visible, "\n"), budget)

	visibleHits := make([]hit, 0, len(hits))
	for _, h := range hits {
		if h.y0 < offset || h.y0 >= offset+len(visible) {
			continue
		}
		visibleHits = append(visibleHits, hit{y0: h.y0 - offset, y1: h.y1 - offset, x0: h.x0, x1: h.x1, kind: h.kind, index: h.index})
	}
	return body, visibleHits
}
