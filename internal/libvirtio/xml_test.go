package libvirtio

import (
	"reflect"
	"strings"
	"testing"
)

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

const plainVMXML = `<domain type='kvm'>
  <name>plain-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b02</uuid>
  <memory unit='MiB'>4096</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

func TestParseDomainXML_GPU(t *testing.T) {
	cfg, err := ParseDomainXML(gpuVMXML)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	if cfg.Name != "gpu-vm-01" {
		t.Errorf("Name = %q, want gpu-vm-01", cfg.Name)
	}
	if cfg.VCPUs != 4 {
		t.Errorf("VCPUs = %d, want 4", cfg.VCPUs)
	}
	if cfg.MemoryKiB != 16777216 {
		t.Errorf("MemoryKiB = %d, want 16777216", cfg.MemoryKiB)
	}
	wantPins := map[int][]int{0: {4}, 1: {68}}
	if !reflect.DeepEqual(cfg.VCPUPins, wantPins) {
		t.Errorf("VCPUPins = %v, want %v", cfg.VCPUPins, wantPins)
	}
	if !reflect.DeepEqual(cfg.MemNodes, []int{1}) {
		t.Errorf("MemNodes = %v, want [1]", cfg.MemNodes)
	}
	if cfg.MemMode != "strict" {
		t.Errorf("MemMode = %q, want strict", cfg.MemMode)
	}
	if !reflect.DeepEqual(cfg.Hostdevs, []string{"0000:81:00.0"}) {
		t.Errorf("Hostdevs = %v, want [0000:81:00.0]", cfg.Hostdevs)
	}
	if cfg.Raw != gpuVMXML {
		t.Errorf("Raw does not match input verbatim")
	}
	if cfg.EmulatorPin != nil {
		t.Errorf("EmulatorPin = %v, want nil (gpuVMXML has no emulatorpin)", cfg.EmulatorPin)
	}
}

func TestParseDomainXML_Plain(t *testing.T) {
	cfg, err := ParseDomainXML(plainVMXML)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	if cfg.MemoryKiB != 4194304 {
		t.Errorf("MemoryKiB = %d, want 4194304 (4096 MiB)", cfg.MemoryKiB)
	}
	if len(cfg.VCPUPins) != 0 {
		t.Errorf("VCPUPins = %v, want empty", cfg.VCPUPins)
	}
	if cfg.MemNodes != nil {
		t.Errorf("MemNodes = %v, want nil", cfg.MemNodes)
	}
	if len(cfg.Hostdevs) != 0 {
		t.Errorf("Hostdevs = %v, want empty", cfg.Hostdevs)
	}
}

func TestParseDomainXML_Malformed(t *testing.T) {
	_, err := ParseDomainXML("<not xml")
	if err == nil {
		t.Fatal("expected error for malformed XML, got nil")
	}
}

func TestParseDomainXML_CpusetRange(t *testing.T) {
	xml := `<domain type='kvm'>
  <name>range-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b03</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4-5'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`
	cfg, err := ParseDomainXML(xml)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	want := map[int][]int{0: {4, 5}}
	if !reflect.DeepEqual(cfg.VCPUPins, want) {
		t.Errorf("VCPUPins = %v, want %v", cfg.VCPUPins, want)
	}
}

func TestParseDomainXML_HostdevMissingAddress(t *testing.T) {
	xml := `<domain type='kvm'>
  <name>bad-hostdev-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b04</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <hostdev mode='subsystem' type='pci' managed='yes'>
      <source/>
    </hostdev>
  </devices>
</domain>`
	_, err := ParseDomainXML(xml)
	if err == nil {
		t.Fatal("expected error for hostdev missing source/address, got nil")
	}
}

func TestParseDomainXML_EmptyCpusetSkipped(t *testing.T) {
	xml := `<domain type='kvm'>
  <name>empty-cpuset-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b05</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset=''/>
    <vcpupin vcpu='1' cpuset='6'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset=''/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`
	cfg, err := ParseDomainXML(xml)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	want := map[int][]int{1: {6}}
	if !reflect.DeepEqual(cfg.VCPUPins, want) {
		t.Errorf("VCPUPins = %v, want %v", cfg.VCPUPins, want)
	}
	if cfg.MemNodes != nil {
		t.Errorf("MemNodes = %v, want nil", cfg.MemNodes)
	}
	if cfg.MemMode != "strict" {
		t.Errorf("MemMode = %q, want strict", cfg.MemMode)
	}
}

func TestSetPinningRoundTrip(t *testing.T) {
	out, err := SetPinning(plainVMXML, map[int][]int{0: {2}, 1: {6}}, 1, []int{2, 6})
	if err != nil {
		t.Fatalf("SetPinning: %v", err)
	}
	cfg, err := ParseDomainXML(out)
	if err != nil {
		t.Fatalf("ParseDomainXML(out): %v", err)
	}
	wantPins := map[int][]int{0: {2}, 1: {6}}
	if !reflect.DeepEqual(cfg.VCPUPins, wantPins) {
		t.Errorf("VCPUPins = %v, want %v", cfg.VCPUPins, wantPins)
	}
	if !reflect.DeepEqual(cfg.MemNodes, []int{1}) {
		t.Errorf("MemNodes = %v, want [1]", cfg.MemNodes)
	}
	if !reflect.DeepEqual(cfg.EmulatorPin, []int{2, 6}) {
		t.Errorf("EmulatorPin = %v, want [2 6]", cfg.EmulatorPin)
	}
	if cfg.MemMode != "strict" {
		t.Errorf("MemMode = %q, want strict", cfg.MemMode)
	}
	if !strings.Contains(out, "<os>") {
		t.Errorf("output missing <os>: %s", out)
	}
	if !strings.Contains(out, "<devices/>") {
		t.Errorf("output missing <devices/>: %s", out)
	}
	if cfg.Name != "plain-vm" {
		t.Errorf("Name = %q, want plain-vm", cfg.Name)
	}
	if cfg.UUID != "2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b02" {
		t.Errorf("UUID = %q, want unchanged", cfg.UUID)
	}
	if cfg.MemoryKiB != 4194304 {
		t.Errorf("MemoryKiB = %d, want 4194304", cfg.MemoryKiB)
	}
}

func TestSetPinningReplacesExisting(t *testing.T) {
	out, err := SetPinning(gpuVMXML, map[int][]int{0: {10}}, -1, nil)
	if err != nil {
		t.Fatalf("SetPinning: %v", err)
	}
	cfg, err := ParseDomainXML(out)
	if err != nil {
		t.Fatalf("ParseDomainXML(out): %v", err)
	}
	wantPins := map[int][]int{0: {10}}
	if !reflect.DeepEqual(cfg.VCPUPins, wantPins) {
		t.Errorf("VCPUPins = %v, want %v", cfg.VCPUPins, wantPins)
	}
	if !reflect.DeepEqual(cfg.MemNodes, []int{1}) {
		t.Errorf("MemNodes = %v, want [1] (memNode -1 leaves numatune untouched)", cfg.MemNodes)
	}
	if !strings.Contains(out, "<hostdev") {
		t.Errorf("output missing hostdev: %s", out)
	}
}

// TestSetPinningEmulator covers the nil/empty/non-empty emulatorpin rule
// directly: nil leaves an existing emulatorpin untouched, a non-nil empty
// slice clears it, and a non-empty slice writes/replaces it.
func TestSetPinningEmulator(t *testing.T) {
	const withEmulatorXML = `<domain type='kvm'>
  <name>emu-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b06</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
    <vcpupin vcpu='1' cpuset='5'/>
    <emulatorpin cpuset='4-5'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

	t.Run("nil leaves untouched", func(t *testing.T) {
		out, err := SetPinning(withEmulatorXML, map[int][]int{0: {4}, 1: {5}}, -1, nil)
		if err != nil {
			t.Fatalf("SetPinning: %v", err)
		}
		cfg, err := ParseDomainXML(out)
		if err != nil {
			t.Fatalf("ParseDomainXML(out): %v", err)
		}
		if !reflect.DeepEqual(cfg.EmulatorPin, []int{4, 5}) {
			t.Errorf("EmulatorPin = %v, want unchanged [4 5]", cfg.EmulatorPin)
		}
	})

	t.Run("empty clears", func(t *testing.T) {
		out, err := SetPinning(withEmulatorXML, map[int][]int{0: {4}, 1: {5}}, -1, []int{})
		if err != nil {
			t.Fatalf("SetPinning: %v", err)
		}
		cfg, err := ParseDomainXML(out)
		if err != nil {
			t.Fatalf("ParseDomainXML(out): %v", err)
		}
		if cfg.EmulatorPin != nil {
			t.Errorf("EmulatorPin = %v, want nil (cleared)", cfg.EmulatorPin)
		}
	})

	t.Run("non-empty replaces", func(t *testing.T) {
		out, err := SetPinning(withEmulatorXML, map[int][]int{0: {4}, 1: {5}}, -1, []int{6})
		if err != nil {
			t.Fatalf("SetPinning: %v", err)
		}
		cfg, err := ParseDomainXML(out)
		if err != nil {
			t.Fatalf("ParseDomainXML(out): %v", err)
		}
		if !reflect.DeepEqual(cfg.EmulatorPin, []int{6}) {
			t.Errorf("EmulatorPin = %v, want [6]", cfg.EmulatorPin)
		}
	})

	t.Run("non-nil creates cputune when none existed", func(t *testing.T) {
		out, err := SetPinning(plainVMXML, map[int][]int{}, -1, []int{0, 1})
		if err != nil {
			t.Fatalf("SetPinning: %v", err)
		}
		cfg, err := ParseDomainXML(out)
		if err != nil {
			t.Fatalf("ParseDomainXML(out): %v", err)
		}
		if !reflect.DeepEqual(cfg.EmulatorPin, []int{0, 1}) {
			t.Errorf("EmulatorPin = %v, want [0 1]", cfg.EmulatorPin)
		}
		if len(cfg.VCPUPins) != 0 {
			t.Errorf("VCPUPins = %v, want empty (pins was empty)", cfg.VCPUPins)
		}
	})
}

// TestParseDomainXML_EmulatorPin covers parsing <cputune><emulatorpin>
// alongside vcpupin.
func TestParseDomainXML_EmulatorPin(t *testing.T) {
	const xml = `<domain type='kvm'>
  <name>emu-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b11</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
    <vcpupin vcpu='1' cpuset='5'/>
    <emulatorpin cpuset='4-5'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`
	cfg, err := ParseDomainXML(xml)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	if !reflect.DeepEqual(cfg.EmulatorPin, []int{4, 5}) {
		t.Errorf("EmulatorPin = %v, want [4 5]", cfg.EmulatorPin)
	}
}

// TestSetPinningEmptyPinsPreservesCputune covers the memory-node-only case
// (the "n" VMs-tab action): an empty pins map must leave <cputune> exactly
// as it was -- neither stripping an existing one nor fabricating an empty
// one where there was none -- while still applying numatune.
func TestSetPinningEmptyPinsPreservesCputune(t *testing.T) {
	cases := []struct {
		name string
		xml  string
	}{
		{"existing cputune", gpuVMXML},
		{"no cputune", plainVMXML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := ParseDomainXML(tc.xml)
			if err != nil {
				t.Fatalf("ParseDomainXML(before): %v", err)
			}

			out, err := SetPinning(tc.xml, map[int][]int{}, 0, nil)
			if err != nil {
				t.Fatalf("SetPinning: %v", err)
			}

			if tc.name == "no cputune" && strings.Contains(out, "<cputune") {
				t.Errorf("output = %q, want no <cputune> fabricated when pins is empty and none existed", out)
			}

			after, err := ParseDomainXML(out)
			if err != nil {
				t.Fatalf("ParseDomainXML(after): %v", err)
			}
			if !reflect.DeepEqual(after.VCPUPins, before.VCPUPins) {
				t.Errorf("VCPUPins = %v, want unchanged %v", after.VCPUPins, before.VCPUPins)
			}
			if !reflect.DeepEqual(after.MemNodes, []int{0}) {
				t.Errorf("MemNodes = %v, want [0]", after.MemNodes)
			}
		})
	}
}

// TestSetPinningEmptyPinsPreservesEmulatorPinByteIdentical covers
// TestSetPinningEmptyPinsPreservesCputune's own "existing cputune" case
// but with an emulatorpin present too: a memory-node-only op (empty pins,
// nil emulator, memNode set) must leave <cputune> -- emulatorpin included
// -- byte-identical to a true no-op call (nil pins, nil emulator, memNode
// -1); comparing against SetPinning's own re-serialization of the
// untouched element, not the original source text, since etree
// normalizes attribute quoting (' to ") on every write regardless of
// whether an element was actually touched.
func TestSetPinningEmptyPinsPreservesEmulatorPinByteIdentical(t *testing.T) {
	const withEmulatorXML = `<domain type='kvm'>
  <name>emu-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b06</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
    <vcpupin vcpu='1' cpuset='5'/>
    <emulatorpin cpuset='4-5'/>
  </cputune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

	baseline, err := SetPinning(withEmulatorXML, nil, -1, nil)
	if err != nil {
		t.Fatalf("SetPinning (baseline no-op): %v", err)
	}
	before := cputuneBlock(t, baseline)

	out, err := SetPinning(withEmulatorXML, map[int][]int{}, 0, nil)
	if err != nil {
		t.Fatalf("SetPinning: %v", err)
	}
	after := cputuneBlock(t, out)

	if after != before {
		t.Errorf("<cputune> = %q, want byte-identical to %q (untouched by a memory-node-only op)", after, before)
	}
}

// cputuneBlock extracts the <cputune>...</cputune> substring from xml,
// failing the test if it isn't present.
func cputuneBlock(t *testing.T, xml string) string {
	t.Helper()
	start := strings.Index(xml, "<cputune>")
	if start < 0 {
		t.Fatalf("xml = %q, want a <cputune> element", xml)
	}
	end := strings.Index(xml, "</cputune>")
	if end < 0 {
		t.Fatalf("xml = %q, want a </cputune> close tag", xml)
	}
	return xml[start : end+len("</cputune>")]
}

func TestStripPinning(t *testing.T) {
	out, err := StripPinning(gpuVMXML)
	if err != nil {
		t.Fatalf("StripPinning: %v", err)
	}
	cfg, err := ParseDomainXML(out)
	if err != nil {
		t.Fatalf("ParseDomainXML(out): %v", err)
	}
	if len(cfg.VCPUPins) != 0 {
		t.Errorf("VCPUPins = %v, want empty", cfg.VCPUPins)
	}
	if cfg.MemNodes != nil {
		t.Errorf("MemNodes = %v, want nil", cfg.MemNodes)
	}
	if strings.Contains(out, "<cputune") {
		t.Errorf("output still contains <cputune: %s", out)
	}
	if strings.Contains(out, "<numatune") {
		t.Errorf("output still contains <numatune: %s", out)
	}
	if !strings.Contains(out, "<hostdev") {
		t.Errorf("output missing hostdev: %s", out)
	}
	if !strings.Contains(out, "<disk") {
		t.Errorf("output missing disk: %s", out)
	}
}

// TestStripPinningRemovesEmulator covers Strip clearing emulatorpin
// alongside vcpupin/numatune.
func TestStripPinningRemovesEmulator(t *testing.T) {
	const xml = `<domain type='kvm'>
  <name>emu-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b12</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>2</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
    <vcpupin vcpu='1' cpuset='5'/>
    <emulatorpin cpuset='4-5'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='0'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`
	out, err := StripPinning(xml)
	if err != nil {
		t.Fatalf("StripPinning: %v", err)
	}
	if strings.Contains(out, "<cputune") {
		t.Errorf("output still contains <cputune (emulatorpin should be gone too): %s", out)
	}
	cfg, err := ParseDomainXML(out)
	if err != nil {
		t.Fatalf("ParseDomainXML(out): %v", err)
	}
	if cfg.EmulatorPin != nil {
		t.Errorf("EmulatorPin = %v, want nil", cfg.EmulatorPin)
	}
}
