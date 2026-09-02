package apply

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// Fixtures copied from internal/libvirtio/xml_test.go (test fixtures do not
// cross packages).

const plainVMXML = `<domain type='kvm'>
  <name>plain-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b02</uuid>
  <memory unit='MiB'>4096</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

const gpuVMXML = `<domain type='kvm'>
  <name>gpu-vm-01</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b01</uuid>
  <memory unit='KiB'>16777216</memory>
  <vcpu placement='static'>4</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
    <vcpupin vcpu='1' cpuset='68'/>
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
    <disk type='file' device='disk'><target dev='vda'/></disk>
  </devices>
</domain>`

// gpuVMXMLNoPins is gpuVMXML with cputune/numatune removed: the unpinned
// backup an OpRestore would replay over the pinned domain above.
const gpuVMXMLNoPins = `<domain type='kvm'>
  <name>gpu-vm-01</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b01</uuid>
  <memory unit='KiB'>16777216</memory>
  <vcpu placement='static'>4</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <hostdev mode='subsystem' type='pci' managed='yes'>
      <source>
        <address domain='0x0000' bus='0x81' slot='0x00' function='0x0'/>
      </source>
    </hostdev>
    <disk type='file' device='disk'><target dev='vda'/></disk>
  </devices>
</domain>`

// gpuVMXMLWithEmulator is gpuVMXML with an emulatorpin added to its
// cputune, for TestRunRestoreVerifyEmulatorMismatch.
const gpuVMXMLWithEmulator = `<domain type='kvm'>
  <name>gpu-vm-01</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b01</uuid>
  <memory unit='KiB'>16777216</memory>
  <vcpu placement='static'>4</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
    <vcpupin vcpu='1' cpuset='68'/>
    <emulatorpin cpuset='4,68'/>
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
    <disk type='file' device='disk'><target dev='vda'/></disk>
  </devices>
</domain>`

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	return string(data)
}

func backupFileCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("reading dir %q: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".xml") {
			n++
		}
	}
	return n
}

func TestRunPinHappyPath(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{"plain-vm": plainVMXML}}
	dir := t.TempDir()

	op := model.PendingOp{
		Kind:        model.OpPin,
		VM:          "plain-vm",
		Pins:        map[int][]int{0: {2}, 1: {6}},
		MemNode:     1,
		EmulatorPin: []int{2, 6},
		StagedHash:  model.HashXML(plainVMXML),
		Summary:     "plain-vm: pin 2 vcpus",
	}

	results := Run(fake, dir, "test-version", []model.PendingOp{op})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("Err = %v, want nil", r.Err)
	}
	if r.Drifted {
		t.Error("Drifted = true, want false")
	}
	if !r.Applied {
		t.Error("Applied = false, want true")
	}

	if r.BackupPath == "" {
		t.Fatal("BackupPath is empty")
	}
	if got := readFile(t, r.BackupPath); got != plainVMXML {
		t.Errorf("backup file content = %q, want original plainVMXML", got)
	}

	if len(fake.Defined) != 1 {
		t.Fatalf("len(fake.Defined) = %d, want 1", len(fake.Defined))
	}

	cfg, err := libvirtio.ParseDomainXML(fake.XML["plain-vm"])
	if err != nil {
		t.Fatalf("ParseDomainXML(stored xml): %v", err)
	}
	wantPins := map[int][]int{0: {2}, 1: {6}}
	if !reflect.DeepEqual(cfg.VCPUPins, wantPins) {
		t.Errorf("stored VCPUPins = %v, want %v", cfg.VCPUPins, wantPins)
	}
	if !reflect.DeepEqual(cfg.MemNodes, []int{1}) {
		t.Errorf("stored MemNodes = %v, want [1]", cfg.MemNodes)
	}
	if !reflect.DeepEqual(cfg.EmulatorPin, []int{2, 6}) {
		t.Errorf("stored EmulatorPin = %v, want [2 6]", cfg.EmulatorPin)
	}
}

func TestRunDriftSkips(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{"plain-vm": plainVMXML}}
	dir := t.TempDir()

	op := model.PendingOp{
		Kind:       model.OpPin,
		VM:         "plain-vm",
		Pins:       map[int][]int{0: {2}},
		MemNode:    -1,
		StagedHash: "stale-hash",
		Summary:    "plain-vm: pin 1 vcpu",
	}

	results := Run(fake, dir, "test-version", []model.PendingOp{op})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if !r.Drifted {
		t.Error("Drifted = false, want true")
	}
	if r.Applied {
		t.Error("Applied = true, want false")
	}
	if r.Err != nil {
		t.Errorf("Err = %v, want nil", r.Err)
	}
	if r.BackupPath != "" {
		t.Errorf("BackupPath = %q, want empty", r.BackupPath)
	}
	if fake.XML["plain-vm"] != plainVMXML {
		t.Error("fake XML was mutated on drift, want unchanged")
	}
	if len(fake.Defined) != 0 {
		t.Errorf("len(fake.Defined) = %d, want 0", len(fake.Defined))
	}
	if n := backupFileCount(t, dir); n != 0 {
		t.Errorf("backup file count = %d, want 0", n)
	}
}

func TestRunStopsOnDefineError(t *testing.T) {
	fake := &libvirtio.Fake{
		XML: map[string]string{
			"plain-vm":  plainVMXML,
			"gpu-vm-01": gpuVMXML,
		},
	}
	dir := t.TempDir()

	ops := []model.PendingOp{
		{
			Kind:       model.OpPin,
			VM:         "plain-vm",
			Pins:       map[int][]int{0: {2}},
			MemNode:    -1,
			StagedHash: model.HashXML(plainVMXML),
			Summary:    "plain-vm: pin 1 vcpu",
		},
		{
			Kind:       model.OpStrip,
			VM:         "gpu-vm-01",
			StagedHash: model.HashXML(gpuVMXML),
			Summary:    "gpu-vm-01: strip pins",
		},
	}

	fake.DefineErr = errDefineBoom

	results := Run(fake, dir, "test-version", ops)
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}

	if results[0].Err == nil {
		t.Error("results[0].Err = nil, want error")
	}
	if results[0].Applied {
		t.Error("results[0].Applied = true, want false")
	}
	if results[0].BackupPath == "" {
		t.Fatal("results[0].BackupPath is empty, want backup saved before failed Define")
	}
	if got := readFile(t, results[0].BackupPath); got != plainVMXML {
		t.Errorf("results[0] backup content = %q, want plainVMXML", got)
	}

	if results[1].Applied {
		t.Error("results[1].Applied = true, want false")
	}
	if results[1].Err != nil {
		t.Errorf("results[1].Err = %v, want nil (not executed)", results[1].Err)
	}
	if results[1].Drifted {
		t.Error("results[1].Drifted = true, want false (not executed)")
	}
	if results[1].BackupPath != "" {
		t.Error("results[1].BackupPath set, want empty (not executed)")
	}

	if len(fake.Defined) != 1 {
		t.Errorf("len(fake.Defined) = %d, want 1", len(fake.Defined))
	}
}

// TestRunMemNodeOnlyPreservesPins covers the memory-node-only VMs-tab
// action end to end: Pins empty, MemNode set, against a VM that already
// has cputune pins. Both the live domain's existing pins and the new
// numatune binding must land.
func TestRunMemNodeOnlyPreservesPins(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{"gpu-vm-01": gpuVMXML}}
	dir := t.TempDir()

	op := model.PendingOp{
		Kind:       model.OpPin,
		VM:         "gpu-vm-01",
		Pins:       map[int][]int{},
		MemNode:    0,
		StagedHash: model.HashXML(gpuVMXML),
		Summary:    "gpu-vm-01: memory -> node 0 (strict); vcpu pinning unchanged",
	}

	results := Run(fake, dir, "test-version", []model.PendingOp{op})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("Err = %v, want nil", r.Err)
	}
	if !r.Applied {
		t.Fatal("Applied = false, want true")
	}

	cfg, err := libvirtio.ParseDomainXML(fake.XML["gpu-vm-01"])
	if err != nil {
		t.Fatalf("ParseDomainXML(stored xml): %v", err)
	}
	wantPins := map[int][]int{0: {4}, 1: {68}}
	if !reflect.DeepEqual(cfg.VCPUPins, wantPins) {
		t.Errorf("VCPUPins = %v, want unchanged %v", cfg.VCPUPins, wantPins)
	}
	if !reflect.DeepEqual(cfg.MemNodes, []int{0}) {
		t.Errorf("MemNodes = %v, want [0]", cfg.MemNodes)
	}
}

func TestRunRestore(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{"gpu-vm-01": gpuVMXML}}
	dir := t.TempDir()

	op := model.PendingOp{
		Kind:       model.OpRestore,
		VM:         "gpu-vm-01",
		BackupXML:  gpuVMXMLNoPins,
		StagedHash: model.HashXML(gpuVMXML),
		Summary:    "gpu-vm-01: restore backup",
	}

	results := Run(fake, dir, "test-version", []model.PendingOp{op})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("Err = %v, want nil", r.Err)
	}
	if !r.Applied {
		t.Error("Applied = false, want true")
	}

	if fake.XML["gpu-vm-01"] != gpuVMXMLNoPins {
		t.Errorf("fake XML after restore = %q, want gpuVMXMLNoPins verbatim", fake.XML["gpu-vm-01"])
	}
	cfg, err := libvirtio.ParseDomainXML(fake.XML["gpu-vm-01"])
	if err != nil {
		t.Fatalf("ParseDomainXML(stored xml): %v", err)
	}
	if len(cfg.VCPUPins) != 0 {
		t.Errorf("stored VCPUPins = %v, want empty", cfg.VCPUPins)
	}
	if cfg.MemNodes != nil {
		t.Errorf("stored MemNodes = %v, want nil", cfg.MemNodes)
	}
}

// TestRunRestoreRefusesNameMismatch covers a corrupted/mismatched backup:
// if BackupXML's own <name> does not match op.VM, Run must refuse before
// ever calling Define, rather than silently overwriting the wrong domain's
// live config with someone else's backup.
func TestRunRestoreRefusesNameMismatch(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{"gpu-vm-01": gpuVMXML}}
	dir := t.TempDir()

	op := model.PendingOp{
		Kind:       model.OpRestore,
		VM:         "gpu-vm-01",
		BackupXML:  plainVMXML, // <name>plain-vm</name>, not gpu-vm-01
		StagedHash: model.HashXML(gpuVMXML),
		Summary:    "gpu-vm-01: restore backup",
	}

	results := Run(fake, dir, "test-version", []model.PendingOp{op})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err == nil {
		t.Fatal("Err = nil, want a name-mismatch error")
	}
	if r.Applied {
		t.Error("Applied = true, want false")
	}
	if fake.XML["gpu-vm-01"] != gpuVMXML {
		t.Errorf("fake XML changed despite the mismatch refusal: %q", fake.XML["gpu-vm-01"])
	}
	if len(fake.Defined) != 0 {
		t.Errorf("Define called %d time(s), want 0 (refuse before writing)", len(fake.Defined))
	}
}

// verifyMismatchHV wraps a Fake but returns a fixed, wrongly-pinned XML on
// the second and later DomainXML calls (the post-Define verify fetch),
// regardless of what Define actually stored. This provokes Run's verify
// step to fail cheaply without needing a Fake variant that mis-stores data.
type verifyMismatchHV struct {
	*libvirtio.Fake
	calls int
}

func (v *verifyMismatchHV) DomainXML(name string) (string, error) {
	v.calls++
	if v.calls == 1 {
		return v.Fake.DomainXML(name)
	}
	return gpuVMXML, nil // still shows the old pins: verify must reject this
}

func TestRunVerifyMismatch(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{"gpu-vm-01": gpuVMXML}}
	hv := &verifyMismatchHV{Fake: fake}
	dir := t.TempDir()

	op := model.PendingOp{
		Kind:       model.OpStrip,
		VM:         "gpu-vm-01",
		StagedHash: model.HashXML(gpuVMXML),
		Summary:    "gpu-vm-01: strip pins",
	}

	results := Run(hv, dir, "test-version", []model.PendingOp{op})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err == nil {
		t.Fatal("Err = nil, want verify mismatch error")
	}
	if r.Applied {
		t.Error("Applied = true, want false on verify mismatch")
	}
}

// TestRunVerifyEmulatorMismatch mirrors TestRunVerifyMismatch for
// emulatorpin: verifyMismatchHV's fixed post-Define XML (gpuVMXML) has no
// emulatorpin, so an op that expects one must fail verify even though its
// vcpupin/numatune both still match.
func TestRunVerifyEmulatorMismatch(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{"gpu-vm-01": gpuVMXML}}
	hv := &verifyMismatchHV{Fake: fake}
	dir := t.TempDir()

	op := model.PendingOp{
		Kind:        model.OpPin,
		VM:          "gpu-vm-01",
		Pins:        map[int][]int{0: {4}, 1: {68}},
		MemNode:     -1,
		EmulatorPin: []int{4, 68},
		StagedHash:  model.HashXML(gpuVMXML),
		Summary:     "gpu-vm-01: pin emulator",
	}

	results := Run(hv, dir, "test-version", []model.PendingOp{op})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err == nil {
		t.Fatal("Err = nil, want emulatorpin mismatch error")
	}
	if !strings.Contains(r.Err.Error(), "emulatorpin") {
		t.Errorf("Err = %v, want mention of emulatorpin", r.Err)
	}
	if r.Applied {
		t.Error("Applied = true, want false on verify mismatch")
	}
}

// TestRunRestoreVerifyEmulatorMismatch covers verify's OpRestore case
// comparing EmulatorPin against the backup's own parsed EmulatorPin (the
// fix -- it used to check only VCPUPins and MemNodes): restoring
// gpuVMXMLWithEmulator must fail verify when the post-Define live XML
// (verifyMismatchHV's fixed gpuVMXML, no emulatorpin at all) doesn't match
// it, even though vcpupin/numatune both still agree.
func TestRunRestoreVerifyEmulatorMismatch(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{"gpu-vm-01": gpuVMXMLWithEmulator}}
	hv := &verifyMismatchHV{Fake: fake}
	dir := t.TempDir()

	op := model.PendingOp{
		Kind:       model.OpRestore,
		VM:         "gpu-vm-01",
		BackupXML:  gpuVMXMLWithEmulator,
		StagedHash: model.HashXML(gpuVMXMLWithEmulator),
		Summary:    "gpu-vm-01: restore backup",
	}

	results := Run(hv, dir, "test-version", []model.PendingOp{op})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if r.Err == nil {
		t.Fatal("Err = nil, want emulatorpin mismatch error")
	}
	if !strings.Contains(r.Err.Error(), "emulatorpin") {
		t.Errorf("Err = %v, want mention of emulatorpin", r.Err)
	}
	if r.Applied {
		t.Error("Applied = true, want false on verify mismatch")
	}
}

func TestCheckDriftDetectsChanged(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{
		"plain-vm":  plainVMXML,
		"gpu-vm-01": gpuVMXML,
	}}
	ops := []model.PendingOp{
		{VM: "plain-vm", StagedHash: model.HashXML(plainVMXML)},
		{VM: "gpu-vm-01", StagedHash: "stale"},
	}
	names, err := CheckDrift(fake, ops)
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"gpu-vm-01"}) {
		t.Errorf("names = %v, want [gpu-vm-01]", names)
	}
}

func TestCheckDriftDomainXMLError(t *testing.T) {
	fake := &libvirtio.Fake{XML: map[string]string{}}
	ops := []model.PendingOp{{VM: "missing-vm", StagedHash: "anything"}}
	_, err := CheckDrift(fake, ops)
	if err == nil {
		t.Fatal("CheckDrift err = nil, want error for missing domain")
	}
}

var errDefineBoom = errors.New("define: boom")
