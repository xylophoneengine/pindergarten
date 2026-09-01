package backup

import (
	"os"
	"testing"
	"time"
)

func TestSaveListLoad(t *testing.T) {
	dir := t.TempDir() + "/bk"
	e1, err := Save(dir, "vm-a", "pin 2 vcpus to node 1", "test", "<domain>a</domain>")
	if err != nil {
		t.Fatalf("Save e1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	e2, err := Save(dir, "vm-b", "strip pinning", "test", "<domain>b</domain>")
	if err != nil {
		t.Fatalf("Save e2: %v", err)
	}

	list, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].Meta.VM != "vm-b" {
		t.Errorf("list[0].Meta.VM = %q, want vm-b (newest first)", list[0].Meta.VM)
	}
	if list[1].Meta.Op != "pin 2 vcpus to node 1" {
		t.Errorf("list[1].Meta.Op = %q, want %q", list[1].Meta.Op, "pin 2 vcpus to node 1")
	}

	xml, err := LoadXML(e1)
	if err != nil {
		t.Fatalf("LoadXML: %v", err)
	}
	if xml != "<domain>a</domain>" {
		t.Errorf("xml = %q, want <domain>a</domain>", xml)
	}
	_ = e2
}

func TestListSkipsCorruptSidecar(t *testing.T) {
	dir := t.TempDir() + "/bk"
	if _, err := Save(dir, "vm-a", "pin 2 vcpus to node 1", "test", "<domain>a</domain>"); err != nil {
		t.Fatalf("Save vm-a: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := Save(dir, "vm-b", "strip pinning", "test", "<domain>b</domain>"); err != nil {
		t.Fatalf("Save vm-b: %v", err)
	}

	garbage := dir + "/20260101T000000.000000000Z_vm-broken.xml.json"
	if err := os.WriteFile(garbage, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write garbage sidecar: %v", err)
	}

	list, err := List(dir)
	if err != nil {
		t.Fatalf("List err = %v, want nil", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2 (garbage sidecar should be skipped)", len(list))
	}
}

func TestListMissingDir(t *testing.T) {
	list, err := List(t.TempDir() + "/nope")
	if err != nil {
		t.Fatalf("List err = %v, want nil", err)
	}
	if len(list) != 0 {
		t.Errorf("len(list) = %d, want 0", len(list))
	}
}
