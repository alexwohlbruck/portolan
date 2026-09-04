package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

// A feed's bbox is the Overpass window AND the shape clip, so a window
// smaller than the railroad truncates the map with no error at all.
// Metro-North carried the subway's box — authored when portolan drew only
// New York City — and came out with 25 of its 114 stations.
func TestWidenFeedWindowsCoversTheFeedsOwnShapes(t *testing.T) {
	doc, err := ParseDoc([]byte(`{"feeds":{
	  "mta-metro-north":{"bbox":[-74.26,40.49,-73.7,40.92],"rail":"testdata/nyc-rail.geojson"},
	  "mta-subway":{"bbox":[-74.26,40.49,-73.7,40.92],"rail":"testdata/nyc-rail.geojson"}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	der := &Derivation{Extent: map[string]Extent{
		// the railroad as measured: north to Poughkeepsie, east to New Haven
		"mta-metro-north": {-73.985, 40.753, -72.922, 41.815},
		// the subway genuinely fits its authored window
		"mta-subway": {-74.03, 40.58, -73.75, 40.90},
	}}

	widened := WidenFeedWindows(doc, der, nil)

	if len(widened) != 1 || widened[0] != "mta-metro-north" {
		t.Fatalf("widened = %v, want only mta-metro-north", widened)
	}
	bb := bboxOf(t, doc, "mta-metro-north")
	// north and east must now reach the data (plus the margin groups use)
	if bb[3] < 41.815 {
		t.Errorf("north edge %v does not reach the railroad at 41.815", bb[3])
	}
	if bb[2] < -72.922 {
		t.Errorf("east edge %v does not reach the railroad at -72.922", bb[2])
	}
	// widening only ever grows: the authored west/south still stand
	if bb[0] > -74.26 || bb[1] > 40.49 {
		t.Errorf("widening shrank the authored window: %v", bb)
	}
	// a feed that already fits is left exactly alone
	if got := bboxOf(t, doc, "mta-subway"); got != [4]float64{-74.26, 40.49, -73.7, 40.92} {
		t.Errorf("subway window moved: %v", got)
	}
}

// The eight original New York entries all name one shared fixture. A widened
// feed outgrows it, and must not rewrite a file the others still read.
func TestWidenFeedWindowsRepointsASharedExtract(t *testing.T) {
	doc, err := ParseDoc([]byte(`{"feeds":{
	  "mta-metro-north":{"bbox":[-74.26,40.49,-73.7,40.92],"rail":"testdata/nyc-rail.geojson"},
	  "nj-transit-rail":{"bbox":[-75.2,39.3,-73.9,41.5],"rail":"build/nj-transit-rail-rail.geojson"}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	der := &Derivation{Extent: map[string]Extent{
		"mta-metro-north": {-73.985, 40.753, -72.922, 41.815},
		"nj-transit-rail": {-75.1, 39.4, -74.0, 41.4},
	}}
	WidenFeedWindows(doc, der, nil)

	if got := railOf(t, doc, "mta-metro-north"); got != "build/mta-metro-north-rail.geojson" {
		t.Errorf("rail = %q, want its own derived extract", got)
	}
	// an untouched feed keeps whatever it had
	if got := railOf(t, doc, "nj-transit-rail"); got != "build/nj-transit-rail-rail.geojson" {
		t.Errorf("nj-transit rail moved: %q", got)
	}
}

// Only feeds measured this run can be judged; a patch run must not widen a
// feed on evidence it never gathered.
func TestWidenFeedWindowsIgnoresUnmeasuredAndOutOfScope(t *testing.T) {
	doc, err := ParseDoc([]byte(`{"feeds":{
	  "a":{"bbox":[0,0,1,1]},
	  "b":{"bbox":[0,0,1,1]}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	der := &Derivation{Extent: map[string]Extent{"a": {0, 0, 9, 9}, "b": {0, 0, 9, 9}}}

	if got := WidenFeedWindows(doc, der, map[string]bool{"a": true}); len(got) != 1 || got[0] != "a" {
		t.Fatalf("scope ignored: %v", got)
	}
	if bboxOf(t, doc, "b") != [4]float64{0, 0, 1, 1} {
		t.Error("out-of-scope feed was widened")
	}

	// and a feed with no measurement at all is untouched
	doc2, _ := ParseDoc([]byte(`{"feeds":{"c":{"bbox":[0,0,1,1]}}}`))
	if got := WidenFeedWindows(doc2, &Derivation{Extent: map[string]Extent{}}, nil); len(got) != 0 {
		t.Errorf("widened an unmeasured feed: %v", got)
	}
}

// Clipping rather than merging is what keeps a widened feed affordable: the
// Northeast Corridor extract is 166 MB, and a feed that swallowed it whole to
// reach its own corner would chart as heavily as the whole corridor.
func TestClipFCKeepsOnlyWhatMeetsTheWindow(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.geojson")
	// tags included: they are what classifies a way as regular-service rail,
	// so an extract that keeps geometry and drops properties is still useless
	write(t, src, `{"type":"FeatureCollection","features":[
	  {"type":"Feature","id":"inside","properties":{"railway":"rail","usage":"main"},"geometry":{"type":"LineString","coordinates":[[0.2,0.2],[0.4,0.4]]}},
	  {"type":"Feature","id":"straddles","properties":{"railway":"rail"},"geometry":{"type":"LineString","coordinates":[[0.9,0.9],[2.0,2.0]]}},
	  {"type":"Feature","id":"far","properties":{"railway":"rail"},"geometry":{"type":"LineString","coordinates":[[5.0,5.0],[6.0,6.0]]}},
	  {"type":"Feature","id":"inside","properties":{"railway":"rail"},"geometry":{"type":"LineString","coordinates":[[0.1,0.1],[0.3,0.3]]}}
	]}`)
	dst := filepath.Join(dir, "out.geojson")

	n, err := clipFC(dst, []string{src}, []float64{0, 0, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	ids := idsOf(t, dst)
	// the straddler is kept whole — the chart does the geometry clipping
	if n != 2 || len(ids) != 2 || !ids["inside"] || !ids["straddles"] {
		t.Fatalf("kept %v (n=%d), want inside + straddles", ids, n)
	}
	if ids["far"] {
		t.Error("a feature outside the window was kept")
	}
	// AND the geometry must survive. The first cut of clipFC wrote every
	// feature with "geometry":null — the right ways with no shape — and the
	// chart reported "no regular-service rail ways". Asserting on ids alone
	// is what let that reach production.
	for _, f := range featuresOf(t, dst) {
		if len(f.Geom) == 0 || string(f.Geom) == "null" {
			t.Fatalf("feature %v was written with no geometry", f.ID)
		}
		if len(f.Props) == 0 || string(f.Props) == "null" {
			t.Errorf("feature %v lost its properties — the tags are what classify a way", f.ID)
		}
	}
}

func bboxOf(t *testing.T, doc *Obj, key string) [4]float64 {
	t.Helper()
	v, _ := feedsObj(doc).Get(key)
	bv, _ := v.(*Obj).Get("bbox")
	arr, _ := bv.([]any)
	if len(arr) != 4 {
		t.Fatalf("%s: bbox is not 4 numbers: %v", key, bv)
	}
	var out [4]float64
	for i, a := range arr {
		f, ok := toFloat(a)
		if !ok {
			t.Fatalf("%s: bbox[%d] not a number", key, i)
		}
		out[i] = f
	}
	return out
}

func railOf(t *testing.T, doc *Obj, key string) string {
	t.Helper()
	v, _ := feedsObj(doc).Get(key)
	return v.(*Obj).Str("rail")
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func idsOf(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fc struct {
		Features []struct {
			ID any `json:"id"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range fc.Features {
		out[f.ID.(string)] = true
	}
	return out
}

// Computing the corrected window is not enough: the entry is only written
// when the registry counts as changed, and the feed only rebuilds when it
// counts as affected. The first cut of this did neither, so a patch run
// reported "registry rewrite: no" and skipped the very feeds it had just
// corrected.
func TestAWidenedWindowRebuildsAndIsWritten(t *testing.T) {
	dir := t.TempDir()
	raw := buildFixture(t, dir)
	t.Chdir(dir)
	cfg, doc := loadFixture(t, raw)

	// Settle the registry first: a fresh fixture derives groups, and that
	// alone marks the registry changed. Only on a settled registry is
	// widening the sole reason anything moves — which is the case that
	// failed in production.
	settle, err := BuildPlan(PlanOpts{
		Config: cfg, Doc: doc, State: &State{Feeds: map[string]FeedState{}},
		Global: true, BuildDir: "build", Log: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if settle.Registry == nil {
		t.Fatal("fixture did not settle")
	}
	cfg2, doc2 := loadFixture(t, settle.Registry)
	if again, err := BuildPlan(PlanOpts{
		Config: cfg2, Doc: doc2, State: &State{Feeds: map[string]FeedState{}},
		Global: true, BuildDir: "build", Log: func(string, ...any) {},
	}); err != nil {
		t.Fatal(err)
	} else if again.RegistryChanged {
		t.Fatal("registry still churning; the isolation this test needs does not hold")
	}

	// now clip "u" to a sliver of its own railroad, the way the New York
	// entries clipped Metro-North
	cfg3, doc3 := loadFixture(t, settle.Registry)
	v, _ := feedsObj(doc3).Get("u")
	v.(*Obj).Set("bbox", []any{-100.01, 39.99, -99.995, 40.01})
	cfg3, _ = loadFixture(t, MarshalDoc(doc3))

	plan, err := BuildPlan(PlanOpts{
		Config: cfg3, Doc: doc3, State: &State{Feeds: map[string]FeedState{}},
		Global: true, BuildDir: "build", Log: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !contains(plan.Widened, "u") {
		t.Fatalf("Widened = %v, want it to carry u", plan.Widened)
	}
	// RegistryChanged is what makes the run WRITE the corrected window and
	// re-parse it; without it the build reads the old file and the widening
	// is computed and thrown away.
	if !plan.RegistryChanged || plan.Registry == nil {
		t.Fatal("a corrected window must mark the registry changed, or it is never written")
	}
	if !contains(plan.Standalone, "u") && !contains(plan.MemberPyramids, "u") {
		t.Errorf("u was corrected but not scheduled to rebuild (standalone=%v members=%v)",
			plan.Standalone, plan.MemberPyramids)
	}
	after, err := ParseDoc(plan.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if bboxOf(t, after, "u")[2] <= -99.995 {
		t.Errorf("written window still clips the railroad: %v", bboxOf(t, after, "u"))
	}
	_ = cfg2
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// Which extract a widened feed borrows from decides whether it builds at all.
// Ranking by window size cut Metro-North's rail from a national intercity BUS
// feed — the widest window in the registry — and the build died with "no
// regular-service rail ways in build/mta-metro-north-rail.geojson".
func TestFeedPreflightBorrowsFromItsOwnGroup(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("build", 0o755); err != nil {
		t.Fatal(err)
	}
	// three extracts, all covering the feed's window
	rail := func(name string, w, s, e, n float64) string {
		p := filepath.Join("build", name)
		write(t, p, `{"type":"FeatureCollection","features":[{"type":"Feature","id":"`+name+`",`+
			`"geometry":{"type":"LineString","coordinates":[[`+ff(w)+`,`+ff(s)+`],[`+ff(e)+`,`+ff(n)+`]]}}]}`)
		return p
	}
	// pad each past railCovers' 2 KB floor
	pad := func(p string) {
		raw, _ := os.ReadFile(p)
		body := string(raw[:len(raw)-len("]}")])
		for i := 0; i < 40; i++ {
			body += `,{"type":"Feature","id":"pad` + ff(float64(i)) + `","geometry":{"type":"LineString","coordinates":[[-80,40],[-79,41]]}}`
		}
		write(t, p, body+"]}")
	}
	groupRail := rail("nec-rail.geojson", -76, 39, -71, 42)
	busRail := rail("intercity-bus-rail.geojson", -125, 25, -66, 49)
	ownRail := rail("mnr-rail.geojson", -74.3, 40.4, -73.6, 41.0) // too small: does not cover
	for _, p := range []string{groupRail, busRail, ownRail} {
		pad(p)
	}

	cfg, _ := loadFixtureCfg(t, `{"feeds":{
	  "mta-metro-north":{"bbox":[-74.1,40.7,-72.9,41.9],"rail":"`+ownRail+`"},
	  "northeast-corridor-region":{"bbox":[-76,39,-71,42],"rail":"`+groupRail+`","members":["mta-metro-north"]},
	  "intercity-bus":{"bbox":[-125,25,-66,49],"rail":"`+busRail+`"}
	}}`)

	var logged string
	if err := feedPreflight(cfg, "mta-metro-north", "build", func(f string, a ...any) {
		logged += fmt.Sprintf(f, a...)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged, "nec-rail.geojson") {
		t.Errorf("borrowed from the wrong extract: %q", logged)
	}
	if strings.Contains(logged, "intercity-bus") {
		t.Error("borrowed from the continental bus network again")
	}
}

func loadFixtureCfg(t *testing.T, raw string) (registry.Config, *Obj) {
	t.Helper()
	cfg, err := registry.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseDoc([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, doc
}

type clippedFeature struct {
	ID    any             `json:"id"`
	Props json.RawMessage `json:"properties"`
	Geom  json.RawMessage `json:"geometry"`
}

func featuresOf(t *testing.T, path string) []clippedFeature {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fc struct {
		Features []clippedFeature `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		t.Fatal(err)
	}
	return fc.Features
}
