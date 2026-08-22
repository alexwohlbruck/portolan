package sync

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// member is one zip entry, in the order it should be written — the whole
// point of several tests is that write order must not matter.
type member struct{ name, body string }

func zipBytes(t *testing.T, members []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		w, err := zw.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeZip(t *testing.T, members []member) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feed.zip")
	if err := os.WriteFile(path, zipBytes(t, members), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestContentHashOrderIndependent(t *testing.T) {
	a := writeZip(t, []member{
		{"routes.txt", "route_id\nA\n"},
		{"stops.txt", "stop_id\n1\n"},
		{"trips.txt", "trip_id\nt1\n"},
	})
	b := writeZip(t, []member{
		{"trips.txt", "trip_id\nt1\n"},
		{"stops.txt", "stop_id\n1\n"},
		{"routes.txt", "route_id\nA\n"},
	})
	ha, err := ContentHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := ContentHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("same members, different order: %s != %s", ha, hb)
	}
}

func TestContentHashSeesContent(t *testing.T) {
	a := writeZip(t, []member{{"routes.txt", "route_id\nA\n"}})
	b := writeZip(t, []member{{"routes.txt", "route_id\nB\n"}})
	ha, _ := ContentHash(a)
	hb, _ := ContentHash(b)
	if ha == hb {
		t.Error("different content, same hash")
	}
}

func TestContentHashIgnoresNonTxt(t *testing.T) {
	a := writeZip(t, []member{{"routes.txt", "route_id\nA\n"}})
	b := writeZip(t, []member{
		{"routes.txt", "route_id\nA\n"},
		{"shapes.geojson", "{}"},
	})
	ha, _ := ContentHash(a)
	hb, _ := ContentHash(b)
	if ha != hb {
		t.Error("a non-.txt member changed the hash")
	}
}

func TestContentHashNotAZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.zip")
	os.WriteFile(path, []byte("<html>rate limited</html>"), 0o644)
	if _, err := ContentHash(path); err == nil {
		t.Error("hashed an HTML page as a feed")
	}
}
