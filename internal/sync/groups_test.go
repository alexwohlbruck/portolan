package sync

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

// The synthetic workspace: nine miniature feeds engineered to trip every
// rule exactly once, at lat 40–42 where a lon degree is ~85 km.
//
//	a,b — share a 2.6 km corridor (≥900 m) → one group, members
//	c,d — overlap only ~300 m (<900 m) → no pair, no group
//	e,f — f is 95% of e (>85% of BOTH) → duplicate, f held out
//	g   — corridor-scale (~40 deg² of shapes) riding a's corridor AND
//	      h's → overlay on two components, member of neither
//	h   — regional feed sharing steel with g only → lone member with an
//	      overlay riding it (the Hartford Line case)
//	u   — rides a's corridor but has no build → undrawn, held out
//
// a's configured bbox is WIDER than its shape extent — the Charlotte
// rule: the group window must take the bbox, not just the shapes.

type pt = [2]float64

func seg(x0, x1, y float64) []pt  { return []pt{{x0, y}, {x1, y}} }
func vseg(x, y0, y1 float64) []pt { return []pt{{x, y0}, {x, y1}} }

type fxFeed struct {
	name   string
	shapes [][]pt
	bbox   []float64
	drawn  bool
}

func fixtureFeeds() (order []string, feeds map[string]fxFeed) {
	corridorA := seg(-100.00, -99.95, 40.00) // ~4.3 km
	corridorH := seg(-80.00, -79.96, 42.00)  // ~3.3 km
	feeds = map[string]fxFeed{
		"a": {"Alpha Transit", [][]pt{corridorA, vseg(-100.00, 40.00, 40.05)},
			[]float64{-100.2, 39.9, -99.9, 40.1}, true},
		"b": {"Beta Rail", [][]pt{seg(-100.00, -99.97, 40.00), vseg(-99.97, 40.00, 40.04)},
			[]float64{-100.01, 39.99, -99.96, 40.05}, true},
		"c": {"Charlie Line", [][]pt{seg(-100.50, -100.485, 40.30)},
			[]float64{-100.51, 40.29, -100.48, 40.31}, true},
		"d": {"Delta Line", [][]pt{seg(-100.4885, -100.47, 40.30)},
			[]float64{-100.50, 40.29, -100.46, 40.31}, true},
		"e": {"Echo Metro", [][]pt{seg(-101.00, -100.96, 40.20)},
			[]float64{-101.01, 40.19, -100.95, 40.21}, true},
		"f": {"Foxtrot Metro", [][]pt{seg(-101.00, -100.962, 40.20)},
			[]float64{-101.01, 40.19, -100.955, 40.21}, true},
		"g": {"Golf Corridor", [][]pt{corridorA, corridorH},
			[]float64{-101, 38, -79, 43}, true},
		"h": {"Hotel Metro", [][]pt{corridorH, vseg(-80.00, 42.00, 42.03)},
			[]float64{-80.01, 41.99, -79.95, 42.04}, true},
		"u": {"Uniform Railway", [][]pt{seg(-100.00, -99.975, 40.00)},
			[]float64{-100.01, 39.99, -99.97, 40.01}, false},
	}
	return []string{"a", "b", "c", "d", "e", "f", "g", "h", "u"}, feeds
}

func ff(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

// writeGTFSZip writes the three tables groups.py reads: one rail route,
// one trip per shape.
func writeGTFSZip(t *testing.T, path string, shapes [][]pt) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("routes.txt", "route_id,route_type\nr1,2\n")
	trips := "route_id,service_id,trip_id,shape_id\n"
	shp := "shape_id,shape_pt_lat,shape_pt_lon,shape_pt_sequence\n"
	for i, poly := range shapes {
		sid := fmt.Sprintf("s%d", i)
		trips += fmt.Sprintf("r1,wk,t%d,%s\n", i, sid)
		for j, p := range poly {
			shp += fmt.Sprintf("%s,%s,%s,%d\n", sid, ff(p[1]), ff(p[0]), j)
		}
	}
	add("trips.txt", trips)
	add("shapes.txt", shp)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildFixture materializes the workspace in dir (which becomes the
// cwd): gtfs/<k>.zip, build/<k>.geojson for drawn feeds, portolan.json.
// Returns the registry bytes.
func buildFixture(t *testing.T, dir string) []byte {
	t.Helper()
	order, feeds := fixtureFeeds()
	for _, sub := range []string{"gtfs", "build"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	doc := NewObj()
	doc.Set("sketches", "sketches")
	fo := NewObj()
	for _, k := range order {
		f := feeds[k]
		writeGTFSZip(t, filepath.Join(dir, "gtfs", k+".zip"), f.shapes)
		if f.drawn {
			if err := os.WriteFile(filepath.Join(dir, "build", k+".geojson"),
				bytes.Repeat([]byte("x"), 4000), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		e := NewObj()
		e.Set("name", f.name)
		e.Set("gtfs", "gtfs/"+k+".zip")
		e.Set("out", "build/"+k+".geojson")
		e.Set("bbox", []any{f.bbox[0], f.bbox[1], f.bbox[2], f.bbox[3]})
		fo.Set(k, e)
	}
	doc.Set("feeds", fo)
	raw := MarshalDoc(doc)
	if err := os.WriteFile(filepath.Join(dir, "portolan.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return raw
}

func loadFixture(t *testing.T, raw []byte) (registry.Config, *Obj) {
	t.Helper()
	cfg, err := registry.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, doc
}

func TestDeriveGroupsRules(t *testing.T) {
	dir := t.TempDir()
	raw := buildFixture(t, dir)
	t.Chdir(dir)
	cfg, _ := loadFixture(t, raw)

	d, err := DeriveGroups(cfg, nil, "build", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c", "d", "e", "f", "g", "h", "u"}
	if !reflect.DeepEqual(d.Measured, want) {
		t.Fatalf("measured = %v", d.Measured)
	}
	// the ≥900 m floor: a‖b qualifies, c‖d (≈300 m) does not
	if d.SharedM("a", "b") < minSharedM {
		t.Fatalf("a‖b shared %.0f m, want ≥900", d.SharedM("a", "b"))
	}
	if d.SharedM("c", "d") != 0 {
		t.Fatalf("c‖d shared %.0f m, want below the floor", d.SharedM("c", "d"))
	}
	// duplicates: f is e again (>85%% of BOTH lengths), smaller held out
	if !reflect.DeepEqual(d.Duplicate, map[string]string{"f": "e"}) {
		t.Fatalf("duplicates = %v", d.Duplicate)
	}
	// undrawn: u has no build ≥3000 bytes
	if !reflect.DeepEqual(d.Undrawn, []string{"u"}) {
		t.Fatalf("undrawn = %v", d.Undrawn)
	}
	// two groups: {a,b}+g, and the lone member h with the overlay riding
	// it; e stands alone (its only partner was a duplicate), c and d
	// never paired, u is held out of both roles
	if len(d.Groups) != 2 {
		t.Fatalf("groups = %+v", d.Groups)
	}
	g1, g2 := d.Groups[0], d.Groups[1]
	if !reflect.DeepEqual(g1.Members, []string{"a", "b"}) ||
		!reflect.DeepEqual(g1.Overlays, []string{"g"}) {
		t.Fatalf("group 1 = %+v", g1)
	}
	if !reflect.DeepEqual(g2.Members, []string{"h"}) ||
		!reflect.DeepEqual(g2.Overlays, []string{"g"}) {
		t.Fatalf("group 2 = %+v", g2)
	}
	// overlay role by extent: g's shapes cover ~40 deg²
	if a := d.Extent["g"].Area(); a <= maxMemberArea {
		t.Fatalf("g area = %.1f, want corridor-scale", a)
	}
	// the Charlotte rule: a's configured bbox is wider than its shapes,
	// and the group window must take the union of both
	if g1.Extent != (Extent{-100.2, 39.9, -99.9, 40.1}) {
		t.Fatalf("group 1 window = %v — configured bbox must widen it", g1.Extent)
	}
	if g2.Extent != (Extent{-80.01, 41.99, -79.95, 42.04}) {
		t.Fatalf("group 2 window = %v", g2.Extent)
	}
}

func TestRewriteGroupsShape(t *testing.T) {
	dir := t.TempDir()
	raw := buildFixture(t, dir)
	t.Chdir(dir)
	cfg, doc := loadFixture(t, raw)
	d, err := DeriveGroups(cfg, nil, "build", nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := RewriteGroups(doc, d, nil)
	if !reflect.DeepEqual(keys, []string{"alpha-transit", "hotel-metro"}) {
		t.Fatalf("group keys = %v", keys)
	}
	feeds := feedsObj(doc)
	v, _ := feeds.Get("alpha-transit")
	e := v.(*Obj)
	if got := e.Str("gtfs"); got != "gtfs/a.zip,gtfs/b.zip,gtfs/g.zip" {
		t.Fatalf("group gtfs = %q", got)
	}
	if got := MarshalDoc(mustGet(t, e, "bbox")); string(got) != "[\n  -100.23,\n  39.87,\n  -99.87,\n  40.13\n]" {
		t.Fatalf("group bbox = %s", got)
	}
	if b, _ := e.m["derived"].(bool); !b {
		t.Fatal("group entry must be derived")
	}
	if e.Str("chart_args") != chartArgsGroup {
		t.Fatalf("chart_args = %q", e.Str("chart_args"))
	}

	// idempotence: deriving over the rewritten registry keeps key, name
	// and members, and a second rewrite is a no-op byte for byte
	raw2 := MarshalDoc(doc)
	cfg2, doc2 := loadFixture(t, raw2)
	d2, err := DeriveGroups(cfg2, nil, "build", nil)
	if err != nil {
		t.Fatal(err)
	}
	keys2 := RewriteGroups(doc2, d2, nil)
	if !reflect.DeepEqual(keys2, keys) {
		t.Fatalf("rewrite not stable: %v then %v", keys, keys2)
	}
	if raw3 := MarshalDoc(doc2); !bytes.Equal(raw2, raw3) {
		t.Fatalf("rewrite not idempotent:\n%s\n----\n%s", raw2, raw3)
	}
}

func mustGet(t *testing.T, o *Obj, k string) any {
	t.Helper()
	v, ok := o.Get(k)
	if !ok {
		t.Fatalf("missing %q", k)
	}
	return v
}

// TestPythonParity runs tools/groups.py --write over the same synthetic
// workspace and demands the Go rewrite produce byte-identical
// portolan.json — members, overlays, windows, key order, formatting,
// everything. This is the credibility of the whole patch feature.
func TestPythonParity(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	groupsPy, err := filepath.Abs("../../tools/groups.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(groupsPy); err != nil {
		t.Skipf("tools/groups.py not found: %v", err)
	}

	dir := t.TempDir()
	raw := buildFixture(t, dir)
	t.Chdir(dir)

	// Go derivation + rewrite, in memory
	cfg, doc := loadFixture(t, raw)
	d, err := DeriveGroups(cfg, nil, "build", nil)
	if err != nil {
		t.Fatal(err)
	}
	RewriteGroups(doc, d, nil)
	goBytes := MarshalDoc(doc)

	// the Python original, on disk
	cmd := exec.Command("python3", groupsPy, "--write")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("groups.py: %v\n%s", err, out)
	}
	pyBytes, err := os.ReadFile(filepath.Join(dir, "portolan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(goBytes, pyBytes) {
		t.Fatalf("Go rewrite diverges from groups.py --write.\npython:\n%s\n\ngo:\n%s\n\n(groups.py said: %s)",
			pyBytes, goBytes, out)
	}
}

// TestMarshalDocMatchesPythonDumps re-serializes the repo's real
// registry and checks it against python3's json.dumps(indent=2) — the
// exact formatter every groups.py --write has used on it.
func TestMarshalDocMatchesPythonDumps(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	regPath, err := filepath.Abs("../../portolan.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(regPath)
	if err != nil {
		t.Skipf("no repo portolan.json: %v", err)
	}
	doc, err := ParseDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	goBytes := MarshalDoc(doc)
	cmd := exec.Command("python3", "-c",
		"import json,sys; sys.stdout.write(json.dumps(json.load(open(sys.argv[1])), indent=2))",
		regPath)
	pyBytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("python3: %v", err)
	}
	if !bytes.Equal(goBytes, pyBytes) {
		i := 0
		for i < len(goBytes) && i < len(pyBytes) && goBytes[i] == pyBytes[i] {
			i++
		}
		lo, hi := i-80, i+80
		if lo < 0 {
			lo = 0
		}
		clip := func(b []byte) string {
			h := hi
			if h > len(b) {
				h = len(b)
			}
			return string(b[lo:h])
		}
		t.Fatalf("MarshalDoc diverges from json.dumps at byte %d:\npython: …%s…\ngo:     …%s…",
			i, clip(pyBytes), clip(goBytes))
	}
}
