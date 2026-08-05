package stages

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

func chainNet(orients []bool) *Network {
	// colinear chain along +X; orients[i]=true means edge stored forward
	net := &Network{}
	for i := 0; i <= len(orients); i++ {
		net.Nodes = append(net.Nodes, Node{At: geo.Pt{X: float64(i) * 100}})
	}
	for i, fwd := range orients {
		pts := []geo.Pt{{X: float64(i) * 100}, {X: float64(i)*100 + 100}}
		from, to := i, i+1
		if !fwd {
			pts = []geo.Pt{pts[1], pts[0]}
			from, to = to, from
		}
		net.Edges = append(net.Edges, Edge{From: from, To: to, Pts: pts,
			Routes: []string{"A", "B"}})
	}
	rebuildAdj(net)
	return net
}

var testRoutes = map[string]gtfs.Route{
	"A": {ID: "A", Color: "0000FF"},
	"B": {ID: "B", Color: "FF6600"},
}

// A straight chain must keep one consistent color order in the travel
// frame, whatever the storage orientations of its edges.
func TestOrderHoldsAlongChain(t *testing.T) {
	for _, orients := range [][]bool{
		{true, true, true},
		{true, false, true},
		{false, true, false},
		{true, true, false},
		{false, false, true},
	} {
		net := chainNet(orients)
		slots, err := Order(net, testRoutes)
		if err != nil {
			t.Fatal(err)
		}
		// travel order along +X: storage order if edge stored forward,
		// reversed otherwise
		var travel [][]string
		for ei := range net.Edges {
			p := append([]string(nil), slots[ei]...)
			if !orients[ei] {
				p[0], p[1] = p[1], p[0]
			}
			travel = append(travel, p)
		}
		for i := 1; i < len(travel); i++ {
			if travel[i][0] != travel[0][0] {
				t.Errorf("orients %v: travel order flips at edge %d: %v",
					orients, i, travel)
				break
			}
		}
	}
}

// A branch that ends at a terminus must inherit the trunk's travel-frame
// order at the fork — the sibling-branch relation must never outvote
// trunk continuity (the Nostrand 2·3/4·5 flip).
func TestOrderBranchInheritsTrunk(t *testing.T) {
	for _, branchRev := range []bool{false, true} {
		net := &Network{}
		net.Nodes = []Node{
			{At: geo.Pt{X: -200}}, {At: geo.Pt{}},
			{At: geo.Pt{X: 200, Y: 20}}, {At: geo.Pt{X: 60, Y: -200}},
		}
		trunk := Edge{From: 0, To: 1,
			Pts: []geo.Pt{{X: -200}, {}}, Routes: []string{"A", "B"}}
		east := Edge{From: 1, To: 2,
			Pts: []geo.Pt{{}, {X: 200, Y: 20}}, Routes: []string{"A", "B"}}
		south := Edge{From: 1, To: 3,
			Pts: []geo.Pt{{}, {X: 40, Y: -120}, {X: 60, Y: -200}}, Routes: []string{"A", "B"}}
		if branchRev {
			south.From, south.To = south.To, south.From
			south.Pts = []geo.Pt{{X: 60, Y: -200}, {X: 40, Y: -120}, {}}
		}
		net.Edges = []Edge{trunk, east, south}
		rebuildAdj(net)
		slots, err := Order(net, testRoutes)
		if err != nil {
			t.Fatal(err)
		}
		// travel frame along trunk→south: trunk ridden storage-forward;
		// south ridden forward iff !branchRev
		trunkOrder := slots[0]
		southOrder := append([]string(nil), slots[2]...)
		if branchRev {
			southOrder[0], southOrder[1] = southOrder[1], southOrder[0]
		}
		if trunkOrder[0] != southOrder[0] {
			t.Errorf("branchRev=%v: south branch flips vs trunk: trunk=%v south(travel)=%v east=%v",
				branchRev, trunkOrder, southOrder, slots[1])
		}
	}
}
