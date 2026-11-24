package sync

import (
	"archive/zip"
	"io"
	"os"
	"strings"
	"testing"
)

// A bus-only operator with no rail and no street layer draws nothing, but
// its stops still take their OSM names, and the next sync leaves it alone.
func TestBusOnlyFeedNamesItsStopsAndStaysClean(t *testing.T) {
	bin := portolanBin(t)
	dir := t.TempDir()
	t.Chdir(dir)
	for _, sub := range []string{"gtfs", "rail", "build"} {
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	line := curveH(-100.00, -99.95, 40.00, 0.002)
	writeMicroGTFSType(t, "gtfs/b1.zip", "b1", 3, []microStop{
		{"s1", "MAIN ST/1 AV", -100.00, 40.00}, {"s2", "MAIN ST/5 AV", -99.975, 40.00},
	}, line)
	writeRail(t, "rail/b1.geojson", map[string][][2]float64{})
	stops := `{"type":"FeatureCollection","features":[
{"type":"Feature","id":"node/1","properties":{"name":"Main Street & 1st Avenue","highway":"bus_stop"},"geometry":{"type":"Point","coordinates":[-100.0001,40.0001]}},
{"type":"Feature","id":"node/2","properties":{"name":"Main Street & 5th Avenue","highway":"bus_stop"},"geometry":{"type":"Point","coordinates":[-99.9751,40.0001]}}]}`
	if err := os.WriteFile("build/b1-stops.geojson", []byte(stops), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := NewObj()
	fo := NewObj()
	e := NewObj()
	e.Set("name", "Bus One")
	e.Set("gtfs", "gtfs/b1.zip")
	e.Set("rail", "rail/b1.geojson")
	e.Set("stops", "build/b1-stops.geojson")
	e.Set("out", "build/b1.geojson")
	e.Set("bbox", []any{-100.01, 39.99, -99.94, 40.01})
	fo.Set("b1", e)
	doc.Set("feeds", fo)
	if err := os.WriteFile("portolan.json", MarshalDoc(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	_, res := planAndRun(t, nil, true, bin)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	names := exportedStopNames(t, "build/export/b1.zip")
	if !strings.Contains(names, "Main Street & 1st Avenue") || !strings.Contains(names, "Main Street & 5th Avenue") {
		t.Fatalf("stops not renamed:\n%s", names)
	}
	st, err := LoadState("build/sync-state.json")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Feeds["b1"].Undrawn {
		t.Fatalf("state row %+v, want undrawn", st.Feeds["b1"])
	}

	_, res = planAndRun(t, nil, true, bin)
	if len(res.Rebuilt) > 0 || len(res.Errors) > 0 {
		t.Fatalf("second run rebuilt %v, errors %v", res.Rebuilt, res.Errors)
	}
}

func exportedStopNames(t *testing.T, path string) string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	f, err := zr.Open("stops.txt")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

