package stages

import (
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

// The M problem in miniature: one 2 km segment, a full-length pattern
// riding all of it and a short-turn pattern terminating 700 m in. The
// segment must cut at the terminal, the near piece carrying both
// patterns' hours and the far piece only the full pattern's. A pattern
// that merely tip-touches the segment's end must not light the rest.
func TestCutSegmentsAtTerminals(t *testing.T) {
	line := func(x0, x1 float64) *geo.Line {
		var pts []geo.Pt
		for x := x0; x <= x1; x += 50 {
			pts = append(pts, geo.Pt{X: x, Y: 0})
		}
		return geo.NewLine(pts)
	}
	full := gtfs.Pattern{Route: gtfs.Route{ID: "M"}, ShapeID: "full"}
	short := gtfs.Pattern{Route: gtfs.Route{ID: "M"}, ShapeID: "short"}
	tip := gtfs.Pattern{Route: gtfs.Route{ID: "M"}, ShapeID: "tip"}

	var day, night, wkd gtfs.Mask168
	day = day.Set(0, 9)     // full: Monday 9am
	night = night.Set(0, 3) // tip: Monday 3am
	wkd = wkd.Set(5, 14)    // short: Saturday 2pm
	SetPatternActs(map[string]gtfs.Mask168{
		"M\x1ffull":  day,
		"M\x1fshort": wkd,
		"M\x1ftip":   night,
	})
	defer SetPatternActs(nil)

	seg := Segment{
		Kind: "steady", Routes: []string{"M"},
		Acts: []string{day.Or(wkd).Or(night).Hex()},
		Line: line(0, 2000),
	}
	paths := []Path{
		{Pattern: full, Line: line(-500, 2500)}, // covers everything
		{Pattern: short, Line: line(-500, 700)}, // terminates 700 m in
		{Pattern: tip, Line: line(1950, 2500)},  // tip-touches the far end
	}
	out := CutSegmentsAtTerminals([]Segment{seg}, paths, nil)
	if len(out) != 2 {
		t.Fatalf("want 2 pieces, got %d: %+v", len(out), out)
	}
	near, far := out[0], out[1]
	if math.Abs(near.Line.Len()-700) > 60 || math.Abs(far.Line.Len()-1300) > 60 {
		t.Fatalf("cut in the wrong place: %.0f + %.0f", near.Line.Len(), far.Line.Len())
	}
	if near.Acts[0] != day.Or(wkd).Hex() {
		t.Fatalf("near piece should carry full+short hours: %s", near.Acts[0])
	}
	// the far piece keeps the full pattern's hours; the tip-toucher's
	// night hours must NOT leak onto it (its coverage misses the middle)
	if far.Acts[0] != day.Hex() {
		t.Fatalf("far piece should carry ONLY the full pattern: %s want %s", far.Acts[0], day.Hex())
	}
}

// A pattern with no mask makes its route untouchable: original acts
// survive and no cut is invented from it.
func TestCutsKeepUnmaskedRoutesIntact(t *testing.T) {
	line := func(x0, x1 float64) *geo.Line {
		var pts []geo.Pt
		for x := x0; x <= x1; x += 50 {
			pts = append(pts, geo.Pt{X: x, Y: 0})
		}
		return geo.NewLine(pts)
	}
	SetPatternActs(map[string]gtfs.Mask168{}) // nothing has a mask
	defer SetPatternActs(nil)
	seg := Segment{Kind: "steady", Routes: []string{"X"},
		Acts: []string{"deadbeef"}, Line: line(0, 2000)}
	paths := []Path{{Pattern: gtfs.Pattern{Route: gtfs.Route{ID: "X"}, ShapeID: "s"}, Line: line(-100, 900)}}
	out := CutSegmentsAtTerminals([]Segment{seg}, paths, nil)
	if len(out) != 1 || out[0].Acts[0] != "deadbeef" {
		t.Fatalf("unmasked route must pass through untouched: %+v", out)
	}
}

// Parallel ribbons of one corridor must cut at the SAME arcs or the
// client's geometry-hash bundling desyncs: the J stays whole while the
// M splits, and re-centering + markers lose the pair.
func TestCutsSynchronizeAcrossRibbons(t *testing.T) {
	line := func(x0, x1 float64) *geo.Line {
		var pts []geo.Pt
		for x := x0; x <= x1; x += 50 {
			pts = append(pts, geo.Pt{X: x, Y: 0})
		}
		return geo.NewLine(pts)
	}
	mFull := gtfs.Pattern{Route: gtfs.Route{ID: "M"}, ShapeID: "full"}
	mShort := gtfs.Pattern{Route: gtfs.Route{ID: "M"}, ShapeID: "short"}
	jFull := gtfs.Pattern{Route: gtfs.Route{ID: "J"}, ShapeID: "jf"}
	var day, wkd, all gtfs.Mask168
	day = day.Set(0, 9)
	wkd = wkd.Set(5, 14)
	all = day.Or(wkd)
	SetPatternActs(map[string]gtfs.Mask168{"M\x1ffull": day, "M\x1fshort": wkd, "J\x1fjf": all})
	defer SetPatternActs(nil)
	// the J and M ribbons share EXACT geometry (one corridor)
	jSeg := Segment{Kind: "steady", Routes: []string{"J"}, Acts: []string{all.Hex()}, Line: line(0, 2000)}
	mSeg := Segment{Kind: "steady", Routes: []string{"M"}, Acts: []string{day.Or(wkd).Hex()}, Line: line(0, 2000)}
	paths := []Path{
		{Pattern: mFull, Line: line(-500, 2500)},
		{Pattern: mShort, Line: line(-500, 700)},
		{Pattern: jFull, Line: line(-500, 2500)},
	}
	out := CutSegmentsAtTerminals([]Segment{jSeg, mSeg}, paths, nil)
	if len(out) != 4 {
		t.Fatalf("both ribbons must split identically: want 4 pieces, got %d", len(out))
	}
	// piece geometries pair up across the two ribbons
	if math.Abs(out[0].Line.Len()-out[2].Line.Len()) > 1 ||
		math.Abs(out[1].Line.Len()-out[3].Line.Len()) > 1 {
		t.Fatalf("piece extents differ across ribbons: %v %v vs %v %v",
			out[0].Line.Len(), out[1].Line.Len(), out[2].Line.Len(), out[3].Line.Len())
	}
	// the J's hours are the same on both its pieces
	if out[0].Acts[0] != all.Hex() || out[1].Acts[0] != all.Hex() {
		t.Fatalf("J acts changed: %v / %v", out[0].Acts, out[1].Acts)
	}
}

// A tail beyond the terminal stop that NO pattern rides is KEPT but
// carries zero hours: at real terminals the "overshoot" is often the
// platforms themselves, so the geometry stays (the terminus clamp caps
// its tip) while any timestamp renders it dark.
func TestRelayTailGoesDark(t *testing.T) {
	line := func(x0, x1 float64) *geo.Line {
		var pts []geo.Pt
		for x := x0; x <= x1; x += 50 {
			pts = append(pts, geo.Pt{X: x, Y: 0})
		}
		return geo.NewLine(pts)
	}
	pat := gtfs.Pattern{Route: gtfs.Route{ID: "L"}, ShapeID: "s"}
	var day gtfs.Mask168
	day = day.Set(0, 9)
	SetPatternActs(map[string]gtfs.Mask168{"L\x1fs": day})
	defer SetPatternActs(nil)
	seg := Segment{Kind: "steady", Routes: []string{"L"},
		Acts: []string{day.Hex()}, Line: line(0, 2000)}
	// the path runs the whole segment (MATCH appends terminal pieces
	// whole) but the terminal STOP is at 1600 — the last 400 m is tail
	paths := []Path{{Pattern: pat, Line: line(-500, 2000)}}
	terms := [][2]geo.Pt{{{X: -500, Y: 0}, {X: 1600, Y: 0}}}
	out := CutSegmentsAtTerminals([]Segment{seg}, paths, terms)
	if len(out) != 2 {
		t.Fatalf("want 2 pieces (tail kept), got %d", len(out))
	}
	if math.Abs(out[0].Line.Len()-1600) > 30 {
		t.Fatalf("service piece should end at the STOP: len %.0f", out[0].Line.Len())
	}
	if out[0].Acts[0] != day.Hex() {
		t.Fatalf("service piece acts: %s", out[0].Acts[0])
	}
	if m, ok := gtfs.ParseMask168(out[1].Acts[0]); !ok || !m.Empty() {
		t.Fatalf("tail must carry ZERO hours: %s", out[1].Acts[0])
	}
}

// The Essex St case: the short pattern's SHAPE overruns its terminal by
// 300 m of tail trackage. With the terminal stop location supplied, the
// cut happens at the STOP — the drawn tail ends at the station.
func TestCutsPullBackToTerminalStop(t *testing.T) {
	line := func(x0, x1 float64) *geo.Line {
		var pts []geo.Pt
		for x := x0; x <= x1; x += 50 {
			pts = append(pts, geo.Pt{X: x, Y: 0})
		}
		return geo.NewLine(pts)
	}
	full := gtfs.Pattern{Route: gtfs.Route{ID: "M"}, ShapeID: "full"}
	short := gtfs.Pattern{Route: gtfs.Route{ID: "M"}, ShapeID: "short"}
	var day, wkd gtfs.Mask168
	day = day.Set(0, 9)
	wkd = wkd.Set(5, 14)
	SetPatternActs(map[string]gtfs.Mask168{"M\x1ffull": day, "M\x1fshort": wkd})
	defer SetPatternActs(nil)
	seg := Segment{Kind: "steady", Routes: []string{"M"},
		Acts: []string{day.Or(wkd).Hex()}, Line: line(0, 2000)}
	paths := []Path{
		{Pattern: full, Line: line(-500, 2500)},
		{Pattern: short, Line: line(-500, 1000)}, // tip overshoots the stop
	}
	terms := [][2]geo.Pt{
		{},                                // full: unknown ends (far away anyway)
		{{X: -500, Y: 0}, {X: 700, Y: 0}}, // short: terminal STOP at x=700
	}
	out := CutSegmentsAtTerminals([]Segment{seg}, paths, terms)
	if len(out) != 2 {
		t.Fatalf("want 2 pieces, got %d", len(out))
	}
	if math.Abs(out[0].Line.Len()-700) > 30 {
		t.Fatalf("cut should sit at the STOP (700), got %.0f", out[0].Line.Len())
	}
	if out[0].Acts[0] != day.Or(wkd).Hex() || out[1].Acts[0] != day.Hex() {
		t.Fatalf("acts wrong: near=%s far=%s", out[0].Acts[0], out[1].Acts[0])
	}
}

// The L problem in miniature: a segment whose 24/7 hours come from a
// pattern that no longer rides its centerline — the geometry drifted,
// which is what --allow-unmatched and a merged rail extract can do in a
// group build. Recomputing from what is left would replace a railway that
// runs all night with the hours of a limited-service pattern.
//
// The same feed built alone got this right, so the guard has to be
// explicit: hours nothing on this segment can explain mean the segment is
// not reconstructible, and the original stands.
func TestCutsKeepHoursNoPatternCanExplain(t *testing.T) {
	line := func(x0, x1, y float64) *geo.Line {
		var pts []geo.Pt
		for x := x0; x <= x1; x += 50 {
			pts = append(pts, geo.Pt{X: x, Y: y})
		}
		return geo.NewLine(pts)
	}
	allDay := gtfs.Pattern{Route: gtfs.Route{ID: "L"}, ShapeID: "allday"}
	rush := gtfs.Pattern{Route: gtfs.Route{ID: "L"}, ShapeID: "rush"}

	var always, peak gtfs.Mask168
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			always = always.Set(d, h)
		}
		peak = peak.Set(d, 8)
	}
	SetPatternActs(map[string]gtfs.Mask168{
		"L\x1fallday": always,
		"L\x1frush":   peak,
	})
	defer SetPatternActs(nil)

	seg := Segment{
		Kind: "steady", Routes: []string{"L"},
		Acts: []string{always.Hex()}, // SPLIT ORed both onto these edges
		Line: line(0, 2000, 0),
	}
	paths := []Path{
		// the all-day pattern drifted 300 m off the drawn centerline: no
		// endpoint inside, and it rides nowhere near it
		{Pattern: allDay, Line: line(-500, 2500, 300)},
		// the rush pattern short-turns 700 m in and does ride the line
		{Pattern: rush, Line: line(-500, 700, 0)},
	}
	out := CutSegmentsAtTerminals([]Segment{seg}, paths, nil)
	for _, sg := range out {
		if sg.Acts[0] != always.Hex() {
			t.Fatalf("a 24/7 railway lost its hours to a rush-only pattern: %s", sg.Acts[0])
		}
	}

	// and once that pattern is back ON the centerline, refinement runs
	// again: the far piece keeps only the all-day hours
	paths[0].Line = line(-500, 2500, 0)
	out = CutSegmentsAtTerminals([]Segment{seg}, paths, nil)
	if len(out) != 2 {
		t.Fatalf("want 2 pieces once reconstructible, got %d", len(out))
	}
	if out[0].Acts[0] != always.Or(peak).Hex() || out[1].Acts[0] != always.Hex() {
		t.Fatalf("near=%s far=%s", out[0].Acts[0], out[1].Acts[0])
	}
}
