package yards

import "testing"

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
