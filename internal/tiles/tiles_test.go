package tiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A station's OSM match is only useful to a consumer holding a feed's stop
// id, which is what stops.json publishes. Proximity and name both fail at a
// station like Chambers St — three of them, and the nearest mapped node to
// the J/Z platform belongs to Brooklyn Bridge — so the exact join is the
// whole point of the file.
func TestWriteStopIndex(t *testing.T) {
	dir := t.TempDir()
	pts := []point{
		{props: map[string]any{
			"ftype":    "station",
			"osm":      "node/2052618392",
			"gtfs_ids": "f-dr5r-nyctsubway:M21;f-dr5r-nyctsubway:M21N",
		}},
		{props: map[string]any{
			"ftype":    "marker",
			"osm":      "node/8410411844",
			"gtfs_ids": "f-dr5r-nyctsubway:640",
		}},
		// No confident match: contributes nothing rather than an empty value,
		// so "not matched" stays distinguishable from "matched to nothing".
		{props: map[string]any{
			"ftype":    "station",
			"gtfs_ids": "f-rioc~nyc:2437315",
		}},
	}

	if err := writeStopIndex(Opts{Out: dir}, pts); err != nil {
		t.Fatalf("writeStopIndex: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "stops.json"))
	if err != nil {
		t.Fatalf("read stops.json: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]string{
		"f-dr5r-nyctsubway:M21":  "node/2052618392",
		"f-dr5r-nyctsubway:M21N": "node/2052618392",
		"f-dr5r-nyctsubway:640":  "node/8410411844",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// Nothing to say means no file, not an empty one.
func TestWriteStopIndexEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := writeStopIndex(Opts{Out: dir}, []point{
		{props: map[string]any{"ftype": "station", "gtfs_ids": "f:1"}},
	}); err != nil {
		t.Fatalf("writeStopIndex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stops.json")); !os.IsNotExist(err) {
		t.Errorf("expected no stops.json, got err=%v", err)
	}
}
