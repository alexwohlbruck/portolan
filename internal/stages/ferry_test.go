package stages

import (
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

func lane(id string, pts ...geo.Pt) bundle.Track {
	return bundle.Track{ID: id, Line: geo.NewLine(pts)}
}

// Three piers, A → B → C. Lane 1 runs A to B and bends north of the
// shape's straight chord; lane 2 runs B to C but starts 30 m from where
// lane 1 ends, as OSM often leaves two lanes into one terminal. Lane 3 is a
// decoy from A to C that the boat never takes.
func ferryFixture(t *testing.T) (Path, geo.Frame) {
	t.Helper()
	frame := geo.NewFrame(geo.LL{Lon: -74, Lat: 40.7})
	tracks := []bundle.Track{
		lane("way/1", geo.Pt{X: 0, Y: 20}, geo.Pt{X: 1500, Y: 600}, geo.Pt{X: 3000, Y: 20}),
		lane("way/2", geo.Pt{X: 3000, Y: -10}, geo.Pt{X: 4500, Y: -400}, geo.Pt{X: 6000, Y: 15}),
		lane("way/3", geo.Pt{X: 0, Y: -20}, geo.Pt{X: 3000, Y: -1500}, geo.Pt{X: 6000, Y: -20}),
	}
	cls := map[string]string{"way/1": "seaway", "way/2": "seaway", "way/3": "seaway"}
	SetWayRailClass(cls)
	tracks = WeldSeaways(tracks, cls)

	piers := map[string]geo.Pt{"A": {X: 0, Y: 0}, "B": {X: 3000, Y: 0}, "C": {X: 6000, Y: 0}}
	stops := map[string]geo.LL{}
	for id, p := range piers {
		stops[id] = frame.ToLL(p)
	}
	SetStopCoords(stops)
	t.Cleanup(func() { SetStopCoords(nil); SetWayRailClass(nil) })

	chord := []geo.LL{frame.ToLL(piers["A"]), frame.ToLL(piers["B"]), frame.ToLL(piers["C"])}
	pat := gtfs.Pattern{
		Route:   gtfs.Route{ID: "F", Type: 4},
		ShapeID: "f1", Trips: 10, Shape: chord,
		StopSeq: []string{"A", "B", "C"},
	}
	paths, err := Match([]gtfs.Pattern{pat}, tracks, frame)
	if err != nil || len(paths) != 1 {
		t.Fatalf("match: %v, %d paths", err, len(paths))
	}
	return paths[0], frame
}

func TestFerryRidesTheLanesPierToPier(t *testing.T) {
	p, _ := ferryFixture(t)
	for _, st := range p.Steps {
		if st.Piece < 0 {
			t.Fatalf("ferry chorded a leg the lanes cover: %v", p.WayIDs)
		}
	}
	want := map[string]bool{"way/1": true, "way/2": true}
	for _, w := range p.WayIDs {
		if !want[wayOf(w)] {
			t.Errorf("rode %s, a lane between piers the boat does not sail", w)
		}
	}
}

func TestFerryRunsPierToPier(t *testing.T) {
	p, _ := ferryFixture(t)
	if math.Abs(p.Line.Pts[0].Y) > 30 || math.Abs(p.Line.Pts[len(p.Line.Pts)-1].X-6000) > 30 {
		t.Errorf("path runs %v → %v, not pier A to pier C", p.Line.Pts[0], p.Line.Pts[len(p.Line.Pts)-1])
	}
}

func wayOf(token string) string {
	for i := range token {
		if token[i] == '#' || token[i] == '@' {
			return token[:i]
		}
	}
	return token
}

func TestWeldKeepsLaneGeometry(t *testing.T) {
	a := lane("way/a", geo.Pt{X: 0, Y: 0}, geo.Pt{X: 1000, Y: 0})
	b := lane("way/b", geo.Pt{X: 500, Y: 40}, geo.Pt{X: 500, Y: 1000})
	out := WeldSeaways([]bundle.Track{a, b}, map[string]string{"way/a": "seaway", "way/b": "seaway"})
	if got := out[1].Line.Pts[0]; got != (geo.Pt{X: 500, Y: 0}) {
		t.Errorf("branch should gain a connector onto the trunk, starts at %v", got)
	}
	if got := out[1].Line.Pts[1]; got != (geo.Pt{X: 500, Y: 40}) {
		t.Errorf("branch's own geometry moved: %v", got)
	}
	if len(out[0].Line.Pts) != 3 || out[0].Line.Pts[1] != (geo.Pt{X: 500, Y: 0}) {
		t.Errorf("trunk needs a vertex where the branch joins: %v", out[0].Line.Pts)
	}
}

func TestWeldLeavesOtherLayersAlone(t *testing.T) {
	a := lane("way/a", geo.Pt{X: 0, Y: 0}, geo.Pt{X: 1000, Y: 0})
	b := lane("way/b", geo.Pt{X: 500, Y: 40}, geo.Pt{X: 500, Y: 1000})
	out := WeldSeaways([]bundle.Track{a, b}, map[string]string{"way/a": "rail", "way/b": "rail"})
	if len(out[1].Line.Pts) != 2 || len(out[0].Line.Pts) != 2 {
		t.Error("rail was welded")
	}
}
