package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
