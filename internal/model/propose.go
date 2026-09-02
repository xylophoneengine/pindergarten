package model

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
)

// Proposal is a suggested vCPU pin / memory-node binding for one VM,
// produced by Propose against an already-projected Snapshot.
type Proposal struct {
	Node      int
	Pins      map[int][]int // 1:1 vcpu -> single thread
	MemNode   int
	Rationale []string // plain-language sentences, why this node/threads
	Warnings  []string // e.g. not enough free full cores, sharing threads
}

// Propose picks a NUMA node and per-vcpu thread pins for vmName against s
// (the caller is expected to have already run Project over any pending ops,
// so Use.Pending reflects staged claims). A VM's own existing pins/binding
// in s count as used threads/memory exactly like any other VM's, so
// re-proposing for an already-pinned VM treats its current claim as
// occupied; that is intentional (simplest rule) rather than an oversight.
func Propose(s *Snapshot, vmName string) (*Proposal, error) {
	vm := s.VM(vmName)
	if vm == nil {
		return nil, fmt.Errorf("model: unknown VM %q", vmName)
	}
	node, rationale := chooseNode(s, vm)
	return proposeOnNode(s, vm, node, nil, rationale)
}

// ProposeWithin is Propose's more targeted variant, used by the pin
// wizard's form to re-propose as the operator changes the node/within
// fields: node -1 means "choose automatically", the same rule Propose
// itself uses (GPU-forced, else ranked by free memory/cores); a concrete
// node id is used outright, with no GPU-forced override -- the caller is
// explicitly picking a node, possibly one other than the VM's GPU node,
// and the resulting Proposal (and so the form's warning/summary) must
// reflect that choice rather than have Propose's own ranking silently
// override it. allowed nil means "any thread on node", matching Propose;
// a non-nil slice restricts thread selection (both the free-cores-first
// pass and the least-used-remaining fallback) to just those thread ids
// -- the form's "within: L3 #k" filter. Propose itself is unchanged; it
// still just calls this with node -1 and allowed nil.
func ProposeWithin(s *Snapshot, vmName string, node int, allowed []int) (*Proposal, error) {
	vm := s.VM(vmName)
	if vm == nil {
		return nil, fmt.Errorf("model: unknown VM %q", vmName)
	}
	var rationale []string
	if node == -1 {
		node, rationale = chooseNode(s, vm)
	}
	return proposeOnNode(s, vm, node, allowed, rationale)
}

// chooseNode is Propose's node-selection step, shared with ProposeWithin's
// node == -1 case: a passthrough device with a known node pins the VM to
// that node outright (PCIe locality trumps everything else); otherwise
// pickNode ranks nodes by free memory first, then by how many fully free
// cores they have, breaking ties by lower node ID. Returns the chosen
// node plus the one-sentence rationale explaining why.
func chooseNode(s *Snapshot, vm *VM) (int, []string) {
	if gpu := gpuDevice(vm); gpu != nil {
		return gpu.Node, []string{fmt.Sprintf(
			"GPU %s sits on NUMA node %d, so this VM is placed on node %d to keep PCIe traffic local.",
			gpu.Addr, gpu.Node, gpu.Node)}
	}
	node := pickNode(s, vm)
	return node, []string{fmt.Sprintf(
		"Node %d was chosen because it has %d KiB free memory (%d KiB needed) and %d fully free core(s), the best of the host's NUMA nodes.",
		node, FreeMemKiB(s, node), vm.MemoryKiB, FullyFreeCoreCount(s, node))}
}

// proposeOnNode is Propose/ProposeWithin's shared implementation once a
// node has been settled on (by chooseNode, or handed in directly by
// ProposeWithin's caller): capacity check, thread selection (restricted
// to allowed when it is non-nil), the memory-shortfall warning, and the
// returned Proposal. rationale carries whatever chooseNode already built
// (when the node was chosen automatically) or is empty (ProposeWithin
// with an explicit node -- the form itself is the operator's own
// rationale for that pick).
func proposeOnNode(s *Snapshot, vm *VM, node int, allowed []int, rationale []string) (*Proposal, error) {
	var warnings []string

	// Capacity check: the node (or, restricted, the allowed set actually
	// on this node) must have at least VCPUs threads, or there is nowhere
	// to place this VM without spilling elsewhere, which neither Propose
	// nor ProposeWithin ever does. Counting allowed's own on-node threads
	// (not just len(allowed)) guards against a caller passing thread ids
	// that aren't actually on node -- selectThreads' pool would otherwise
	// come up short of what this check promised.
	total := nodeThreadCount(s.Topo, node)
	if allowed != nil {
		total = 0
		for _, t := range allowed {
			if th, ok := s.Topo.Threads[t]; ok && th.Node == node {
				total++
			}
		}
	}
	if vm.VCPUs > total {
		return nil, fmt.Errorf("model: VM %q needs %d vcpus but node %d only has %d threads", vm.Name, vm.VCPUs, node, total)
	}

	// Thread selection: fill fully-free cores first (both siblings, in
	// core order), then spill onto the least-used remaining threads of
	// the same node if that is not enough.
	pins, shareWarning := selectThreads(s, node, vm.VCPUs, allowed)
	if shareWarning != "" {
		warnings = append(warnings, shareWarning)
	}
	rationale = append(rationale, fmt.Sprintf(
		"vCPUs are pinned to threads %s on node %d.", hostinfo.FormatCPUList(assignedThreads(pins)), node))

	// Memory is a soft constraint. If the chosen node (forced by the GPU,
	// picked by ranking, or handed in directly) does not have enough free
	// memory, proceed anyway but warn about the shortfall.
	free := FreeMemKiB(s, node)
	if free < vm.MemoryKiB {
		warnings = append(warnings, fmt.Sprintf(
			"Node %d has only %d KiB free but this VM needs %d KiB; the memory binding proceeds anyway.",
			node, free, vm.MemoryKiB))
	}

	// Memory always binds to the same node as the vCPUs.
	rationale = append(rationale, fmt.Sprintf(
		"Memory is bound to node %d so the kernel cannot allocate it on the other node.", node))

	return &Proposal{
		Node:      node,
		Pins:      pins,
		MemNode:   node,
		Rationale: rationale,
		Warnings:  warnings,
	}, nil
}

// gpuDevice returns the first device of vm with a known node, or nil if
// there is none. Mirrors VM.GPUNode's own selection rule, but keeps the
// Addr around for rationale text.
func gpuDevice(vm *VM) *Device {
	for i := range vm.Devices {
		if vm.Devices[i].Node != -1 {
			return &vm.Devices[i]
		}
	}
	return nil
}

// pickNode ranks s.Topo.Nodes for vm: enough free memory first, then most
// fully-free cores, then lower node ID. Nodes is sorted by ID ascending, so
// only replacing the running best on a strict improvement keeps the lower
// ID on a tie.
func pickNode(s *Snapshot, vm *VM) int {
	best := s.Topo.Nodes[0].ID
	bestMemOK := FreeMemKiB(s, best) >= vm.MemoryKiB
	bestFreeCores := FullyFreeCoreCount(s, best)

	for _, n := range s.Topo.Nodes[1:] {
		memOK := FreeMemKiB(s, n.ID) >= vm.MemoryKiB
		freeCores := FullyFreeCoreCount(s, n.ID)
		if betterNode(memOK, freeCores, bestMemOK, bestFreeCores) {
			best, bestMemOK, bestFreeCores = n.ID, memOK, freeCores
		}
	}
	return best
}

// betterNode reports whether candidate (memOK, freeCores) beats the current
// best under the ranking rule: enough memory first, then more fully-free
// cores. Equal on both leaves the current best in place (lower node ID
// wins ties, enforced by pickNode's iteration order).
func betterNode(memOK bool, freeCores int, bestMemOK bool, bestFreeCores int) bool {
	if memOK != bestMemOK {
		return memOK
	}
	return freeCores > bestFreeCores
}

// FreeMemKiB is a node's ranking-time free memory: total minus what is
// already bound there, floored at zero (BoundMemKiB can exceed total under
// FlagMemPressure). Deliberately not MemFreeKiB, which is live kernel data
// the wizard has no business consulting for a config-only proposal.
func FreeMemKiB(s *Snapshot, node int) uint64 {
	var total uint64
	for _, n := range s.Topo.Nodes {
		if n.ID == node {
			total = n.MemTotalKiB
			break
		}
	}
	bound := s.BoundMemKiB[node]
	if bound >= total {
		return 0
	}
	return total - bound
}

// FullyFreeCoreCount counts node's cores where every thread is unused: zero
// VMs and zero Pending claims. Exported alongside FreeMemKiB: the wizard
// form's per-node hint line ("free: N cores, M free") needs the same
// ranking-time figure pickNode itself uses.
func FullyFreeCoreCount(s *Snapshot, node int) int {
	count := 0
	for _, c := range s.Topo.Cores {
		if c.Node == node && coreFullyFree(s, c) {
			count++
		}
	}
	return count
}

// coreFullyFree reports whether every thread of c is unused in s.Use.
func coreFullyFree(s *Snapshot, c hostinfo.Core) bool {
	for _, t := range c.Threads {
		if threadUseCount(s, t) > 0 {
			return false
		}
	}
	return true
}

// threadUseCount is how many VMs (current + pending) claim thread t.
func threadUseCount(s *Snapshot, t int) int {
	u := s.Use[t]
	return len(u.VMs) + len(u.Pending)
}

// nodeThreadCount returns how many threads node has in total, or 0 if the
// node ID is not part of topo.
func nodeThreadCount(topo *hostinfo.Topology, node int) int {
	for _, n := range topo.Nodes {
		if n.ID == node {
			return len(n.Threads)
		}
	}
	return 0
}

// selectThreads assigns each of vcpus vcpus (0..vcpus-1) one thread on
// node. It fills fully-free cores first, in core order, both siblings
// before moving to the next core; any remainder is filled from the node's
// least-used remaining threads (ties broken by thread ID), which may reuse
// threads other VMs already claim. In that case it returns a warning
// naming the VMs whose threads got reused; otherwise "". allowed nil
// considers every thread on node, as before ProposeWithin existed; a
// non-nil slice restricts both passes to just those thread ids (the
// wizard form's "within: L3 #k" filter) -- callers are expected to have
// already sized their own capacity check off allowed, so this never
// needs to report "not enough", only which VMs got shared onto.
func selectThreads(s *Snapshot, node int, vcpus int, allowed []int) (map[int][]int, string) {
	var allowedSet map[int]bool
	if allowed != nil {
		allowedSet = make(map[int]bool, len(allowed))
		for _, t := range allowed {
			allowedSet[t] = true
		}
	}
	inAllowed := func(t int) bool { return allowedSet == nil || allowedSet[t] }

	var freeThreads []int
	for _, c := range s.Topo.Cores {
		if c.Node != node || !coreFullyFree(s, c) {
			continue
		}
		for _, t := range c.Threads {
			if inAllowed(t) {
				freeThreads = append(freeThreads, t)
			}
		}
	}

	pins := make(map[int][]int, vcpus)
	vcpu := 0
	for ; vcpu < vcpus && vcpu < len(freeThreads); vcpu++ {
		pins[vcpu] = []int{freeThreads[vcpu]}
	}
	if vcpu == vcpus {
		return pins, ""
	}

	assigned := make(map[int]bool, len(pins))
	for _, t := range pins {
		assigned[t[0]] = true
	}
	var pool []int
	for _, n := range s.Topo.Nodes {
		if n.ID != node {
			continue
		}
		for _, t := range n.Threads {
			if !assigned[t] && inAllowed(t) {
				pool = append(pool, t)
			}
		}
	}
	sort.Slice(pool, func(i, j int) bool {
		ui, uj := threadUseCount(s, pool[i]), threadUseCount(s, pool[j])
		if ui != uj {
			return ui < uj
		}
		return pool[i] < pool[j]
	})

	sharedNames := map[string]bool{}
	for i := 0; vcpu < vcpus; vcpu, i = vcpu+1, i+1 {
		t := pool[i]
		pins[vcpu] = []int{t}
		u := s.Use[t]
		for _, name := range u.VMs {
			sharedNames[name] = true
		}
		for _, name := range u.Pending {
			sharedNames[name] = true
		}
	}

	if len(sharedNames) == 0 {
		return pins, ""
	}
	names := make([]string, 0, len(sharedNames))
	for name := range sharedNames {
		names = append(names, name)
	}
	sort.Strings(names)
	warning := "Not enough free cores on this node; this VM ends up sharing threads with: " + strings.Join(names, ", ")
	return pins, warning
}

// assignedThreads returns the threads pins assigns, sorted ascending.
func assignedThreads(pins map[int][]int) []int {
	ids := make([]int, 0, len(pins))
	for _, t := range pins {
		ids = append(ids, t[0])
	}
	sort.Ints(ids)
	return ids
}
