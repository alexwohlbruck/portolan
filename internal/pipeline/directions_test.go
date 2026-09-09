package pipeline

import (
	"archive/zip"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/style"
)

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func readZipFile(t *testing.T, path, want string) (string, bool) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, e := range zr.File {
		if filepath.Base(e.Name) == want {
			r, err := e.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			var sb strings.Builder
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				sb.Write(buf[:n])
				if err != nil {
					break
				}
			}
			return sb.String(), true
		}
	}
	return "", false
}

const mtaish = "route_id,agency_id,route_short_name,route_long_name\n" +
	"4,MTA NYCT,4,Lexington Avenue Express\n" +
	"L,MTA NYCT,L,14 St-Canarsie Local\n"

const mtaTrips = "route_id,trip_id,direction_id,trip_headsign\n" +
	"4,t1,0,Woodlawn\n4,t2,1,Crown Hts-Utica Av\n" +
	"L,t3,0,8 Av\nL,t4,1,Canarsie-Rockaway Pkwy\n"

// The MTA publishes no directions.txt at all — that is the case this exists
// for. A feed that has none gains one, because direction_id is an opaque 0/1
// until something names it.
func TestExportNamesDirectionsAFeedDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "mta-subway.zip")
	writeZip(t, src, map[string]string{"routes.txt": mtaish, "trips.txt": mtaTrips})

	sty := style.New(style.Doc{
		Agencies: map[string]style.Entity{
			"MTA NYCT": {Directions: map[string]string{"0": "Uptown", "1": "Downtown"}},
		},
		Routes: map[string]style.Entity{
			"L": {Directions: map[string]string{"0": "8 Av", "1": "Canarsie"}},
		},
	}.Config())

	dirs, err := resolveDirections(src, sty)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.zip")
	if err := rewriteZip(src, dst, nil, dirs); err != nil {
		t.Fatal(err)
	}

	body, ok := readZipFile(t, dst, "directions.txt")
	if !ok {
		t.Fatal("no directions.txt was written")
	}
	got := map[string]string{}
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs[1:] {
		got[r[0]+"/"+r[1]] = r[2]
	}
	// the agency's naming reaches the 4...
	if got["4/0"] != "Uptown" || got["4/1"] != "Downtown" {
		t.Errorf("4 = %q/%q, want Uptown/Downtown", got["4/0"], got["4/1"])
	}
	// ...and the L says what the L's own signs say
	if got["L/0"] != "8 Av" || got["L/1"] != "Canarsie" {
		t.Errorf("L = %q/%q, want 8 Av/Canarsie", got["L/0"], got["L/1"])
	}
}

// 367 of the 1499 feeds in the fleet already publish directions.txt. Curation
// corrects rows without discarding the agency's own work.
func TestExportMergesIntoAFeedsOwnDirections(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "feed.zip")
	writeZip(t, src, map[string]string{
		"routes.txt": mtaish,
		"trips.txt":  mtaTrips,
		"directions.txt": "route_id,direction_id,direction\n" +
			"4,0,NORTHBOUND\n4,1,SOUTHBOUND\n",
	})

	sty := style.New(style.Doc{Routes: map[string]style.Entity{
		"4": {Directions: map[string]string{"0": "Uptown"}},
	}}.Config())

	dirs, err := resolveDirections(src, sty)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.zip")
	if err := rewriteZip(src, dst, nil, dirs); err != nil {
		t.Fatal(err)
	}
	body, _ := readZipFile(t, dst, "directions.txt")
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range recs[1:] {
		got[r[0]+"/"+r[1]] = r[2]
	}
	if got["4/0"] != "Uptown" {
		t.Errorf("4/0 = %q, want the curated Uptown", got["4/0"])
	}
	// the row nobody curated keeps the agency's own word
	if got["4/1"] != "SOUTHBOUND" {
		t.Errorf("4/1 = %q, want the feed's own SOUTHBOUND kept", got["4/1"])
	}
}

// A pair nobody named is left out rather than guessed — a board showing the
// wrong compass point is worse than one showing none.
func TestExportWritesNothingWhenNothingIsNamed(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "feed.zip")
	writeZip(t, src, map[string]string{"routes.txt": mtaish, "trips.txt": mtaTrips})

	dirs, err := resolveDirections(src, style.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Fatalf("named %d directions with no curation: %v", len(dirs), dirs)
	}
	dst := filepath.Join(dir, "out.zip")
	if err := rewriteZip(src, dst, nil, dirs); err != nil {
		t.Fatal(err)
	}
	if _, ok := readZipFile(t, dst, "directions.txt"); ok {
		t.Error("wrote a directions.txt with nothing to say")
	}
}
