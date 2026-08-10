package pipeline

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/style"
)

// An AUTHORED network: what a planning tool, scenario editor or
// simulator hands over. No shapes.txt, no trips.txt, no stop_times.txt,
// no calendar — the geometry is stated outright and there is no
// timetable at all, which is the case R4 is about.

const (
	gridN    = 10    // gridN × gridN junctions
	gridStep = 800.0 // metres between them
)

var authoredFrame = geo.NewFrame(geo.LL{Lon: -73.75, Lat: 40.75})

func gridLL(ix, iy int) geo.LL {
	return authoredFrame.ToLL(geo.Pt{X: float64(ix) * gridStep, Y: float64(iy) * gridStep})
}

func nodeName(ix, iy int) string { return fmt.Sprintf("n%d_%d", ix, iy) }

// authoredGraph lays out gridN east–west lines and gridN north–south
// ones on a grid. Every interior junction is a real crossing of two
// routes, so ORDER and FAIR do the work they would on a city; every
// route's OWN subgraph is a simple path, so its traversal is derivable
// from structure and the feed needs to state nothing.
//
// jog displaces one mid-edge vertex, far from any junction — the local
// perturbation the locality test rebuilds around.
func authoredGraph(jog float64) string {
	var feats []string
	for ix := 0; ix < gridN; ix++ {
		for iy := 0; iy < gridN; iy++ {
			ll := gridLL(ix, iy)
			feats = append(feats, fmt.Sprintf(
				`{"type":"Feature","properties":{"node":%q},`+
					`"geometry":{"type":"Point","coordinates":[%.9f,%.9f]}}`,
				nodeName(ix, iy), ll.Lon, ll.Lat))
		}
	}
	edge := func(id, from, to, routes string, pts []geo.LL) {
		var cs []string
		for _, ll := range pts {
			cs = append(cs, fmt.Sprintf("[%.9f,%.9f]", ll.Lon, ll.Lat))
		}
		feats = append(feats, fmt.Sprintf(
			`{"type":"Feature","properties":{"edge":%q,"from":%q,"to":%q,"routes":%q,"tracks":2},`+
				`"geometry":{"type":"LineString","coordinates":[%s]}}`,
			id, from, to, routes, strings.Join(cs, ",")))
	}
	// east–west: route EW<iy> rides row iy
	for iy := 0; iy < gridN; iy++ {
		for ix := 0; ix < gridN-1; ix++ {
			mid := geo.Pt{X: (float64(ix) + 0.5) * gridStep, Y: float64(iy) * gridStep}
			// the jog goes on one edge only, in the middle of the network
			if jog != 0 && ix == 4 && iy == 4 {
				mid.Y += jog
			}
			edge(fmt.Sprintf("ew%d_%d", iy, ix), nodeName(ix, iy), nodeName(ix+1, iy),
				fmt.Sprintf("EW%d", iy),
				[]geo.LL{gridLL(ix, iy), authoredFrame.ToLL(mid), gridLL(ix+1, iy)})
		}
	}
	// north–south: route NS<ix> rides column ix
	for ix := 0; ix < gridN; ix++ {
		for iy := 0; iy < gridN-1; iy++ {
			edge(fmt.Sprintf("ns%d_%d", ix, iy), nodeName(ix, iy), nodeName(ix, iy+1),
				fmt.Sprintf("NS%d", ix),
				[]geo.LL{gridLL(ix, iy), gridLL(ix, iy+1)})
		}
	}
	return `{"type":"FeatureCollection","features":[` + strings.Join(feats, ",") + "]}"
}

// authoredFeed writes the smallest legal GTFS an authored network needs:
// what the routes are called and where the stops are. Nothing else — no
// trips.txt, no stop_times.txt, no shapes.txt, no calendar.
func authoredFeed(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "feed.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	put := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	put("agency.txt", "agency_id,agency_name\nAUTH,Authored Transit\n")

	routes := &strings.Builder{}
	routes.WriteString("route_id,agency_id,route_short_name,route_long_name,route_type,route_color,route_text_color\n")
	palette := []string{"E4002B", "0039A6", "00933C", "FF6319", "996633",
		"A7A9AC", "FCCC0A", "6CBE45", "808183", "EE352E"}
	for i := 0; i < gridN; i++ {
		fmt.Fprintf(routes, "EW%d,AUTH,E%d,East-West %d,1,%s,FFFFFF\n",
			i, i, i, palette[i%len(palette)])
		fmt.Fprintf(routes, "NS%d,AUTH,N%d,North-South %d,1,%s,000000\n",
			i, i, i, palette[(i+3)%len(palette)])
	}
	put("routes.txt", routes.String())

	stops := &strings.Builder{}
	stops.WriteString("stop_id,stop_name,stop_lat,stop_lon\n")
	for ix := 0; ix < gridN; ix++ {
		for iy := 0; iy < gridN; iy++ {
			ll := gridLL(ix, iy)
			fmt.Fprintf(stops, "s%d_%d,Junction %d/%d,%.9f,%.9f\n", ix, iy, ix, iy, ll.Lat, ll.Lon)
		}
	}
	put("stops.txt", stops.String())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// chartAuthored runs one corridors build into its own directory and
// returns the output prefix.
func chartAuthored(t *testing.T, dir, graph, feed string, logf func(string, ...any)) string {
	t.Helper()
	gpath := filepath.Join(dir, "corridors.geojson")
	if err := os.WriteFile(gpath, []byte(graph), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "build.geojson")
	// the anchor is pinned: two builds of DIFFERENT networks must share a
	// projection origin or every float in them is incomparable
	anchor := geo.LL{Lon: -73.75, Lat: 40.75}
	err := Chart(ChartOpts{
		GTFS: feed, Corridors: gpath, Out: out,
		Anchor: &anchor, Style: style.New(),
	}, logf)
	if err != nil {
		t.Fatalf("chart --corridors: %v", err)
	}
	return out
}

func TestAuthoredNetworkChartsWithoutATimetable(t *testing.T) {
	dir := t.TempDir()
	feed := authoredFeed(t, dir)
	var log []string
	start := time.Now()
	out := chartAuthored(t, dir, authoredGraph(0), feed,
		func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) })
	elapsed := time.Since(start)

	// R9: with BUNDLE and MATCH gone, tens of routes over low hundreds of
	// edges should chart in well under a second.
	t.Logf("charted %d routes / %d edges in %v", 2*gridN, 2*gridN*(gridN-1), elapsed)
	if elapsed > time.Second {
		t.Errorf("authored build took %v, want well under 1s", elapsed)
	}

	for _, suffix := range []string{"", ".stations.geojson", ".style.json",
		".trackcenter.geojson", ".nodes.geojson"} {
		if _, err := os.Stat(out + suffix); err != nil {
			t.Errorf("missing artifact %q: %v", suffix, err)
		}
	}

	segs := readFC(t, out)
	if len(segs) == 0 {
		t.Fatal("no segments drawn")
	}
	// R4: no calendar anywhere, so nothing may claim to know hours
	for _, f := range segs {
		if _, ok := f.Props["acts"]; ok {
			t.Fatalf("a feed with no calendar must not emit acts: %v", f.Props)
		}
	}
	// every route must reach the map — this is the check that catches a
	// route id quietly lost between the graph and the emitters
	seen := map[string]bool{}
	for _, f := range segs {
		for _, r := range strings.Split(f.Props["routes"].(string), ",") {
			seen[r] = true
		}
	}
	for i := 0; i < gridN; i++ {
		for _, want := range []string{fmt.Sprintf("EW%d", i), fmt.Sprintf("NS%d", i)} {
			if !seen[want] {
				t.Errorf("route %s rides corridors but never drew", want)
			}
		}
	}
	// stations come from stops.txt and edge membership, with no pattern
	// list to build them from
	sts := readFC(t, out+".stations.geojson")
	if len(sts) == 0 {
		t.Error("no stations built from stops.txt alone")
	}
	joined := strings.Join(log, "\n")
	if !strings.Contains(joined, "derived from structure") {
		t.Errorf("expected structural traversal derivation; log:\n%s", joined)
	}
}

func TestAuthoredBuildIsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	feed := authoredFeed(t, dir)
	graph := authoredGraph(0)

	a := chartAuthored(t, filepath.Join(mkdir(t, dir, "a")), graph, feed, quietf)
	b := chartAuthored(t, filepath.Join(mkdir(t, dir, "b")), graph, feed, quietf)

	for _, suffix := range []string{"", ".stations.geojson", ".trackcenter.geojson",
		".nodes.geojson", ".style.json"} {
		ra, err := os.ReadFile(a + suffix)
		if err != nil {
			t.Fatal(err)
		}
		rb, err := os.ReadFile(b + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ra, rb) {
			t.Errorf("%q differs between two builds of identical input (%d vs %d bytes)",
				suffix, len(ra), len(rb))
		}
	}
}

// TestLocalChangeStaysLocal is R8's claim under test: an editing client
// that nudges one corridor must not see ribbon order reshuffle across
// the city. ORDER is a local descent, so a jog in the middle of one edge
// — nowhere near a junction — may change that edge's own drawing and
// must leave everyone else's slots alone.
func TestLocalChangeStaysLocal(t *testing.T) {
	dir := t.TempDir()
	feed := authoredFeed(t, dir)

	base := chartAuthored(t, mkdir(t, dir, "base"), authoredGraph(0), feed, quietf)
	// 4 m sideways on one mid-edge vertex: enough to move geometry, far
	// too little to change what meets what
	jogged := chartAuthored(t, mkdir(t, dir, "jog"), authoredGraph(4), feed, quietf)

	// guard against a vacuous pass: if the jog changed nothing at all,
	// "slots did not move" would be true for the wrong reason
	ra, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := os.ReadFile(jogged)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ra, rb) {
		t.Fatal("the perturbed build is byte-identical — the jog never reached the geometry, " +
			"so this test is proving nothing")
	}

	slotsA := slotsByRoute(t, base)
	slotsB := slotsByRoute(t, jogged)

	moved := []string{}
	for k, v := range slotsA {
		if w, ok := slotsB[k]; !ok || v != w {
			moved = append(moved, k)
		}
	}
	// the jogged edge belongs to EW4; nothing else may move
	for _, k := range moved {
		if !strings.HasPrefix(k, "EW4|") {
			t.Errorf("slot moved on %s (%v → %v) — a local edit reordered a distant ribbon",
				k, slotsA[k], slotsB[k])
		}
	}
	if len(slotsA) == 0 {
		t.Fatal("no slots read back")
	}
	t.Logf("%d slot keys, %d moved", len(slotsA), len(moved))
}

// slotsByRoute keys each drawn ribbon by route and by where it is, so
// two builds can be compared without depending on emission order. The
// position is rounded to 50 m: a 4 m jog must not itself re-key a
// segment, or the test would report its own rounding as instability.
func slotsByRoute(t *testing.T, out string) map[string]int {
	t.Helper()
	m := map[string]int{}
	for _, f := range readFC(t, out) {
		if f.Geometry.Type != "LineString" || len(f.Geometry.Coords) == 0 {
			continue
		}
		routes, _ := f.Props["routes"].(string)
		slot, _ := f.Props["slot"].(float64)
		kind, _ := f.Props["kind"].(string)
		if kind != "steady" {
			continue
		}
		c := f.Geometry.Coords[0]
		p := authoredFrame.ToXY(geo.LL{Lon: c[0], Lat: c[1]})
		key := fmt.Sprintf("%s|%.0f|%.0f", routes, p.X/50, p.Y/50)
		m[key] = int(slot)
	}
	return m
}

type testFeature struct {
	Props    map[string]any `json:"properties"`
	Geometry struct {
		Type   string       `json:"type"`
		Coords [][2]float64 `json:"coordinates"`
	} `json:"geometry"`
}

func readFC(t *testing.T, path string) []testFeature {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fc struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var out []testFeature
	for _, rm := range fc.Features {
		var f testFeature
		// station features carry Point geometry; the coords shape differs
		// and unmarshalling simply leaves it empty, which is fine here
		json.Unmarshal(rm, &f)
		out = append(out, f)
	}
	return out
}

func mkdir(t *testing.T, parent, name string) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func quietf(string, ...any) {}
