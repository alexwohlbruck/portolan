package yards

import (
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// The centerline contract, checked on a ladder with a through track and a
// branch: every entrance owns a node and a centerline, every centerline
// reaches an entrance, the whole skeleton rides real steel, and nothing
// doubles back.
func TestCenterlineRules(t *testing.T) {
	tracks := ladder(8, 4, 4, 100, 700, "yard")
	tracks = append(tracks,
		Track{ID: "through", Line: straight(16, -300, 1100)},
		Track{ID: "branch", Line: geo.NewLine([]geo.Pt{
			{X: 1100, Y: 40}, {X: 700, Y: 30}, {X: 400, Y: 20}, {X: 100, Y: 16},
		})})
	ix := Build(tracks, DefaultParams())
	if len(ix.Regions()) != 1 {
		t.Fatalf("regions = %d, want 1", len(ix.Regions()))
	}
	r := ix.Regions()[0]
	if len(r.Entrances) < 2 {
		t.Fatalf("entrances = %d, want >= 2", len(r.Entrances))
	}
	if len(r.Steel) == 0 {
		t.Error("region carries no in-yard track geometry")
	}

	// 1 + 4: every entrance is a skeleton node, and owns a centerline.
	nodeOf := map[int]int{}
	for ni, n := range r.SkelNodes {
		if n.Entrance >= 0 {
			if prev, dup := nodeOf[n.Entrance]; dup {
				t.Errorf("entrance %d has two nodes (%d, %d)", n.Entrance, prev, ni)
			}
			nodeOf[n.Entrance] = ni
			if n.Pt != r.Entrances[n.Entrance].Pt {
				t.Errorf("entrance %d node at %v, entrance at %v", n.Entrance, n.Pt, r.Entrances[n.Entrance].Pt)
			}
		}
	}
	deg := map[int]int{}
	for _, e := range r.Skel {
		deg[e.A]++
		deg[e.B]++
	}
	for ei := range r.Entrances {
		ni, ok := nodeOf[ei]
		if !ok {
			t.Errorf("entrance %d has no skeleton node", ei)
			continue
		}
		if deg[ni] == 0 {
			t.Errorf("entrance %d has no centerline", ei)
		}
	}

	// 3: every centerline is connected to some entrance.
	adj := map[int][]int{}
	for i, e := range r.Skel {
		adj[e.A] = append(adj[e.A], i)
		adj[e.B] = append(adj[e.B], i)
	}
	seenN := map[int]bool{}
	var stack []int
	for _, ni := range nodeOf {
		if !seenN[ni] {
			seenN[ni] = true
			stack = append(stack, ni)
		}
	}
	seenE := map[int]bool{}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, ei := range adj[n] {
			seenE[ei] = true
			nx := r.Skel[ei].A + r.Skel[ei].B - n
			if !seenN[nx] {
				seenN[nx] = true
				stack = append(stack, nx)
			}
		}
	}
	for i := range r.Skel {
		if !seenE[i] {
			t.Errorf("skeleton edge %d floats free of every entrance", i)
		}
	}

	// 5: the interior of every run rides real steel — no track jumping.
	// (The last metres at an entrance legitimately converge from the rail
	// onto the bundle's averaged centerpoint.)
	for i, e := range r.Skel {
		l := e.Line
		for s := 0.0; s <= l.Len(); s += 10 {
			p := l.AtArc(s)
			if math.Min(s, l.Len()-s) < 40 {
				continue
			}
			near := math.Inf(1)
			for _, st := range r.Steel {
				if d, ok := st.DistToCapped(p, 30); ok && d < near {
					near = d
				}
			}
			if near > 12 {
				t.Fatalf("skeleton edge %d strays %.0f m from any track at arc %.0f", i, near, s)
			}
		}
	}

	// 6: flowing geometry — no switchback kinks along a run.
	for i, e := range r.Skel {
		if turn := geo.MaxTurnDeg(e.Line.Resample(12)); turn > 45 {
			t.Errorf("skeleton edge %d kinks %.0f deg", i, turn)
		}
	}
}

// A tagged ladder with a through track running past both throats: the
// through track pierces the outline twice — two entrances — and the spine
// between them must ride it, ending bit-equal on the entrance points.
func TestEntrancesAndSpines(t *testing.T) {
	tracks := append(ladder(6, 4, 4, 100, 500, "yard"),
		Track{ID: "main", Line: straight(0, -300, 800)})
	ix := Build(tracks, DefaultParams())
	if len(ix.Regions()) != 1 {
		t.Fatalf("regions = %d, want 1", len(ix.Regions()))
	}
	r := ix.Regions()[0]
	if len(r.Entrances) != 2 {
		t.Fatalf("entrances = %d, want 2 (through track in and out)", len(r.Entrances))
	}
	var west, east *Entrance
	for i := range r.Entrances {
		if r.Entrances[i].Pt.X < 300 {
			west = &r.Entrances[i]
		} else {
			east = &r.Entrances[i]
		}
	}
	if west == nil || east == nil {
		t.Fatalf("entrances not on both throats: %+v", r.Entrances)
	}
	if west.Heading.X >= 0 {
		t.Errorf("west entrance heading %v does not point out", west.Heading)
	}
	if east.Heading.X <= 0 {
		t.Errorf("east entrance heading %v does not point out", east.Heading)
	}
	if len(west.WayIDs) != 1 || west.WayIDs[0] != "main" {
		t.Errorf("west entrance ways = %v, want [main]", west.WayIDs)
	}

	if len(r.Spines) < 1 {
		t.Fatal("no spine between the two entrances")
	}
	sp := r.Spines[0]
	a, b := r.Entrances[sp.From].Pt, r.Entrances[sp.To].Pt
	pts := sp.Line.Pts
	if pts[0] != a || pts[len(pts)-1] != b {
		t.Fatalf("spine endpoints %v..%v not bit-equal to entrances %v..%v",
			pts[0], pts[len(pts)-1], a, b)
	}
	// The interior must stay inside the yard.
	for s := 10.0; s < sp.Line.Len()-10; s += 15 {
		if p := sp.Line.AtArc(s); !ix.InYard(p) {
			t.Fatalf("spine interior point %v leaves the yard", p)
		}
	}
}
