package stages

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

// Two same-trunk regional edges running 40 m apart for 1 km, each
// continuing beyond the shared stretch from its own endpoint nodes, with
// a third edge hanging off one of those nodes. After the merge: one
// trunk over the shared run, the approaches intact, and — the first
// attempt's failure — nothing severed: every surviving edge endpoint
// must still be a live node shared with its continuation.
func TestCorridorMergeKeepsNodeIdentity(t *testing.T) {
	reg := func(id string) gtfs.Route { return gtfs.Route{ID: id, Type: 2, Agency: "LI"} }
	routes := map[string]gtfs.Route{"a": reg("a"), "b": reg("b"), "c": reg("c")}

	line := func(x0, y0, x1, y1 float64, n int) []geo.Pt {
		pts := make([]geo.Pt, n)
		for i := range pts {
			f := float64(i) / float64(n-1)
			pts[i] = geo.Pt{X: x0 + f*(x1-x0), Y: y0 + f*(y1-y0)}
		}
		return pts
	}
	net := &Network{
		Nodes: []Node{
			{At: geo.Pt{X: -400, Y: 0}},  // 0: a's west approach start
			{At: geo.Pt{X: 1400, Y: 0}},  // 1: a's east end
			{At: geo.Pt{X: -400, Y: 40}}, // 2: b's west start
			{At: geo.Pt{X: 1000, Y: 40}}, // 3: b's east end (overlap reaches it)
			{At: geo.Pt{X: 1000, Y: 400}}, // 4: hangs off b's east end
		},
		Edges: []Edge{
			// a: west approach then 0..1400 along y=0
			{From: 0, To: 1, Pts: line(-400, 0, 1400, 0, 90), Routes: []string{"a"}},
			// b: parallel at y=40 from -400 to 1000 (ends mid-corridor of a)
			{From: 2, To: 3, Pts: line(-400, 40, 1000, 40, 70), Routes: []string{"b"}},
			// c: departs from b's east node northward
			{From: 3, To: 4, Pts: line(1000, 40, 1000, 400, 20), Routes: []string{"c"}},
		},
	}
	rebuildAdj(net)
	if m := MergeParallelCorridors(net, routes); m == 0 {
		t.Fatalf("expected a merge")
	}
	// connectivity: from node 0 every edge set must be reachable — no
	// floating stubs (the first attempt's failure mode)
	rebuildAdj(net)
	seen := map[int]bool{}
	stack := []int{0}
	for len(stack) > 0 {
		ni := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[ni] {
			continue
		}
		seen[ni] = true
		for _, ei := range net.Nodes[ni].Adj {
			stack = append(stack, net.Edges[ei].From, net.Edges[ei].To)
		}
	}
	for ei, e := range net.Edges {
		if !seen[e.From] || !seen[e.To] {
			t.Fatalf("edge %d (%v) severed from the corridor: nodes %d,%d unreachable",
				ei, e.Routes, e.From, e.To)
		}
	}
	// the shared stretch must carry both riders on one edge
	shared := 0
	for _, e := range net.Edges {
		if len(e.Routes) == 2 {
			shared++
		}
	}
	if shared == 0 {
		t.Fatalf("no merged trunk edge carrying both routes")
	}
	// c's departure must still attach to a node that another edge touches
	for _, e := range net.Edges {
		if len(e.Routes) == 1 && e.Routes[0] == "c" {
			deg := len(net.Nodes[e.From].Adj) + len(net.Nodes[e.To].Adj)
			if deg < 3 {
				t.Fatalf("c's departure dangles: degree %d", deg)
			}
		}
	}
}
