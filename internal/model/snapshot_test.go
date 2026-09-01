package model

import (
	"reflect"
	"testing"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
)

// testTopo returns the 2-node/8-thread topology from Task 2, built
// literally (no sysfs): node0 threads 0,1,4,5; node1 threads 2,3,6,7;
// cores {0,4},{1,5} on node0 socket0; {2,6},{3,7} on node1 socket1.
func testTopo() *hostinfo.Topology {
	threads := map[int]hostinfo.Thread{
		0: {ID: 0, Core: 0, Socket: 0, Node: 0, Sibling: 4},
		4: {ID: 4, Core: 0, Socket: 0, Node: 0, Sibling: 0},
		1: {ID: 1, Core: 1, Socket: 0, Node: 0, Sibling: 5},
		5: {ID: 5, Core: 1, Socket: 0, Node: 0, Sibling: 1},
		2: {ID: 2, Core: 0, Socket: 1, Node: 1, Sibling: 6},
		6: {ID: 6, Core: 0, Socket: 1, Node: 1, Sibling: 2},
		3: {ID: 3, Core: 1, Socket: 1, Node: 1, Sibling: 7},
		7: {ID: 7, Core: 1, Socket: 1, Node: 1, Sibling: 3},
	}
	cores := []hostinfo.Core{
		{Socket: 0, ID: 0, Node: 0, Threads: []int{0, 4}},
		{Socket: 0, ID: 1, Node: 0, Threads: []int{1, 5}},
		{Socket: 1, ID: 0, Node: 1, Threads: []int{2, 6}},
		{Socket: 1, ID: 1, Node: 1, Threads: []int{3, 7}},
	}
	nodes := []hostinfo.Node{
		{ID: 0, Threads: []int{0, 1, 4, 5}, MemTotalKiB: 1000},
		{ID: 1, Threads: []int{2, 3, 6, 7}, MemTotalKiB: 1000},
	}
	return &hostinfo.Topology{Nodes: nodes, Cores: cores, Threads: threads}
}

const plainXML = `<domain type='kvm'>
  <name>plain-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b01</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

const pinnedNoNumaXML = `<domain type='kvm'>
  <name>pinned-no-numa</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b02</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
    <vcpupin vcpu='1' cpuset='1'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

const gpuOnNode1PinnedNode0XML = `<domain type='kvm'>
  <name>gpu-mismatch</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b03</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>1</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='1'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <hostdev mode='subsystem' type='pci' managed='yes'>
      <source>
        <address domain='0x0000' bus='0x81' slot='0x00' function='0x0'/>
      </source>
    </hostdev>
  </devices>
</domain>`

const pinnedAcrossNodesXML = `<domain type='kvm'>
  <name>cross-node</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b04</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
    <vcpupin vcpu='1' cpuset='2'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

const gpuXML = `<domain type='kvm'>
  <name>gpu-unknown</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b05</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <hostdev mode='subsystem' type='pci' managed='yes'>
      <source>
        <address domain='0x0000' bus='0x81' slot='0x00' function='0x0'/>
      </source>
    </hostdev>
  </devices>
</domain>`

const partialPinXML = `<domain type='kvm'>
  <name>partial-pin</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b06</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='0'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

const brokenXML = `<domain type='kvm'>
  <vcpu>not-a-number</vcpu>
</domain>`

func TestFlags(t *testing.T) {
	cases := []struct {
		name string
		xml  string
		pci  map[string]int
		want []FlagKind
	}{
		{"unpinned", plainXML, nil, []FlagKind{FlagUnpinned}},
		{"pinned no membind", pinnedNoNumaXML, nil, []FlagKind{FlagNoMemBind}},
		{"gpu mismatch", gpuOnNode1PinnedNode0XML, map[string]int{"0000:81:00.0": 1}, []FlagKind{FlagNodeMismatch}},
		{"cross node spill", pinnedAcrossNodesXML, nil, []FlagKind{FlagCrossNode, FlagNoMemBind}},
		{"gpu unknown", gpuXML, map[string]int{"0000:81:00.0": -1}, []FlagKind{FlagGPUUnknownNode, FlagUnpinned}},
		{"partial pin", partialPinXML, nil, []FlagKind{FlagPartialPin}},
		{"unsupported", brokenXML, nil, []FlagKind{FlagUnsupported}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := libvirtio.ParseDomainXML(tc.xml)
			var dom libvirtio.Domain
			if err != nil {
				dom = libvirtio.Domain{
					Config:   &libvirtio.DomainConfig{Name: "broken", Raw: tc.xml},
					ParseErr: err,
				}
			} else {
				dom = libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning}
			}

			pciNode := func(addr string) int {
				if tc.pci == nil {
					return -1
				}
				if n, ok := tc.pci[addr]; ok {
					return n
				}
				return -1
			}

			snap := Build(testTopo(), []libvirtio.Domain{dom}, pciNode)
			if len(snap.VMs) != 1 {
				t.Fatalf("VMs = %d, want 1", len(snap.VMs))
			}
			vm := snap.VMs[0]

			gotKinds := make(map[FlagKind]bool, len(vm.Flags))
			for _, f := range vm.Flags {
				gotKinds[f.Kind] = true
				if f.Cause == "" || f.Consequence == "" {
					t.Errorf("flag %v has empty Cause/Consequence: %+v", f.Kind, f)
				}
			}
			wantKinds := make(map[FlagKind]bool, len(tc.want))
			for _, k := range tc.want {
				wantKinds[k] = true
			}
			if !reflect.DeepEqual(gotKinds, wantKinds) {
				t.Errorf("flag kinds = %v, want %v", gotKinds, wantKinds)
			}
		})
	}
}

const memPressureXMLA = `<domain type='kvm'>
  <name>vm-a</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b07</uuid>
  <memory unit='KiB'>800</memory>
  <vcpu>1</vcpu>
  <numatune>
    <memory mode='strict' nodeset='0'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

const memPressureXMLB = `<domain type='kvm'>
  <name>vm-b</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b08</uuid>
  <memory unit='KiB'>800</memory>
  <vcpu>1</vcpu>
  <numatune>
    <memory mode='strict' nodeset='0'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

func TestMemPressure(t *testing.T) {
	cfgA, err := libvirtio.ParseDomainXML(memPressureXMLA)
	if err != nil {
		t.Fatalf("ParseDomainXML A: %v", err)
	}
	cfgB, err := libvirtio.ParseDomainXML(memPressureXMLB)
	if err != nil {
		t.Fatalf("ParseDomainXML B: %v", err)
	}

	doms := []libvirtio.Domain{
		{Config: cfgA, State: libvirtio.StateRunning},
		{Config: cfgB, State: libvirtio.StateRunning},
	}

	snap := Build(testTopo(), doms, func(string) int { return -1 })

	if snap.BoundMemKiB[0] != 1600 {
		t.Errorf("BoundMemKiB[0] = %d, want 1600", snap.BoundMemKiB[0])
	}

	for _, name := range []string{"vm-a", "vm-b"} {
		vm := snap.VM(name)
		if vm == nil {
			t.Fatalf("VM(%q) = nil", name)
		}
		if !vm.HasFlag(FlagMemPressure) {
			t.Errorf("VM(%q) missing FlagMemPressure, flags = %+v", name, vm.Flags)
		}
	}
}

const threadUseXMLAlpha = `<domain type='kvm'>
  <name>alpha</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b09</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>1</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

const threadUseXMLZeta = `<domain type='kvm'>
  <name>zeta</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b10</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>1</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

func TestThreadUse(t *testing.T) {
	cfgAlpha, err := libvirtio.ParseDomainXML(threadUseXMLAlpha)
	if err != nil {
		t.Fatalf("ParseDomainXML alpha: %v", err)
	}
	cfgZeta, err := libvirtio.ParseDomainXML(threadUseXMLZeta)
	if err != nil {
		t.Fatalf("ParseDomainXML zeta: %v", err)
	}

	doms := []libvirtio.Domain{
		{Config: cfgZeta, State: libvirtio.StateRunning},
		{Config: cfgAlpha, State: libvirtio.StateRunning},
	}

	snap := Build(testTopo(), doms, func(string) int { return -1 })

	want := []string{"alpha", "zeta"}
	if !reflect.DeepEqual(snap.Use[4].VMs, want) {
		t.Errorf("Use[4].VMs = %v, want %v", snap.Use[4].VMs, want)
	}

	if !reflect.DeepEqual(snap.VMs[0], *snap.VM("alpha")) {
		t.Errorf("VMs not sorted by name: %v", snap.VMs)
	}
}

func TestUnsupportedNoParseErr(t *testing.T) {
	// Sanity: a domain with a nil ParseErr never gets FlagUnsupported, and
	// Unsupported detection never depends on Config == nil (Config is
	// always non-nil per the libvirtio package invariant).
	cfg, err := libvirtio.ParseDomainXML(plainXML)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	dom := libvirtio.Domain{Config: cfg, State: libvirtio.StateRunning, ParseErr: nil}
	snap := Build(testTopo(), []libvirtio.Domain{dom}, func(string) int { return -1 })
	if snap.VMs[0].Unsupported {
		t.Errorf("Unsupported = true, want false")
	}
	if snap.VMs[0].HasFlag(FlagUnsupported) {
		t.Errorf("HasFlag(FlagUnsupported) = true, want false")
	}
}

func TestGPUNode(t *testing.T) {
	v := VM{Devices: []Device{{Addr: "a", Node: -1}, {Addr: "b", Node: 1}}}
	if got := v.GPUNode(); got != 1 {
		t.Errorf("GPUNode() = %d, want 1", got)
	}

	v2 := VM{Devices: []Device{{Addr: "a", Node: -1}}}
	if got := v2.GPUNode(); got != -1 {
		t.Errorf("GPUNode() = %d, want -1", got)
	}

	v3 := VM{}
	if got := v3.GPUNode(); got != -1 {
		t.Errorf("GPUNode() = %d, want -1", got)
	}
}

func TestSnapshotVMAbsent(t *testing.T) {
	snap := Build(testTopo(), nil, func(string) int { return -1 })
	if got := snap.VM("nope"); got != nil {
		t.Errorf("VM(\"nope\") = %+v, want nil", got)
	}
}
