// Package apply executes staged pending ops against libvirt: it is the
// only place in pindergarten that writes domain config. Every write is a
// config-only Define, backed up beforehand, with drift re-checked right
// before writing and the result verified right after.
package apply

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/xylophoneengine/pindergarten/internal/backup"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// Result reports the outcome of applying one PendingOp.
type Result struct {
	Op         model.PendingOp
	BackupPath string
	Applied    bool
	Drifted    bool
	Err        error
}

// CheckDrift returns the names (deduped, in op order) of VMs whose current
// domain config XML hash differs from the op's StagedHash. A DomainXML
// error is returned to the caller as-is.
func CheckDrift(h libvirtio.Hypervisor, ops []model.PendingOp) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	for _, op := range ops {
		xml, err := h.DomainXML(op.VM)
		if err != nil {
			return nil, err
		}
		if model.HashXML(xml) != op.StagedHash && !seen[op.VM] {
			seen[op.VM] = true
			names = append(names, op.VM)
		}
	}
	return names, nil
}

// Run executes ops sequentially against h, one Result per op in order.
//
// Per op: fetch the live XML and re-check drift against StagedHash (a
// mismatch sets Drifted and moves on to the next op without touching
// libvirt); back up the live XML (always, before any Define, including
// OpRestore); build the new XML for the op's Kind; Define it; then
// re-fetch and verify the result matches intent. The first hard error
// (fetch, backup, build, Define, or verify) stops the run: that op's Err
// is set and every remaining op is returned untouched (Applied=false,
// Drifted=false, Err=nil).
func Run(h libvirtio.Hypervisor, backupDir, version string, ops []model.PendingOp) []Result {
	results := make([]Result, len(ops))
	stopped := false

	for i, op := range ops {
		results[i].Op = op
		if stopped {
			continue
		}

		xml, err := h.DomainXML(op.VM)
		if err != nil {
			results[i].Err = fmt.Errorf("%s: fetch xml: %w", op.VM, err)
			stopped = true
			continue
		}

		if model.HashXML(xml) != op.StagedHash {
			results[i].Drifted = true
			continue
		}

		entry, err := backup.Save(backupDir, op.VM, op.Summary, version, xml)
		if err != nil {
			results[i].Err = fmt.Errorf("%s: backup: %w", op.VM, err)
			stopped = true
			continue
		}
		results[i].BackupPath = entry.XMLPath

		newXML, err := buildXML(op, xml)
		if err != nil {
			results[i].Err = fmt.Errorf("%s: build xml: %w", op.VM, err)
			stopped = true
			continue
		}

		if err := h.Define(newXML); err != nil {
			results[i].Err = fmt.Errorf("%s: define: %w", op.VM, err)
			stopped = true
			continue
		}

		if err := verify(h, op); err != nil {
			results[i].Err = fmt.Errorf("%s: verify: %w", op.VM, err)
			stopped = true
			continue
		}

		results[i].Applied = true
	}

	return results
}

// buildXML produces the new domain XML for op given the current live xml.
func buildXML(op model.PendingOp, xml string) (string, error) {
	switch op.Kind {
	case model.OpPin:
		return libvirtio.SetPinning(xml, op.Pins, op.MemNode, op.EmulatorPin)
	case model.OpStrip:
		return libvirtio.StripPinning(xml)
	case model.OpRestore:
		cfg, err := libvirtio.ParseDomainXML(op.BackupXML)
		if err != nil {
			return "", fmt.Errorf("parsing backup xml: %w", err)
		}
		if cfg.Name != op.VM {
			return "", fmt.Errorf("backup xml is for domain %q, not %q", cfg.Name, op.VM)
		}
		return op.BackupXML, nil
	default:
		return "", fmt.Errorf("apply: unknown op kind %v", op.Kind)
	}
}

// verify re-fetches op.VM's domain XML after Define and confirms its
// parsed pins/membind match op's intent.
func verify(h libvirtio.Hypervisor, op model.PendingOp) error {
	xml, err := h.DomainXML(op.VM)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	cfg, err := libvirtio.ParseDomainXML(xml)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	switch op.Kind {
	case model.OpStrip:
		if len(cfg.VCPUPins) != 0 {
			return fmt.Errorf("vcpupins not empty: %v", cfg.VCPUPins)
		}
		if len(cfg.MemNodes) != 0 {
			return fmt.Errorf("memnodes not empty: %v", cfg.MemNodes)
		}
		if len(cfg.EmulatorPin) != 0 {
			return fmt.Errorf("emulatorpin not empty: %v", cfg.EmulatorPin)
		}
	case model.OpPin:
		// An empty Pins means "no cputune opinion" (a memory-node-only
		// change): SetPinning leaves the live cputune untouched, which can
		// legitimately be non-empty, so only compare when Pins says
		// something.
		if len(op.Pins) > 0 && !reflect.DeepEqual(normalizePins(cfg.VCPUPins), normalizePins(op.Pins)) {
			return fmt.Errorf("vcpupins mismatch: got %v want %v", cfg.VCPUPins, op.Pins)
		}
		if op.MemNode >= 0 {
			want := []int{op.MemNode}
			if !reflect.DeepEqual(cfg.MemNodes, want) {
				return fmt.Errorf("memnodes mismatch: got %v want %v", cfg.MemNodes, want)
			}
		}
		// op.EmulatorPin == nil means "no emulatorpin opinion" (a
		// memory-node-only change), same rule as Pins above: SetPinning
		// leaves the live emulatorpin untouched, so only compare when the
		// op actually says something.
		if op.EmulatorPin != nil {
			got := append([]int(nil), cfg.EmulatorPin...)
			want := append([]int(nil), op.EmulatorPin...)
			sort.Ints(got)
			sort.Ints(want)
			if !reflect.DeepEqual(got, want) {
				return fmt.Errorf("emulatorpin mismatch: got %v want %v", cfg.EmulatorPin, op.EmulatorPin)
			}
		}
	case model.OpRestore:
		wantCfg, err := libvirtio.ParseDomainXML(op.BackupXML)
		if err != nil {
			return fmt.Errorf("parsing backup xml: %w", err)
		}
		if !reflect.DeepEqual(normalizePins(cfg.VCPUPins), normalizePins(wantCfg.VCPUPins)) {
			return fmt.Errorf("vcpupins mismatch: got %v want %v", cfg.VCPUPins, wantCfg.VCPUPins)
		}
		if !reflect.DeepEqual(normalizeNodes(cfg.MemNodes), normalizeNodes(wantCfg.MemNodes)) {
			return fmt.Errorf("memnodes mismatch: got %v want %v", cfg.MemNodes, wantCfg.MemNodes)
		}
	}
	return nil
}

// normalizePins treats a nil and an empty pin map as equal.
func normalizePins(m map[int][]int) map[int][]int {
	if len(m) == 0 {
		return map[int][]int{}
	}
	return m
}

// normalizeNodes treats a nil and an empty node slice as equal.
func normalizeNodes(s []int) []int {
	if len(s) == 0 {
		return nil
	}
	return s
}
