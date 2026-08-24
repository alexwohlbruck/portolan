package yards

import "testing"

// A tagged ladder with a through track running past both throats: the
// through track pierces the outline twice — two entrances — and the
// ladder's own two ends are terminal bundles.
func TestEntrances(t *testing.T) {
	tracks := append(ladder(6, 4, 4, 100, 500, "yard"),
		Track{ID: "main", Line: straight(0, -300, 800)})
	ix := Build(tracks, DefaultParams())
	if len(ix.Regions()) != 1 {
		t.Fatalf("regions = %d, want 1", len(ix.Regions()))
	}
	r := ix.Regions()[0]
	// two outline crossings (the through track in and out) plus two
	// terminal bundles (the ladder's own ends, west and east)
	var west, east *Entrance
	terms := 0
	for i := range r.Entrances {
		e := &r.Entrances[i]
		if e.Terminal {
			terms++
			continue
		}
		if e.Pt.X < 300 {
			west = e
		} else {
			east = e
		}
	}
	if terms != 2 {
		t.Errorf("terminal entrances = %d, want 2 (the ladder's two ends)", terms)
	}
	for i := range r.Entrances {
		if e := &r.Entrances[i]; e.Terminal {
			// the bundle end is the AVERAGE of its tracks' ends, so it
			// sits inside the ladder's width, not on one rail
			if e.Pt.Y < 4 || e.Pt.Y > 4+5*4 {
				t.Errorf("terminal entrance at %v is not inside the bundle", e.Pt)
			}
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
}
