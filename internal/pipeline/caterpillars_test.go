package pipeline

import (
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// catLine is a straight east-west polyline in frame metres at height y,
// with a vertex every step so truncated twins hash differently.
func catLine(x0, x1, y, step float64) *geo.Line {
	var pts []geo.Pt
	for x := x0; x <= x1; x += step {
		pts = append(pts, geo.Pt{X: x, Y: y})
	}
	return geo.NewLine(pts)
}

// The merge that fuses one corridor's extent-mismatched chains must not
// reach the next street over. At Franklin Av the LIRR's six-branch roster
// grew the merge radius past the 150 m to Fulton St and its chain
// swallowed the A/C whole — the bullets rode the Atlantic Branch. The
// donor's chain is CONSUMED by a merge, so the failure is total: the
// route's bullets appear only on the wrong line.
func TestCaterpillarMergeStaysOnItsCorridor(t *testing.T) {
	frame := geo.NewFrame(geo.LL{Lat: 40.68, Lon: -73.95})

	routes := map[string]gtfs.Route{
		"A": {ID: "A", ShortName: "A", Agency: "MTA"},
		"C": {ID: "C", ShortName: "C", Agency: "MTA"},
		"J": {ID: "J", ShortName: "J", Agency: "MTA"},
		"M": {ID: "M", ShortName: "M", Agency: "MTA"},
	}
	branches := []string{"Babylon", "Hempstead", "West Hempstead", "Far Rockaway", "Long Beach", "Oyster Bay"}
	var lirr []string
	for _, b := range branches {
		routes[b] = gtfs.Route{ID: b, LongName: b, Agency: "LI"}
	}
	for _, b := range branches {
		lirr = append(lirr, b)
	}

	// Fulton St analog: a sibling pair on one exact geometry at y=0.
	fulton := catLine(0, 3000, 0, 25)
	// Atlantic Branch analog 150 m away — six word-label routes, so under
	// the old point-radius rule its mergeR was 60+6*20=180 m. It sorts
	// first (smaller Y), so it hosted.
	atlantic := catLine(0, 3000, -150, 25)
	// Same-corridor extent mismatch far away at y=600: twin centerlines
	// whose cuts differ by 30 m. These SHOULD still fuse into one chain.
	jLine := catLine(500, 2500, 600, 10)
	mLine := catLine(530, 2500, 600, 10)

	segs := []stages.Segment{
		{Kind: "steady", Mode: "subway", BandMin: 15, Routes: []string{"A"}, OffsetPx: -2, Line: fulton},
		{Kind: "steady", Mode: "subway", BandMin: 15, Routes: []string{"C"}, OffsetPx: 2, Line: fulton},
		{Kind: "steady", Mode: "regional", BandMin: 15, Routes: lirr, OffsetPx: 0, Line: atlantic},
		{Kind: "steady", Mode: "subway", BandMin: 15, Routes: []string{"J"}, OffsetPx: -2, Line: jLine},
		{Kind: "steady", Mode: "subway", BandMin: 15, Routes: []string{"M"}, OffsetPx: 2, Line: mLine},
	}

	out := BuildCaterpillars(segs, nil, routes, frame)

	groupsOf := map[string]map[int]bool{}
	for _, b := range out {
		if groupsOf[b.Route] == nil {
			groupsOf[b.Route] = map[int]bool{}
		}
		groupsOf[b.Route][b.Group] = true
		var wantY float64
		switch b.Route {
		case "A", "C":
			wantY = 0
		case "J", "M":
			wantY = 600
		default:
			wantY = -150
		}
		if y := frame.ToXY(b.LL).Y; math.Abs(y-wantY) > 10 {
			t.Errorf("route %s anchored at y=%.0f, its corridor is y=%.0f — riding another line", b.Route, y, wantY)
		}
	}
	for _, r := range []string{"A", "C", "J", "M"} {
		if len(groupsOf[r]) == 0 {
			t.Errorf("route %s placed no bullets at all", r)
		}
	}
	// The J and M are one corridor cut differently: some anchor must have
	// fused them, or the cross-track gate is too tight and the clump the
	// merge exists to fix is back.
	fused := false
	for g := range groupsOf["J"] {
		if groupsOf["M"][g] {
			fused = true
			break
		}
	}
	if !fused {
		t.Errorf("J and M share a corridor but no chain carries both — same-corridor merge broke")
	}
}
