package tui

import (
	"fmt"
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

// lineWith returns the first line of out containing sub, failing the test
// if none does.
func lineWith(t *testing.T, out, sub string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	t.Fatalf("output = %q, want a line containing %q", out, sub)
	return ""
}

// buildTopologyTab is a test-only helper (no production caller --
// renderTopologyTab composes its own machine box directly, windowed to a
// budget): it renders the full, unwindowed (every row, no scroll applied)
// topology drawing at width w: machine > socket > node > L3 domain, each
// level skipped entirely when the topology has no data for it (no sockets
// at all -- a hand-built fixture, most likely -- puts nodes directly under
// the machine box; no L3 domains puts a node's cores directly in its own
// box, see renderTopoNodeBox), plus one line per GPU attached under its
// node (or under "unknown locality" when hostinfo couldn't place it).
// Boxes lay out left to right inside their parent, wrapping to a new row
// once the parent's own width budget is exhausted (wrapBoxesInto); a final
// truncateLines pass guarantees no line exceeds w even so (boxes only ever
// shrink-wrap their own content, never grow past what's asked of them, so
// this is a safety net, not the primary mechanism).
func buildTopologyTab(s *model.Snapshot, w int) (string, []hit) {
	inner, hits := wrapBoxesInto(topologyChildren(s, w), w-2)
	mw := machineBoxWidth(inner, w)
	block := panelInner(topologyMachineTitle(s), inner, mw, 0)
	return truncateLines(block, w), offsetHits(hits, 1, 1)
}

// realHostTopo lives in views_test.go (1 socket, 1 node, two 6-core SMT2
// L3 domains, two display GPUs -- AMD amdgpu and NVIDIA); reused here
// unchanged.

// TestTopologyRealHostSmoke covers the compact drawing's basic shape on
// realHostTopo (1 socket, 1 node, two 6-core SMT2 L3 domains, two GPUs) at
// 120 columns: nesting order machine > socket > node > L3 domain, the two
// L3 boxes actually sitting side by side (not stacked -- both titles land
// on the same physical line, the box top border), the ruler row's exact
// "every second cell" label placement over L3 #0's 6 cores (ids 0-5, SMT2
// cellW=3: labels at columns 0/6/12), and both GPUs' one-line entries
// (unused here, so "host (<driver>)").
func TestTopologyRealHostSmoke(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, hits := buildTopologyTab(s, 120)

	iMachine := mustIndex(t, out, "machine")
	iSocket := mustIndex(t, out, "socket 0")
	iNode := mustIndex(t, out, "node 0")
	iL30 := mustIndex(t, out, "L3 #0")
	iL31 := mustIndex(t, out, "L3 #1")
	if iMachine >= iSocket || iSocket >= iNode || iNode >= iL30 || iL30 >= iL31 {
		t.Fatalf("nesting order wrong: machine=%d socket=%d node=%d L3#0=%d L3#1=%d", iMachine, iSocket, iNode, iL30, iL31)
	}

	titleLine := lineWith(t, out, "L3 #0")
	if !strings.Contains(titleLine, "L3 #1") {
		t.Fatalf("title line = %q, want L3 #0 and L3 #1 side by side (same line)", titleLine)
	}

	if !strings.Contains(out, "0     2     4") {
		t.Fatalf("buildTopologyTab() = %q, want L3 #0's ruler row labeling cores 0, 2, 4 (every second cell)", out)
	}

	if !strings.Contains(out, "gpu 06:00.0  AMD  host (amdgpu)") {
		t.Fatalf("buildTopologyTab() = %q, want the amdgpu device's one-line entry", out)
	}
	if !strings.Contains(out, "gpu 09:00.0  NVIDIA GA102  host (nvidia)") {
		t.Fatalf("buildTopologyTab() = %q, want the nvidia device's one-line entry", out)
	}

	for i, l := range strings.Split(out, "\n") {
		if lw := lipgloss.Width(l); lw > 120 {
			t.Fatalf("line %d width = %d, want <= 120: %q", i, lw, l)
		}
	}

	// Every glyph cell's own hit must carry the right global core index --
	// spot-check core 6 (first core of L3 #1).
	found := false
	for _, h := range hits {
		if h.kind == "topocore" && h.index == 6 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no topocore hit for global core index 6 (hits = %+v)", hits)
	}

	body, _ := renderTopologyTab(s, 120, 40, 0)
	if n := strings.Count(body, "\n") + 1; n != 40 {
		t.Fatalf("renderTopologyTab() has %d lines, want exactly 40 (budget)", n)
	}
	for i, l := range strings.Split(body, "\n") {
		if lw := lipgloss.Width(l); lw > 120 {
			t.Fatalf("windowed line %d width = %d, want <= 120: %q", i, lw, l)
		}
	}
}

// TestTopologyGPUPassthroughShowsVM covers the GPU line's own two states:
// unused ("host (<driver>)") vs. passed through to a VM ("vm: <name>").
func TestTopologyGPUPassthroughShowsVM(t *testing.T) {
	s := &model.Snapshot{
		Topo:        realHostTopo(),
		Use:         map[int]model.ThreadUse{},
		BoundMemKiB: map[int]uint64{},
		VMs: []model.VM{{
			Name:    "gpu-vm",
			Devices: []model.Device{{Addr: "0000:09:00.0", Node: 0}},
		}},
	}
	out, _ := buildTopologyTab(s, 120)
	if !strings.Contains(out, "gpu 09:00.0  NVIDIA GA102  vm: gpu-vm") {
		t.Fatalf("buildTopologyTab() = %q, want \"vm: gpu-vm\" for the passed-through nvidia GPU", out)
	}
	if !strings.Contains(out, "gpu 06:00.0  AMD  host (amdgpu)") {
		t.Fatalf("buildTopologyTab() = %q, want the unused amdgpu GPU still showing \"host (amdgpu)\"", out)
	}
}

// TestTopologyNoSocketNoL3Nesting covers the level-skipping gate on
// testTopo (2 nodes, no socket or L3 data at all): the drawing must nest
// machine > node directly (no "socket"/"L3 #" text anywhere), with the
// node's own cores rendered as a bare ruler/glyph grid (no L3 fence --
// checked by counting top-left box corners: exactly 3, the machine box
// and its two node boxes, no extra L3 box nested inside either). Node
// titles aren't asserted verbatim here: testTopo's 2-core nodes render a
// box too narrow for "node N  <mem>" to fit without spliceTitle
// truncating it, which is expected/correct behavior for a tiny box, not
// checked by this test.
func TestTopologyNoSocketNoL3Nesting(t *testing.T) {
	s := &model.Snapshot{Topo: testTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 100)

	mustIndex(t, out, "machine")
	if strings.Contains(out, "socket") || strings.Contains(out, "L3 #") {
		t.Fatalf("buildTopologyTab() = %q, want no socket/L3 level without that data", out)
	}
	if n := strings.Count(out, "\u256d"); n != 3 {
		t.Fatalf("buildTopologyTab() = %q, has %d top-left box corners, want 3 (machine + 2 node boxes, no L3 box)", out, n)
	}
}

// TestTopologyUnknownLocalityBox covers a real multi-node host's PCI
// device that hostinfo.Read left at Node -1 (unresolvable -- see
// hostinfo's own TestPCINumaNodeInMultiNode): it must still be drawn
// somewhere, in its own "unknown locality" box directly under the machine
// box, rather than silently vanishing or being guessed onto the wrong
// node. A device with a known Node must still land inside that node's own
// box, not the unknown one. Checked directly against renderTopoNodeBox/
// renderTopoUnknownBox's own output, not by comparing string offsets in
// the assembled drawing -- node and unknown-locality boxes sit side by
// side there, so their content interleaves row by row rather than
// nesting linearly the way the old machine>socket>node>L3 stack did.
func TestTopologyUnknownLocalityBox(t *testing.T) {
	topo := testTopo()
	known := hostinfo.PCIDevice{Addr: "0000:05:00.0", Class: "030000", VendorID: "1002", DeviceID: "aaaa", VendorName: "AMD", DeviceName: "Known", Node: 0}
	unknown := hostinfo.PCIDevice{Addr: "0000:99:00.0", Class: "030000", VendorID: "10de", DeviceID: "bbbb", VendorName: "NVIDIA", DeviceName: "Unknown", Node: -1}
	topo.PCIDevices = []hostinfo.PCIDevice{known, unknown}
	s := &model.Snapshot{Topo: topo, Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}

	nodeBox := renderTopoNodeBox(s, topo.Nodes[0], 80)
	if !strings.Contains(nodeBox.block, "AMD Known") {
		t.Fatalf("node 0's box = %q, want the known-locality GPU's line", nodeBox.block)
	}
	if strings.Contains(nodeBox.block, "NVIDIA Unknown") {
		t.Fatalf("node 0's box = %q, want the unknown-locality GPU NOT placed on a real node", nodeBox.block)
	}

	unknownBox := renderTopoUnknownBox(s, []hostinfo.PCIDevice{unknown}, 80)
	if !strings.Contains(unknownBox.block, "NVIDIA Unknown") {
		t.Fatalf("unknown-locality box = %q, want the -1-node GPU's line", unknownBox.block)
	}

	out, _ := buildTopologyTab(s, 120)
	mustIndex(t, out, "unknown locality")
	mustIndex(t, out, "AMD Known")
	mustIndex(t, out, "NVIDIA Unknown")
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

	body, _ := renderTopologyTab(s, 120, 30, 0)
	if n := strings.Count(body, "\n") + 1; n != 30 {
		t.Fatalf("renderTopologyTab() has %d lines, want exactly 30 (budget)", n)
	}

	full, _ := buildTopologyTab(s, 120)
	total := strings.Count(full, "\n") + 1
	if total <= 3 {
		t.Skip("fixture's drawing isn't tall enough to exercise scrolling at this width")
	}
	scrolled, _ := renderTopologyTab(s, 120, 3, total-3)
	unscrolled, _ := renderTopologyTab(s, 120, 3, 0)
	if scrolled == unscrolled {
		t.Fatalf("renderTopologyTab() at scroll %d == scroll 0, want scrolling to actually move the window", total-3)
	}
}

// TestTopologyFillsWithBorderNotBlankRows covers renderTopologyTab padding
// short content with the machine box's own border extended down to the
// full budget, never bare blank rows below its closing border: at 120x60
// with the (short) 2-node testTopo fixture, every line of the body must
// start with a border glyph.
func TestTopologyFillsWithBorderNotBlankRows(t *testing.T) {
	s := &model.Snapshot{Topo: testTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	body, _ := renderTopologyTab(s, 120, 60, 0)
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

// bigTwoNodeTopo builds a synthetic 2-node, 200-core topology (1 thread
// per core, no SMT, no L3 domains, no sockets) -- large enough that the
// glyph grid must wrap several times within each node's own box at any
// real terminal width.
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

// isGlyphRune reports whether r is one of glyphChar's plain (unstyled)
// glyphs -- free/pinned/shared -- never a space, separator, or border.
func isGlyphRune(r rune) bool {
	return r == '\u25cb' || r == '\u25cf' || r == '\u25d0'
}

// TestTopologyManyCoresWrapAndHits covers the 200-core scaling case at
// 120x40: each node's 100-core glyph grid wraps to a second ruler/glyph
// row pair (a single row only holds ~58 no-SMT cores at this width), and
// every recorded "topocore" hit lands on an actual glyph character, not a
// space, ruler digit, or border.
func TestTopologyManyCoresWrapAndHits(t *testing.T) {
	s := &model.Snapshot{Topo: bigTwoNodeTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}

	body, hits := renderTopologyTab(s, 120, 40, 0)
	lines := strings.Split(body, "\n")
	if len(lines) != 40 {
		t.Fatalf("renderTopologyTab() has %d lines, want exactly 40 (budget)", len(lines))
	}
	if len(hits) == 0 {
		t.Fatal("no topocore hits recorded")
	}
	indices := map[int]bool{}
	for _, h := range hits {
		if h.kind != "topocore" {
			continue
		}
		if r := glyphAt(lines, h.y0, h.x0); !isGlyphRune(r) {
			t.Fatalf("hit %+v lands on %q, want a glyph character", h, r)
		}
		indices[h.index] = true
	}
	// Core 99 is node 0's last core, past the first row's ~58-core width --
	// only reachable via the grid's wrapped second row, so its presence
	// here (correctly hit-indexed) proves the wrap actually happened.
	if !indices[99] {
		t.Fatalf("no topocore hit for core 99 (hits = %+v), want the grid's wrapped second row still hit-indexed", hits)
	}
}

// smtTopo returns a small custom fixture with the given threads-per-core
// (1 = no SMT), one node, coreCount cores, no L3/socket data -- used to
// pin down renderTopoCoreGrid's cell-width/ruler-stride math at fixture
// sizes small enough to assert on exact string content.
func smtTopo(coreCount, threadsPerCore int) *hostinfo.Topology {
	threads := make(map[int]hostinfo.Thread)
	var cores []hostinfo.Core
	var nodeThreads []int
	nextID := 0
	for c := 0; c < coreCount; c++ {
		var ids []int
		for k := 0; k < threadsPerCore; k++ {
			id := nextID
			nextID++
			sibling := -1
			threads[id] = hostinfo.Thread{ID: id, Core: c, Socket: 0, Node: 0, Sibling: sibling, L3: -1}
			ids = append(ids, id)
			nodeThreads = append(nodeThreads, id)
		}
		cores = append(cores, hostinfo.Core{Socket: 0, ID: c, Node: 0, L3: -1, Threads: ids})
	}
	return &hostinfo.Topology{
		Nodes:   []hostinfo.Node{{ID: 0, Threads: nodeThreads, MemTotalKiB: 1024, MemFreeKiB: 512}},
		Cores:   cores,
		Threads: threads,
	}
}

// TestTopologyNoSMTCellWidth covers a no-SMT (1 thread per core) fixture:
// cell width must be 2 (1 glyph + 1 trailing space), not the SMT2 width
// of 3 -- 4 free cores render as an exact "\u25cb \u25cb \u25cb \u25cb "
// glyph row, ruler labels at columns 0 and 4 (cellW*2).
func TestTopologyNoSMTCellWidth(t *testing.T) {
	s := &model.Snapshot{Topo: smtTopo(4, 1), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 80)
	if !strings.Contains(out, "\u25cb \u25cb \u25cb \u25cb") {
		t.Fatalf("buildTopologyTab() = %q, want a no-SMT glyph row with 2-char cells", out)
	}
	if !strings.Contains(out, "0   2") {
		t.Fatalf("buildTopologyTab() = %q, want ruler labels at columns 0 and 4 (cellW=2, stride=2)", out)
	}
}

// TestTopologySMT2CellWidth covers the SMT2 case directly (cellW=3, one
// glyph pair plus one trailing space per cell): 3 free cores render as
// "\u25cb\u25cb \u25cb\u25cb \u25cb\u25cb" (a single space between glyph pairs, not the no-SMT case's
// bare single glyph).
func TestTopologySMT2CellWidth(t *testing.T) {
	s := &model.Snapshot{Topo: smtTopo(3, 2), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 80)
	if !strings.Contains(out, "\u25cb\u25cb \u25cb\u25cb \u25cb\u25cb") {
		t.Fatalf("buildTopologyTab() = %q, want an SMT2 glyph row with 3-char cells", out)
	}
}

// epycLikeTopo builds a synthetic 2-socket, 2-node topology, 12 L3
// domains x 8 SMT2 cores each per node (96 cores / 192 threads per node) --
// large enough to exercise the L3 boxes' own "4 per row" flow-wrap at a
// real terminal width, per the topology-redesign brief's EPYC-like sizing
// example.
func epycLikeTopo() *hostinfo.Topology {
	const coresPerL3 = 8
	const l3PerNode = 12
	const coresPerNode = coresPerL3 * l3PerNode

	threads := make(map[int]hostinfo.Thread)
	var cores []hostinfo.Core
	var nodes []hostinfo.Node
	var sockets []hostinfo.Socket
	var l3s []hostinfo.L3Domain
	nextL3ID := 0
	for n := 0; n < 2; n++ {
		base := n * coresPerNode * 2
		var nodeThreads []int
		for c := 0; c < coresPerNode; c++ {
			l3ID := nextL3ID + c/coresPerL3
			t0, t1 := base+c, base+coresPerNode+c
			threads[t0] = hostinfo.Thread{ID: t0, Core: c, Socket: n, Node: n, Sibling: t1, L3: l3ID}
			threads[t1] = hostinfo.Thread{ID: t1, Core: c, Socket: n, Node: n, Sibling: t0, L3: l3ID}
			cores = append(cores, hostinfo.Core{Socket: n, ID: c, Node: n, L3: l3ID, Threads: []int{t0, t1}})
			nodeThreads = append(nodeThreads, t0, t1)
		}
		nodes = append(nodes, hostinfo.Node{ID: n, Threads: nodeThreads, MemTotalKiB: 128 * 1024 * 1024, MemFreeKiB: 64 * 1024 * 1024})
		sockets = append(sockets, hostinfo.Socket{ID: n, Model: fmt.Sprintf("EPYC-%d", n), Nodes: []int{n}})
		for k := 0; k < l3PerNode; k++ {
			id := nextL3ID + k
			var l3Threads []int
			for c := k * coresPerL3; c < (k+1)*coresPerL3; c++ {
				l3Threads = append(l3Threads, base+c, base+coresPerNode+c)
			}
			l3s = append(l3s, hostinfo.L3Domain{ID: id, Node: n, Socket: n, Threads: l3Threads})
		}
		nextL3ID += l3PerNode
	}
	return &hostinfo.Topology{Nodes: nodes, Cores: cores, Threads: threads, Sockets: sockets, L3Domains: l3s}
}

// TestTopologyEpycFlowsFourPerRow covers the L3 boxes' own flow-wrap on a
// wide, many-L3-domain node: at 120 columns each L3 box (8 SMT2 cores ->
// 26 columns wide) only leaves room for 4 per row, so L3 #0-#3 must share
// one physical line and L3 #4 must not.
func TestTopologyEpycFlowsFourPerRow(t *testing.T) {
	s := &model.Snapshot{Topo: epycLikeTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 120)

	row := lineWith(t, out, "L3 #0")
	for _, want := range []string{"L3 #1", "L3 #2", "L3 #3"} {
		if !strings.Contains(row, want) {
			t.Fatalf("row = %q, want %q on the same line as L3 #0 (4 boxes per row)", row, want)
		}
	}
	if strings.Contains(row, "L3 #4") {
		t.Fatalf("row = %q, want L3 #4 wrapped onto its own row, not sharing this one", row)
	}

	for i, l := range strings.Split(out, "\n") {
		if lw := lipgloss.Width(l); lw > 120 {
			t.Fatalf("line %d width = %d, want <= 120: %q", i, lw, l)
		}
	}
	if total := strings.Count(out, "\n") + 1; total > 34 {
		t.Fatalf("buildTopologyTab() has %d lines, want <= 34 at 120x60", total)
	}
}

// TestTopologyLongGPUNameTruncates covers a GPU with a name long enough to
// overflow the node box's own width: it must be truncated (ansiTruncate's
// ".." marker) to the node's inner width, never wrapped onto a second
// line (which would otherwise fragment the box's own border).
func TestTopologyLongGPUNameTruncates(t *testing.T) {
	topo := testTopo()
	topo.PCIDevices = []hostinfo.PCIDevice{
		{
			Addr: "0000:05:00.0", Class: "030000", VendorID: "1002", DeviceID: "aaaa",
			VendorName: "A Very Long Vendor Name That Goes On And On",
			DeviceName: "An Equally Long Device Name That Keeps Going And Going And Going",
			Node:       0,
		},
	}
	s := &model.Snapshot{Topo: topo, Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 80)

	if !strings.Contains(out, "..") {
		t.Fatalf("buildTopologyTab() = %q, want the long GPU name truncated with \"..\"", out)
	}
	if strings.Contains(out, "going And Going") {
		t.Fatalf("buildTopologyTab() = %q, want the device name's tail actually dropped, not merely marked", out)
	}
	for i, l := range strings.Split(out, "\n") {
		if lw := lipgloss.Width(l); lw > 80 {
			t.Fatalf("line %d width = %d, want <= 80 (no wrapped border fragment): %q", i, lw, l)
		}
	}
}

// bigNoSMTTopo builds a synthetic 128-core, 1-node, no-SMT topology (1
// thread per core, all 128 cores sharing one L3 domain) -- sysfs core ids
// run 0-127, reaching 3 digits at the high end, at cellW=2 (no SMT).
// Regression fixture for finding 1: renderTopoCoreGrid's ruler row used to
// be able to write a 3-digit label past its own availWidth, and the box
// builders (renderTopoL3Box/renderTopoNodeBox) handed that over-wide body
// straight to panelInner, whose lipgloss Width().Render() word-wraps
// (rather than clips) it -- producing an orphan ruler line that shifted
// every row below it, and every recorded hit's y0, down by one. The single
// L3 domain (rather than none) is what puts the ruler row's own available
// width at exactly the parity that overflows at both 80 and 120 columns,
// through renderTopoL3Box's extra border level.
func bigNoSMTTopo() *hostinfo.Topology {
	threads := map[int]hostinfo.Thread{}
	var cores []hostinfo.Core
	var nodeThreads, l3Threads []int
	for id := 0; id < 128; id++ {
		threads[id] = hostinfo.Thread{ID: id, Core: id, Socket: 0, Node: 0, Sibling: -1, L3: 0}
		cores = append(cores, hostinfo.Core{Socket: 0, ID: id, Node: 0, L3: 0, Threads: []int{id}})
		nodeThreads = append(nodeThreads, id)
		l3Threads = append(l3Threads, id)
	}
	return &hostinfo.Topology{
		Nodes:     []hostinfo.Node{{ID: 0, Threads: nodeThreads, MemTotalKiB: 64 * 1024 * 1024, MemFreeKiB: 32 * 1024 * 1024}},
		Cores:     cores,
		Threads:   threads,
		L3Domains: []hostinfo.L3Domain{{ID: 0, Node: 0, Socket: 0, Threads: l3Threads}},
	}
}

// TestTopologyNoSMT128CoreRulerNoOverflow is the regression test for
// finding 1, rendered through the real App.View() (so panelInner's own
// word-wrap is actually exercised, not just renderTopoCoreGrid in
// isolation) at both 80x30 and 120x30: no rendered line may exceed the
// terminal width, and every recorded "topocore" hit must still land on an
// actual glyph rune, not a ruler digit shifted down by an orphan wrapped
// row.
func TestTopologyNoSMT128CoreRulerNoOverflow(t *testing.T) {
	for _, w := range []int{80, 120} {
		t.Run(fmt.Sprintf("w=%d", w), func(t *testing.T) {
			a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
			a.snap = &model.Snapshot{Topo: bigNoSMTTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
			a.doms = map[string]*libvirtio.DomainConfig{}
			a.tab = tabTopology
			a.Update(tea.WindowSizeMsg{Width: w, Height: 30})
			view := a.View() // record hits

			lines := strings.Split(view, "\n")
			for i, l := range lines {
				if lw := lipgloss.Width(l); lw > w {
					t.Fatalf("line %d width = %d, want <= %d: %q", i, lw, w, l)
				}
			}

			found := false
			for _, h := range a.hits {
				if h.kind != "topocore" {
					continue
				}
				found = true
				if r := glyphAt(lines, h.y0, h.x0); !isGlyphRune(r) {
					t.Fatalf("hit %+v at width %d lands on %q, want a glyph character", h, w, r)
				}
			}
			if !found {
				t.Fatalf("no topocore hit recorded at width %d", w)
			}
		})
	}
}

// fourDigitIDTopo builds a tiny 1-node, no-SMT topology whose 6 cores carry
// 4-digit sysfs ids (1000-1005) -- used to pin down rulerStride's own
// escalation to stride 3 (finding 2): a label needs maxLen+1 columns (the
// digits plus a separating space), and stride 2 only gives it 2*cellW -- at
// cellW=2 a 4-digit id needs 5 columns but stride 2 only gives 4, so
// adjacent labels used to collide into one unbroken run of digits
// ("10001002...") instead of staying separated.
func fourDigitIDTopo() *hostinfo.Topology {
	threads := map[int]hostinfo.Thread{}
	var cores []hostinfo.Core
	var nodeThreads []int
	for i := 0; i < 6; i++ {
		id := 1000 + i
		threads[i] = hostinfo.Thread{ID: i, Core: id, Socket: 0, Node: 0, Sibling: -1, L3: -1}
		cores = append(cores, hostinfo.Core{Socket: 0, ID: id, Node: 0, L3: -1, Threads: []int{i}})
		nodeThreads = append(nodeThreads, i)
	}
	return &hostinfo.Topology{
		Nodes:   []hostinfo.Node{{ID: 0, Threads: nodeThreads, MemTotalKiB: 1024, MemFreeKiB: 512}},
		Cores:   cores,
		Threads: threads,
	}
}

// TestTopologyRulerStrideEscalatesForWideLabels covers finding 2: 4-digit
// core ids at cellW=2 (no SMT) must escalate the ruler's default stride
// (every 2nd cell) to every 3rd, so labels stay separated by at least one
// space instead of running together.
func TestTopologyRulerStrideEscalatesForWideLabels(t *testing.T) {
	s := &model.Snapshot{Topo: fourDigitIDTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := buildTopologyTab(s, 80)

	rulerLine := lineWith(t, out, "1000")
	if strings.Contains(rulerLine, "10001001") || strings.Contains(rulerLine, "10001002") {
		t.Fatalf("ruler line = %q, want 4-digit labels separated by at least one space, not run together", rulerLine)
	}
	if !strings.Contains(rulerLine, "1000  1003") {
		t.Fatalf("ruler line = %q, want labels at stride 3 (ids 1000 and 1003) separated by a gap", rulerLine)
	}
}

// TestTopologyHitsAcrossWidths covers the click contract at a range of
// widths: a click on a glyph cell switches to the CPU Map tab and moves
// its cursor to that core, and the recorded hit itself always lands on a
// glyph character.
func TestTopologyHitsAcrossWidths(t *testing.T) {
	for _, w := range []int{80, 120, 200} {
		t.Run(fmt.Sprintf("w=%d", w), func(t *testing.T) {
			a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
			a.snap = &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
			a.doms = map[string]*libvirtio.DomainConfig{}
			a.tab = tabTopology
			a.Update(tea.WindowSizeMsg{Width: w, Height: 30})
			view := a.View() // record hits

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
				t.Fatalf("no topocore hit recorded at width %d (hits = %+v)", w, a.hits)
			}
			if r := glyphAt(strings.Split(view, "\n"), h.y0, h.x0); !isGlyphRune(r) {
				t.Fatalf("hit %+v at width %d lands on %q, want a glyph character", h, w, r)
			}

			a.Update(press(h.x0, h.y0))
			if a.tab != tabCPUMap {
				t.Fatalf("tab = %d after clicking a core cell at width %d, want 1 (CPU Map)", a.tab, w)
			}
			if a.cursor != h.index {
				t.Fatalf("cursor = %d after clicking a core cell at width %d, want %d", a.cursor, w, h.index)
			}
		})
	}
}
