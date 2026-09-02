package hostinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// isolateHostFiles points cpuinfoPath and pciIDsPaths at nonexistent files
// for the duration of the test, so Read never picks up the real host's
// /proc/cpuinfo or pci.ids. Tests that care about socket models or PCI
// names override the vars themselves after calling this.
func isolateHostFiles(t *testing.T) {
	t.Helper()
	origCPUInfo, origPCIIDs := cpuinfoPath, pciIDsPaths
	cpuinfoPath = filepath.Join(t.TempDir(), "no-cpuinfo")
	pciIDsPaths = []string{filepath.Join(t.TempDir(), "no-pci.ids")}
	t.Cleanup(func() {
		cpuinfoPath = origCPUInfo
		pciIDsPaths = origPCIIDs
	})
}

// writeCache creates /sys/devices/system/cpu/cpuN/cache/indexI/{level,type,
// shared_cpu_list} for the given cpu.
func writeCache(t *testing.T, root string, cpu, index int, level, typ, sharedList string) {
	t.Helper()
	d := fmt.Sprintf("%s/devices/system/cpu/cpu%d/cache/index%d", root, cpu, index)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	writes := map[string]string{
		"level":           level,
		"type":            typ,
		"shared_cpu_list": sharedList,
	}
	for name, content := range writes {
		if err := os.WriteFile(d+"/"+name, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writePCIDevice creates /sys/bus/pci/devices/<addr>/{class,vendor,device,
// numa_node} plus a driver symlink if driver != "".
func writePCIDevice(t *testing.T, root, addr, class, vendor, device, numaNode, driver string) {
	t.Helper()
	d := root + "/bus/pci/devices/" + addr
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"class": class, "vendor": vendor, "device": device, "numa_node": numaNode}
	for name, content := range files {
		if err := os.WriteFile(d+"/"+name, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if driver != "" {
		if err := os.Symlink("../../../../bus/pci/drivers/"+driver, d+"/driver"); err != nil {
			t.Fatal(err)
		}
	}
}

func writeCPUInfo(t *testing.T, path string, blocks []map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var s string
	for _, b := range blocks {
		for k, v := range b {
			s += k + "\t: " + v + "\n"
		}
		s += "\n"
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePCIIDs(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSysfs(t *testing.T, root string, nodes map[int]string, mem map[int][2]uint64,
	cpus map[int][3]string, pci map[string]string) {
	t.Helper()
	mustWrite := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustMkdirAll := func(path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for n, cpulist := range nodes {
		d := fmt.Sprintf("%s/devices/system/node/node%d", root, n)
		mustMkdirAll(d)
		mustWrite(d+"/cpulist", []byte(cpulist+"\n"))
		m := mem[n]
		mi := fmt.Sprintf("Node %d MemTotal: %d kB\nNode %d MemFree: %d kB\n", n, m[0], n, m[1])
		mustWrite(d+"/meminfo", []byte(mi))
	}
	for c, v := range cpus { // v = [package, core_id, siblings_list]
		d := fmt.Sprintf("%s/devices/system/cpu/cpu%d/topology", root, c)
		mustMkdirAll(d)
		mustWrite(d+"/physical_package_id", []byte(v[0]+"\n"))
		mustWrite(d+"/core_id", []byte(v[1]+"\n"))
		mustWrite(d+"/thread_siblings_list", []byte(v[2]+"\n"))
	}
	for addr, node := range pci {
		d := root + "/bus/pci/devices/" + addr
		mustMkdirAll(d)
		mustWrite(d+"/numa_node", []byte(node+"\n"))
	}
}

func TestParseCPUList(t *testing.T) {
	cases := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{"0-3,8", []int{0, 1, 2, 3, 8}, false},
		{"", []int{}, false},
		{"5", []int{5}, false},
		{"1-2,2", []int{1, 2}, false},
		{"x", nil, true},
	}
	for _, c := range cases {
		got, err := ParseCPUList(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseCPUList(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCPUList(%q): unexpected error: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseCPUList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormatCPUList(t *testing.T) {
	got := FormatCPUList([]int{4, 68})
	want := "4,68"
	if got != want {
		t.Errorf("FormatCPUList = %q, want %q", got, want)
	}
}

func TestReadTwoNodeSMT(t *testing.T) {
	isolateHostFiles(t)
	root := t.TempDir()
	writeSysfs(t, root,
		map[int]string{0: "0-1,4-5", 1: "2-3,6-7"},
		map[int][2]uint64{0: {1000, 400}, 1: {1000, 900}},
		map[int][3]string{
			0: {"0", "0", "0,4"}, 4: {"0", "0", "0,4"},
			1: {"0", "1", "1,5"}, 5: {"0", "1", "1,5"},
			2: {"1", "0", "2,6"}, 6: {"1", "0", "2,6"},
			3: {"1", "1", "3,7"}, 7: {"1", "1", "3,7"},
		}, nil)
	topo, err := Read(root)
	if err != nil {
		t.Fatalf("Read: unexpected error: %v", err)
	}
	if len(topo.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(topo.Nodes))
	}
	if topo.Nodes[0].ID != 0 || topo.Nodes[0].MemTotalKiB != 1000 || topo.Nodes[0].MemFreeKiB != 400 {
		t.Errorf("node0 = %+v, want ID:0 MemTotalKiB:1000 MemFreeKiB:400", topo.Nodes[0])
	}
	if !reflect.DeepEqual(topo.Nodes[0].Threads, []int{0, 1, 4, 5}) {
		t.Errorf("node0.Threads = %v, want [0 1 4 5]", topo.Nodes[0].Threads)
	}

	wantThread4 := Thread{ID: 4, Core: 0, Socket: 0, Node: 0, Sibling: 0, L3: -1}
	if topo.Threads[4] != wantThread4 {
		t.Errorf("Threads[4] = %+v, want %+v", topo.Threads[4], wantThread4)
	}

	if len(topo.Cores) != 4 {
		t.Fatalf("expected 4 cores, got %d", len(topo.Cores))
	}
	var found bool
	for _, c := range topo.Cores {
		if c.Socket == 0 && c.ID == 0 {
			found = true
			if !reflect.DeepEqual(c.Threads, []int{0, 4}) {
				t.Errorf("core(Socket:0,ID:0).Threads = %v, want [0 4]", c.Threads)
			}
		}
	}
	if !found {
		t.Errorf("core with Socket:0, ID:0 not found")
	}

	// No cache/ dirs were written: L3 grouping must not error, and every
	// thread/core must report "no L3 domain known" rather than domain 0.
	if len(topo.L3Domains) != 0 {
		t.Errorf("L3Domains = %+v, want none (no cache dirs)", topo.L3Domains)
	}
	if topo.Threads[4].L3 != -1 {
		t.Errorf("Threads[4].L3 = %d, want -1", topo.Threads[4].L3)
	}
	for _, c := range topo.Cores {
		if c.L3 != -1 {
			t.Errorf("core %+v.L3 = %d, want -1", c, c.L3)
		}
	}

	wantSockets := []Socket{{ID: 0, Nodes: []int{0}}, {ID: 1, Nodes: []int{1}}}
	if !reflect.DeepEqual(topo.Sockets, wantSockets) {
		t.Errorf("Sockets = %+v, want %+v", topo.Sockets, wantSockets)
	}
}

func TestReadSkipsOfflineCPU(t *testing.T) {
	isolateHostFiles(t)
	root := t.TempDir()
	writeSysfs(t, root,
		map[int]string{0: "0-1,4-5", 1: "2-3,6-7"},
		map[int][2]uint64{0: {1000, 400}, 1: {1000, 900}},
		map[int][3]string{
			0: {"0", "0", "0,4"}, 4: {"0", "0", "0,4"},
			1: {"0", "1", "1,5"}, 5: {"0", "1", "1,5"},
			2: {"1", "0", "2,6"}, 6: {"1", "0", "2,6"},
			3: {"1", "1", "3,7"}, 7: {"1", "1", "3,7"},
		}, nil)
	// Offline CPU: cpuN directory exists but the kernel has removed its
	// topology/ subdirectory. cpu8 is intentionally absent from every
	// node's cpulist too, matching what an offlined CPU looks like.
	offlineDir := fmt.Sprintf("%s/devices/system/cpu/cpu8", root)
	if err := os.MkdirAll(offlineDir, 0o755); err != nil {
		t.Fatal(err)
	}

	topo, err := Read(root)
	if err != nil {
		t.Fatalf("Read: unexpected error with offline cpu present: %v", err)
	}
	if _, ok := topo.Threads[8]; ok {
		t.Errorf("Threads[8] present, want offline cpu absent")
	}
	for _, c := range topo.Cores {
		for _, tid := range c.Threads {
			if tid == 8 {
				t.Errorf("core %+v contains offline thread 8", c)
			}
		}
	}
	for _, n := range topo.Nodes {
		for _, tid := range n.Threads {
			if tid == 8 {
				t.Errorf("node %+v contains offline thread 8", n)
			}
		}
	}
}

func TestPCINumaNode(t *testing.T) {
	isolateHostFiles(t)
	root := t.TempDir()
	writeSysfs(t, root, map[int]string{0: "0"},
		map[int][2]uint64{0: {1, 1}},
		map[int][3]string{0: {"0", "0", "0"}},
		map[string]string{"0000:81:00.0": "1", "0000:01:00.0": "-1"})

	if got := PCINumaNode(root, "0000:81:00.0"); got != 1 {
		t.Errorf("PCINumaNode(81:00.0) = %d, want 1", got)
	}
	if got := PCINumaNode(root, "0000:01:00.0"); got != -1 {
		t.Errorf("PCINumaNode(01:00.0) = %d, want -1", got)
	}
	if got := PCINumaNode(root, "0000:ff:00.0"); got != -1 {
		t.Errorf("PCINumaNode(missing) = %d, want -1", got)
	}
}

// ryzenCPUs mirrors a Ryzen 9 5900X's cpu topology fixture input: 1 socket,
// 12 cores (0-11), SMT pairs (i, i+12), all on physical package 0.
func ryzenCPUs() map[int][3]string {
	cpus := make(map[int][3]string, 24)
	for c := 0; c < 12; c++ {
		siblings := fmt.Sprintf("%d,%d", c, c+12)
		cpus[c] = [3]string{"0", strconv.Itoa(c), siblings}
		cpus[c+12] = [3]string{"0", strconv.Itoa(c), siblings}
	}
	return cpus
}

// TestReadL3DedupesEquivalentSharedCPULists covers the fix for
// readL3Domains deduping by the raw shared_cpu_list string rather than
// the parsed, canonically-formatted set: two CPUs actually in the same
// L3 domain whose own shared_cpu_list files spell it differently (a
// compact range vs the fully expanded list -- the same two forms a real
// kernel could plausibly disagree on across CPUs) must still collapse to
// exactly one domain, not two.
func TestReadL3DedupesEquivalentSharedCPULists(t *testing.T) {
	isolateHostFiles(t)
	root := t.TempDir()
	writeSysfs(t, root, map[int]string{0: "0-1"},
		map[int][2]uint64{0: {1000, 500}},
		map[int][3]string{
			0: {"0", "0", "0"},
			1: {"0", "1", "1"},
		}, nil)
	writeCache(t, root, 0, 3, "3", "Unified", "0-1")
	writeCache(t, root, 1, 3, "3", "Unified", "0,1")

	topo, err := Read(root)
	if err != nil {
		t.Fatalf("Read: unexpected error: %v", err)
	}
	if len(topo.L3Domains) != 1 {
		t.Fatalf("L3Domains = %+v, want exactly 1 domain (both CPUs' shared_cpu_list names the same set, just spelled differently)", topo.L3Domains)
	}
}

// TestParsePCIIDsSkipsDeviceClassSection covers the curVendor-reset guard on
// a "C 03  Display controller" device-class section: without it, the
// section's own indented subentries would be misfiled as devices of
// whatever real vendor's block came last before the section started. Here
// vendor 1234's real device 0001 ("Real Device One") is deliberately given
// the same 4-hex-digit ID as a bogus subentry under the class section
// ("VGA compatible controller") that follows it -- without the guard, the
// class section's entry is parsed second and silently overwrites the real
// device's name in the map.
func TestParsePCIIDsSkipsDeviceClassSection(t *testing.T) {
	names := parsePCIIDs("1234  Test Vendor\n" +
		"\t0001  Real Device One\n" +
		"C 03  Display controller\n" +
		"\t0001  VGA compatible controller\n")
	if got := names.devices["12340001"]; got != "Real Device One" {
		t.Errorf(`devices["12340001"] = %q, want "Real Device One" (class section must not overwrite it)`, got)
	}
}

// TestReadRealHostMirror mirrors this dev box's real topology: an AMD Ryzen
// 9 5900X, 1 socket, 1 NUMA node (cpus 0-23), two L3 domains (0-5,12-17 and
// 6-11,18-23), and two display-class PCI devices with numa_node -1.
func TestReadRealHostMirror(t *testing.T) {
	isolateHostFiles(t)
	root := t.TempDir()
	writeSysfs(t, root,
		map[int]string{0: "0-23"},
		map[int][2]uint64{0: {32 * 1024 * 1024, 16 * 1024 * 1024}},
		ryzenCPUs(), nil)

	for _, cpu := range []int{0, 1, 2, 3, 4, 5, 12, 13, 14, 15, 16, 17} {
		writeCache(t, root, cpu, 3, "3", "Unified", "0-5,12-17")
	}
	for _, cpu := range []int{6, 7, 8, 9, 10, 11, 18, 19, 20, 21, 22, 23} {
		writeCache(t, root, cpu, 3, "3", "Unified", "6-11,18-23")
	}
	// Decoy: a level-2 unified cache must not be mistaken for L3.
	writeCache(t, root, 0, 2, "2", "Unified", "0,12")

	writePCIDevice(t, root, "0000:06:00.0", "0x030000", "0x1002", "0x743f", "-1", "amdgpu")
	writePCIDevice(t, root, "0000:09:00.0", "0x030000", "0x10de", "0x2216", "-1", "nvidia")
	// Decoy: non-display, no vfio-pci driver -> excluded.
	writePCIDevice(t, root, "0000:00:1f.3", "0x040300", "0x1022", "0x1487", "0", "")
	// Non-display device bound to vfio-pci -> included via driver match.
	writePCIDevice(t, root, "0000:0a:00.0", "0x020000", "0x8086", "0x1521", "-1", "vfio-pci")

	cpuinfoPath = filepath.Join(root, "proc-cpuinfo")
	writeCPUInfo(t, cpuinfoPath, []map[string]string{
		{"processor": "0", "physical id": "0", "model name": "AMD Ryzen 9 5900X 12-Core Processor"},
		{"processor": "23", "physical id": "0", "model name": "AMD Ryzen 9 5900X 12-Core Processor"},
	})
	pciIDsPath := filepath.Join(root, "pci.ids")
	writePCIIDs(t, pciIDsPath, "# comment line, and a blank line above/below it should be ignored\n\n"+
		"1002  Advanced Micro Devices, Inc. [AMD/ATI]\n"+
		"\t743f  Navi 10 [Radeon RX 5700 XT]\n"+
		"\t\t1002 0123  Some Board Vendor's Radeon variant\n"+ // subvendor line: ignored
		"10de  NVIDIA Corporation\n"+
		"\t2216  GA102 [GeForce RTX 3080 Ti]\n"+
		"C 03  Display controller\n"+ // device-class section: its own indented
		"\t00  VGA compatible controller\n") // lines must not be read as devices
	pciIDsPaths = []string{pciIDsPath}

	topo, err := Read(root)
	if err != nil {
		t.Fatalf("Read: unexpected error: %v", err)
	}

	wantSockets := []Socket{{ID: 0, Model: "AMD Ryzen 9 5900X 12-Core Processor", Nodes: []int{0}}}
	if !reflect.DeepEqual(topo.Sockets, wantSockets) {
		t.Errorf("Sockets = %+v, want %+v", topo.Sockets, wantSockets)
	}

	if len(topo.L3Domains) != 2 {
		t.Fatalf("expected 2 L3Domains, got %d: %+v", len(topo.L3Domains), topo.L3Domains)
	}
	wantD0 := []int{0, 1, 2, 3, 4, 5, 12, 13, 14, 15, 16, 17}
	wantD1 := []int{6, 7, 8, 9, 10, 11, 18, 19, 20, 21, 22, 23}
	if topo.L3Domains[0].ID != 0 || !reflect.DeepEqual(topo.L3Domains[0].Threads, wantD0) {
		t.Errorf("L3Domains[0] = %+v, want ID:0 Threads:%v", topo.L3Domains[0], wantD0)
	}
	if topo.L3Domains[0].Node != 0 || topo.L3Domains[0].Socket != 0 {
		t.Errorf("L3Domains[0] Node/Socket = %d/%d, want 0/0", topo.L3Domains[0].Node, topo.L3Domains[0].Socket)
	}
	if topo.L3Domains[1].ID != 1 || !reflect.DeepEqual(topo.L3Domains[1].Threads, wantD1) {
		t.Errorf("L3Domains[1] = %+v, want ID:1 Threads:%v", topo.L3Domains[1], wantD1)
	}
	if topo.Threads[0].L3 != 0 {
		t.Errorf("Threads[0].L3 = %d, want 0", topo.Threads[0].L3)
	}
	if topo.Threads[23].L3 != 1 {
		t.Errorf("Threads[23].L3 = %d, want 1", topo.Threads[23].L3)
	}
	for _, c := range topo.Cores {
		if c.ID == 0 && c.L3 != 0 {
			t.Errorf("core ID:0 L3 = %d, want 0", c.L3)
		}
		if c.ID == 6 && c.L3 != 1 {
			t.Errorf("core ID:6 L3 = %d, want 1", c.L3)
		}
	}

	if len(topo.PCIDevices) != 3 {
		t.Fatalf("expected 3 PCIDevices, got %d: %+v", len(topo.PCIDevices), topo.PCIDevices)
	}
	wantAMD := PCIDevice{
		Addr: "0000:06:00.0", Class: "030000",
		VendorID: "1002", DeviceID: "743f",
		VendorName: "Advanced Micro Devices, Inc. [AMD/ATI]", DeviceName: "Navi 10 [Radeon RX 5700 XT]",
		Driver: "amdgpu", Node: -1,
	}
	if topo.PCIDevices[0] != wantAMD {
		t.Errorf("PCIDevices[0] = %+v, want %+v", topo.PCIDevices[0], wantAMD)
	}
	wantNV := PCIDevice{
		Addr: "0000:09:00.0", Class: "030000",
		VendorID: "10de", DeviceID: "2216",
		VendorName: "NVIDIA Corporation", DeviceName: "GA102 [GeForce RTX 3080 Ti]",
		Driver: "nvidia", Node: -1,
	}
	if topo.PCIDevices[1] != wantNV {
		t.Errorf("PCIDevices[1] = %+v, want %+v", topo.PCIDevices[1], wantNV)
	}
	wantVFIO := PCIDevice{
		Addr: "0000:0a:00.0", Class: "020000",
		VendorID: "8086", DeviceID: "1521",
		Driver: "vfio-pci", Node: -1,
	}
	if topo.PCIDevices[2] != wantVFIO {
		t.Errorf("PCIDevices[2] = %+v, want %+v", topo.PCIDevices[2], wantVFIO)
	}

	// Single-node host: numa_node -1 resolves to the only node, node 0.
	if got := PCINumaNodeIn(topo, root, "0000:06:00.0"); got != 0 {
		t.Errorf("PCINumaNodeIn(single-node, -1) = %d, want 0", got)
	}
}

// TestPCINumaNodeInMultiNode confirms PCINumaNodeIn leaves -1 alone when the
// host has more than one NUMA node: an unknown device node must not be
// silently guessed.
func TestPCINumaNodeInMultiNode(t *testing.T) {
	isolateHostFiles(t)
	root := t.TempDir()
	writeSysfs(t, root,
		map[int]string{0: "0-1,4-5", 1: "2-3,6-7"},
		map[int][2]uint64{0: {1000, 400}, 1: {1000, 900}},
		map[int][3]string{
			0: {"0", "0", "0,4"}, 4: {"0", "0", "0,4"},
			1: {"0", "1", "1,5"}, 5: {"0", "1", "1,5"},
			2: {"1", "0", "2,6"}, 6: {"1", "0", "2,6"},
			3: {"1", "1", "3,7"}, 7: {"1", "1", "3,7"},
		},
		map[string]string{"0000:81:00.0": "-1"})

	topo, err := Read(root)
	if err != nil {
		t.Fatalf("Read: unexpected error: %v", err)
	}
	if got := PCINumaNodeIn(topo, root, "0000:81:00.0"); got != -1 {
		t.Errorf("PCINumaNodeIn(multi-node, -1) = %d, want -1", got)
	}
	if got := PCINumaNodeIn(nil, root, "0000:81:00.0"); got != -1 {
		t.Errorf("PCINumaNodeIn(nil topo, -1) = %d, want -1", got)
	}
}
