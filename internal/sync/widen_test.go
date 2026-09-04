package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	write(t, src, `{"type":"FeatureCollection","features":[
	  {"type":"Feature","id":"inside","geometry":{"type":"LineString","coordinates":[[0.2,0.2],[0.4,0.4]]}},
	  {"type":"Feature","id":"straddles","geometry":{"type":"LineString","coordinates":[[0.9,0.9],[2.0,2.0]]}},
	  {"type":"Feature","id":"far","geometry":{"type":"LineString","coordinates":[[5.0,5.0],[6.0,6.0]]}},
	  {"type":"Feature","id":"inside","geometry":{"type":"LineString","coordinates":[[0.1,0.1],[0.3,0.3]]}}
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
