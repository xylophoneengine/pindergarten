package model

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
)

// OpKind identifies the kind of change a PendingOp stages.
type OpKind int

const (
	OpPin     OpKind = iota // set Pins + MemNode
	OpStrip                 // clear Pins and MemNodes
	OpRestore               // define BackupXML verbatim
)

// PendingOp is one staged, not-yet-applied change to a VM's pinning or
// membind state.
type PendingOp struct {
	Kind       OpKind
	VM         string
	Pins       map[int][]int
	MemNode    int    // -1 = leave numatune untouched (OpPin only)
	BackupXML  string // OpRestore only
	StagedHash string // sha256 hex of domain config XML at staging time
	StagedXML  string // domain config XML at staging time (same XML StagedHash hashes), for the drift screen's diff
	Summary    string // human line, e.g. "vm-x: pin 4 vcpus -> node 1 threads 2,3,6,7; memory -> node 1"
}

// HashXML returns the sha256 hex digest of xml, used to detect drift
// between a staged op and the domain's live XML.
func HashXML(xml string) string {
	sum := sha256.Sum256([]byte(xml))
	return hex.EncodeToString(sum[:])
}

// Queue holds the operator's staged, not-yet-applied ops.
type Queue struct{ Ops []PendingOp }

// Add stages op, replacing (in place) any existing op for the same VM.
func (q *Queue) Add(op PendingOp) {
	for i := range q.Ops {
		if q.Ops[i].VM == op.VM {
			q.Ops[i] = op
			return
		}
	}
	q.Ops = append(q.Ops, op)
}

// Remove drops the op at index i. Out-of-range i is a no-op.
func (q *Queue) Remove(i int) {
	if i < 0 || i >= len(q.Ops) {
		return
	}
	q.Ops = append(q.Ops[:i], q.Ops[i+1:]...)
}

// Clear empties the queue.
func (q *Queue) Clear() {
	q.Ops = nil
}

// Len returns the number of staged ops.
func (q *Queue) Len() int {
	return len(q.Ops)
}

// Project returns a copy of s with ops applied to VM pin/membind state,
// Use recomputed (claims made only via ops land in ThreadUse.Pending, not
// ThreadUse.VMs), and Flags recomputed. s and doms are left untouched. An
// op naming a VM absent from s is ignored (no panic).
func Project(s *Snapshot, doms map[string]*libvirtio.DomainConfig, ops []PendingOp) *Snapshot {
	vms := make([]VM, len(s.VMs))
	for i, v := range s.VMs {
		vms[i] = deepCopyVM(v)
	}

	touched := make(map[string]bool, len(ops))
	for _, op := range ops {
		idx := -1
		for i := range vms {
			if vms[i].Name == op.VM {
				idx = i
				break
			}
		}
		if idx == -1 {
			continue // unknown VM name: ignore the op
		}
		touched[op.VM] = true
		applyOp(&vms[idx], op)
	}

	use, bound := computeFlags(s.Topo, vms)
	for t, tu := range use {
		var kept, pending []string
		for _, name := range tu.VMs {
			if touched[name] {
				pending = append(pending, name)
			} else {
				kept = append(kept, name)
			}
		}
		use[t] = ThreadUse{VMs: kept, Pending: pending}
	}

	return &Snapshot{Topo: s.Topo, VMs: vms, Use: use, BoundMemKiB: bound}
}

// applyOp mutates v (a VM already private to the projection) per op's
// semantics.
func applyOp(v *VM, op PendingOp) {
	switch op.Kind {
	case OpPin:
		v.Pins = copyPins(op.Pins)
		if op.MemNode != -1 {
			v.MemNodes = []int{op.MemNode}
		}
	case OpStrip:
		v.Pins = map[int][]int{}
		v.MemNodes = nil
	case OpRestore:
		cfg, err := libvirtio.ParseDomainXML(op.BackupXML)
		if err != nil {
			return // leave VM as-is; it is still marked touched by the caller
		}
		v.Pins = cfg.VCPUPins
		v.MemNodes = cfg.MemNodes
	}
}

// deepCopyVM copies v and everything it points to, so mutating the result
// never aliases the original VM (which may itself alias a parsed
// libvirtio.DomainConfig).
func deepCopyVM(v VM) VM {
	nv := v
	nv.Pins = copyPins(v.Pins)
	if v.MemNodes != nil {
		nv.MemNodes = append([]int(nil), v.MemNodes...)
	}
	if v.Devices != nil {
		nv.Devices = append([]Device(nil), v.Devices...)
	}
	if v.Flags != nil {
		nv.Flags = append([]Flag(nil), v.Flags...)
	}
	return nv
}

// copyPins returns a deep copy of pins, or nil if pins is nil.
func copyPins(pins map[int][]int) map[int][]int {
	if pins == nil {
		return nil
	}
	cp := make(map[int][]int, len(pins))
	for k, threads := range pins {
		cp[k] = append([]int(nil), threads...)
	}
	return cp
}
