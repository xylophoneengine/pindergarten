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

// formatCPURanges compacts a sorted, deduplicated list of ints into range
// notation ("0-5,12-17"), the same style Linux's own sysfs cpulist files
// use (and hostinfo.ParseCPUList understands) -- unlike hostinfo.
// FormatCPUList's plain comma list (used elsewhere in this package for
// short, usually-non-contiguous lists: a core's own thread pair, a VM's
// pin set), a whole L3 domain's thread list is long and naturally
// contiguous, so the compact form reads far better.
func formatCPURanges(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	var parts []string
	start, prev := ids[0], ids[0]
	flush := func(end int) {
		if start == end {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start, end))
		}
	}
	for _, id := range ids[1:] {
		if id == prev+1 {
			prev = id
			continue
		}
		flush(prev)
		start, prev = id, id
	}
	flush(prev)
	return strings.Join(parts, ",")
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

// overviewNodeCard renders one NUMA node's card body: a memory bar (with
// its used/total and, when overcommitted, a red OVER marker right next to
// the percentage -- MemTotalKiB == 0 shows "total unknown" instead of a
// meaningless 0%), a threads bar (pinned solid, pending a second color),
// and the gpus:/vms: lists. The node's own id is the panel's title, not a
// line here, and free/pinned-count figures already visible in the bars
// aren't repeated in a separate summary line.
func overviewNodeCard(s *model.Snapshot, node hostinfo.Node) string {
	bound := s.BoundMemKiB[node.ID]

	var b strings.Builder
	if node.MemTotalKiB == 0 {
		fmt.Fprintf(&b, "memory %s total unknown  used %s\n", bar(overviewBarWidth, 0, barFilledStyle), fmtKiB(bound))
	} else {
		memFrac := float64(bound) / float64(node.MemTotalKiB)
		pct := int(memFrac*100 + 0.5)
		over := ""
		if bound > node.MemTotalKiB {
			over = " " + overStyle.Render("OVER")
		}
		fmt.Fprintf(&b, "memory %s %d%%%s  %s/%s\n", bar(overviewBarWidth, memFrac, barFilledStyle), pct, over, fmtKiB(bound), fmtKiB(node.MemTotalKiB))
	}
	fmt.Fprintf(&b, "free %s\n", fmtKiB(node.MemFreeKiB))

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

// overviewCardNaturalHeight estimates node's card height (memory/free/
// threads lines, +1 each for a present gpus:/vms: line, +2 borders)
// without actually rendering the card -- cheap enough to call once per
// node just to seed splitStackedBudget's primary/secondary split, mirroring
// how renderVMsTab/renderCPUMapTab estimate their own primary panel's
// natural size from a formula rather than a full render.
func overviewCardNaturalHeight(s *model.Snapshot, node hostinfo.Node) int {
	h := 5 // memory + free + threads lines, plus 2 borders
	if gpusOnNode(s, node.ID) != "" {
		h++
	}
	if vmsOnNode(s, node.ID) != "" {
		h++
	}
	return h
}

// renderOverviewCards renders the Overview tab's primary (left, or above
// when stacked) panel: one bordered card per NUMA node (side by side,
// each at the full budget height -- else stacked, starting from scroll,
// clamped by the caller via App.clampOverviewScroll; up/down/wheel on the
// Overview tab move it), showing as many full cards as fit budget at
// their natural height (fitStackedCount) rather than truncating every
// card's content, then splitting budget evenly across just those shown
// cards (splitStackedFill) so they stretch to fill it exactly instead of
// leaving blank space below the last one -- so a short terminal with many
// nodes doesn't push the key bar off-screen, and a tall one doesn't leave
// the tab looking squished to the top either. A trailing "+N more nodes
// (scroll)" line appears once some are hidden.
func renderOverviewCards(s *model.Snapshot, w, budget, scroll int) string {
	cardWidths, sideBySide := equalSplit(w, len(s.Topo.Nodes), sideCardMinWidth)
	bodies := make([]string, len(s.Topo.Nodes))
	heights := make([]int, len(s.Topo.Nodes))
	for i, node := range s.Topo.Nodes {
		bodies[i] = overviewNodeCard(s, node)
		heights[i] = lineCount(bodies[i]) + 2 // borders
	}

	start, n := 0, len(s.Topo.Nodes)
	cardBudget := budget
	if !sideBySide {
		start = scroll
		if start < 0 {
			start = 0
		}
		if start > len(heights)-1 {
			start = len(heights) - 1
		}
		n = fitStackedCount(heights[start:], budget)
		if hidden := len(s.Topo.Nodes) - n; hidden > 0 {
			cardBudget-- // reserve the "+N more nodes (scroll)" footer line
			if cardBudget < 1 {
				cardBudget = 1
			}
		}
	}
	fillHeights := splitStackedFill(cardBudget, n)
	panels := make([]string, n)
	for k := 0; k < n; k++ {
		i := start + k
		h := budget
		if !sideBySide {
			h = fillHeights[k]
		}
		panels[k], _ = panelH(fmt.Sprintf("node %d", s.Topo.Nodes[i].ID), bodies[i], cardWidths[i], h, true)
	}
	out := joinPanels(panels, sideBySide)

	if !sideBySide {
		if hidden := len(s.Topo.Nodes) - n; hidden > 0 {
			out += "\n" + keyBarLabelStyle.Render(fmt.Sprintf("+%d more nodes (scroll)", hidden))
		}
	}
	return out
}

// nodeByID returns the node with the given id from nodes, or nil if there
// is none.
func nodeByID(nodes []hostinfo.Node, id int) *hostinfo.Node {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}

// pciDisplayName renders a PCI device's vendor/device name for the
// hardware tree: "<VendorName> <DeviceName>" (either half omitted if
// empty), falling back to the bare hex IDs ("<vendorID>:<deviceID>") when
// neither name resolved (no pci.ids file, or an unknown vendor).
func pciDisplayName(d hostinfo.PCIDevice) string {
	name := strings.TrimSpace(d.VendorName + " " + d.DeviceName)
	if name == "" {
		name = d.VendorID + ":" + d.DeviceID
	}
	return name
}

// pciDriverOrNone renders a PCI device's bound driver, or "none" when it
// has none.
func pciDriverOrNone(driver string) string {
	if driver == "" {
		return "none"
	}
	return driver
}

// isDisplayDevice reports whether class (a hostinfo.PCIDevice.Class hex
// string, no "0x" prefix) is a display controller (0x03xxxx).
func isDisplayDevice(class string) bool {
	return strings.HasPrefix(class, "03")
}

// renderOverviewHardware renders the Overview tab's secondary (right, or
// below when stacked) panel: a simplified lstopo-style hardware listing --
// one block per socket ("socket N  <CPU model>"), each with its own NUMA
// nodes ("  node N  <threads> threads  mem <total>"), each with its own
// L3 domains ("    L3 #k  threads <cpulist>") and display-class PCI
// devices ("    gpu <addr>  <vendor/device name>  (<driver>)"). Filled to
// budget like every body panel (see panelH's fill option); topo.Sockets
// empty (a topology hostinfo.Read never actually produces, but hand-built
// fixtures elsewhere in this package sometimes do) just renders an empty
// panel rather than erroring.
func renderOverviewHardware(s *model.Snapshot, w, budget int) string {
	var b strings.Builder
	for _, sock := range s.Topo.Sockets {
		title := fmt.Sprintf("socket %d", sock.ID)
		if sock.Model != "" {
			title += "  " + sock.Model
		}
		b.WriteString(title + "\n")
		for _, nodeID := range sock.Nodes {
			node := nodeByID(s.Topo.Nodes, nodeID)
			if node == nil {
				continue
			}
			fmt.Fprintf(&b, "  node %d  %d threads  mem %s\n", nodeID, len(node.Threads), fmtKiB(node.MemTotalKiB))
			for _, l3 := range s.Topo.L3Domains {
				if l3.Node != nodeID || l3.Socket != sock.ID {
					continue
				}
				fmt.Fprintf(&b, "    L3 #%d  threads %s\n", l3.ID, formatCPURanges(l3.Threads))
			}
			for _, dev := range s.Topo.PCIDevices {
				if dev.Node != nodeID || !isDisplayDevice(dev.Class) {
					continue
				}
				fmt.Fprintf(&b, "    gpu %s  %s  (%s)\n", dev.Addr, pciDisplayName(dev), pciDriverOrNone(dev.Driver))
			}
		}
	}
	body := strings.TrimRight(b.String(), "\n")
	panel, _ := panelH("hardware", body, w, budget, true)
	return panel
}

// overviewCardsLayout returns the width and budget the Overview tab's node
// cards actually get: the whole w/budget when the topology has no socket
// data at all (a hand-built fixture, most likely -- hostinfo.Read never
// actually produces this -- so the hardware panel would be empty and is
// skipped entirely), or when the hardware panel sits beside the cards
// (splitBodyWidth judged the terminal wide enough); otherwise (stacked)
// the cards' own natural height (estimated via overviewCardNaturalHeight,
// not a full render) caps their share via splitStackedBudget, the same
// primary/secondary split every other stacked tab uses. Shared by
// renderOverviewTab and App.clampOverviewScroll so the actual layout and
// the scroll clamp always agree on how much room the cards have.
func overviewCardsLayout(s *model.Snapshot, w, budget int) (cardsW, cardsBudget, hardwareBudget int) {
	if len(s.Topo.Sockets) == 0 {
		return w, budget, 0
	}
	primaryW, _, sideBySide := splitBodyWidth(w)
	if sideBySide {
		return primaryW, budget, budget
	}
	_, cardsSideBySide := equalSplit(w, len(s.Topo.Nodes), sideCardMinWidth)
	natural := 0
	for _, node := range s.Topo.Nodes {
		h := overviewCardNaturalHeight(s, node)
		if cardsSideBySide {
			if h > natural {
				natural = h
			}
		} else {
			natural += h
		}
	}
	cardsBudget, hardwareBudget = splitStackedBudget(budget, natural)
	return w, cardsBudget, hardwareBudget
}

// renderOverviewTab renders the Overview tab: the node cards (renderOverview-
// Cards) as the primary panel, the hardware listing (renderOverviewHardware)
// as the secondary one -- side by side when the terminal is wide enough
// (splitBodyWidth), else stacked with the cards first and the hardware
// listing below, splitting budget between them the same way every other
// stacked tab does (splitStackedBudget). Then a one-line-per-VM list of
// any domains flagged Unsupported.
func renderOverviewTab(s *model.Snapshot, w, budget, scroll int) string {
	primaryW, primaryBudget, secondaryBudget := overviewCardsLayout(s, w, budget)

	cards := renderOverviewCards(s, primaryW, primaryBudget, scroll)
	out := cards
	if secondaryBudget > 0 {
		_, secondaryW, sideBySide := splitBodyWidth(w)
		hardware := renderOverviewHardware(s, secondaryW, secondaryBudget)
		if sideBySide {
			out = lipgloss.JoinHorizontal(lipgloss.Top, cards, " ", hardware)
		} else {
			out = cards + "\n" + hardware
		}
	}

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

// cpuMapLegend is the CPU Map block's bottom legend line, shown once below
// every node's panel rather than repeated per panel. withL3 (the
// topology actually has L3 domain data -- see cpuMapNodeGrid) adds the
// "| L3 boundary" entry naming the separator cpuMapNodeGrid draws between
// adjacent L3 domains' cells.
func cpuMapLegend(withL3 bool) string {
	legend := "\u25cf pinned  \u25cb free  \u25d0 shared  (" + pendingGlyphStyle.Render("yellow") + " = pending)"
	if withL3 {
		legend += "  | L3 boundary"
	}
	return legend
}

// cpuNodeMinWidth is the minimum per-panel width (CPU Map's per-node
// panels) below which they stack instead of sitting side by side.
const cpuNodeMinWidth = 20

// globalCoreIndices returns, for node's cores in nodeCores(s, node) order,
// their index in the unrestricted s.Topo.Cores -- so a local (per-node)
// core position can be translated back to the global one the CPU Map
// tab's cursor (and vice versa, via localCoreIndex) is expressed in.
func globalCoreIndices(s *model.Snapshot, node int) []int {
	var idx []int
	for i, c := range s.Topo.Cores {
		if c.Node == node {
			idx = append(idx, i)
		}
	}
	return idx
}

// localCoreIndex returns the position of target within idx (a
// globalCoreIndices result), or -1 if target isn't in it -- i.e. the
// cursor belongs to a different node than the one idx was built for.
func localCoreIndex(idx []int, target int) int {
	for i, g := range idx {
		if g == target {
			return i
		}
	}
	return -1
}

// nodeIndexOf returns the position of the node with the given id within
// nodes, or -1 if there is none.
func nodeIndexOf(nodes []hostinfo.Node, id int) int {
	for i, n := range nodes {
		if n.ID == id {
			return i
		}
	}
	return -1
}

// cpuMapHitXLimit returns the x-limit to clip a node panel's core hits
// against: innerWidth, minus 2 more (the ".." truncation marker's width)
// when the grid's natural row width actually exceeds innerWidth (i.e. a
// row really did get truncated) -- so a hit never lands on the marker
// instead of a real cell. A grid that fits within innerWidth as-is needs
// no such adjustment, or its own rightmost cells would lose their hits for
// no reason.
func cpuMapHitXLimit(cores, innerWidth int) int {
	rowCores := cores
	if rowCores > coresPerRow {
		rowCores = coresPerRow
	}
	naturalWidth := rowCores*3 - 1
	if naturalWidth > innerWidth {
		return innerWidth - 2
	}
	return innerWidth
}

// cpuMapNodeGridRows returns how many physical lines cpuMapNodeGrid's
// grid spans for a node with this many cores: the same row count
// renderNodeMap's plain grid would need (coresPerRow per row), doubled
// once L3 grouping is active (a label line above every row of cells) --
// shared with renderCPUMapTab's own per-node height estimate so it never
// disagrees with what cpuMapNodeGrid actually renders.
func cpuMapNodeGridRows(cores int, withL3 bool) int {
	rows := (cores + coresPerRow - 1) / coresPerRow
	if rows < 1 {
		rows = 1
	}
	if withL3 {
		rows *= 2
	}
	return rows
}

// cpuMapNodeGrid renders node's cores as a grid, exactly like
// renderNodeMap (shared with the wizard, left untouched), except that --
// once the topology actually has L3 domain data (s.Topo.L3Domains
// non-empty; a fixture that never sets it leaves every core's L3 at the
// same default and would otherwise look like one giant, spurious "L3 #0"
// domain) -- it also groups cores by L3 domain: a "L3 #k" label line
// above each row wherever a domain starts, and the normal single-space
// separator between cores replaced with "|" at a domain boundary. Column
// positions (and so hit x-ranges) are exactly the same either way -- only
// the separator glyph and an extra label line above a row of cells
// differ -- so cursor/hit semantics are unchanged.
func cpuMapNodeGrid(s *model.Snapshot, node int, cursor int, kind string) (string, []hit) {
	if len(s.Topo.L3Domains) == 0 {
		return renderNodeMap(s, node, nil, cursor, kind)
	}

	cores := nodeCores(s, node)
	var rows, labels []string
	var hits []hit
	var cellLine, labelLine strings.Builder
	col, rowIdx, x := 0, 0, 0
	prevL3 := -2 // sentinel: never a real domain ID, forces a label at col 0
	flushRow := func() {
		rows = append(rows, cellLine.String())
		labels = append(labels, strings.TrimRight(labelLine.String(), " "))
		cellLine.Reset()
		labelLine.Reset()
	}
	for i, core := range cores {
		if col == coresPerRow {
			flushRow()
			rowIdx++
			col, x = 0, 0
			prevL3 = -2
		}
		if col > 0 {
			sep := " "
			if core.L3 != prevL3 {
				sep = "|"
			}
			cellLine.WriteString(sep)
			x++
		}
		if core.L3 != prevL3 {
			for labelLine.Len() < x {
				labelLine.WriteString(" ")
			}
			fmt.Fprintf(&labelLine, "L3 #%d", core.L3)
			prevL3 = core.L3
		}
		hits = append(hits, hit{y0: 2*rowIdx + 1, y1: 2*rowIdx + 2, x0: x, x1: x + 2, kind: kind, index: i})
		cellLine.WriteString(nodeMapCell(s, core, nil, i == cursor))
		x += 2
		col++
	}
	flushRow()

	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(labels[i])
		b.WriteString("\n")
		b.WriteString(row)
	}
	return b.String(), hits
}

// renderCPUMapTab renders the CPU Map tab: one bordered panel per NUMA
// node (side by side when each would get at least cpuNodeMinWidth
// columns, else stacked, windowed around the cursor's own node -- like
// scrollWindow's "keep visible" rule, but over variously-sized panels --
// so moving the cursor onto a node whose panel isn't currently shown
// still brings it into view) holding that node's cell grid, a legend line
// shown once below them all, and a detail panel for the cursor's core
// (below the node panels, or beside them when wide). Each node panel's
// height is clamped to whatever budget is actually left as they're laid
// out, so even one taller than the whole budget can't overflow it.
func renderCPUMapTab(s *model.Snapshot, cursor, w, budget int) (string, []hit) {
	primaryW, secondaryW, sideBySide := splitBodyWidth(w)
	nodePanelWidths, nodeSideBySide := equalSplit(primaryW, len(s.Topo.Nodes), cpuNodeMinWidth)

	withL3 := len(s.Topo.L3Domains) > 0
	nodeHeights := make([]int, len(s.Topo.Nodes))
	naturalNodeLines := 0
	for i, node := range s.Topo.Nodes {
		nodeHeights[i] = cpuMapNodeGridRows(len(nodeCores(s, node.ID)), withL3) + 2
		if nodeSideBySide {
			if nodeHeights[i] > naturalNodeLines {
				naturalNodeLines = nodeHeights[i]
			}
		} else {
			naturalNodeLines += nodeHeights[i]
		}
	}

	primaryBudget, secondaryBudget := budget, budget
	if !sideBySide {
		primaryBudget, secondaryBudget = splitStackedBudget(budget, naturalNodeLines+1)
	}

	nodeAreaBudget := primaryBudget - 1 // reserve the legend line below
	if nodeAreaBudget < 3 {
		nodeAreaBudget = 3
	}

	// When the node panels stack among themselves too (nodeSideBySide
	// false), show as many full ones as fit nodeAreaBudget -- windowed
	// around the cursor's own node, so it's always among them -- rather
	// than giving every one that whole budget independently, then split
	// nodeAreaBudget evenly across just those shown (splitStackedFill) so
	// they stretch to fill it exactly instead of leaving blank space below
	// the last one.
	start, numNodePanels := 0, len(s.Topo.Nodes)
	if !nodeSideBySide {
		cursorNode := -1
		if cursor >= 0 && cursor < len(s.Topo.Cores) {
			cursorNode = nodeIndexOf(s.Topo.Nodes, s.Topo.Cores[cursor].Node)
		}
		if cursorNode < 0 {
			cursorNode = 0
		}
		start, numNodePanels = fitStackedWindow(nodeHeights, nodeAreaBudget, cursorNode)
	}
	fillHeights := splitStackedFill(nodeAreaBudget, numNodePanels)

	panels := make([]string, numNodePanels)
	var hits []hit
	cumX, cumY := 0, 0
	for k := 0; k < numNodePanels; k++ {
		i := start + k
		node := s.Topo.Nodes[i]
		idx := globalCoreIndices(s, node.ID)
		grid, gridHits := cpuMapNodeGrid(s, node.ID, localCoreIndex(idx, cursor), "core")
		for j := range gridHits {
			gridHits[j].index = idx[gridHits[j].index]
		}
		h := nodeAreaBudget
		if !nodeSideBySide {
			h = fillHeights[k]
		}
		p, kept := panelH(fmt.Sprintf("node %d", node.ID), grid, nodePanelWidths[i], h, true)
		panels[k] = p

		wLimit := cpuMapHitXLimit(len(nodeCores(s, node.ID)), nodePanelWidths[i]-2)
		gridHits = offsetHits(clipHitsToWindow(gridHits, kept, wLimit), 1, 1) // border
		if nodeSideBySide {
			hits = append(hits, offsetHits(gridHits, 0, cumX)...)
			cumX += nodePanelWidths[i] + 1 // +1 for joinPanels' 1-column gap
		} else {
			hits = append(hits, offsetHits(gridHits, cumY, 0)...)
			cumY += lineCount(p)
		}
	}
	// The legend is a free-standing line below the bordered node panels,
	// not itself inside a border -- word-wrapped to primaryW (two lines
	// if it doesn't fit one) so it can never be the widest line in
	// mapBlock and stretch the whole primary block wider than primaryW
	// (lipgloss.JoinHorizontal, used both here for a side-by-side node
	// layout and by the caller to join the primary/secondary panels,
	// pads every line of a block to its own widest line -- a too-wide
	// legend silently widened the whole CPU Map tab by one column at
	// width 120-121, clipping the detail panel's own right border into
	// ".." once the final assembled view got truncated back down to the
	// terminal's actual width). truncateLines is a last-resort safety net
	// in case wrapping still left something wider (a single unbreakable
	// token longer than primaryW, say).
	legend := lipgloss.NewStyle().Width(primaryW).Render(cpuMapLegend(withL3))
	mapBlock := truncateLines(joinPanels(panels, nodeSideBySide)+"\n"+legend, primaryW)

	var detailPanel string
	if secondaryBudget > 0 {
		detailPanel, _ = panelWrapH("core detail", cpuMapDetail(s, cursor), secondaryW, secondaryBudget, true)
	}

	if sideBySide {
		if detailPanel == "" {
			return mapBlock, hits
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, mapBlock, " ", detailPanel), hits
	}
	if detailPanel == "" {
		return mapBlock, hits
	}
	return mapBlock + "\n" + detailPanel, hits
}

// glyphChar returns thread t's plain (unstyled) glyph: pinned (solid), free,
// or shared (2+ claimants counting VMs+Pending). Factored out so
// nodeMapCell (the CPU Map/wizard node map's shared cell renderer) can
// apply its own cursor/highlight/pending style on top of the same base
// glyph.
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
// socket, node and thread list, then (once the topology actually has L3
// data -- s.Topo.L3Domains non-empty; core.L3's own "-1 unknown" default
// isn't enough on its own, since a hand-built fixture that never sets it
// reads as the equally-valid-looking domain 0) its L3 domain, then one
// line per thread naming its pinning VM(s) and pending claimant(s).
// Returns "" for an out-of-range index.
func cpuMapDetail(s *model.Snapshot, coreIdx int) string {
	if coreIdx < 0 || coreIdx >= len(s.Topo.Cores) {
		return ""
	}
	core := s.Topo.Cores[coreIdx]

	var b strings.Builder
	fmt.Fprintf(&b, "core %d (socket %d, node %d)  threads %s\n",
		core.ID, core.Socket, core.Node, hostinfo.FormatCPUList(core.Threads))
	if len(s.Topo.L3Domains) > 0 && core.L3 != -1 {
		fmt.Fprintf(&b, "L3 #%d\n", core.L3)
	}
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

// buildVMCols builds the VMs table's 8 columns and their per-row values
// (s.VMs order) -- shared by renderVMs (which may drop some, to fit a
// width) and vmsReducedWidth (which needs to know how wide the table would
// be with its low-priority columns already dropped, to decide whether a
// two-column layout is worth it).
func buildVMCols(s *model.Snapshot) []vmCol {
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
	return cols
}

// vmLowPriorityCols are the columns renderVMsTab considers already "worth
// dropping" when deciding whether a two-column layout is worth taking:
// GPUNODE/MEMNODE/MEM (the first three of vmDropOrder, not STATE) --
// renderVMs itself may still drop further (including STATE) if the
// primary panel it settles on turns out tighter still.
var vmLowPriorityCols = []string{"GPUNODE", "MEMNODE", "MEM"}

// vmsReducedWidth returns how wide the VMs table would be with
// vmLowPriorityCols already dropped -- the width renderVMsTab checks to
// decide a two-column layout is still worth it even when the *full*
// (every-column) table wouldn't fit the narrower primary panel that'd
// leave for it.
func vmsReducedWidth(cols []vmCol) int {
	return vmsWidthExcluding(cols, vmLowPriorityCols)
}

// vmsWidthExcluding returns how wide a table built from cols would be,
// omitting any column whose name is in exclude.
func vmsWidthExcluding(cols []vmCol, exclude []string) int {
	total, n := 0, 0
	for _, c := range cols {
		dropped := false
		for _, name := range exclude {
			if c.name == name {
				dropped = true
				break
			}
		}
		if dropped {
			continue
		}
		total += vmColWidth(c)
		n++
	}
	if n > 1 {
		total += 2 * (n - 1)
	}
	return total
}

// renderVMs renders the VMs tab's table: name, state, vcpus, mem, pins
// summary, mem node, gpu node, and flag badges, one row per VM (s.VMs is
// sorted by name). Rows never wrap: NAME and PINS are capped (truncated
// with ".."), and STATE/MEM/MEMNODE/GPUNODE are dropped -- in that priority
// -- if the table still doesn't fit w. Rows scroll (keeping sel visible)
// to fit rowBudget; the row at sel gets a background highlight instead of
// full reverse video. Alongside the string it returns one "vm" hit per
// visible row, bounded to w (so a click in a neighboring panel in a
// two-column layout can't land on it), 0-based relative to the table's own
// top-left corner: row 0 is the header.
func renderVMs(cols []vmCol, sel, w, rowBudget int) (string, []hit) {
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

	nRows := 0
	if len(cols) > 0 {
		nRows = len(cols[0].vals)
	}
	rows := make([]string, nRows)
	for i := 0; i < nRows; i++ {
		line := strings.Join(rowCells(i), "  ")
		if i == sel {
			line = selectedRowStyle.Render(line)
		}
		rows[i] = line
	}
	dataBudget := rowBudget - 1 // reserve the header row
	if dataBudget < 1 {
		dataBudget = 1 // always attempt at least one data row
	}
	visible, offset, _ := scrollWindow(rows, dataBudget, sel)

	lines := append([]string{tableHeaderStyle.Render(strings.Join(rowCells(-1), "  "))}, visible...)
	hits := make([]hit, 0, len(visible))
	for i := range visible {
		hits = append(hits, hit{y0: i + 1, y1: i + 2, x0: 0, x1: w, kind: "vm", index: offset + i})
	}
	return strings.Join(lines, "\n"), hits
}

// flagBulletIndent is the width of "- [!] " -- a flag bullet's continuation
// lines are indented this many spaces, so wrapped text still lines up
// under the sentence rather than the "- [!] " marker.
const flagBulletIndent = 6

// wrapFlagBullet renders one flag's "- [!] <sentence>" bullet, word-
// wrapping the sentence to width-flagBulletIndent and indenting every
// continuation line by flagBulletIndent spaces so it lines up under the
// first line's text rather than restarting at column 0.
func wrapFlagBullet(sentence string, width int) string {
	contentW := width - flagBulletIndent
	if contentW < 1 {
		contentW = 1
	}
	lines := strings.Split(lipgloss.NewStyle().Width(contentW).Render(sentence), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "- %s %s", warningStyle.Render("[!]"), lines[0])
	for _, l := range lines[1:] {
		b.WriteString("\n" + strings.Repeat(" ", flagBulletIndent) + l)
	}
	return b.String()
}

// vmDetail renders the detail panel for the VM at index sel: a key/value
// grid (state, vcpus, mem, pins, mem node, gpu node), then one "[!] <Cause>
// <Consequence>" bullet per flag (word-wrapped to width with a hanging
// indent under the text). Returns "" for an out-of-range index.
func vmDetail(s *model.Snapshot, sel, width int) string {
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
		b.WriteString(wrapFlagBullet(f.Cause+" "+f.Consequence, width))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderVMsTab renders the VMs tab: the table panel, and a detail panel
// (titled with the selected VM's name) below it, or beside it when wide.
// Two-column only kicks in when the table -- with its low-priority
// columns (GPUNODE/MEMNODE/MEM) already dropped -- fits the primary
// panel's width; renderVMs may then drop further still if that panel
// turns out tighter yet. Below that (even the reduced table wouldn't fit),
// it stays stacked at full width instead, so a merely-slightly-wider
// terminal never counterintuitively shows *less* than a narrower one did.
func renderVMsTab(s *model.Snapshot, sel, w, budget int) (string, []hit) {
	cols := buildVMCols(s)
	reduced := vmsReducedWidth(cols)

	primaryW, secondaryW, sideBySide := splitBodyWidth(w)
	if sideBySide && reduced > primaryW-2 {
		sideBySide = false
		primaryW, secondaryW = w, w
	}

	primaryBudget, secondaryBudget := budget, budget
	if !sideBySide {
		primaryBudget, secondaryBudget = splitStackedBudget(budget, len(s.VMs)+1)
	}

	table, hits := renderVMs(cols, sel, primaryW-2, primaryBudget-2)
	tablePanel, _ := panelH("VMs", table, primaryW, primaryBudget, true)
	hits = offsetHits(hits, 1, 1)

	title := "detail"
	if sel >= 0 && sel < len(s.VMs) {
		title = s.VMs[sel].Name
	}
	var detailPanel string
	if secondaryBudget > 0 {
		detailPanel, _ = panelWrapH(title, vmDetail(s, sel, secondaryW-2), secondaryW, secondaryBudget, true)
	}

	if sideBySide {
		if detailPanel == "" {
			return tablePanel, hits
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, tablePanel, " ", detailPanel), hits
	}
	if detailPanel == "" {
		return tablePanel, hits
	}
	return tablePanel + "\n" + detailPanel, hits
}
