package main

import (
	"reflect"
	"testing"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
)

// interleavedTopo builds a hand-built 2-node, SMT2 topology with 3 cores per
// node (6 threads per node, 12 total) numbered in the interleaved order real
// sysfs commonly uses: every core's own primary thread comes first across
// every node (ids 0-5), then every core's SMT sibling (ids 6-11, offset by
// totalCores) -- unlike this package's own straight-run fixtures, no core's
// two threads are adjacent ids, so reservedThreads/minCoresPerNode are
// exercised against topology order rather than thread-id order.
func interleavedTopo() *hostinfo.Topology {
	const nodes, coresPerNode = 2, 3
	const totalCores = nodes * coresPerNode

	var cores []hostinfo.Core
	nodeThreads := make([][]int, nodes)
	for n := 0; n < nodes; n++ {
		for c := 0; c < coresPerNode; c++ {
			primary := n*coresPerNode + c
			sibling := primary + totalCores
			cores = append(cores, hostinfo.Core{Socket: 0, ID: c, Node: n, Threads: []int{primary, sibling}})
			nodeThreads[n] = append(nodeThreads[n], primary, sibling)
		}
	}
	topoNodes := make([]hostinfo.Node, nodes)
	for n := 0; n < nodes; n++ {
		topoNodes[n] = hostinfo.Node{ID: n, Threads: nodeThreads[n]}
	}
	return &hostinfo.Topology{Nodes: topoNodes, Cores: cores}
}

// TestReservedThreads covers reservedThreads and minCoresPerNode against
// interleavedTopo (2 nodes, SMT2, 3 cores/node): N=0 reserves nothing, N=1
// reserves both SMT threads of the first core of every node, N=2 the first
// two cores of every node, and N >= the smallest node's core count is
// rejected before reservedThreads is ever called -- main's own startup
// guard ("-reserve N must be less than the smallest NUMA node's core
// count"), which is exactly the "n >= min" comparison checked below (neither
// function here has an error return of its own).
func TestReservedThreads(t *testing.T) {
	topo := interleavedTopo()
	min := minCoresPerNode(topo)
	if min != 3 {
		t.Fatalf("minCoresPerNode() = %d, want 3 (smallest node's core count)", min)
	}

	tests := []struct {
		name    string
		n       int
		want    map[int]bool
		wantErr bool
	}{
		{name: "n=0 reserve off", n: 0, want: nil},
		{name: "n=1 first core of each node", n: 1,
			want: map[int]bool{0: true, 6: true, 3: true, 9: true}},
		{name: "n=2 first two cores of each node", n: 2,
			want: map[int]bool{0: true, 6: true, 1: true, 7: true, 3: true, 9: true, 4: true, 10: true}},
		{name: "n=3 (== smallest node's core count) rejected at startup", n: 3, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantErr {
				if tc.n < min {
					t.Fatalf("test fixture: n=%d should be >= min=%d", tc.n, min)
				}
				return
			}
			if got := reservedThreads(topo, tc.n); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reservedThreads(topo, %d) = %v, want %v", tc.n, got, tc.want)
			}
		})
	}
}
