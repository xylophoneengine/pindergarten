package hostinfo

import (
	"fmt"
	"os"
	"reflect"
	"testing"
)

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

	wantThread4 := Thread{ID: 4, Core: 0, Socket: 0, Node: 0, Sibling: 0}
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
}

func TestPCINumaNode(t *testing.T) {
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
