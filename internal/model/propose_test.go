package model

import (
	"reflect"
	"strings"
	"testing"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
)

// fourVCPUXML is a plain 4-vcpu VM with no pins, no membind, no devices.
// MemoryKiB (800) fits under any single node's 1000 KiB total in testTopo.
const fourVCPUXML = `<domain type='kvm'>
  <name>four-vcpu</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b20</uuid>
  <memory unit='KiB'>800</memory>
  <vcpu>4</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// node1HogXML pins both its vcpus to threads 2 and 6: the two threads of
// core {2,6} on node1 in testTopo, so that core is fully used and node1's
// only other core ({3,7}) is left as the sole fully-free core.
const node1HogXML = `<domain type='kvm'>
  <name>node1-hog</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b21</uuid>
  <memory unit='KiB'>100</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='2'/>
    <vcpupin vcpu='1' cpuset='6'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// bigVCPUXML asks for more vcpus than any node in testTopo has threads (4).
const bigVCPUXML = `<domain type='kvm'>
  <name>too-big</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b22</uuid>
  <memory unit='KiB'>100</memory>
  <vcpu>5</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// threeVCPUXML has an odd vcpu count, so filling node0's two fully-free
// cores in sibling-pair order leaves one core half used.
const threeVCPUXML = `<domain type='kvm'>
  <name>three-vcpu</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b23</uuid>
  <memory unit='KiB'>300</memory>
  <vcpu>3</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// buildSnap parses xmls into domains and builds a Snapshot against
// testTopo(), resolving PCI addresses via pci (nil for no hostdevs).
func buildSnap(t *testing.T, pci map[string]int, xmls ...string) *Snapshot {
	t.Helper()
	doms := make([]libvirtio.Domain, len(xmls))
	for i, x := range xmls {
		cfg, err := libvirtio.ParseDomainXML(x)
		if err != nil {
			t.Fatalf("ParseDomainXML: %v", err)
		}
		doms[i] = libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning}
	}
	pciNode := func(addr string) int {
		if pci == nil {
			return -1
		}
		if n, ok := pci[addr]; ok {
			return n
		}
		return -1
	}
	return Build(testTopo(), doms, pciNode)
}

func TestProposeFollowsGPU(t *testing.T) {
	// gpuXML's hostdev resolves to node 1; both nodes are otherwise empty.
	snap := buildSnap(t, map[string]int{"0000:81:00.0": 1}, gpuXML)

	got, err := Propose(snap, "gpu-unknown")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got.Node != 1 {
		t.Errorf("Node = %d, want 1", got.Node)
	}
	if got.MemNode != 1 {
		t.Errorf("MemNode = %d, want 1", got.MemNode)
	}

	joined := strings.Join(got.Rationale, " ")
	if !strings.Contains(joined, "0000:81:00.0") {
		t.Errorf("Rationale = %v, want mention of device addr", got.Rationale)
	}
	if !strings.Contains(joined, "node 1") {
		t.Errorf("Rationale = %v, want mention of node 1", got.Rationale)
	}
}

func TestProposePrefersFreeMemoryAndCores(t *testing.T) {
	snap := buildSnap(t, nil, plainXML) // vcpu 2, memory 1000, no gpu

	// Force node0 to look nearly full: only 50 KiB free, less than the
	// VM's 1000 KiB requirement. Node1 stays untouched (1000 KiB free).
	snap.BoundMemKiB[0] = 950

	got, err := Propose(snap, "plain-vm")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got.Node != 1 {
		t.Errorf("Node = %d, want 1", got.Node)
	}
	if got.MemNode != 1 {
		t.Errorf("MemNode = %d, want 1", got.MemNode)
	}
}

func TestProposeSiblingPairs(t *testing.T) {
	// pinnedNoNumaXML occupies one thread of each of node0's two cores
	// (threads 0 and 1), leaving node0 with zero fully-free cores. node1
	// stays fully free, so the 4-vcpu VM must land there.
	snap := buildSnap(t, nil, pinnedNoNumaXML, fourVCPUXML)

	got, err := Propose(snap, "four-vcpu")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got.Node != 1 {
		t.Fatalf("Node = %d, want 1", got.Node)
	}

	want := map[int][]int{0: {2}, 1: {6}, 2: {3}, 3: {7}}
	if !reflect.DeepEqual(got.Pins, want) {
		t.Errorf("Pins = %v, want %v", got.Pins, want)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", got.Warnings)
	}
}

func TestProposeSharingWarning(t *testing.T) {
	// pinnedNoNumaXML blocks node0's cores fully-free count (as above).
	// node1HogXML fully occupies node1's core {2,6}, leaving only core
	// {3,7} fully free there. The 4-vcpu VM must still land on node1 (its
	// only viable node) and its last two vcpus reuse threads 2 and 6.
	snap := buildSnap(t, nil, pinnedNoNumaXML, node1HogXML, fourVCPUXML)

	got, err := Propose(snap, "four-vcpu")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got.Node != 1 {
		t.Fatalf("Node = %d, want 1", got.Node)
	}

	want := map[int][]int{0: {3}, 1: {7}, 2: {2}, 3: {6}}
	if !reflect.DeepEqual(got.Pins, want) {
		t.Errorf("Pins = %v, want %v", got.Pins, want)
	}

	joined := strings.Join(got.Warnings, " ")
	if !strings.Contains(joined, "sharing threads with:") {
		t.Errorf("Warnings = %v, want mention of \"sharing threads with:\"", got.Warnings)
	}
	if !strings.Contains(joined, "node1-hog") {
		t.Errorf("Warnings = %v, want mention of node1-hog", got.Warnings)
	}
}

func TestProposeGPUInsufficientMemory(t *testing.T) {
	// Node 1 has only 500 KiB free but the GPU VM needs 1000 KiB. Propose
	// must still place it there (the GPU node is a hard constraint) and
	// warn about the shortfall instead of erroring or moving nodes.
	snap := buildSnap(t, map[string]int{"0000:81:00.0": 1}, gpuXML)
	snap.BoundMemKiB[1] = 500

	got, err := Propose(snap, "gpu-unknown")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got.Node != 1 {
		t.Fatalf("Node = %d, want 1", got.Node)
	}
	joined := strings.Join(got.Warnings, " ")
	if !strings.Contains(joined, "500 KiB") {
		t.Errorf("Warnings = %v, want mention of the 500 KiB shortfall", got.Warnings)
	}
}

func TestProposeOddVCPUHalfUsedCore(t *testing.T) {
	// Both nodes are empty, so node0 wins the ID tiebreak. Filling its two
	// fully-free cores {0,4} then {1,5} for 3 vcpus leaves thread 5 unused.
	snap := buildSnap(t, nil, threeVCPUXML)

	got, err := Propose(snap, "three-vcpu")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got.Node != 0 {
		t.Fatalf("Node = %d, want 0", got.Node)
	}
	want := map[int][]int{0: {0}, 1: {4}, 2: {1}}
	if !reflect.DeepEqual(got.Pins, want) {
		t.Errorf("Pins = %v, want %v", got.Pins, want)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none (thread 5 is left free, not shared)", got.Warnings)
	}
}

func TestProposeZeroFreeCoresSharing(t *testing.T) {
	// pinnedNoNumaXML occupies one thread of each of node0's two cores, so
	// node0 has zero fully-free cores (unlike TestProposeSharingWarning,
	// where one core stayed fully free). Starving node1 of memory forces
	// node0 anyway, so thread selection must build every pin from the
	// least-used-thread fallback, not from any free-core phase.
	snap := buildSnap(t, nil, pinnedNoNumaXML, fourVCPUXML)
	snap.BoundMemKiB[1] = 950

	got, err := Propose(snap, "four-vcpu")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got.Node != 0 {
		t.Fatalf("Node = %d, want 0", got.Node)
	}
	want := map[int][]int{0: {4}, 1: {5}, 2: {0}, 3: {1}}
	if !reflect.DeepEqual(got.Pins, want) {
		t.Errorf("Pins = %v, want %v", got.Pins, want)
	}
	joined := strings.Join(got.Warnings, " ")
	if !strings.Contains(joined, "sharing threads with:") {
		t.Errorf("Warnings = %v, want mention of \"sharing threads with:\"", got.Warnings)
	}
	if !strings.Contains(joined, "pinned-no-numa") {
		t.Errorf("Warnings = %v, want mention of pinned-no-numa", got.Warnings)
	}
}

func TestProposeTooBig(t *testing.T) {
	snap := buildSnap(t, nil, bigVCPUXML) // vcpu 5, no node has 5 threads

	_, err := Propose(snap, "too-big")
	if err == nil {
		t.Fatal("Propose: want error, got nil")
	}
}

func TestProposeUnknownVM(t *testing.T) {
	snap := buildSnap(t, nil, plainXML)

	_, err := Propose(snap, "nope")
	if err == nil {
		t.Fatal("Propose: want error, got nil")
	}
}

// twoVCPUXML is a plain 2-vcpu VM with no pins, no membind, no devices --
// used by the reserved-threads tests below, which need a vcpu count that
// matches exactly one (non-reserved) core in testTopo.
const twoVCPUXML = `<domain type='kvm'>
  <name>two-vcpu</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b24</uuid>
  <memory unit='KiB'>200</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// TestProposeWithinExcludesReservedCore covers selectThreads' fully-free
// pass: reserving node0's core {0,4} must leave only core {1,5}
// eligible, even though {0,4} would otherwise be the first fully-free
// core in topology order.
func TestProposeWithinExcludesReservedCore(t *testing.T) {
	snap := buildSnap(t, nil, twoVCPUXML).WithReserved(map[int]bool{0: true, 4: true})

	got, err := ProposeWithin(snap, "two-vcpu", 0, nil)
	if err != nil {
		t.Fatalf("ProposeWithin: %v", err)
	}
	want := map[int][]int{0: {1}, 1: {5}}
	if !reflect.DeepEqual(got.Pins, want) {
		t.Errorf("Pins = %v, want %v (reserved core {0,4} must be skipped)", got.Pins, want)
	}
}

// TestFullyFreeCoreCountIgnoresReserved covers FullyFreeCoreCount: node0
// has two cores total, but one is reserved, so only one counts as fully
// free even though nothing has actually claimed either yet.
func TestFullyFreeCoreCountIgnoresReserved(t *testing.T) {
	snap := buildSnap(t, nil).WithReserved(map[int]bool{0: true, 4: true})
	if got := FullyFreeCoreCount(snap, 0); got != 1 {
		t.Errorf("FullyFreeCoreCount(node 0) = %d, want 1 (core {0,4} reserved)", got)
	}
}

// TestProposeWithinReservedNodeInsufficientCapacity covers both the
// capacity check and the fallback pool: reserving every thread on node0
// must make ProposeWithin refuse it as too small, never silently reuse a
// reserved thread to make up the shortfall.
func TestProposeWithinReservedNodeInsufficientCapacity(t *testing.T) {
	snap := buildSnap(t, nil, twoVCPUXML).WithReserved(map[int]bool{0: true, 4: true, 1: true, 5: true})
	if _, err := ProposeWithin(snap, "two-vcpu", 0, nil); err == nil {
		t.Error("ProposeWithin succeeded on a fully-reserved node, want a capacity error")
	}
}

// TestProposalEmulatorMatchesAssignedThreads covers Proposal.Emulator's
// default: the same threads Pins itself assigns.
func TestProposalEmulatorMatchesAssignedThreads(t *testing.T) {
	snap := buildSnap(t, nil, fourVCPUXML)
	got, err := Propose(snap, "four-vcpu")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	want := assignedThreads(got.Pins)
	if !reflect.DeepEqual(got.Emulator, want) {
		t.Errorf("Emulator = %v, want %v (assigned vCPU threads)", got.Emulator, want)
	}
}

// TestProposeWithinAutoMatchesPropose covers the wizard form's default
// state: ProposeWithin(s, vm, -1, nil) (choose automatically, any
// thread) must produce exactly what Propose itself does.
func TestProposeWithinAutoMatchesPropose(t *testing.T) {
	snap := buildSnap(t, nil, plainXML)

	want, err := Propose(snap, "plain-vm")
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	got, err := ProposeWithin(snap, "plain-vm", -1, nil)
	if err != nil {
		t.Fatalf("ProposeWithin: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProposeWithin(-1, nil) = %+v, want %+v (same as Propose)", got, want)
	}
}

// TestProposeWithinExplicitNodeIgnoresGPU covers the form's "cycle to
// another node" case: gpu-unknown's hostdev resolves to node 1, so plain
// Propose would force node 1 -- but ProposeWithin with an explicit node
// (0) must honor that choice outright, not silently override it back
// onto the GPU's node.
func TestProposeWithinExplicitNodeIgnoresGPU(t *testing.T) {
	snap := buildSnap(t, map[string]int{"0000:81:00.0": 1}, gpuXML)

	got, err := ProposeWithin(snap, "gpu-unknown", 0, nil)
	if err != nil {
		t.Fatalf("ProposeWithin: %v", err)
	}
	if got.Node != 0 {
		t.Fatalf("Node = %d, want 0 (the explicit node, not the GPU's node 1)", got.Node)
	}
	if got.MemNode != 0 {
		t.Errorf("MemNode = %d, want 0", got.MemNode)
	}
	for vcpu, threads := range got.Pins {
		if th, ok := snap.Topo.Threads[threads[0]]; !ok || th.Node != 0 {
			t.Errorf("Pins[%d] = %v, want a thread on node 0", vcpu, threads)
		}
	}
}

// TestProposeWithinAllowedRestrictsThreads covers the form's "within: L3
// #k" filter: allowed = node1's core {2,6} only. A 2-vcpu VM must be
// pinned to exactly threads 2 and 6; the same VM asking for more vcpus
// than allowed contains must error rather than spill onto node1's other
// (disallowed) core {3,7}.
func TestProposeWithinAllowedRestrictsThreads(t *testing.T) {
	snap := buildSnap(t, nil, plainXML) // vcpu 2, memory 1000

	got, err := ProposeWithin(snap, "plain-vm", 1, []int{2, 6})
	if err != nil {
		t.Fatalf("ProposeWithin: %v", err)
	}
	if got.Node != 1 {
		t.Fatalf("Node = %d, want 1", got.Node)
	}
	want := map[int][]int{0: {2}, 1: {6}}
	if !reflect.DeepEqual(got.Pins, want) {
		t.Errorf("Pins = %v, want %v (restricted to the allowed set)", got.Pins, want)
	}

	_, err = ProposeWithin(snap, "plain-vm", 1, []int{2})
	if err == nil {
		t.Fatal("ProposeWithin(allowed of 1 thread, 2 vcpus): want error, got nil")
	}
}

// TestProposeWithinUnknownVM mirrors TestProposeUnknownVM for the new
// entry point.
func TestProposeWithinUnknownVM(t *testing.T) {
	snap := buildSnap(t, nil, plainXML)

	if _, err := ProposeWithin(snap, "nope", -1, nil); err == nil {
		t.Fatal("ProposeWithin: want error, got nil")
	}
}

// l3Topo builds a single-node, SMT2 topology with 4 L3 domains of 3 cores
// each (6 threads per domain, 24 threads total): core c (0-11) has
// sibling c+12, and belongs to L3 domain c/3. selectThreads' own
// fewest-domains packing tests need more than one L3 domain, unlike
// testTopo (2 nodes, no L3 data at all).
func l3Topo() *hostinfo.Topology {
	const domains, coresPerDomain = 4, 3
	const cores = domains * coresPerDomain

	threads := make(map[int]hostinfo.Thread, cores*2)
	coreList := make([]hostinfo.Core, cores)
	var nodeThreads []int
	for c := 0; c < cores; c++ {
		l3 := c / coresPerDomain
		sibling := c + cores
		threads[c] = hostinfo.Thread{ID: c, Core: c, Socket: 0, Node: 0, Sibling: sibling, L3: l3}
		threads[sibling] = hostinfo.Thread{ID: sibling, Core: c, Socket: 0, Node: 0, Sibling: c, L3: l3}
		coreList[c] = hostinfo.Core{Socket: 0, ID: c, Node: 0, L3: l3, Threads: []int{c, sibling}}
		nodeThreads = append(nodeThreads, c, sibling)
	}
	return &hostinfo.Topology{
		Nodes:   []hostinfo.Node{{ID: 0, Threads: nodeThreads, MemTotalKiB: 1000}},
		Cores:   coreList,
		Threads: threads,
	}
}

// snapWithUsedThreads builds a bare Snapshot against l3Topo with the given
// threads marked claimed by vmName -- enough for selectThreads' own
// tests, which only exercise coreFullyFree/threadUseCount, not the rest
// of Build's flag machinery.
func snapWithUsedThreads(vmName string, usedThreads ...int) *Snapshot {
	use := make(map[int]ThreadUse, len(usedThreads))
	for _, t := range usedThreads {
		use[t] = ThreadUse{VMs: []string{vmName}}
	}
	return &Snapshot{Topo: l3Topo(), Use: use, BoundMemKiB: map[int]uint64{}}
}

// TestSelectThreadsPrefersWholeFreeL3 covers the packing rule's main case:
// L3 #0 has one core used (2 of its 3 cores free), L3 #1-#3 are fully
// free (3 cores each). A 6-vcpu VM needs 3 cores, which doesn't fit L3 #0
// (only 2 free) but fits any of #1-#3 outright; the lowest-id tie among
// those wins, and L3 #0 must not be touched at all.
func TestSelectThreadsPrefersWholeFreeL3(t *testing.T) {
	snap := snapWithUsedThreads("hog", 0, 12) // core 0 (threads 0,12) used
	pins, note, warning := selectThreads(snap, 0, 6, nil)

	want := map[int][]int{0: {3}, 1: {15}, 2: {4}, 3: {16}, 4: {5}, 5: {17}}
	if !reflect.DeepEqual(pins, want) {
		t.Errorf("pins = %v, want %v (L3 domain #1's 3 free cores, L3 #0 left untouched)", pins, want)
	}
	if len(warning) != 0 {
		t.Errorf("warning = %v, want none (the whole VM fits one domain)", warning)
	}
	if !strings.Contains(note, "L3 domain #1") {
		t.Errorf("note = %q, want mention of L3 domain #1", note)
	}
}

// TestSelectThreadsBestFitLeavesEmptyDomains covers best fit's other
// half: L3 #0 has one core used (2 free), a 4-vcpu VM needs exactly 2
// cores -- L3 #0's 2 free cores fit exactly, and are the smallest fitting
// group (L3 #1-#3 have 3 free each), so the VM lands there instead of
// starting a fresh, previously-empty domain.
func TestSelectThreadsBestFitLeavesEmptyDomains(t *testing.T) {
	snap := snapWithUsedThreads("hog", 0, 12) // core 0 (threads 0,12) used
	pins, note, warning := selectThreads(snap, 0, 4, nil)

	want := map[int][]int{0: {1}, 1: {13}, 2: {2}, 3: {14}}
	if !reflect.DeepEqual(pins, want) {
		t.Errorf("pins = %v, want %v (L3 #0's own 2 free cores)", pins, want)
	}
	if len(warning) != 0 {
		t.Errorf("warning = %v, want none", warning)
	}
	if !strings.Contains(note, "L3 domain #0") {
		t.Errorf("note = %q, want mention of L3 domain #0", note)
	}
}

// TestSelectThreadsSpansFewestDomainsWhenNoneFits covers the field bug
// itself: every L3 domain has exactly 2 free cores (one core per domain
// used), and the VM needs 5 cores (10 vcpus) -- no single domain has
// enough, so the fewest domains that together do are used, largest-free-
// first (all tied at 2, so lowest id first): L3 #0 and #1 fully (2+2=4
// cores), then only one of L3 #2's two free cores to reach 5, leaving its
// other core free. The warning must name the 3-domain spread.
func TestSelectThreadsSpansFewestDomainsWhenNoneFits(t *testing.T) {
	snap := snapWithUsedThreads("hog", 0, 12, 3, 15, 6, 18, 9, 21) // core 0,3,6,9 used, one per domain
	pins, note, warning := selectThreads(snap, 0, 10, nil)

	want := map[int][]int{
		0: {1}, 1: {13}, 2: {2}, 3: {14},
		4: {4}, 5: {16}, 6: {5}, 7: {17},
		8: {7}, 9: {19},
	}
	if !reflect.DeepEqual(pins, want) {
		t.Errorf("pins = %v, want %v (domains #0, #1, #2 in core order; thread 8/20 in domain #2 left unused)", pins, want)
	}
	if !strings.Contains(note, "L3 domains #0, #1, #2") {
		t.Errorf("note = %q, want it to name L3 domains #0, #1, #2", note)
	}
	joined := strings.Join(warning, " ")
	if !strings.Contains(joined, "spans 3 L3 domains") {
		t.Errorf("warning = %v, want it to mention \"spans 3 L3 domains\"", warning)
	}
}

// TestSelectThreadsWithinFilterUnchanged covers the "within: L3 #k"
// filter: allowed restricted to one whole L3 domain's threads produces
// exactly the same single-group result the pre-packing code already gave
// (computed by hand): a 6-vcpu VM filling L3 #2's 3 free cores in core
// order, both siblings each.
func TestSelectThreadsWithinFilterUnchanged(t *testing.T) {
	snap := snapWithUsedThreads("") // nothing used
	allowed := []int{6, 18, 7, 19, 8, 20}
	pins, note, warning := selectThreads(snap, 0, 6, allowed)

	want := map[int][]int{0: {6}, 1: {18}, 2: {7}, 3: {19}, 4: {8}, 5: {20}}
	if !reflect.DeepEqual(pins, want) {
		t.Errorf("pins = %v, want %v", pins, want)
	}
	if len(warning) != 0 {
		t.Errorf("warning = %v, want none", warning)
	}
	if !strings.Contains(note, "L3 domain #2") {
		t.Errorf("note = %q, want mention of L3 domain #2", note)
	}
}

// TestSelectThreadsFallbackStillShares covers the existing fallback path,
// now reached through the packing rule instead of the old flat scan:
// every core is used except one (core 2, L3 #0), so a 4-vcpu VM's last 2
// vcpus must reuse already-claimed threads. Only one domain ever had any
// free cores, and it alone was too small (2 free threads for 4 vcpus), so
// the placement note still names it (the part that was actually used);
// the sharing warning must still fire and pins must still be 1:1.
func TestSelectThreadsFallbackStillShares(t *testing.T) {
	used := []int{0, 12, 1, 13, 3, 15, 4, 16, 5, 17, 6, 18, 7, 19, 8, 20, 9, 21, 10, 22, 11, 23}
	snap := snapWithUsedThreads("hog", used...)
	pins, note, warning := selectThreads(snap, 0, 4, nil)

	if !strings.Contains(note, "L3 domain #0") {
		t.Errorf("note = %q, want mention of L3 domain #0 (its 2 free threads are used before the fallback)", note)
	}
	joined := strings.Join(warning, " ")
	if !strings.Contains(joined, "sharing threads with:") {
		t.Errorf("warning = %v, want mention of \"sharing threads with:\"", warning)
	}
	if !strings.Contains(joined, "hog") {
		t.Errorf("warning = %v, want mention of hog", warning)
	}
	for vcpu, thr := range pins {
		if len(thr) != 1 {
			t.Errorf("pins[%d] = %v, want exactly one thread (1:1)", vcpu, thr)
		}
	}
	if len(pins) != 4 {
		t.Fatalf("len(pins) = %d, want 4", len(pins))
	}
}

// TestProposeWithinL3Placement is a ProposeWithin-level check that
// selectThreads' two return values land in the right Proposal field: the
// placement note in Rationale, the cross-domain-spread sentence in
// Warnings. A selectThreads-only test wouldn't catch the two being
// swapped at the proposeOnNode call site; this would.
func TestProposeWithinL3Placement(t *testing.T) {
	t.Run("single domain", func(t *testing.T) {
		snap := &Snapshot{
			Topo:        l3Topo(),
			VMs:         []VM{{Name: "vm", VCPUs: 6, MemoryKiB: 100}},
			Use:         map[int]ThreadUse{0: {VMs: []string{"hog"}}, 12: {VMs: []string{"hog"}}},
			BoundMemKiB: map[int]uint64{},
		}
		got, err := ProposeWithin(snap, "vm", 0, nil)
		if err != nil {
			t.Fatalf("ProposeWithin: %v", err)
		}
		if !strings.Contains(strings.Join(got.Rationale, " "), "L3 domain #") {
			t.Errorf("Rationale = %v, want mention of an L3 domain", got.Rationale)
		}
	})

	t.Run("spread across domains", func(t *testing.T) {
		use := map[int]ThreadUse{}
		for _, th := range []int{0, 12, 3, 15, 6, 18, 9, 21} {
			use[th] = ThreadUse{VMs: []string{"hog"}}
		}
		snap := &Snapshot{
			Topo:        l3Topo(),
			VMs:         []VM{{Name: "vm", VCPUs: 10, MemoryKiB: 100}},
			Use:         use,
			BoundMemKiB: map[int]uint64{},
		}
		got, err := ProposeWithin(snap, "vm", 0, nil)
		if err != nil {
			t.Fatalf("ProposeWithin: %v", err)
		}
		if !strings.Contains(strings.Join(got.Warnings, " "), "spans") {
			t.Errorf("Warnings = %v, want mention of \"spans\"", got.Warnings)
		}
	})
}
