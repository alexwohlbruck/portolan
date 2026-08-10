package gtfs

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A feed is a zip, a directory, or a set of in-memory tables. All three
// must produce the same Feed — the whole point of the abstraction is
// that an interactive caller stops writing a zip per rebuild without
// getting different output for it.

var tinyTables = Tables{
	"agency.txt": "agency_id,agency_name\nA,Authored\n",
	"routes.txt": "route_id,agency_id,route_short_name,route_type,route_color\n" +
		"R1,A,1,1,EE352E\nR2,A,2,1,0039A6\n",
	"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\n" +
		"s1,Alpha,40.700,-74.000\ns2,Beta,40.700,-73.990\n",
	"trips.txt": "route_id,service_id,trip_id\nR1,wk,t1\nR2,wk,t2\n",
	"stop_times.txt": "trip_id,stop_sequence,stop_id\n" +
		"t1,1,s1\nt1,2,s2\nt2,1,s2\nt2,2,s1\n",
}

func writeZip(t *testing.T, dir string, tables Tables, prefix string) string {
	t.Helper()
	path := filepath.Join(dir, "feed.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range tables {
		w, err := zw.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeDir(t *testing.T, dir string, tables Tables) string {
	t.Helper()
	sub := filepath.Join(dir, "feed")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range tables {
		if err := os.WriteFile(filepath.Join(sub, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sub
}

// summary reduces a Feed to what a caller can observe, so the three
// sources can be compared without depending on map order.
func summary(t *testing.T, f *Feed) string {
	t.Helper()
	var b strings.Builder
	ids := make([]string, 0, len(f.Routes))
	for id := range f.Routes {
		ids = append(ids, id)
	}
	sortStrings(ids)
	for _, id := range ids {
		r := f.Routes[id]
		b.WriteString(r.ID + "|" + r.ShortName + "|" + r.Color + "|" + r.Agency + "\n")
	}
	sids := make([]string, 0, len(f.Stops))
	for id := range f.Stops {
		sids = append(sids, id)
	}
	sortStrings(sids)
	for _, id := range sids {
		b.WriteString(id + "|" + f.Stops[id].Name + "\n")
	}
	b.WriteString("patterns:")
	for _, p := range f.Patterns {
		b.WriteString(" " + p.Route.ID + "/" + strings.Join(p.StopSeq, ">"))
	}
	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestZipDirectoryAndInlineAgree(t *testing.T) {
	dir := t.TempDir()
	zipPath := writeZip(t, dir, tinyTables, "")
	dirPath := writeDir(t, dir, tinyTables)

	fromZip, err := LoadFiltered(zipPath, 0.99, nil)
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	fromDir, err := LoadFiltered(dirPath, 0.99, nil)
	if err != nil {
		t.Fatalf("directory: %v", err)
	}
	fromMem, err := LoadTables(tinyTables, 0.99, nil)
	if err != nil {
		t.Fatalf("inline: %v", err)
	}

	z, d, m := summary(t, fromZip), summary(t, fromDir), summary(t, fromMem)
	if z != d {
		t.Errorf("directory differs from zip:\n--- zip ---\n%s\n--- dir ---\n%s", z, d)
	}
	if z != m {
		t.Errorf("inline differs from zip:\n--- zip ---\n%s\n--- inline ---\n%s", z, m)
	}
	if len(fromZip.Routes) != 2 {
		t.Errorf("got %d routes, want 2", len(fromZip.Routes))
	}
}

func TestZipWithAContainingFolder(t *testing.T) {
	// agencies ship feeds zipped from a containing folder as often as
	// from the files themselves; asking for "routes.txt" must find it
	dir := t.TempDir()
	path := writeZip(t, dir, tinyTables, "gtfs/")
	f, err := LoadFiltered(path, 0.99, nil)
	if err != nil {
		t.Fatalf("nested zip: %v", err)
	}
	if len(f.Routes) != 2 {
		t.Errorf("got %d routes, want 2", len(f.Routes))
	}
}

func TestInlineNamesTolerateAMissingSuffix(t *testing.T) {
	// a caller assembling JSON reaches for "routes" before "routes.txt"
	bare := Tables{}
	for name, body := range tinyTables {
		bare[strings.TrimSuffix(name, ".txt")] = body
	}
	f, err := LoadTables(bare, 0.99, nil)
	if err != nil {
		t.Fatalf("bare names: %v", err)
	}
	if len(f.Routes) != 2 {
		t.Errorf("got %d routes, want 2", len(f.Routes))
	}
}

func TestInlineWithoutRoutesIsNamedNotGuessed(t *testing.T) {
	err := Tables{"stops.txt": "stop_id\ns1\n"}.Valid()
	if err == nil {
		t.Fatal("tables with no routes.txt must be rejected")
	}
	if !strings.Contains(err.Error(), "routes.txt") {
		t.Errorf("error should name what is missing, got: %v", err)
	}
}

func TestShapelessInlineFeedBuildsPatternsFromStopOrder(t *testing.T) {
	f, err := LoadTables(tinyTables, 0.99, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Patterns) != 2 {
		t.Fatalf("got %d patterns, want 2 (one per distinct stop sequence)", len(f.Patterns))
	}
	for _, p := range f.Patterns {
		if len(p.StopSeq) != 2 {
			t.Errorf("%s: StopSeq %v, want two stops in riding order", p.Route.ID, p.StopSeq)
		}
		if len(p.Shape) != 0 {
			t.Errorf("%s: a shapeless feed must not invent geometry", p.Route.ID)
		}
	}
	// the two routes ride opposite ways, so their sequences must differ —
	// this is the ordering the corridor traversal depends on
	if f.Patterns[0].StopSeq[0] == f.Patterns[1].StopSeq[0] {
		t.Error("both patterns start at the same stop; riding order was lost")
	}
}
