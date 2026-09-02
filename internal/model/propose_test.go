package model

import (
	"reflect"
	"strings"
	"testing"

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
