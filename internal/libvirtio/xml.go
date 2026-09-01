// Package libvirtio parses and edits libvirt domain XML.
package libvirtio

import (
	"fmt"
	"strconv"

	"github.com/beevik/etree"
	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
)

// DomainConfig is the parsed subset of a libvirt domain XML document
// relevant to vCPU pinning.
type DomainConfig struct {
	Name      string
	UUID      string
	VCPUs     int
	MemoryKiB uint64
	VCPUPins  map[int][]int // vcpu -> host thread IDs; empty map if no cputune
	MemNodes  []int         // numatune nodeset; nil if no numatune
	MemMode   string        // numatune mode attr, "" if none
	Hostdevs  []string      // PCI addrs "0000:81:00.0" from passthrough hostdevs
	Raw       string        // original XML verbatim
}

// ParseDomainXML parses a libvirt domain XML document into a DomainConfig.
func ParseDomainXML(raw string) (*DomainConfig, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromString(raw); err != nil {
		return nil, fmt.Errorf("libvirtio: parsing domain xml: %w", err)
	}
	domain := doc.SelectElement("domain")
	if domain == nil {
		return nil, fmt.Errorf("libvirtio: domain xml missing <domain> root")
	}

	cfg := &DomainConfig{
		VCPUPins: map[int][]int{},
		Raw:      raw,
	}

	if e := domain.SelectElement("name"); e != nil {
		cfg.Name = e.Text()
	}
	if e := domain.SelectElement("uuid"); e != nil {
		cfg.UUID = e.Text()
	}

	if e := domain.SelectElement("vcpu"); e != nil {
		n, err := strconv.Atoi(e.Text())
		if err != nil {
			return nil, fmt.Errorf("libvirtio: parsing vcpu count: %w", err)
		}
		cfg.VCPUs = n
	}

	if e := domain.SelectElement("memory"); e != nil {
		kib, err := memoryKiB(e)
		if err != nil {
			return nil, err
		}
		cfg.MemoryKiB = kib
	}

	if cputune := domain.SelectElement("cputune"); cputune != nil {
		for _, pin := range cputune.SelectElements("vcpupin") {
			vcpu, err := strconv.Atoi(pin.SelectAttrValue("vcpu", ""))
			if err != nil {
				return nil, fmt.Errorf("libvirtio: parsing vcpupin vcpu attr: %w", err)
			}
			threads, err := hostinfo.ParseCPUList(pin.SelectAttrValue("cpuset", ""))
			if err != nil {
				return nil, fmt.Errorf("libvirtio: parsing vcpupin cpuset: %w", err)
			}
			cfg.VCPUPins[vcpu] = threads
		}
	}

	if numatune := domain.SelectElement("numatune"); numatune != nil {
		if mem := numatune.SelectElement("memory"); mem != nil {
			cfg.MemMode = mem.SelectAttrValue("mode", "")
			nodes, err := hostinfo.ParseCPUList(mem.SelectAttrValue("nodeset", ""))
			if err != nil {
				return nil, fmt.Errorf("libvirtio: parsing numatune nodeset: %w", err)
			}
			cfg.MemNodes = nodes
		}
	}

	if devices := domain.SelectElement("devices"); devices != nil {
		for _, hd := range devices.SelectElements("hostdev") {
			if hd.SelectAttrValue("mode", "") != "subsystem" || hd.SelectAttrValue("type", "") != "pci" {
				continue
			}
			source := hd.SelectElement("source")
			if source == nil {
				continue
			}
			addr := source.SelectElement("address")
			if addr == nil {
				continue
			}
			pciAddr, err := formatPCIAddress(addr)
			if err != nil {
				return nil, err
			}
			cfg.Hostdevs = append(cfg.Hostdevs, pciAddr)
		}
	}

	return cfg, nil
}

// memoryKiB reads a <memory unit='...'>N</memory>-shaped element and
// converts its value to KiB.
func memoryKiB(e *etree.Element) (uint64, error) {
	n, err := strconv.ParseUint(e.Text(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("libvirtio: parsing memory value: %w", err)
	}
	unit := e.SelectAttrValue("unit", "KiB")
	switch unit {
	case "b", "bytes":
		return n / 1024, nil
	case "KiB", "":
		return n, nil
	case "MiB":
		return n * 1024, nil
	case "GiB":
		return n * 1024 * 1024, nil
	case "TiB":
		return n * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("libvirtio: unsupported memory unit %q", unit)
	}
}

// formatPCIAddress reads a hex <address domain= bus= slot= function=/>
// element and formats it as "0000:81:00.0".
func formatPCIAddress(addr *etree.Element) (string, error) {
	parse := func(attr string) (uint64, error) {
		v, err := strconv.ParseUint(addr.SelectAttrValue(attr, ""), 0, 32)
		if err != nil {
			return 0, fmt.Errorf("libvirtio: parsing hostdev address %s: %w", attr, err)
		}
		return v, nil
	}
	domain, err := parse("domain")
	if err != nil {
		return "", err
	}
	bus, err := parse("bus")
	if err != nil {
		return "", err
	}
	slot, err := parse("slot")
	if err != nil {
		return "", err
	}
	function, err := parse("function")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04x:%02x:%02x.%x", domain, bus, slot, function), nil
}
