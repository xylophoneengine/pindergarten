package libvirtio

import (
	"reflect"
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
