package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// mustIndex returns strings.Index(s, sub), failing the test if sub isn't
// present at all.
func mustIndex(t *testing.T, s, sub string) int {
	t.Helper()
	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("output = %q, want %q present", s, sub)
	}
	return i
}

// TestTopologyNestingRealHost covers point 5 of the topology brief on the
// full real-host fixture (1 socket, 1 node, two L3 domains, two GPUs):
// the drawing must nest machine > socket > node > L3 domain > core, in
// that order, plus a GPU box (colored by whether a VM passes it through)
// under the node.
func TestTopologyNestingRealHost(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, hits := buildTopologyTab(s, 120)

	iMachine := mustIndex(t, out, "machine")
	iSocket := mustIndex(t, out, "socket 0")
	iNode := mustIndex(t, out, "node 0")
	iL30 := mustIndex(t, out, "L3 #0")
	iL31 := mustIndex(t, out, "L3 #1")
	iCore := mustIndex(t, out, "core 0")
	mustIndex(t, out, "gpu 06:00.0")
	mustIndex(t, out, "gpu 09:00.0")

	if iMachine >= iSocket || iSocket >= iNode || iNode >= iL30 || iL30 >= iCore || iL30 >= iL31 {
		t.Fatalf("nesting order wrong: machine=%d socket=%d node=%d L3#0=%d L3#1=%d core=%d",
			iMachine, iSocket, iNode, iL30, iL31, iCore)
	}
	if !strings.Contains(out, "AMD") || !strings.Contains(out, "free") {
		t.Fatalf("buildTopologyTab() = %q, want the amdgpu device's name and \"free\" status (no VM passes it through)", out)
	}
	if !strings.Contains(out, "NVIDIA GA102") {
		t.Fatalf("buildTopologyTab() = %q, want the nvidia device's name", out)
	}

	// Every core box's own hit must carry the right global core index --
	// spot-check core 6 (first core of L3 #1, per realHostTopo).
	found := false
	for _, h := range hits {
		if h.kind == "topocore" && h.index == 6 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no topocore hit for global core index 6 (hits = %+v)", hits)
	}
}

// TestTopologyGPUInUseColoring covers the "colored by whether a VM passes
// it through" requirement: staging/using a GPU changes its box's content
// from "free" to "in use".
func TestTopologyGPUInUseColoring(t *testing.T) {
	topo := realHostTopo()
	s := &model.Snapshot{
		Topo:        topo,
		Use:         map[int]model.ThreadUse{},
		BoundMemKiB: map[int]uint64{},
		VMs: []model.VM{{
			Name:    "gpu-vm",
			Devices: []model.Device{{Addr: "0000:09:00.0", Node: 0}},
		}},
	}
	out, _ := buildTopologyTab(s, 120)
	if !strings.Contains(out, "in use") {
		t.Fatalf("buildTopologyTab() = %q, want \"in use\" for the passed-through nvidia GPU", out)
	}
	if !strings.Contains(out, "free") {
		t.Fatalf("buildTopologyTab() = %q, want \"free\" still present for the unused amdgpu GPU", out)
	}
}

// TestTopologyNestingTwoNodeTopo covers the level-skipping gate on
// testTopo (2 nodes, no socket or L3 data at all): the drawing must nest
// machine > node > core directly, with no "socket"/"L3 #" text anywhere.
func TestTopologyNestingTwoNodeTopo(t *testing.T) {
	s := &model.Snapshot{Topo: testTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 100)

	iMachine := mustIndex(t, out, "machine")
	iNode0 := mustIndex(t, out, "node 0")
	mustIndex(t, out, "node 1")
	iCore := mustIndex(t, out, "core 0")
	if iMachine >= iNode0 || iNode0 >= iCore {
		t.Fatalf("nesting order wrong: machine=%d node0=%d core=%d", iMachine, iNode0, iCore)
	}
	if strings.Contains(out, "socket") || strings.Contains(out, "L3 #") {
		t.Fatalf("buildTopologyTab() = %q, want no socket/L3 level without that data", out)
	}
}

// TestTopologyUnknownLocalityBox covers point 1 of the topology-compact
// brief: a real multi-node host's PCI device that hostinfo.Read left at
// Node -1 (unresolvable -- see hostinfo's own TestPCINumaNodeInMultiNode)
// must still be drawn somewhere, in its own "unknown locality" box
// directly under the machine box, rather than silently vanishing (the
// original bug report) or being guessed onto the wrong node. A device
// with a known Node must still land inside that node's own box, not the
// unknown one.
func TestTopologyUnknownLocalityBox(t *testing.T) {
	topo := testTopo()
	topo.PCIDevices = []hostinfo.PCIDevice{
		{Addr: "0000:05:00.0", Class: "030000", VendorID: "1002", DeviceID: "aaaa", VendorName: "AMD", DeviceName: "Known", Node: 0},
		{Addr: "0000:99:00.0", Class: "030000", VendorID: "10de", DeviceID: "bbbb", VendorName: "NVIDIA", DeviceName: "Unknown", Node: -1},
	}
	s := &model.Snapshot{Topo: topo, Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 120)

	iUnknownBox := mustIndex(t, out, "unknown locality")
	iKnownGPU := mustIndex(t, out, "AMD Known")
	iUnknownGPU := mustIndex(t, out, "NVIDIA Unknown")
	iNode0 := mustIndex(t, out, "node 0")

	if iNode0 >= iKnownGPU || iKnownGPU >= iUnknownBox {
		t.Fatalf("nesting order wrong: want node 0 (%d) < known gpu (%d) < unknown locality box (%d)", iNode0, iKnownGPU, iUnknownBox)
	}
	if iUnknownGPU <= iUnknownBox {
		t.Fatalf("unknown-locality gpu at %d, want it after the \"unknown locality\" box heading at %d", iUnknownGPU, iUnknownBox)
	}
}

// TestTopologyWidthInvariant covers the "clips horizontally, never wraps"
// requirement at a range of widths, for both fixtures: no rendered line
// may exceed w.
func TestTopologyWidthInvariant(t *testing.T) {
	fixtures := map[string]*model.Snapshot{
		"real-host": {Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}},
		"two-node":  {Topo: testTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}},
	}
	for name, s := range fixtures {
		for _, w := range []int{40, 60, 80, 120, 200} {
			out, _ := buildTopologyTab(s, w)
			for i, l := range strings.Split(out, "\n") {
				if lw := lipgloss.Width(l); lw > w {
					t.Fatalf("%s at width %d: line %d width = %d, want <= %d: %q", name, w, i, lw, w, l)
				}
			}
		}
	}
}

// TestTopologyFillsAndScrolls covers the height side: renderTopologyTab
// always fills exactly budget lines (padding short content, like every
// other body panel this project's fill option gives -- see panelH), and
// windows to whatever scroll offset the caller (App.clampTopologyScroll)
// already clamped.
func TestTopologyFillsAndScrolls(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}

	body, _ := renderTopologyTab(s, 120, 30, 0, topoZoomDetailed)
	if n := strings.Count(body, "\n") + 1; n != 30 {
		t.Fatalf("renderTopologyTab() has %d lines, want exactly 30 (budget)", n)
	}

	full, _ := buildTopologyTab(s, 120)
	total := strings.Count(full, "\n") + 1
	if total <= 3 {
		t.Skip("fixture's drawing isn't tall enough to exercise scrolling at this width")
	}
	scrolled, _ := renderTopologyTab(s, 120, 3, total-3, topoZoomDetailed)
	unscrolled, _ := renderTopologyTab(s, 120, 3, 0, topoZoomDetailed)
	if scrolled == unscrolled {
		t.Fatalf("renderTopologyTab() at scroll %d == scroll 0, want scrolling to actually move the window", total-3)
	}
}

// TestTopologyFillsWithBorderNotBlankRows covers the fix for
// renderTopologyTab padding short content with bare blank rows (no
// border at all) below the machine box's own closing border, instead of
// giving the box itself the fill: at 120x60 with the (short) 2-node
// testTopo fixture, every line of the body must start with a border
// glyph -- the box's own border, extended down to the full budget, never
// a blank line floating below it.
func TestTopologyFillsWithBorderNotBlankRows(t *testing.T) {
	s := &model.Snapshot{Topo: testTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	body, _ := renderTopologyTab(s, 120, 60, 0, topoZoomDetailed)
	lines := strings.Split(body, "\n")
	if len(lines) != 60 {
		t.Fatalf("renderTopologyTab() has %d lines, want exactly 60 (budget)", len(lines))
	}
	for i, l := range lines {
		r := []rune(l)
		if len(r) == 0 || (r[0] != '\u2502' && r[0] != '\u256d' && r[0] != '\u2570') {
			t.Fatalf("line %d = %q, want it to start with a border glyph (box border/corner)", i, l)
		}
	}
}

// TestTopologyClickJumpsToCPUMapCursor covers the click contract: a click
// on a core box switches to the CPU Map tab and moves its cursor to that
// core.
func TestTopologyClickJumpsToCPUMapCursor(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	a.snap = &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a.doms = map[string]*libvirtio.DomainConfig{}
	a.tab = tabTopology
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	_ = a.View() // record hits

	var h hit
	found := false
	for _, hh := range a.hits {
		if hh.kind == "topocore" {
			h = hh
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no topocore hit recorded (hits = %+v)", a.hits)
	}

	a.Update(press(h.x0, h.y0))
	if a.tab != tabCPUMap {
		t.Fatalf("tab = %d after clicking a core box, want %d (CPU Map)", a.tab, tabCPUMap)
	}
	if a.cursor != h.index {
		t.Fatalf("cursor = %d after clicking a core box, want %d", a.cursor, h.index)
	}
}

// bigTwoNodeTopo builds a synthetic 2-node, 200-core topology (1 thread
// per core, no SMT, no L3 domains, no sockets) -- large enough that
// per-core bordered boxes (detailed mode, 3 rows each) can't come close
// to fitting any real terminal, the topology-compact brief's own
// "imagine 400 cores across two NUMA nodes" scaling problem.
func bigTwoNodeTopo() *hostinfo.Topology {
	const coresPerNode = 100
	threads := make(map[int]hostinfo.Thread, coresPerNode*2)
	var cores []hostinfo.Core
	nodes := make([]hostinfo.Node, 2)
	for n := 0; n < 2; n++ {
		var nodeThreads []int
		for c := 0; c < coresPerNode; c++ {
			id := n*coresPerNode + c
			threads[id] = hostinfo.Thread{ID: id, Core: c, Socket: n, Node: n, Sibling: -1, L3: -1}
			cores = append(cores, hostinfo.Core{Socket: n, ID: c, Node: n, L3: -1, Threads: []int{id}})
			nodeThreads = append(nodeThreads, id)
		}
		nodes[n] = hostinfo.Node{ID: n, Threads: nodeThreads, MemTotalKiB: 64 * 1024 * 1024, MemFreeKiB: 32 * 1024 * 1024}
	}
	return &hostinfo.Topology{Nodes: nodes, Cores: cores, Threads: threads}
}

// glyphAt returns the rune at (y, x) in lines (ANSI-stripped first, since
// styling shouldn't affect which character is there) -- 0 if the line is
// too short to have a column x at all.
func glyphAt(lines []string, y, x int) rune {
	if y < 0 || y >= len(lines) {
		return 0
	}
	plain := []rune(ansi.Strip(ansi.TruncateLeft(lines[y], x, "")))
	if len(plain) == 0 {
		return 0
	}
	return plain[0]
}

// isGlyphRune reports whether r is one of nodeMapCell's plain (unstyled)
// glyphs -- free/pinned/shared -- never a space, separator, or border.
func isGlyphRune(r rune) bool {
	return r == '\u25cb' || r == '\u25cf' || r == '\u25d0'
}

// TestTopologyAutoPicksCompactForManyCores covers point 2 of the
// topology-compact brief: auto mode measures the detailed drawing and
// falls back to compact once it doesn't fit -- at 120x40 with 200 cores
// across 2 nodes, the output must be compact (dense grids, not one
// bordered box per core: no "core 0" title anywhere), fit the budget,
// and every recorded "topocore" hit must land on an actual glyph
// character, not a space or border (so a click can't silently miss).
func TestTopologyAutoPicksCompactForManyCores(t *testing.T) {
	s := &model.Snapshot{Topo: bigTwoNodeTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	body, hits := renderTopologyTab(s, 120, 40, 0, topoZoomAuto)
	lines := strings.Split(body, "\n")
	if len(lines) != 40 {
		t.Fatalf("renderTopologyTab() has %d lines, want exactly 40 (budget)", len(lines))
	}
	if strings.Contains(body, "core 0 ") || strings.Contains(body, "core 0(") {
		t.Fatalf("renderTopologyTab() = %q, want compact mode (no per-core \"core N\" boxes)", body)
	}
	if !strings.Contains(body, "cores 0-") {
		t.Fatalf("renderTopologyTab() = %q, want a compact box's \"cores <range>\" title", body)
	}
	if len(hits) == 0 {
		t.Fatal("no topocore hits recorded")
	}
	for _, h := range hits {
		if h.kind != "topocore" {
			continue
		}
		if r := glyphAt(lines, h.y0, h.x0); !isGlyphRune(r) {
			t.Fatalf("hit %+v lands on %q, want a glyph character", h, r)
		}
	}
}

// TestTopologyAutoPicksDetailedWhenItFits covers the other side of auto
// mode: realHostTopo (12 cores) at 120x40 easily fits, so auto must keep
// the more informative detailed drawing (individual "core N" boxes), not
// fall back to compact needlessly.
func TestTopologyAutoPicksDetailedWhenItFits(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	body, _ := renderTopologyTab(s, 120, 40, 0, topoZoomAuto)
	mustIndex(t, body, "core 0")
	if strings.Contains(body, "cores 0-") {
		t.Fatalf("renderTopologyTab() = %q, want detailed mode (no compact-box \"cores <range>\" title)", body)
	}
}

// TestTopologyCompactL3BoxTitle covers the compact L3 box's own title
// format ("L3 #k  cores <range>  threads <range>") on realHostTopo,
// forced into compact mode explicitly (topoZoomCompact) regardless of
// whether it would auto-fit, since this fixture is small enough that
// auto would otherwise always pick detailed.
func TestTopologyCompactL3BoxTitle(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	body, _ := buildTopologyTabCompact(s, 120)
	if !strings.Contains(body, "L3 #0  cores 0-5  threads 0-5,12-17") {
		t.Fatalf("buildTopologyTabCompact() = %q, want L3 #0's compact title with core/thread ranges", body)
	}
}

// TestTopologyZoomCycles covers the 'z' key: auto -> detailed -> compact
// -> auto, on a fixture (realHostTopo) where auto itself would pick
// detailed, so forcing compact is only visible through the override.
func TestTopologyZoomCycles(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	a.snap = &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a.doms = map[string]*libvirtio.DomainConfig{}
	a.tab = tabTopology
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	if a.topoZoom != topoZoomAuto {
		t.Fatalf("topoZoom = %d, want topoZoomAuto initially", a.topoZoom)
	}
	view := a.View()
	mustIndex(t, view, "core 0") // auto picked detailed

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if a.topoZoom != topoZoomDetailed {
		t.Fatalf("topoZoom = %d after one 'z', want topoZoomDetailed", a.topoZoom)
	}

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if a.topoZoom != topoZoomCompact {
		t.Fatalf("topoZoom = %d after two 'z', want topoZoomCompact", a.topoZoom)
	}
	view = a.View()
	if strings.Contains(view, "core 0 ") || strings.Contains(view, "core 0(") {
		t.Fatalf("view = %q, want compact mode forced (no \"core 0\" box)", view)
	}
	mustIndex(t, view, "cores 0-")

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if a.topoZoom != topoZoomAuto {
		t.Fatalf("topoZoom = %d after three 'z', want back to topoZoomAuto", a.topoZoom)
	}
}

// TestTopologyCoreBoxNamesThreadIDs covers point 3 of the topology-
// compact brief: sysfs core_id can skip numbers (AMD: core ids 8-13 for
// threads 6-11), so a detailed-mode core box's title also names its
// thread ids ("core N (T1,T2)"), not just the (possibly-gapped) core_id
// alone. realHostTopo's core 0 has threads 0 and 12 (its SMT sibling).
func TestTopologyCoreBoxNamesThreadIDs(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 120)
	mustIndex(t, out, "core 0 (0,12)")
}
