// Package hostinfo reads CPU/NUMA topology from a Linux sysfs tree.
package hostinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Thread describes one logical CPU (hardware thread).
type Thread struct{ ID, Core, Socket, Node, Sibling int } // Sibling: other SMT thread ID, -1 if none

// Core groups the threads that share a (Socket, core_id).
type Core struct {
	Socket, ID, Node int
	Threads          []int // sorted
}

// Node describes one NUMA node.
type Node struct {
	ID                      int
	Threads                 []int // sorted
	MemTotalKiB, MemFreeKiB uint64
}

// Topology is the full host CPU/NUMA topology.
type Topology struct {
	Nodes   []Node // sorted by ID
	Cores   []Core // sorted by Node, then Socket, then ID
	Threads map[int]Thread
}

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
		}

		key := coreKey{socket, core}
		coreThreads[key] = append(coreThreads[key], id)
	}

	cores := make([]Core, 0, len(coreThreads))
	for key, tids := range coreThreads {
		sort.Ints(tids)
		node := -1
		if len(tids) > 0 {
			node = threads[tids[0]].Node
		}
		cores = append(cores, Core{
			Socket:  key.socket,
			ID:      key.id,
			Node:    node,
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

	return &Topology{Nodes: nodes, Cores: cores, Threads: threads}, nil
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
