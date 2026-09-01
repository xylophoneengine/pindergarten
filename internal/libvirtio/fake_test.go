package libvirtio

import "testing"

func TestFakeDefineUpdates(t *testing.T) {
	f := &Fake{XML: map[string]string{"plain-vm": plainVMXML}}
	doms, err := f.ListDomains()
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	if len(doms) != 1 {
		t.Fatalf("len(doms) = %d, want 1", len(doms))
	}
	if doms[0].Config.Name != "plain-vm" {
		t.Errorf("Name = %q, want plain-vm", doms[0].Config.Name)
	}
	if doms[0].State != StateShutoff {
		t.Errorf("State = %v, want StateShutoff", doms[0].State)
	}

	modified, err := SetPinning(plainVMXML, map[int][]int{0: {2}}, 0)
	if err != nil {
		t.Fatalf("SetPinning: %v", err)
	}
	if err := f.Define(modified); err != nil {
		t.Fatalf("Define: %v", err)
	}
	xml, err := f.DomainXML("plain-vm")
	if err != nil {
		t.Fatalf("DomainXML: %v", err)
	}
	cfg, err := ParseDomainXML(xml)
	if err != nil {
		t.Fatalf("ParseDomainXML: %v", err)
	}
	want := map[int][]int{0: {2}}
	if len(cfg.VCPUPins) != len(want) || cfg.VCPUPins[0][0] != 2 {
		t.Errorf("VCPUPins = %v, want %v", cfg.VCPUPins, want)
	}
}

func TestFakeUnparsableDomainListed(t *testing.T) {
	f := &Fake{XML: map[string]string{"weird": "<domain><name>weird</name><vcpu>bogus</vcpu></domain>"}}
	doms, err := f.ListDomains()
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	if len(doms) != 1 {
		t.Fatalf("len(doms) = %d, want 1", len(doms))
	}
	if doms[0].ParseErr == nil {
		t.Fatal("ParseErr = nil, want non-nil")
	}
	if doms[0].Config == nil || doms[0].Config.Name != "weird" {
		t.Errorf("Config = %+v, want Name = weird", doms[0].Config)
	}
}
