package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// pinnedNode0XML pins both vcpus onto node 0 threads (0 and 1, on separate
// cores) and binds memory to node 0.
const pinnedNode0XML = `<domain type='kvm'>
  <name>pinned-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b10</uuid>
  <memory unit='KiB'>500</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
    <vcpupin vcpu='1' cpuset='1'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='0'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// overcommitNode0XML binds a huge chunk of memory to node 0 (whose
// MemTotalKiB is 1000 in testTopo) so node 0 goes OVER.
const overcommitNode0XML = `<domain type='kvm'>
  <name>overcommit-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b11</uuid>
  <memory unit='KiB'>5000</memory>
  <vcpu>1</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='0'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

func snapFromXML(t *testing.T, xmls map[string]string) *model.Snapshot {
	t.Helper()
	f := &libvirtio.Fake{ConnURI: "test:///x", XML: xmls}
	doms, err := f.ListDomains()
	if err != nil {
		t.Fatal(err)
	}
	return model.Build(testTopo(), doms, func(string) int { return -1 })
}

func TestOverviewShowsPressure(t *testing.T) {
	s := snapFromXML(t, map[string]string{"overcommit-vm": overcommitNode0XML})
	out := renderOverviewTab(s, 80, 24, 0)
	if !strings.Contains(out, "OVER") {
		t.Fatalf("renderOverview() = %q, want it to contain OVER", out)
	}
}

func TestOverviewNoPressure(t *testing.T) {
	s := snapFromXML(t, map[string]string{"pinned-vm": pinnedNode0XML})
	out := renderOverviewTab(s, 80, 24, 0)
	if strings.Contains(out, "OVER") {
		t.Fatalf("renderOverview() = %q, want no OVER for a node under its total", out)
	}
}

// TestOverviewNodeCardsHaveBars covers the per-node memory progress bar:
// each node's card must contain one (bracketed, with a percentage)
// alongside the existing summary line.
func TestOverviewNodeCardsHaveBars(t *testing.T) {
	s := snapFromXML(t, map[string]string{"pinned-vm": pinnedNode0XML})
	out := renderOverviewTab(s, 100, 24, 0)
	if n := strings.Count(out, "memory ["); n != len(s.Topo.Nodes) {
		t.Fatalf("renderOverviewTab() has %d \"memory [\" bars, want %d (one per node): %q", n, len(s.Topo.Nodes), out)
	}
	if !strings.Contains(out, "%") {
		t.Fatalf("renderOverviewTab() = %q, want a percentage in the bar line", out)
	}
}

// TestOverviewUnknownTotalMemory covers a node whose MemTotalKiB is 0 (not
// reported by sysfs): the memory bar must say "total unknown" rather than
// a meaningless "0%".
func TestOverviewUnknownTotalMemory(t *testing.T) {
	topo := testTopo()
	topo.Nodes[0].MemTotalKiB = 0
	s := &model.Snapshot{Topo: topo, Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out := renderOverviewTab(s, 100, 24, 0)
	if !strings.Contains(out, "total unknown") {
		t.Fatalf("renderOverviewTab() = %q, want \"total unknown\" for a node with MemTotalKiB == 0", out)
	}
}

// realHostTopo mirrors this project's own dev box (see internal/hostinfo's
// TestReadRealHostMirror): an AMD Ryzen 9 5900X, 1 socket, 1 NUMA node, 12
// cores (24 threads, SMT pairs i/i+12), two L3 domains (cores 0-5 and
// 6-11), and two display GPUs (one amdgpu, one nvidia) -- the first
// fixture in this package to actually populate Sockets/L3Domains/
// PCIDevices, since everything before the topology round left them at
// their zero value. VendorName/DeviceName are hand-picked simplified
// names (not what a real pci.ids lookup would resolve to) matching the
// round's own smoke-render spec.
func realHostTopo() *hostinfo.Topology {
	threads := make(map[int]hostinfo.Thread, 24)
	cores := make([]hostinfo.Core, 0, 12)
	nodeThreads := make([]int, 0, 24)
	for c := 0; c < 12; c++ {
		l3 := 0
		if c >= 6 {
			l3 = 1
		}
		sibling := c + 12
		threads[c] = hostinfo.Thread{ID: c, Core: c, Socket: 0, Node: 0, Sibling: sibling, L3: l3}
		threads[sibling] = hostinfo.Thread{ID: sibling, Core: c, Socket: 0, Node: 0, Sibling: c, L3: l3}
		cores = append(cores, hostinfo.Core{Socket: 0, ID: c, Node: 0, L3: l3, Threads: []int{c, sibling}})
		nodeThreads = append(nodeThreads, c, sibling)
	}
	return &hostinfo.Topology{
		Nodes:   []hostinfo.Node{{ID: 0, Threads: nodeThreads, MemTotalKiB: 32 * 1024 * 1024, MemFreeKiB: 16 * 1024 * 1024}},
		Cores:   cores,
		Threads: threads,
		Sockets: []hostinfo.Socket{{ID: 0, Model: "AMD Ryzen 9 5900X 12-Core Processor", Nodes: []int{0}}},
		L3Domains: []hostinfo.L3Domain{
			{ID: 0, Node: 0, Socket: 0, Threads: []int{0, 1, 2, 3, 4, 5, 12, 13, 14, 15, 16, 17}},
			{ID: 1, Node: 0, Socket: 0, Threads: []int{6, 7, 8, 9, 10, 11, 18, 19, 20, 21, 22, 23}},
		},
		PCIDevices: []hostinfo.PCIDevice{
			{Addr: "0000:06:00.0", Class: "030000", VendorID: "1002", DeviceID: "743f", VendorName: "AMD", Driver: "amdgpu", Node: 0},
			{Addr: "0000:09:00.0", Class: "030000", VendorID: "10de", DeviceID: "2216", VendorName: "NVIDIA", DeviceName: "GA102", Driver: "nvidia", Node: 0},
		},
	}
}

// TestOverviewHardwarePanel covers point 2 of the topology brief: the
// Overview tab's secondary (right, or below when stacked) panel becomes a
// simplified lstopo-style hardware listing once the topology has socket
// data -- socket, its node(s), each node's L3 domains and GPUs.
func TestOverviewHardwarePanel(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out := renderOverviewTab(s, 140, 24, 0)

	for _, want := range []string{
		"hardware",
		"socket 0",
		"AMD Ryzen 9 5900X 12-Core Processor",
		"node 0",
		"24 threads",
		"L3 #0",
		"0-5,12-17",
		"L3 #1",
		"6-11,18-23",
		"gpu 0000:06:00.0",
		"AMD",
		"(amdgpu)",
		"gpu 0000:09:00.0",
		"NVIDIA GA102",
		"(nvidia)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderOverviewTab() = %q, want %q present", out, want)
		}
	}
}

// TestOverviewHardwarePanelSkippedWithoutSocketData covers the fallback:
// a topology with no Sockets at all (every fixture elsewhere in this
// package, and any hand-built one in general) renders the cards at the
// full body width/budget, no empty "hardware" panel taking up room.
func TestOverviewHardwarePanelSkippedWithoutSocketData(t *testing.T) {
	s := &model.Snapshot{Topo: testTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out := renderOverviewTab(s, 140, 24, 0)
	if strings.Contains(out, "hardware") {
		t.Fatalf("renderOverviewTab() = %q, want no \"hardware\" panel without socket data", out)
	}
}

func TestCPUMapMarksPinned(t *testing.T) {
	s := snapFromXML(t, map[string]string{"pinned-vm": pinnedNode0XML})
	out, _ := renderCPUMapTab(s, -1, 80, 24)
	if !strings.Contains(out, "\u25cf") {
		t.Fatalf("renderCPUMapTab() = %q, want it to contain the pinned glyph \\u25cf", out)
	}
	if !strings.Contains(out, "\u25cb") {
		t.Fatalf("renderCPUMapTab() = %q, want it to contain the free glyph \\u25cb", out)
	}
}

// TestCPUMapL3Grouping covers point 3 of the topology brief: once the
// topology has L3 domain data, the CPU Map tab groups a node's cores by
// L3 domain -- a "L3 #k" label above each domain's cells, a "|" boundary
// separator between adjacent domains, "L3 boundary" named in the legend,
// and "L3 #n" in the detail panel for the cursor's core -- with hits
// still mapping to the right (global) core index despite the extra label
// line and boundary glyph.
func TestCPUMapL3Grouping(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, hits := renderCPUMapTab(s, 0, 100, 24)

	for _, want := range []string{"L3 #0", "L3 #1", "|", "L3 boundary"} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderCPUMapTab() = %q, want %q present", out, want)
		}
	}

	// Core 6 is the first core belonging to L3 #1 (cores 0-5 are L3 #0,
	// 6-11 are L3 #1, per realHostTopo).
	var got *hit
	for i := range hits {
		if hits[i].kind == "core" && hits[i].index == 6 {
			got = &hits[i]
		}
	}
	if got == nil {
		t.Fatalf("no core hit for index 6 recorded (hits = %+v)", hits)
	}

	detail := cpuMapDetail(s, 6)
	if !strings.Contains(detail, "L3 #1") {
		t.Fatalf("cpuMapDetail(6) = %q, want \"L3 #1\"", detail)
	}
}

// TestCPUMapNoL3DataRendersUnchanged covers the gate: a topology with no
// L3 domains at all (testTopo, every fixture elsewhere in this package)
// renders the CPU Map tab with no L3-grouping text at all -- cpuMapNode-
// Grid falls back to plain renderNodeMap output, and the legend omits
// "L3 boundary".
func TestCPUMapNoL3DataRendersUnchanged(t *testing.T) {
	s := &model.Snapshot{Topo: testTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := renderCPUMapTab(s, -1, 80, 24)
	if strings.Contains(out, "L3") {
		t.Fatalf("renderCPUMapTab() = %q, want no L3 grouping text without L3Domains data", out)
	}
}

// TestCPUMapLegendFitsPrimaryWidth covers the fix for the CPU Map
// legend's own natural width (61 cols, once L3 grouping adds "| L3
// boundary") exceeding splitBodyWidth(120)'s 60-column primary share:
// unwrapped, that stretched the whole primary block a column wider than
// its allotted width, and the assembled tab (primary+gap+secondary) came
// out one column too wide overall -- which, once truncated back down to
// the terminal's actual width by App.clampWidth, chopped the detail
// panel's own right border into "..". At widths 120 and 121 with
// realHostTopo (whose L3 domains trigger the longer legend), every line
// must fit within w and never show that truncation artifact.
//
// clipLinesTo(out, budget) mirrors renderFull's own final step (View()
// force-clips the whole rendered body down to its height budget,
// regardless of what a render function produced internally): reserving
// only 1 line of primaryBudget for the legend, when it actually wraps to
// 2 lines at these widths (its 61-col content doesn't fit primaryW's 60),
// left mapBlock -- and so the whole joined body -- one line taller than
// budget, and that top-down clip silently dropped the legend's second
// line, the one holding "boundary".
func TestCPUMapLegendFitsPrimaryWidth(t *testing.T) {
	s := &model.Snapshot{Topo: realHostTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	const budget = 24
	for _, w := range []int{120, 121} {
		out, _ := renderCPUMapTab(s, 0, w, budget)
		out = clipLinesTo(out, budget)
		for i, l := range strings.Split(out, "\n") {
			if lw := lipgloss.Width(l); lw > w {
				t.Fatalf("width %d: line %d width = %d, want <= %d: %q", w, i, lw, w, l)
			}
		}
		if strings.Contains(out, "..") {
			t.Fatalf("width %d: renderCPUMapTab() = %q, want no \"..\" truncation artifact", w, out)
		}
		// The legend wraps word-by-word at these widths, splitting "L3" and
		// "boundary" onto separate physical lines (lipgloss's own
		// Style.Width wrap) -- so check both halves survived rather than
		// the literal joined phrase, which no longer appears contiguous
		// once wrapped.
		if !strings.Contains(out, "L3") || !strings.Contains(out, "boundary") {
			t.Fatalf("width %d: renderCPUMapTab() = %q, want both \"L3\" and \"boundary\" present (legend's 2nd wrapped line must not be clipped off)", w, out)
		}
	}
}

func TestCPUMapDetail(t *testing.T) {
	s := snapFromXML(t, map[string]string{"pinned-vm": pinnedNode0XML})
	// core 0 (node 0, socket 0, id 0) has threads 0 and 4; thread 0 is
	// pinned to pinned-vm, thread 4 is free.
	out := cpuMapDetail(s, 0)
	if !strings.Contains(out, "thread 0: pinned-vm") {
		t.Fatalf("cpuMapDetail(0) = %q, want it to name pinned-vm on thread 0", out)
	}
	if !strings.Contains(out, "thread 4: free") {
		t.Fatalf("cpuMapDetail(0) = %q, want thread 4 free", out)
	}
}

func TestCPUMapDetailOutOfRange(t *testing.T) {
	s := snapFromXML(t, map[string]string{})
	if out := cpuMapDetail(s, 99); out != "" {
		t.Fatalf("cpuMapDetail(99) = %q, want empty string for out-of-range index", out)
	}
	if out := cpuMapDetail(s, -1); out != "" {
		t.Fatalf("cpuMapDetail(-1) = %q, want empty string for out-of-range index", out)
	}
}

func TestCPUMapDetailPending(t *testing.T) {
	s := snapFromXML(t, map[string]string{"pinned-vm": pinnedNode0XML})
	// Project a re-pin of pinned-vm's vcpu 0 onto (still-free) thread 4:
	// Project marks the whole VM "touched", so its new claim on thread 4
	// lands in ThreadUse.Pending rather than ThreadUse.VMs.
	proj := model.Project(s, nil, []model.PendingOp{{
		Kind:    model.OpPin,
		VM:      "pinned-vm",
		Pins:    map[int][]int{0: {4}},
		MemNode: -1,
	}})
	out := cpuMapDetail(proj, 0)
	if !strings.Contains(out, "thread 4: pending: pinned-vm") {
		t.Fatalf("cpuMapDetail(0) on projected snapshot = %q, want pending claim on thread 4", out)
	}
}

func TestCPUMapSharedDetail(t *testing.T) {
	xmls := map[string]string{
		"vm-a": `<domain type='kvm'>
  <name>vm-a</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b13</uuid>
  <memory unit='KiB'>500</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
    <vcpupin vcpu='1' cpuset='1'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`,
		"vm-b": `<domain type='kvm'>
  <name>vm-b</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b12</uuid>
  <memory unit='KiB'>500</memory>
  <vcpu>1</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`,
	}
	s := snapFromXML(t, xmls)
	out := cpuMapDetail(s, 0)
	if !strings.Contains(out, "thread 0: vm-a, vm-b (shared)") {
		t.Fatalf("cpuMapDetail(0) = %q, want shared claim on thread 0", out)
	}
}

func TestCursorMoves(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.tab = 1

	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyRight})
	view := a.View()
	// Core index 1 is socket 0, core id 1, threads 1 and 5 in testTopo.
	if !strings.Contains(view, "core 1 (socket 0, node 0)") {
		t.Fatalf("View() after right arrow = %q, want detail for core 1", view)
	}
	if !strings.Contains(view, "threads 1,5") {
		t.Fatalf("View() after right arrow = %q, want threads 1,5 in detail", view)
	}
}

// singleThreadTopo is a 1-node topology with one no-SMT core (thread 0
// alone) and one 2-thread core, so renderCPUMap must handle a core whose
// Threads has length 1 without panicking.
func singleThreadTopo() *hostinfo.Topology {
	threads := map[int]hostinfo.Thread{
		0: {ID: 0, Core: 0, Socket: 0, Node: 0, Sibling: -1},
		1: {ID: 1, Core: 1, Socket: 0, Node: 0, Sibling: 2},
		2: {ID: 2, Core: 1, Socket: 0, Node: 0, Sibling: 1},
	}
	cores := []hostinfo.Core{
		{Socket: 0, ID: 0, Node: 0, Threads: []int{0}},
		{Socket: 0, ID: 1, Node: 0, Threads: []int{1, 2}},
	}
	nodes := []hostinfo.Node{
		{ID: 0, Threads: []int{0, 1, 2}, MemTotalKiB: 1000},
	}
	return &hostinfo.Topology{Nodes: nodes, Cores: cores, Threads: threads}
}

func TestCPUMapNoSMTCoreDoesNotPanic(t *testing.T) {
	s := &model.Snapshot{Topo: singleThreadTopo(), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	out, _ := renderCPUMapTab(s, 0, 80, 24)
	if !strings.Contains(out, "node 0") {
		t.Fatalf("renderCPUMapTab() = %q, want a node 0 heading", out)
	}
	_ = cpuMapDetail(s, 0)
}

func TestCursorClampAndOtherTabsInert(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)

	// Cursor keys are inert outside the CPU Map tab.
	a.tab = 0
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyRight})
	if a.cursor != 0 {
		t.Fatalf("cursor = %d on Overview tab after right arrow, want 0 (inert)", a.cursor)
	}

	a.tab = 1
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if a.cursor != 0 {
		t.Fatalf("cursor = %d after left arrow at 0, want clamped to 0", a.cursor)
	}
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.cursor != 3 {
		t.Fatalf("cursor = %d after down arrow (4 cores total), want clamped to 3", a.cursor)
	}
}

// TestPinsSummary covers all five branches directly: pinned (single node,
// every vcpu pinned), unpinned, partial (some vcpus unpinned), cross-node
// (pinned threads span more than one node), and unknown (a pinned thread id
// that resolves to no topology node at all -- the bug this fix round found:
// pinsSummary used to fall through to "0 pinned -> node 0" in that case).
func TestPinsSummary(t *testing.T) {
	topo := testTopo() // threads 0,1,4,5 -> node 0; 2,3,6,7 -> node 1
	cases := []struct {
		name  string
		pins  map[int][]int
		vcpus int
		want  string
	}{
		{"unpinned", nil, 2, "unpinned"},
		{"partial", map[int][]int{0: {0}}, 2, "partial"},
		{"cross-node", map[int][]int{0: {0}, 1: {2}}, 2, "cross-node"},
		{"pinned", map[int][]int{0: {0}, 1: {1}}, 2, "2 pinned -> node 0"},
		{"unknown", map[int][]int{0: {999}}, 1, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := &model.VM{Pins: c.pins, VCPUs: c.vcpus}
			if got := pinsSummary(topo, v); got != c.want {
				t.Fatalf("pinsSummary() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStateName(t *testing.T) {
	cases := []struct {
		st   libvirtio.DomState
		want string
	}{
		{libvirtio.StateRunning, "running"},
		{libvirtio.StateShutoff, "shut off"},
		{libvirtio.StateOther, "other"},
	}
	for _, c := range cases {
		if got := stateName(c.st); got != c.want {
			t.Fatalf("stateName(%v) = %q, want %q", c.st, got, c.want)
		}
	}
}

func TestGPUNodeCol(t *testing.T) {
	cases := []struct {
		name    string
		devices []model.Device
		want    string
	}{
		{"none", nil, "-"},
		{"unknown", []model.Device{{Addr: "0000:81:00.0", Node: -1}}, "?"},
		{"known", []model.Device{{Addr: "0000:81:00.0", Node: 1}}, "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := &model.VM{Devices: c.devices}
			if got := gpuNodeCol(v); got != c.want {
				t.Fatalf("gpuNodeCol() = %q, want %q", got, c.want)
			}
		})
	}
}
