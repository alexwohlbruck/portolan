package stages

import (
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

// The Gold Runner problem in miniature: one straight 20 km track and
// three patterns. Route GR has an honest decimated shape (five vertices,
// all ON the steel) and a road-traced reverse twin running 2 km to the
// side with five times the service. Route X has only the road tracing.
// The liar with an honest sibling must ride the sibling's matched steel —
// even though its own service weight would have matched it first — and
// the liar without one must keep today's behavior and bridge its own
// shape, because a lying shape with no honest sibling is still the only
// geometry that exists. The decimated shape must not be flagged: its
// vertices sit on the track, and chords are the confidence machinery's
// business, not distrust's.
func TestMatchDistrustsOffNetworkShapes(t *testing.T) {
	frame := geo.NewFrame(geo.LL{})
	var steel []geo.Pt
	for x := 0.0; x <= 20000; x += 100 {
		steel = append(steel, geo.Pt{X: x, Y: 0})
	}
	tracks := []bundle.Track{{ID: "way/steel", Line: geo.NewLine(steel)}}

	lls := func(step, y float64, rev bool) []geo.LL {
		var out []geo.LL
		for x := 0.0; x <= 20000; x += step {
			out = append(out, frame.ToLL(geo.Pt{X: x, Y: y}))
		}
		if rev {
			for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
				out[i], out[j] = out[j], out[i]
			}
		}
		return out
	}
	// terminals sit beside the steel, like platforms do
	termA := frame.ToLL(geo.Pt{X: 0, Y: 40})
	termB := frame.ToLL(geo.Pt{X: 20000, Y: 40})

	honest := gtfs.Pattern{
		Route: gtfs.Route{ID: "GR", Type: 2}, ShapeID: "honest", Trips: 2,
		Shape: lls(5000, 0, false),
		TermA: termA, TermB: termB, TermAID: "A", TermBID: "B",
	}
	liar := gtfs.Pattern{
		Route: gtfs.Route{ID: "GR", Type: 2}, ShapeID: "road", Trips: 10,
		Shape: lls(100, 2000, true),
		TermA: termB, TermB: termA, TermAID: "B", TermBID: "A",
	}
	orphan := gtfs.Pattern{
		Route: gtfs.Route{ID: "X", Type: 2}, ShapeID: "orphan", Trips: 3,
		Shape: lls(100, 2000, false),
		TermA: termA, TermB: termB, TermAID: "A", TermBID: "B",
	}

	paths, err := Match([]gtfs.Pattern{honest, liar, orphan}, tracks, frame)
	if err != nil {
		t.Fatal(err)
	}
	byShape := map[string]Path{}
	for _, p := range paths {
		byShape[p.Pattern.ShapeID] = p
	}
	if len(byShape) != 3 {
		t.Fatalf("want 3 paths, got %d", len(byShape))
	}
	gapless := func(p Path) bool {
		for _, w := range p.WayIDs {
			if w == "gap" {
				return false
			}
		}
		return true
	}
	maxY := func(p Path) float64 {
		worst := 0.0
		for _, q := range p.Line.Pts {
			worst = math.Max(worst, math.Abs(q.Y))
		}
		return worst
	}

	h := byShape["honest"]
	if h.Guide != "" || !gapless(h) || maxY(h) > 50 {
		t.Fatalf("honest decimated shape must match its own steel ungapped: guide=%q maxY=%.0f", h.Guide, maxY(h))
	}
	l := byShape["road"]
	if l.Guide != "honest" {
		t.Fatalf("road tracing with an honest sibling must borrow its path, got guide=%q", l.Guide)
	}
	if !gapless(l) || maxY(l) > 50 {
		t.Fatalf("guided pattern must ride the steel, not bridge the road: maxY=%.0f wayIDs=%v", maxY(l), l.WayIDs)
	}
	o := byShape["orphan"]
	if o.Guide != "" || gapless(o) {
		t.Fatalf("orphan road tracing has no donor and must keep bridging: guide=%q wayIDs=%v", o.Guide, o.WayIDs)
	}
	if maxY(o) < 1500 {
		t.Fatalf("orphan bridge should carry its own shape geometry, maxY=%.0f", maxY(o))
	}
}
