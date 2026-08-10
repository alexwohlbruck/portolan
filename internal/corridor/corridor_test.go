package corridor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

// testFrame anchors every fixture at the same place, so metres in these
// tests are metres and the numbers below are readable.
var testFrame = geo.NewFrame(geo.LL{Lon: -74, Lat: 40.7})

func at(x, y float64) geo.LL { return testFrame.ToLL(geo.Pt{X: x, Y: y}) }

// fc assembles a FeatureCollection the way a caller would.
type fc struct{ feats []string }

func (f *fc) node(id string, x, y float64) *fc {
	ll := at(x, y)
	f.feats = append(f.feats, fmt.Sprintf(
		`{"type":"Feature","properties":{"node":%q},"geometry":{"type":"Point","coordinates":[%.10f,%.10f]}}`,
		id, ll.Lon, ll.Lat))
	return f
}

func (f *fc) edge(id, from, to, routes string, pts ...[2]float64) *fc {
	var cs []string
	for _, p := range pts {
		ll := at(p[0], p[1])
		cs = append(cs, fmt.Sprintf("[%.10f,%.10f]", ll.Lon, ll.Lat))
	}
	props := fmt.Sprintf(`"edge":%q,"routes":%q`, id, routes)
	if from != "" {
		props += fmt.Sprintf(`,"from":%q,"to":%q`, from, to)
	}
	f.feats = append(f.feats, fmt.Sprintf(
		`{"type":"Feature","properties":{%s},"geometry":{"type":"LineString","coordinates":[%s]}}`,
		props, strings.Join(cs, ",")))
	return f
}

func (f *fc) String() string {
	return `{"type":"FeatureCollection","features":[` + strings.Join(f.feats, ",") + `]}`
}

func (f *fc) load(t *testing.T) *Graph {
	t.Helper()
	g, err := LoadReader(strings.NewReader(f.String()), "test")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return g
}

func quiet(string, ...any) {}

func TestExplicitTopologyJoinsByID(t *testing.T) {
	// two corridors meeting at b — the endpoints are 3 m apart, well
	// outside the snap tolerance, so only the explicit ids can join them
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).node("c", 200, 0).
		edge("e0", "a", "b", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		edge("e1", "b", "c", "R1", [2]float64{103, 0}, [2]float64{200, 0}).
		load(t)

	net, err := g.Network(testFrame)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	if len(net.Nodes) != 3 || len(net.Edges) != 2 {
		t.Fatalf("got %d nodes / %d edges, want 3/2", len(net.Nodes), len(net.Edges))
	}
	if g.Synthesized != 0 {
		t.Errorf("explicit ids should synthesize nothing, got %d", g.Synthesized)
	}
	if got := len(net.Nodes[1].Adj); got != 2 {
		t.Errorf("node b has degree %d, want 2 — the edges did not join", got)
	}
}

func TestDanglingNodeReferenceFails(t *testing.T) {
	g := (&fc{}).
		node("a", 0, 0).
		edge("e0", "a", "nowhere", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		load(t)
	_, err := g.Network(testFrame)
	if err == nil {
		t.Fatal("a dangling node reference must fail, not silently drop the edge")
	}
	if !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("error should name the missing node, got: %v", err)
	}
}

func TestShortEdgeFailsAtParse(t *testing.T) {
	src := `{"type":"FeatureCollection","features":[
	 {"type":"Feature","properties":{"edge":"stub"},
	  "geometry":{"type":"LineString","coordinates":[[-74,40.7]]}}]}`
	_, err := LoadReader(strings.NewReader(src), "test")
	if err == nil || !strings.Contains(err.Error(), "stub") {
		t.Fatalf("a one-coordinate edge must be named and rejected, got: %v", err)
	}
}

func TestSnappingFallbackAndReport(t *testing.T) {
	// no from/to anywhere: endpoints inside SnapTol fuse, the rest are
	// invented and counted
	g := (&fc{}).
		edge("e0", "", "", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		edge("e1", "", "", "R1", [2]float64{100.4, 0}, [2]float64{200, 0}).
		load(t)
	net, err := g.Network(testFrame)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	if len(net.Nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (the 0.4 m pair fuses)", len(net.Nodes))
	}
	if g.Synthesized != 3 {
		t.Errorf("synthesized %d, want 3 — the count is what warns the caller", g.Synthesized)
	}
}

func TestUnknownRouteIDFails(t *testing.T) {
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).
		edge("e0", "a", "b", "R1,GHOST", [2]float64{0, 0}, [2]float64{100, 0}).
		load(t)
	net, err := g.Network(testFrame)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	routes := map[string]gtfs.Route{"R1": {ID: "R1"}}
	err = g.Validate(net, routes, testFrame, quiet)
	if err == nil || !strings.Contains(err.Error(), "GHOST") {
		t.Fatalf("a route id absent from routes.txt must fail loudly, got: %v", err)
	}
}

func TestRouteIDsRoundTripVerbatim(t *testing.T) {
	// ids callers actually use: colons, spaces, mixed case, a leading
	// digit. None of them may be normalised on the way through.
	ids := []string{"MTA NYCT:A", "s-1", "Rot terdam_9", "07"}
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).
		edge("e0", "a", "b", strings.Join(ids, ","), [2]float64{0, 0}, [2]float64{100, 0}).
		load(t)
	net, err := g.Network(testFrame)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	got := net.Edges[0].Routes
	if len(got) != len(ids) {
		t.Fatalf("got %v, want %v", got, ids)
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Errorf("route %d: got %q, want %q", i, got[i], ids[i])
		}
	}
}

func TestDisconnectedGraphIsReportedNotRejected(t *testing.T) {
	// a ferry that touches no track is a real network, not a bad one
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).
		node("c", 5000, 5000).node("d", 5100, 5000).
		edge("e0", "a", "b", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		edge("e1", "c", "d", "F1", [2]float64{5000, 5000}, [2]float64{5100, 5000}).
		load(t)
	net, err := g.Network(testFrame)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	var log []string
	err = g.Validate(net, map[string]gtfs.Route{"R1": {ID: "R1"}, "F1": {ID: "F1"}}, testFrame,
		func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) })
	if err != nil {
		t.Fatalf("disconnection must not fail the build: %v", err)
	}
	if !strings.Contains(strings.Join(log, "\n"), "2 disconnected components") {
		t.Errorf("disconnection must be reported; log was:\n%s", strings.Join(log, "\n"))
	}
}

func TestDividedCorridorIsNotADuplicate(t *testing.T) {
	src := `{"type":"FeatureCollection","features":[
	 {"type":"Feature","properties":{"node":"a"},"geometry":{"type":"Point","coordinates":[-74,40.7]}},
	 {"type":"Feature","properties":{"node":"b"},"geometry":{"type":"Point","coordinates":[-73.999,40.7]}},
	 {"type":"Feature","properties":{"edge":"up","from":"a","to":"b","routes":"R1","oneway":"forward"},
	  "geometry":{"type":"LineString","coordinates":[[-74,40.7],[-73.999,40.7]]}},
	 {"type":"Feature","properties":{"edge":"dn","from":"b","to":"a","routes":"R1","oneway":"backward"},
	  "geometry":{"type":"LineString","coordinates":[[-73.999,40.70005],[-74,40.70005]]}}]}`
	g, err := LoadReader(strings.NewReader(src), "test")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	net, err := g.Network(testFrame)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	if net.Edges[0].OneWay != "forward" || net.Edges[1].OneWay != "backward" {
		t.Fatalf("oneway lost: %q %q", net.Edges[0].OneWay, net.Edges[1].OneWay)
	}
	var log []string
	if err := g.Validate(net, map[string]gtfs.Route{"R1": {ID: "R1"}}, testFrame,
		func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) }); err != nil {
		t.Fatalf("validate: %v", err)
	}
	joined := strings.Join(log, "\n")
	if !strings.Contains(joined, "1 divided corridors") {
		t.Errorf("a one-way pair should read as divided; log:\n%s", joined)
	}
	if strings.Contains(joined, "more than one two-way edge") {
		t.Errorf("a one-way pair must not be reported as a duplicate; log:\n%s", joined)
	}
}

func TestBadOneWayValueFails(t *testing.T) {
	src := `{"type":"FeatureCollection","features":[
	 {"type":"Feature","properties":{"edge":"e0","oneway":"both"},
	  "geometry":{"type":"LineString","coordinates":[[-74,40.7],[-73.999,40.7]]}}]}`
	if _, err := LoadReader(strings.NewReader(src), "test"); err == nil {
		t.Fatal("oneway=both must be rejected rather than quietly ignored")
	}
}

// --- traversal ---------------------------------------------------------

func TestStructuralWalkCoversAPath(t *testing.T) {
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).node("c", 200, 0).node("d", 300, 0).
		edge("e0", "a", "b", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		edge("e1", "b", "c", "R1", [2]float64{100, 0}, [2]float64{200, 0}).
		edge("e2", "c", "d", "R1", [2]float64{200, 0}, [2]float64{300, 0}).
		load(t)
	net, _ := g.Network(testFrame)
	paths := g.Traversals(net, nil, testFrame, quiet)
	if len(paths) != 1 {
		t.Fatalf("a simple path is one walk, got %d", len(paths))
	}
	if l := paths[0].Line.Len(); l < 299 || l > 301 {
		t.Errorf("walk is %.1f m, want ~300 — the whole path, walked once", l)
	}
}

func TestStructuralWalkCoversARing(t *testing.T) {
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).node("c", 100, 100).
		edge("e0", "a", "b", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		edge("e1", "b", "c", "R1", [2]float64{100, 0}, [2]float64{100, 100}).
		edge("e2", "c", "a", "R1", [2]float64{100, 100}, [2]float64{0, 0}).
		load(t)
	net, _ := g.Network(testFrame)
	paths := g.Traversals(net, nil, testFrame, quiet)
	if len(paths) != 1 {
		t.Fatalf("a ring is one walk, got %d", len(paths))
	}
	// 100 + 100 + hypot(100,100)
	if l := paths[0].Line.Len(); l < 341 || l > 343 {
		t.Errorf("ring walk is %.1f m, want ~341.4 — every edge exactly once", l)
	}
}

func TestForkWithoutEvidenceIsReportedAndUnattested(t *testing.T) {
	// a Y: trunk a→b, branches b→c and b→d. Structure alone cannot say
	// whether the route rides c→d, so no walk is invented.
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).node("c", 200, 50).node("d", 200, -50).
		edge("e0", "a", "b", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		edge("e1", "b", "c", "R1", [2]float64{100, 0}, [2]float64{200, 50}).
		edge("e2", "b", "d", "R1", [2]float64{100, 0}, [2]float64{200, -50}).
		load(t)
	net, _ := g.Network(testFrame)
	var log []string
	paths := g.Traversals(net, nil, testFrame,
		func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) })
	if len(paths) != 0 {
		t.Fatalf("a fork has more than one traversal — none may be invented, got %d", len(paths))
	}
	joined := strings.Join(log, "\n")
	if !strings.Contains(joined, "R1") || !strings.Contains(joined, "fork") {
		t.Errorf("the route must be named in the report; log:\n%s", joined)
	}
}

func TestStopOrderResolvesAFork(t *testing.T) {
	// same Y, now with a stop sequence riding the trunk and the NORTH
	// branch. The walk must cover e0+e1 and stay off e2.
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).node("c", 200, 50).node("d", 200, -50).
		edge("e0", "a", "b", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		edge("e1", "b", "c", "R1", [2]float64{100, 0}, [2]float64{200, 50}).
		edge("e2", "b", "d", "R1", [2]float64{100, 0}, [2]float64{200, -50}).
		load(t)
	net, _ := g.Network(testFrame)

	feed := &gtfs.Feed{
		Routes: map[string]gtfs.Route{"R1": {ID: "R1"}},
		Stops: map[string]gtfs.Stop{
			"s_a": {LL: at(10, 0)},
			"s_c": {LL: at(190, 45)},
		},
		Patterns: []gtfs.Pattern{{
			Route: gtfs.Route{ID: "R1"}, ShapeID: "p1",
			StopSeq: []string{"s_a", "s_c"},
		}},
	}
	paths := g.Traversals(net, feed, testFrame, quiet)
	if len(paths) != 1 {
		t.Fatalf("one pattern, one walk — got %d", len(paths))
	}
	// the south branch ends at (200,-50); the walk must never go near it
	south := testFrame.ToXY(at(200, -50))
	if d := paths[0].Line.DistTo(south); d < 50 {
		t.Errorf("walk passes %.1f m from the unridden south branch tip — it took the wrong fork", d)
	}
	north := testFrame.ToXY(at(200, 50))
	if d := paths[0].Line.DistTo(north); d > 1 {
		t.Errorf("walk misses the ridden north branch tip by %.1f m", d)
	}
}

func TestShapeOffTheGraphIsRejectedAsEvidence(t *testing.T) {
	g := (&fc{}).
		node("a", 0, 0).node("b", 100, 0).
		edge("e0", "a", "b", "R1", [2]float64{0, 0}, [2]float64{100, 0}).
		load(t)
	net, _ := g.Network(testFrame)
	feed := &gtfs.Feed{
		Routes: map[string]gtfs.Route{"R1": {ID: "R1"}},
		Patterns: []gtfs.Pattern{{
			Route: gtfs.Route{ID: "R1"}, ShapeID: "elsewhere",
			// 500 m off the corridor: a shape for a different network
			Shape: []geo.LL{at(0, 500), at(100, 500)},
		}},
	}
	var log []string
	paths := g.Traversals(net, feed, testFrame,
		func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) })
	if !strings.Contains(strings.Join(log, "\n"), "stray") {
		t.Errorf("a shape off the graph must be reported; log:\n%s", strings.Join(log, "\n"))
	}
	// it should have fallen through to structure, which for a lone edge
	// is unambiguous
	if len(paths) != 1 {
		t.Fatalf("want the structural fallback to still produce a walk, got %d", len(paths))
	}
	if d := paths[0].Line.DistTo(testFrame.ToXY(at(50, 0))); d > 1 {
		t.Errorf("fallback walk is not on the corridor (%.1f m off)", d)
	}
}
