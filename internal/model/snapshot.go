// Package model turns a host topology and a set of libvirt domains into a
// Snapshot: per-VM pinning/membind state plus conflict Flags an operator
// can act on.
package model

import (
	"sort"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
)

// FlagKind identifies one kind of pinning/NUMA conflict.
type FlagKind int

const (
	FlagUnpinned FlagKind = iota
	FlagPartialPin
	FlagNoMemBind
	FlagNodeMismatch
	FlagCrossNode
	FlagMemPressure
	FlagGPUUnknownNode
	FlagUnsupported
)

// Flag is one detected conflict, with operator-readable explanation text.
type Flag struct {
	Kind        FlagKind
	Cause       string // one sentence
	Consequence string // one sentence
}

// Device is a passthrough PCI device attached to a VM.
type Device struct {
	Addr string
	Node int // -1 unknown
}

// VM is one libvirt domain's pinning/membind state plus detected conflicts.
type VM struct {
	Name        string
	State       libvirtio.DomState
	VCPUs       int
	MemoryKiB   uint64
	Pins        map[int][]int
	MemNodes    []int
	Devices     []Device
	Flags       []Flag
	Unsupported bool
}

// HasFlag reports whether v carries a flag of kind k.
func (v *VM) HasFlag(k FlagKind) bool {
	for _, f := range v.Flags {
		if f.Kind == k {
			return true
		}
	}
	return false
}

// GPUNode returns the node of the first device with a known node, or -1 if
// there is none.
func (v *VM) GPUNode() int {
	for _, d := range v.Devices {
		if d.Node != -1 {
			return d.Node
		}
	}
	return -1
}

// ThreadUse records which VMs claim a host thread.
type ThreadUse struct {
	VMs     []string // sorted
	Pending []string // VMs claiming this thread via pending ops (filled by Project)
}

// Snapshot is the current allocation of every VM against the host topology.
type Snapshot struct {
	Topo        *hostinfo.Topology
	VMs         []VM // sorted by name
	Use         map[int]ThreadUse
	BoundMemKiB map[int]uint64 // node -> sum of memory of VMs bound to it
}

// VM returns the VM named name, or nil if absent.
func (s *Snapshot) VM(name string) *VM {
	for i := range s.VMs {
		if s.VMs[i].Name == name {
			return &s.VMs[i]
		}
	}
	return nil
}

// Build converts doms into VMs against topo and computes their conflict
// Flags, thread usage, and per-node bound memory. pciNode resolves a
// hostdev's PCI address to its NUMA node (-1 if unknown).
func Build(topo *hostinfo.Topology, doms []libvirtio.Domain, pciNode func(addr string) int) *Snapshot {
	vms := make([]VM, len(doms))
	for i, d := range doms {
		vms[i] = domainToVM(d, pciNode)
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })

	use, bound := computeFlags(topo, vms)
	return &Snapshot{Topo: topo, VMs: vms, Use: use, BoundMemKiB: bound}
}

// domainToVM builds a VM from a libvirt domain. Config is always non-nil
// (per the libvirtio package invariant); an unsupported domain is detected
// via ParseErr != nil, never via Config == nil.
func domainToVM(d libvirtio.Domain, pciNode func(addr string) int) VM {
	cfg := d.Config
	v := VM{
		Name:      cfg.Name,
		State:     d.State,
		VCPUs:     cfg.VCPUs,
		MemoryKiB: cfg.MemoryKiB,
		Pins:      cfg.VCPUPins,
		MemNodes:  cfg.MemNodes,
	}
	for _, addr := range cfg.Hostdevs {
		v.Devices = append(v.Devices, Device{Addr: addr, Node: pciNode(addr)})
	}
	if d.ParseErr != nil {
		v.Unsupported = true
		v.Flags = []Flag{flagFor(FlagUnsupported)}
	}
	return v
}

// computeFlags recomputes every VM's Flags plus thread usage and per-node
// bound memory. It is factored out (rather than inlined in Build) so
// Project can re-run the identical logic on a projected VM set. Pure: no
// I/O, mutates only the VM Flags fields of the vms it was given.
func computeFlags(topo *hostinfo.Topology, vms []VM) (map[int]ThreadUse, map[int]uint64) {
	nodeOfThread := make(map[int]int, len(topo.Threads))
	for id, th := range topo.Threads {
		nodeOfThread[id] = th.Node
	}
	nodeTotal := make(map[int]uint64, len(topo.Nodes))
	for _, n := range topo.Nodes {
		nodeTotal[n.ID] = n.MemTotalKiB
	}

	threadVMs := make(map[int][]string)
	bound := make(map[int]uint64)

	for i := range vms {
		v := &vms[i]
		if v.Unsupported {
			continue
		}
		v.Flags = nil

		vmThreads := map[int]bool{} // dedupe: a VM's own vcpus can share a thread
		for _, threads := range v.Pins {
			for _, t := range threads {
				vmThreads[t] = true
			}
		}
		for t := range vmThreads {
			threadVMs[t] = append(threadVMs[t], v.Name)
		}

		pinNodes := pinnedNodeSet(v.Pins, nodeOfThread)

		switch {
		case len(v.Pins) == 0:
			v.Flags = append(v.Flags, flagFor(FlagUnpinned))
		case len(v.Pins) < v.VCPUs:
			v.Flags = append(v.Flags, flagFor(FlagPartialPin))
		}

		if len(v.Pins) > 0 && v.MemNodes == nil {
			v.Flags = append(v.Flags, flagFor(FlagNoMemBind))
		}

		if len(pinNodes) > 1 {
			v.Flags = append(v.Flags, flagFor(FlagCrossNode))
		}

		if nodeMismatch(v, pinNodes) {
			v.Flags = append(v.Flags, flagFor(FlagNodeMismatch))
		}

		for _, d := range v.Devices {
			if d.Node == -1 {
				v.Flags = append(v.Flags, flagFor(FlagGPUUnknownNode))
				break
			}
		}

		if v.MemNodes != nil {
			for _, n := range v.MemNodes {
				bound[n] += v.MemoryKiB
			}
		}
	}

	overcommitted := make(map[int]bool)
	for node, total := range nodeTotal {
		if bound[node] > total {
			overcommitted[node] = true
		}
	}
	for i := range vms {
		v := &vms[i]
		if v.Unsupported || v.MemNodes == nil {
			continue
		}
		for _, n := range v.MemNodes {
			if overcommitted[n] {
				v.Flags = append(v.Flags, flagFor(FlagMemPressure))
				break
			}
		}
	}

	use := make(map[int]ThreadUse, len(threadVMs))
	for t, names := range threadVMs {
		sort.Strings(names)
		use[t] = ThreadUse{VMs: names}
	}

	return use, bound
}

// nodeMismatch implements the FlagNodeMismatch rule: the pinned threads'
// node-set differs from MemNodes when both exist, or the VM's GPU node is
// excluded by either.
func nodeMismatch(v *VM, pinNodes map[int]bool) bool {
	memNodes := intSet(v.MemNodes)

	if len(v.Pins) > 0 && v.MemNodes != nil && !setsEqual(pinNodes, memNodes) {
		return true
	}

	gpuNode := v.GPUNode()
	if gpuNode < 0 {
		return false
	}
	if len(pinNodes) > 0 && !pinNodes[gpuNode] {
		return true
	}
	if v.MemNodes != nil && !memNodes[gpuNode] {
		return true
	}
	return false
}

// pinnedNodeSet returns the set of NUMA nodes touched by any thread in
// pins, per nodeOfThread.
func pinnedNodeSet(pins map[int][]int, nodeOfThread map[int]int) map[int]bool {
	set := map[int]bool{}
	for _, threads := range pins {
		for _, t := range threads {
			if node, ok := nodeOfThread[t]; ok {
				set[node] = true
			}
		}
	}
	return set
}

func intSet(ids []int) map[int]bool {
	if ids == nil {
		return nil
	}
	set := make(map[int]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func setsEqual(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// flagTexts holds the fixed Cause/Consequence sentence pair for each flag
// kind.
var flagTexts = map[FlagKind][2]string{
	FlagUnpinned: {
		"This VM's vCPUs are not pinned to any host thread.",
		"The scheduler is free to move it across NUMA nodes, causing unpredictable latency.",
	},
	FlagPartialPin: {
		"Only some of this VM's vCPUs are pinned; the rest float free.",
		"The unpinned vCPUs can land on a different NUMA node than the pinned ones and slow down cross-vCPU communication.",
	},
	FlagNoMemBind: {
		"RAM is not bound to a NUMA node.",
		"The kernel may allocate it on either node and starve the node where GPU VMs live.",
	},
	FlagNodeMismatch: {
		"The pinned vCPUs and the memory binding, or the passthrough device, sit on different NUMA nodes.",
		"Every memory access crosses the NUMA interconnect, adding latency and stealing bandwidth from other VMs.",
	},
	FlagCrossNode: {
		"This VM's vCPUs are pinned to threads on more than one NUMA node.",
		"Memory accesses that cross nodes pay extra latency and congest the interconnect between sockets.",
	},
	FlagMemPressure: {
		"The memory bound to this node exceeds the node's total RAM.",
		"The host will swap or reclaim pages under load, causing stalls for every VM bound to this node.",
	},
	FlagGPUUnknownNode: {
		"The NUMA node of a passthrough device could not be determined.",
		"Placement decisions for this VM cannot account for the device's true node affinity.",
	},
	FlagUnsupported: {
		"The domain XML could not be parsed.",
		"This VM is shown read-only and excluded from pinning suggestions.",
	},
}

// flagFor returns the fixed Cause/Consequence text for a flag kind.
func flagFor(k FlagKind) Flag {
	t := flagTexts[k]
	return Flag{Kind: k, Cause: t[0], Consequence: t[1]}
}
