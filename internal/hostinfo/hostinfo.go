// Package hostinfo reads CPU/NUMA topology from a Linux sysfs tree.
package hostinfo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Thread describes one logical CPU (hardware thread).
type Thread struct{ ID, Core, Socket, Node, Sibling, L3 int } // Sibling: other SMT thread ID, -1 if none; L3: L3Domain ID, -1 if unknown

// Core groups the threads that share a (Socket, core_id).
type Core struct {
	Socket, ID, Node, L3 int   // L3: L3Domain ID, -1 if unknown
	Threads              []int // sorted
}

// Node describes one NUMA node.
type Node struct {
	ID                      int
	Threads                 []int // sorted
	MemTotalKiB, MemFreeKiB uint64
}

// Socket describes one physical CPU package.
type Socket struct {
	ID    int
	Model string // from /proc/cpuinfo "model name"; empty if unknown
	Nodes []int  // sorted NUMA node IDs containing any of this socket's threads
}

// L3Domain groups the threads that share a unified L3 cache.
type L3Domain struct {
	ID      int
	Node    int
	Socket  int
	Threads []int // sorted
}

// PCIDevice describes one PCI device kept because it is a display
// controller or is bound to vfio-pci.
type PCIDevice struct {
	Addr                   string
	Class                  string // hex class code, no "0x" prefix, e.g. "030000"
	VendorID, DeviceID     string // hex IDs, no "0x" prefix
	VendorName, DeviceName string // from a pci.ids file; empty if unknown
	Driver                 string // bound kernel driver name, empty if none
	Node                   int    // from numa_node, -1 if unknown or missing
}

// Topology is the full host CPU/NUMA topology.
type Topology struct {
	Nodes      []Node // sorted by ID
	Cores      []Core // sorted by Node, then Socket, then ID
	Threads    map[int]Thread
	Sockets    []Socket    // sorted by ID
	L3Domains  []L3Domain  // sorted by ID
	PCIDevices []PCIDevice // sorted by Addr
}

// cpuinfoPath is the source of per-socket CPU model names. Tests override
// it to point at a fixture file.
var cpuinfoPath = "/proc/cpuinfo"

// pciIDsPaths are searched in order for PCI vendor/device names. Tests
// override it to point at a fixture file.
var pciIDsPaths = []string{"/usr/share/hwdata/pci.ids", "/usr/share/misc/pci.ids"}

// ParseCPUList parses a Linux-style cpulist ("0-3,8") into a sorted,
// deduplicated slice of ints. An empty string yields an empty (non-nil)
// slice.
func ParseCPUList(s string) ([]int, error) {
	ids := []int{}
	s = strings.TrimSpace(s)
	if s == "" {
		return ids, nil
	}
	seen := make(map[int]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, err := strconv.Atoi(lo)
			if err != nil {
				return nil, fmt.Errorf("hostinfo: invalid cpulist %q: %w", s, err)
			}
			b, err := strconv.Atoi(hi)
			if err != nil {
				return nil, fmt.Errorf("hostinfo: invalid cpulist %q: %w", s, err)
			}
			for i := a; i <= b; i++ {
				if !seen[i] {
					seen[i] = true
					ids = append(ids, i)
				}
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("hostinfo: invalid cpulist %q: %w", s, err)
			}
			if !seen[n] {
				seen[n] = true
				ids = append(ids, n)
			}
		}
	}
	sort.Ints(ids)
	return ids, nil
}

// FormatCPUList formats a slice of ints as a comma-separated list, e.g.
// [4 68] -> "4,68".
func FormatCPUList(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Read reads the sysfs tree rooted at sysfsRoot and builds the host
// topology. It performs no caching; every call re-reads from disk.
func Read(sysfsRoot string) (*Topology, error) {
	nodeDirs, err := filepath.Glob(filepath.Join(sysfsRoot, "devices/system/node/node[0-9]*"))
	if err != nil {
		return nil, err
	}

	nodeOfThread := make(map[int]int)
	nodes := make([]Node, 0, len(nodeDirs))
	for _, dir := range nodeDirs {
		base := filepath.Base(dir)
		id, err := strconv.Atoi(strings.TrimPrefix(base, "node"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: bad node dir %q: %w", base, err)
		}

		cpulistRaw, err := readTrimmed(filepath.Join(dir, "cpulist"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: reading cpulist for node %d: %w", id, err)
		}
		threads, err := ParseCPUList(cpulistRaw)
		if err != nil {
			return nil, fmt.Errorf("hostinfo: parsing cpulist for node %d: %w", id, err)
		}
		for _, t := range threads {
			nodeOfThread[t] = id
		}

		memTotal, memFree, err := readMeminfo(filepath.Join(dir, "meminfo"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: reading meminfo for node %d: %w", id, err)
		}

		nodes = append(nodes, Node{
			ID:          id,
			Threads:     threads,
			MemTotalKiB: memTotal,
			MemFreeKiB:  memFree,
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	cpuDirs, err := filepath.Glob(filepath.Join(sysfsRoot, "devices/system/cpu/cpu[0-9]*"))
	if err != nil {
		return nil, err
	}

	threads := make(map[int]Thread)
	type coreKey struct{ socket, id int }
	coreThreads := make(map[coreKey][]int)

	for _, dir := range cpuDirs {
		base := filepath.Base(dir)
		id, err := strconv.Atoi(strings.TrimPrefix(base, "cpu"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: bad cpu dir %q: %w", base, err)
		}
		topoDir := filepath.Join(dir, "topology")

		socketRaw, err := readTrimmed(filepath.Join(topoDir, "physical_package_id"))
		if errors.Is(err, fs.ErrNotExist) {
			// Kernel removes topology/ for an offlined CPU; the cpuN dir
			// itself stays. Such a CPU is offline: it belongs in no
			// Thread/Core.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hostinfo: reading physical_package_id for cpu %d: %w", id, err)
		}
		socket, err := strconv.Atoi(socketRaw)
		if err != nil {
			return nil, fmt.Errorf("hostinfo: parsing physical_package_id for cpu %d: %w", id, err)
		}

		coreRaw, err := readTrimmed(filepath.Join(topoDir, "core_id"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: reading core_id for cpu %d: %w", id, err)
		}
		core, err := strconv.Atoi(coreRaw)
		if err != nil {
			return nil, fmt.Errorf("hostinfo: parsing core_id for cpu %d: %w", id, err)
		}

		siblingsRaw, err := readTrimmed(filepath.Join(topoDir, "thread_siblings_list"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: reading thread_siblings_list for cpu %d: %w", id, err)
		}
		siblings, err := ParseCPUList(siblingsRaw)
		if err != nil {
			return nil, fmt.Errorf("hostinfo: parsing thread_siblings_list for cpu %d: %w", id, err)
		}
		sibling := -1
		for _, s := range siblings {
			if s != id {
				sibling = s
				break
			}
		}

		threads[id] = Thread{
			ID:      id,
			Core:    core,
			Socket:  socket,
			Node:    nodeOfThread[id],
			Sibling: sibling,
			L3:      -1,
		}

		key := coreKey{socket, core}
		coreThreads[key] = append(coreThreads[key], id)
	}

	l3Domains, err := readL3Domains(cpuDirs, threads)
	if err != nil {
		return nil, err
	}

	cores := make([]Core, 0, len(coreThreads))
	for key, tids := range coreThreads {
		sort.Ints(tids)
		node, l3 := -1, -1
		if len(tids) > 0 {
			node = threads[tids[0]].Node
			l3 = threads[tids[0]].L3
		}
		cores = append(cores, Core{
			Socket:  key.socket,
			ID:      key.id,
			Node:    node,
			L3:      l3,
			Threads: tids,
		})
	}
	sort.Slice(cores, func(i, j int) bool {
		if cores[i].Node != cores[j].Node {
			return cores[i].Node < cores[j].Node
		}
		if cores[i].Socket != cores[j].Socket {
			return cores[i].Socket < cores[j].Socket
		}
		return cores[i].ID < cores[j].ID
	})

	sockets, err := readSockets(cpuinfoPath, threads)
	if err != nil {
		return nil, err
	}

	pciDevices, err := readPCIDevices(sysfsRoot, pciIDsPaths)
	if err != nil {
		return nil, err
	}

	return &Topology{
		Nodes:      nodes,
		Cores:      cores,
		Threads:    threads,
		Sockets:    sockets,
		L3Domains:  l3Domains,
		PCIDevices: pciDevices,
	}, nil
}

// readL3Domains groups the online threads in cpuDirs by shared unified L3
// cache (cache/index*/ with level 3, type Unified), assigns each group an
// ID in ascending order of its lowest thread, and sets threads[id].L3 for
// every thread in a group. Missing cache directories are not an error: no
// domains are produced and every thread keeps its default L3 of -1.
func readL3Domains(cpuDirs []string, threads map[int]Thread) ([]L3Domain, error) {
	seen := map[string]bool{}
	var rawLists []string
	for _, dir := range cpuDirs {
		base := filepath.Base(dir)
		id, err := strconv.Atoi(strings.TrimPrefix(base, "cpu"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: bad cpu dir %q: %w", base, err)
		}
		if _, online := threads[id]; !online {
			continue
		}

		idxDirs, err := filepath.Glob(filepath.Join(dir, "cache/index[0-9]*"))
		if err != nil {
			return nil, err
		}
		for _, idxDir := range idxDirs {
			level, err := readTrimmed(filepath.Join(idxDir, "level"))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("hostinfo: reading level in %q: %w", idxDir, err)
			}
			if level != "3" {
				continue
			}
			typ, err := readTrimmed(filepath.Join(idxDir, "type"))
			if err != nil {
				return nil, fmt.Errorf("hostinfo: reading type in %q: %w", idxDir, err)
			}
			if typ != "Unified" {
				continue
			}
			raw, err := readTrimmed(filepath.Join(idxDir, "shared_cpu_list"))
			if err != nil {
				return nil, fmt.Errorf("hostinfo: reading shared_cpu_list in %q: %w", idxDir, err)
			}
			if !seen[raw] {
				seen[raw] = true
				rawLists = append(rawLists, raw)
			}
		}
	}

	groups := make([][]int, 0, len(rawLists))
	for _, raw := range rawLists {
		ids, err := ParseCPUList(raw)
		if err != nil {
			return nil, fmt.Errorf("hostinfo: parsing shared_cpu_list %q: %w", raw, err)
		}
		if len(ids) == 0 {
			continue
		}
		groups = append(groups, ids)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })

	domains := make([]L3Domain, 0, len(groups))
	for i, g := range groups {
		node, socket := -1, -1
		if th, ok := threads[g[0]]; ok {
			node, socket = th.Node, th.Socket
		}
		for _, t := range g {
			if th, ok := threads[t]; ok {
				th.L3 = i
				threads[t] = th
			}
		}
		domains = append(domains, L3Domain{ID: i, Node: node, Socket: socket, Threads: g})
	}
	return domains, nil
}

// readSockets builds one Socket per physical_package_id found among
// threads, with its CPU model from cpuinfoPath and the sorted set of NUMA
// nodes its threads belong to.
func readSockets(cpuinfoPath string, threads map[int]Thread) ([]Socket, error) {
	models, err := readCPUModels(cpuinfoPath)
	if err != nil {
		return nil, err
	}

	nodesOf := map[int]map[int]bool{}
	for _, th := range threads {
		if nodesOf[th.Socket] == nil {
			nodesOf[th.Socket] = map[int]bool{}
		}
		nodesOf[th.Socket][th.Node] = true
	}

	ids := make([]int, 0, len(nodesOf))
	for s := range nodesOf {
		ids = append(ids, s)
	}
	sort.Ints(ids)

	sockets := make([]Socket, 0, len(ids))
	for _, s := range ids {
		nodes := make([]int, 0, len(nodesOf[s]))
		for n := range nodesOf[s] {
			nodes = append(nodes, n)
		}
		sort.Ints(nodes)
		sockets = append(sockets, Socket{ID: s, Model: models[s], Nodes: nodes})
	}
	return sockets, nil
}

// readCPUModels parses /proc/cpuinfo-format text at path into a map of
// physical_package_id -> "model name". A missing file yields an empty map,
// not an error.
func readCPUModels(path string) (map[int]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[int]string{}, nil
	}
	if err != nil {
		return nil, err
	}

	models := make(map[int]string)
	physID, haveID, model := 0, false, ""
	flush := func() {
		if haveID {
			if _, ok := models[physID]; !ok {
				models[physID] = model
			}
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			physID, haveID, model = 0, false, ""
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "physical id":
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				physID, haveID = n, true
			}
		case "model name":
			model = strings.TrimSpace(val)
		}
	}
	flush()
	return models, nil
}

// readPCIDevices enumerates /sys/bus/pci/devices under sysfsRoot, keeping
// display controllers (class 0x03xxxx) and any device bound to vfio-pci.
// Vendor/device names come from the first readable file in idsPaths;
// absent that, devices keep their bare hex IDs.
func readPCIDevices(sysfsRoot string, idsPaths []string) ([]PCIDevice, error) {
	names := loadPCIIDs(idsPaths)

	dirs, err := filepath.Glob(filepath.Join(sysfsRoot, "bus/pci/devices/*"))
	if err != nil {
		return nil, err
	}

	devices := make([]PCIDevice, 0, len(dirs))
	for _, dir := range dirs {
		classRaw, err := readTrimmed(filepath.Join(dir, "class"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hostinfo: reading class in %q: %w", dir, err)
		}
		vendorRaw, err := readTrimmed(filepath.Join(dir, "vendor"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: reading vendor in %q: %w", dir, err)
		}
		deviceRaw, err := readTrimmed(filepath.Join(dir, "device"))
		if err != nil {
			return nil, fmt.Errorf("hostinfo: reading device in %q: %w", dir, err)
		}

		driver := ""
		if target, err := os.Readlink(filepath.Join(dir, "driver")); err == nil {
			driver = filepath.Base(target)
		}

		class := hexTrim(classRaw)
		isDisplay := len(class) >= 2 && class[:2] == "03"
		if !isDisplay && driver != "vfio-pci" {
			continue
		}

		node := -1
		if numaRaw, err := readTrimmed(filepath.Join(dir, "numa_node")); err == nil {
			if n, err := strconv.Atoi(numaRaw); err == nil {
				node = n
			}
		}

		vendorID, deviceID := hexTrim(vendorRaw), hexTrim(deviceRaw)
		devices = append(devices, PCIDevice{
			Addr:       filepath.Base(dir),
			Class:      class,
			VendorID:   vendorID,
			DeviceID:   deviceID,
			VendorName: names.vendors[vendorID],
			DeviceName: names.devices[vendorID+deviceID],
			Driver:     driver,
			Node:       node,
		})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Addr < devices[j].Addr })
	return devices, nil
}

// hexTrim lowercases s and strips a leading "0x", as found in sysfs
// class/vendor/device files.
func hexTrim(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
}

// pciNames holds vendor/device names parsed from a pci.ids file, keyed by
// lowercase hex ID (device keys are vendorID+deviceID).
type pciNames struct {
	vendors map[string]string
	devices map[string]string
}

// loadPCIIDs reads the first existing file in paths and parses its
// vendor/device lines (subvendor lines and comments are skipped). No
// existing file yields empty maps, not an error.
func loadPCIIDs(paths []string) pciNames {
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return parsePCIIDs(string(b))
	}
	return pciNames{vendors: map[string]string{}, devices: map[string]string{}}
}

func parsePCIIDs(s string) pciNames {
	names := pciNames{vendors: map[string]string{}, devices: map[string]string{}}
	curVendor := ""
	for _, line := range strings.Split(s, "\n") {
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "\t\t"):
			continue // subvendor line: not needed
		case strings.HasPrefix(line, "\t"):
			if id, name, ok := splitPCIIDLine(strings.TrimPrefix(line, "\t")); ok && curVendor != "" {
				names.devices[curVendor+id] = name
			}
		default:
			if id, name, ok := splitPCIIDLine(line); ok {
				curVendor = id
				names.vendors[id] = name
			}
		}
	}
	return names
}

// splitPCIIDLine splits a pci.ids entry line ("DDDD  Name") into its hex ID
// and name.
func splitPCIIDLine(s string) (id, name string, ok bool) {
	if len(s) < 6 {
		return "", "", false
	}
	id = strings.ToLower(s[:4])
	if _, err := strconv.ParseUint(id, 16, 32); err != nil {
		return "", "", false
	}
	return id, strings.TrimSpace(s[4:]), true
}

func readMeminfo(path string) (total, free uint64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "MemTotal:" && i+1 < len(fields) {
				total, err = strconv.ParseUint(fields[i+1], 10, 64)
				if err != nil {
					return 0, 0, fmt.Errorf("hostinfo: parsing MemTotal in %q: %w", line, err)
				}
			}
			if f == "MemFree:" && i+1 < len(fields) {
				free, err = strconv.ParseUint(fields[i+1], 10, 64)
				if err != nil {
					return 0, 0, fmt.Errorf("hostinfo: parsing MemFree in %q: %w", line, err)
				}
			}
		}
	}
	return total, free, nil
}

// PCINumaNode returns the NUMA node reported for the PCI device at addr, or
// -1 if the device's numa_node file is missing or unparsable.
func PCINumaNode(sysfsRoot, addr string) int {
	raw, err := readTrimmed(filepath.Join(sysfsRoot, "bus/pci/devices", addr, "numa_node"))
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return n
}

// PCINumaNodeIn is like PCINumaNode, but resolves an unknown (-1) node to
// topo's only NUMA node when the host has exactly one. topo may be nil, in
// which case it behaves exactly like PCINumaNode.
func PCINumaNodeIn(topo *Topology, sysfsRoot, addr string) int {
	n := PCINumaNode(sysfsRoot, addr)
	if n == -1 && topo != nil && len(topo.Nodes) == 1 {
		return topo.Nodes[0].ID
	}
	return n
}
