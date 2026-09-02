package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/xylophoneengine/pindergarten/internal/backup"
	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// screenshotWidth/screenshotHeight are the fixed terminal size every README
// screenshot frame is rendered at.
const (
	screenshotWidth  = 140
	screenshotHeight = 42
)

// showcaseTopo builds the 2-node, 2x96-core SMT2 EPYC host (epycHostTopo)
// plus two socket entries and three PCI display devices: a GPU passed
// through to gpu-train-01 (node 0), a GPU passed through to win11-desktop
// (node 1), and a third GPU still bound to a host driver (not passed
// through to any VM) -- wide enough, and varied enough, to make every
// tab's screenshot look like a real host rather than the package's minimal
// test fixtures.
func showcaseTopo() *hostinfo.Topology {
	topo := epycHostTopo()
	topo.Sockets = []hostinfo.Socket{
		{ID: 0, Model: "AMD EPYC 9654 96-Core Processor", Nodes: []int{0}},
		{ID: 1, Model: "AMD EPYC 9654 96-Core Processor", Nodes: []int{1}},
	}
	topo.PCIDevices = []hostinfo.PCIDevice{
		{Addr: "0000:01:00.0", Class: "030000", VendorID: "10de", DeviceID: "2803", VendorName: "NVIDIA", DeviceName: "AD104 [GeForce RTX 4060]", Driver: "nvidia", Node: 0},
		{Addr: "0000:41:00.0", Class: "030000", VendorID: "10de", DeviceID: "2204", VendorName: "NVIDIA", DeviceName: "GA102 [GeForce RTX 3090]", Driver: "vfio-pci", Node: 0},
		{Addr: "0000:c1:00.0", Class: "030000", VendorID: "10de", DeviceID: "2684", VendorName: "NVIDIA", DeviceName: "AD102 [GeForce RTX 4090]", Driver: "vfio-pci", Node: 1},
	}
	return topo
}

// showcasePCINode resolves the showcase topology's three GPU addresses to
// their NUMA node; anything else is unknown, matching a real pciNode
// implementation backed by hostinfo.PCINumaNode.
func showcasePCINode(addr string) int {
	switch addr {
	case "0000:01:00.0", "0000:41:00.0":
		return 0
	case "0000:c1:00.0":
		return 1
	default:
		return -1
	}
}

// Showcase domain XML fixtures: six VMs covering the spread of states this
// project's views distinguish.
//
//   - gpu-train-01: fully pinned to node 0 (core 0, L3 #0), memory bound,
//     GPU passed through on node 0.
//   - ci-runner-a: fully pinned to node 0 (core 12, L3 #1, a different L3
//     domain than gpu-train-01), memory bound, no GPU.
//   - log-shipper: pinned to node 0, one vcpu sharing gpu-train-01's own
//     thread 0 (so the "shared" glyph shows on the CPU Map/Topology), the
//     other on its own core, memory bound.
//   - db-primary: fully pinned to node 1 (core 0, L3 #8), memory bound, no
//     GPU.
//   - win11-desktop: unpinned, GPU passed through on node 1 -- the wizard
//     demo target.
//   - batch-worker: pinned cross-node (one thread per node), no memory
//     binding -- flagged, and the mem-node-picker demo target.
const (
	showcaseGPUTrainXML = `<domain type='kvm'>
  <name>gpu-train-01</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f0001</uuid>
  <memory unit='KiB'>33554432</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='0'/>
    <vcpupin vcpu='1' cpuset='192'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='0'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <hostdev mode='subsystem' type='pci'>
      <source>
        <address domain='0x0000' bus='0x41' slot='0x00' function='0x0'/>
      </source>
    </hostdev>
  </devices>
</domain>`

	showcaseCIRunnerXML = `<domain type='kvm'>
  <name>ci-runner-a</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f0002</uuid>
  <memory unit='KiB'>8388608</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='12'/>
    <vcpupin vcpu='1' cpuset='204'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='0'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

	showcaseLogShipperXML = `<domain type='kvm'>
  <name>log-shipper</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f0003</uuid>
  <memory unit='KiB'>16777216</memory>
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

	showcaseDBPrimaryXML = `<domain type='kvm'>
  <name>db-primary</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f0004</uuid>
  <memory unit='KiB'>67108864</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='96'/>
    <vcpupin vcpu='1' cpuset='288'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='1'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

	showcaseWin11XML = `<domain type='kvm'>
  <name>win11-desktop</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f0005</uuid>
  <memory unit='KiB'>16777216</memory>
  <vcpu>4</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <hostdev mode='subsystem' type='pci'>
      <source>
        <address domain='0x0000' bus='0xc1' slot='0x00' function='0x0'/>
      </source>
    </hostdev>
  </devices>
</domain>`

	showcaseBatchWorkerXML = `<domain type='kvm'>
  <name>batch-worker</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f0006</uuid>
  <memory unit='KiB'>8388608</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='50'/>
    <vcpupin vcpu='1' cpuset='146'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`
)

// showcaseApp builds the App used for every screenshot frame: a Fake
// hypervisor seeded with the six VMs above against showcaseTopo, scanned
// once, sized screenshotWidth x screenshotHeight, and left in edit mode.
func showcaseApp(t *testing.T) *App {
	t.Helper()
	f := &libvirtio.Fake{
		ConnURI: "qemu+ssh://showcase-host/system",
		XML: map[string]string{
			"gpu-train-01":  showcaseGPUTrainXML,
			"ci-runner-a":   showcaseCIRunnerXML,
			"log-shipper":   showcaseLogShipperXML,
			"db-primary":    showcaseDBPrimaryXML,
			"win11-desktop": showcaseWin11XML,
			"batch-worker":  showcaseBatchWorkerXML,
		},
		States: map[string]libvirtio.DomState{
			"gpu-train-01":  libvirtio.StateRunning,
			"ci-runner-a":   libvirtio.StateRunning,
			"log-shipper":   libvirtio.StateRunning,
			"db-primary":    libvirtio.StateRunning,
			"win11-desktop": libvirtio.StateRunning,
			"batch-worker":  libvirtio.StateRunning,
		},
	}
	scan := func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error) {
		doms, err := f.ListDomains()
		if err != nil {
			return nil, nil, err
		}
		domsMap := make(map[string]*libvirtio.DomainConfig, len(doms))
		for _, d := range doms {
			domsMap[d.Config.Name] = d.Config
		}
		return model.Build(showcaseTopo(), doms, showcasePCINode), domsMap, nil
	}

	a := New(f, scan, t.TempDir(), "v0.1.1")
	runScan(t, a)
	a.Update(tea.WindowSizeMsg{Width: screenshotWidth, Height: screenshotHeight})
	enterEdit(a)
	return a
}

// writeFrame writes a.View() verbatim (raw ANSI, TrueColor already forced
// by the caller) to <dir>/<name>.ans, after asserting it is exactly
// screenshotHeight lines and no line wider than screenshotWidth cells --
// every render function is expected to fill/clip to that budget, so a
// frame that doesn't is a layout bug, not just a cosmetic surprise render.py
// would happily render anyway.
func writeFrame(t *testing.T, dir, name string, a *App) {
	t.Helper()
	view := a.View()
	if got := lineCount(view); got != screenshotHeight {
		t.Fatalf("frame %s: View() has %d lines, want exactly %d", name, got, screenshotHeight)
	}
	if got := maxLineWidth(view); got > screenshotWidth {
		t.Fatalf("frame %s: widest line is %d cells, want at most %d", name, got, screenshotWidth)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".ans"), []byte(view), 0o644); err != nil {
		t.Fatalf("write frame %s: %v", name, err)
	}
}

// fixBackupTimestamp overwrites a backup's JSON sidecar's Time field with a
// fixed value: backup.Save stamps time.Now(), which would otherwise make
// the Backups frame (and so its rendered PNG) different on every run.
func fixBackupTimestamp(t *testing.T, e backup.Entry, at time.Time) {
	t.Helper()
	jsonPath := e.XMLPath + ".json"
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read backup meta: %v", err)
	}
	var meta backup.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal backup meta: %v", err)
	}
	meta.Time = at
	out, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal backup meta: %v", err)
	}
	if err := os.WriteFile(jsonPath, out, 0o600); err != nil {
		t.Fatalf("write backup meta: %v", err)
	}
}

// TestWriteScreenshots renders a fixed sequence of frames from a showcase
// App -- a 2-node, 3-GPU EPYC host with 6 VMs in various pinning states --
// to raw .ans files (View()'s own output, ANSI escapes included) for the
// README screenshot renderer (tools/screenshots/render.py) to turn into
// PNGs. Skipped unless PINDERGARTEN_SCREENSHOT_DIR is set; see `make
// screenshots`.
func TestWriteScreenshots(t *testing.T) {
	dir := os.Getenv("PINDERGARTEN_SCREENSHOT_DIR")
	if dir == "" {
		t.Skip("PINDERGARTEN_SCREENSHOT_DIR not set; run via `make screenshots`")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	// Force color output: the test harness otherwise renders every style as
	// plain, uncolored text, but a README screenshot should look like a
	// real color terminal.
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	a := showcaseApp(t)

	// Open the pin wizard for win11-desktop (unpinned, GPU on node 1) and
	// capture its proposal form before accepting -- this is the "wizard"
	// frame.
	a.tab = tabVMs
	a.vmSel = vmIndex(t, a, "win11-desktop")
	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	writeFrame(t, dir, "wizard", a)

	// Accept the proposal: the first of the two staged pending ops the
	// rest of the frames (overview, cpumap, vms, pending) show.
	sendKey(a, 'A')
	if a.wizard != nil {
		t.Fatal("wizard still open after accepting the proposal")
	}

	// Stage a second, differently-shaped pending op: bind batch-worker's
	// memory to node 0 (one of its own cross-node pins), via the mem-node
	// picker rather than the wizard -- batch-worker has no GPU and no
	// single pin node, so this stages immediately with no confirm.
	a.vmSel = vmIndex(t, a, "batch-worker")
	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatalf("status = %q, mem-node picker did not open", a.status)
	}
	sendKey(a, '0')
	if a.memPicker != nil {
		t.Fatal("mem-node picker still open after picking a node")
	}
	if a.queue.Len() != 2 {
		t.Fatalf("queue.Len() = %d after staging both ops, want 2", a.queue.Len())
	}

	a.tab = tabOverview
	writeFrame(t, dir, "overview", a)
	a.tab = tabTopology
	writeFrame(t, dir, "topology", a)

	// Core 0 (node 0, threads 0/192) is gpu-train-01's own pin, so the
	// detail panel shows a VM rather than "free".
	a.tab = tabCPUMap
	a.cursor = 0
	writeFrame(t, dir, "cpumap", a)

	a.tab = tabVMs
	a.vmSel = vmIndex(t, a, "gpu-train-01")
	writeFrame(t, dir, "vms", a)

	a.tab = tabPending
	writeFrame(t, dir, "pending", a)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e1, err := backup.Save(a.backupDir, "gpu-train-01", "pin 2 vcpus -> node 0 threads 0,192; memory -> node 0 (strict)", a.version, showcaseGPUTrainXML)
	if err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	fixBackupTimestamp(t, e1, base)
	e2, err := backup.Save(a.backupDir, "db-primary", "memory -> node 1 (strict); vcpu pinning unchanged", a.version, showcaseDBPrimaryXML)
	if err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	fixBackupTimestamp(t, e2, base.Add(-time.Hour))
	a.tab = tabBackups
	writeFrame(t, dir, "backups", a)

	sendKey(a, '?')
	if !a.help {
		t.Fatal("'?' did not open the help overlay")
	}
	writeFrame(t, dir, "help", a)
	sendKeyType(a, tea.KeyEsc)
	if a.help {
		t.Fatal("help overlay still open after esc")
	}
}
