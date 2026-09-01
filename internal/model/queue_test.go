package model

import (
	"reflect"
	"testing"

	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
)

func TestAddReplacesSameVM(t *testing.T) {
	var q Queue
	q.Add(PendingOp{VM: "vm-a", Summary: "first"})
	q.Add(PendingOp{VM: "vm-a", Summary: "second"})

	if q.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", q.Len())
	}
	if q.Ops[0].Summary != "second" {
		t.Errorf("Ops[0].Summary = %q, want %q", q.Ops[0].Summary, "second")
	}
}

func TestAddKeepsPosition(t *testing.T) {
	var q Queue
	q.Add(PendingOp{VM: "vm-a"})
	q.Add(PendingOp{VM: "vm-b"})
	q.Add(PendingOp{VM: "vm-a", Summary: "updated"})

	if q.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", q.Len())
	}
	if q.Ops[0].VM != "vm-a" || q.Ops[0].Summary != "updated" {
		t.Errorf("Ops[0] = %+v, want vm-a updated in place", q.Ops[0])
	}
	if q.Ops[1].VM != "vm-b" {
		t.Errorf("Ops[1].VM = %q, want vm-b", q.Ops[1].VM)
	}
}

func TestRemove(t *testing.T) {
	var q Queue
	q.Add(PendingOp{VM: "vm-a"})
	q.Add(PendingOp{VM: "vm-b"})

	q.Remove(-1) // out of range: no-op
	q.Remove(2)  // out of range: no-op
	if q.Len() != 2 {
		t.Fatalf("Len() after out-of-range Remove = %d, want 2", q.Len())
	}

	q.Remove(0)
	if q.Len() != 1 || q.Ops[0].VM != "vm-b" {
		t.Errorf("after Remove(0), Ops = %+v, want just vm-b", q.Ops)
	}
}

func buildPlainSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	cfg, err := libvirtio.ParseDomainXML(plainXML)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	dom := libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning}
	return Build(testTopo(), []libvirtio.Domain{dom}, func(string) int { return -1 })
}

func TestProjectPin(t *testing.T) {
	snap := buildPlainSnapshot(t)

	// Snapshot the original state to compare after Project.
	origPins := snap.VM("plain-vm").Pins
	if len(origPins) != 0 {
		t.Fatalf("precondition: plain-vm.Pins = %v, want empty", origPins)
	}
	origUse2, hadUse2 := snap.Use[2]

	ops := []PendingOp{
		{Kind: OpPin, VM: "plain-vm", Pins: map[int][]int{0: {2}}, MemNode: 1},
	}
	projected := Project(snap, nil, ops)

	pvm := projected.VM("plain-vm")
	if pvm == nil {
		t.Fatalf("projected VM(plain-vm) = nil")
	}
	if want := (map[int][]int{0: {2}}); !reflect.DeepEqual(pvm.Pins, want) {
		t.Errorf("projected Pins = %v, want %v", pvm.Pins, want)
	}
	if want := []int{1}; !reflect.DeepEqual(pvm.MemNodes, want) {
		t.Errorf("projected MemNodes = %v, want %v", pvm.MemNodes, want)
	}
	if pvm.HasFlag(FlagUnpinned) {
		t.Errorf("projected VM still has FlagUnpinned")
	}
	if want := []string{"plain-vm"}; !reflect.DeepEqual(projected.Use[2].Pending, want) {
		t.Errorf("projected Use[2].Pending = %v, want %v", projected.Use[2].Pending, want)
	}
	if len(projected.Use[2].VMs) != 0 {
		t.Errorf("projected Use[2].VMs = %v, want empty (claim is pending only)", projected.Use[2].VMs)
	}

	// Original snapshot must be untouched.
	if len(snap.VM("plain-vm").Pins) != 0 {
		t.Errorf("original snapshot Pins mutated: %v", snap.VM("plain-vm").Pins)
	}
	gotUse2, stillHas := snap.Use[2]
	if stillHas != hadUse2 || !reflect.DeepEqual(gotUse2, origUse2) {
		t.Errorf("original snapshot Use[2] mutated: got %+v, want %+v (present=%v)", gotUse2, origUse2, hadUse2)
	}
}

func TestProjectDoesNotAliasOriginal(t *testing.T) {
	cfg, err := libvirtio.ParseDomainXML(pinnedNoNumaXML)
	if err != nil {
		t.Fatal(err)
	}
	dom := libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning}
	snap := Build(testTopo(), []libvirtio.Domain{dom}, func(string) int { return -1 })
	projected := Project(snap, map[string]*libvirtio.DomainConfig{"pinned-no-numa": cfg}, nil)
	projected.VM("pinned-no-numa").Pins[0][0] = 99
	if got := snap.VM("pinned-no-numa").Pins[0][0]; got == 99 {
		t.Errorf("original snapshot aliases projection: Pins[0][0] = %d", got)
	}
	if got := cfg.VCPUPins[0][0]; got == 99 {
		t.Errorf("doms config aliases projection: VCPUPins[0][0] = %d", got)
	}
}

func TestProjectPinMemNodeUnchanged(t *testing.T) {
	cfg, err := libvirtio.ParseDomainXML(gpuOnNode1PinnedNode0XML)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	dom := libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning}
	snap := Build(testTopo(), []libvirtio.Domain{dom}, func(string) int { return -1 })

	origMemNodes := snap.VM("gpu-mismatch").MemNodes
	if want := []int{1}; !reflect.DeepEqual(origMemNodes, want) {
		t.Fatalf("precondition: gpu-mismatch.MemNodes = %v, want %v", origMemNodes, want)
	}

	ops := []PendingOp{
		{Kind: OpPin, VM: "gpu-mismatch", Pins: map[int][]int{0: {6}}, MemNode: -1},
	}
	projected := Project(snap, nil, ops)

	pvm := projected.VM("gpu-mismatch")
	if want := (map[int][]int{0: {6}}); !reflect.DeepEqual(pvm.Pins, want) {
		t.Errorf("projected Pins = %v, want %v", pvm.Pins, want)
	}
	if want := []int{1}; !reflect.DeepEqual(pvm.MemNodes, want) {
		t.Errorf("projected MemNodes = %v, want unchanged %v", pvm.MemNodes, want)
	}
}

func TestProjectStrip(t *testing.T) {
	cfg, err := libvirtio.ParseDomainXML(pinnedNoNumaXML)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	dom := libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning}
	snap := Build(testTopo(), []libvirtio.Domain{dom}, func(string) int { return -1 })

	if snap.VM("pinned-no-numa").HasFlag(FlagUnpinned) {
		t.Fatalf("precondition: pinned-no-numa already unpinned")
	}

	ops := []PendingOp{{Kind: OpStrip, VM: "pinned-no-numa"}}
	projected := Project(snap, nil, ops)

	pvm := projected.VM("pinned-no-numa")
	if pvm.Pins == nil || len(pvm.Pins) != 0 {
		t.Errorf("projected Pins = %v, want empty non-nil map", pvm.Pins)
	}
	if pvm.MemNodes != nil {
		t.Errorf("projected MemNodes = %v, want nil", pvm.MemNodes)
	}
	if !pvm.HasFlag(FlagUnpinned) {
		t.Errorf("projected VM missing FlagUnpinned, flags = %+v", pvm.Flags)
	}

	// Original untouched.
	orig := snap.VM("pinned-no-numa")
	if len(orig.Pins) != 2 {
		t.Errorf("original snapshot Pins mutated: %v", orig.Pins)
	}
}

func TestProjectRestore(t *testing.T) {
	cfg, err := libvirtio.ParseDomainXML(pinnedNoNumaXML)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	dom := libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning}
	snap := Build(testTopo(), []libvirtio.Domain{dom}, func(string) int { return -1 })

	ops := []PendingOp{{Kind: OpRestore, VM: "pinned-no-numa", BackupXML: plainXML}}
	projected := Project(snap, nil, ops)

	pvm := projected.VM("pinned-no-numa")
	if len(pvm.Pins) != 0 {
		t.Errorf("restored Pins = %v, want empty (plainXML has no cputune)", pvm.Pins)
	}
	if pvm.MemNodes != nil {
		t.Errorf("restored MemNodes = %v, want nil", pvm.MemNodes)
	}
}

func TestProjectRestoreParseErrorLeavesVMAsIs(t *testing.T) {
	cfg, err := libvirtio.ParseDomainXML(pinnedNoNumaXML)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	dom := libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning}
	snap := Build(testTopo(), []libvirtio.Domain{dom}, func(string) int { return -1 })

	ops := []PendingOp{{Kind: OpRestore, VM: "pinned-no-numa", BackupXML: brokenXML}}
	projected := Project(snap, nil, ops)

	pvm := projected.VM("pinned-no-numa")
	if want := (map[int][]int{0: {0}, 1: {1}}); !reflect.DeepEqual(pvm.Pins, want) {
		t.Errorf("Pins after failed restore = %v, want unchanged %v", pvm.Pins, want)
	}
	// Still counted as touched: its pinned threads move to Pending.
	if want := []string{"pinned-no-numa"}; !reflect.DeepEqual(projected.Use[0].Pending, want) {
		t.Errorf("Use[0].Pending = %v, want %v", projected.Use[0].Pending, want)
	}
}

func TestProjectUnknownVMIgnored(t *testing.T) {
	snap := buildPlainSnapshot(t)
	ops := []PendingOp{{Kind: OpStrip, VM: "no-such-vm"}}

	projected := Project(snap, nil, ops)
	if len(projected.VMs) != len(snap.VMs) {
		t.Errorf("VMs = %v, want unchanged count", projected.VMs)
	}
}

func TestHashXMLStable(t *testing.T) {
	h1 := HashXML(plainXML)
	h2 := HashXML(plainXML)
	if h1 != h2 {
		t.Errorf("HashXML not stable: %q != %q", h1, h2)
	}
	if h3 := HashXML(pinnedNoNumaXML); h3 == h1 {
		t.Errorf("HashXML(%q) == HashXML(%q), want different hashes", "pinnedNoNumaXML", "plainXML")
	}
}
