package gtfs

// RailShapes — the raw shape polylines of a feed's rail-typed routes,
// exactly as tools/groups.py reads them. This is the measurement input
// for cross-feed grouping (internal/sync/groups.go), and it deliberately
// bypasses Load's pattern machinery: no coverage pruning, no terminal
// trimming, no loop excision. Grouping asks where the feed's trains RUN,
// and every published shape answers that; the drawing pipeline's
// clean-ups would change the measurement.
//
// Parity notes (the Python original is the contract):
//   - route_type matches railTypes as a STRING — an absent or empty
//     route_type is not rail, where Load's Atoi would read it as 0.
//   - rows shorter than the header are skipped whole, like csv.DictReader
//     fed through groups.py's rows().
//   - a malformed number in shapes.txt is an ERROR — groups.py raises and
//     the caller skips the feed as unreadable, so we do too.
//   - one deviation: member files resolve through feedFiles, which also
//     finds tables zipped under a folder ("gtfs/routes.txt"). groups.py
//     checks top-level names only and would return nothing for such a
//     feed; being tolerant here is strictly more correct.

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// RailShapes returns each rail-typed shape's ordered points, in
// first-seen shapes.txt order. railTypes holds route_type strings
// ("2", "109", …). A feed missing routes/trips/shapes, or with no rail
// routes, returns nil, nil.
func RailShapes(path string, railTypes map[string]bool) ([][]geo.LL, error) {
	files, closeFeed, err := feedFiles(path)
	if err != nil {
		return nil, err
	}
	defer closeFeed()
	for _, n := range []string{"routes.txt", "trips.txt", "shapes.txt"} {
		if _, ok := files[n]; !ok {
			return nil, nil
		}
	}

	rail := map[string]bool{}
	if err := eachRowStrict(files["routes.txt"], func(get func(string) string) error {
		if railTypes[get("route_type")] {
			rail[get("route_id")] = true
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("%s: routes.txt: %w", path, err)
	}
	if len(rail) == 0 {
		return nil, nil
	}
	shapes := map[string]bool{}
	if err := eachRowStrict(files["trips.txt"], func(get func(string) string) error {
		if rail[get("route_id")] && get("shape_id") != "" {
			shapes[get("shape_id")] = true
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("%s: trips.txt: %w", path, err)
	}
	if len(shapes) == 0 {
		return nil, nil
	}

	type spt struct {
		seq, lon, lat float64
	}
	by := map[string][]spt{}
	var order []string
	if err := eachRowStrict(files["shapes.txt"], func(get func(string) string) error {
		id := get("shape_id")
		if !shapes[id] {
			return nil
		}
		seq, e1 := strconv.ParseFloat(get("shape_pt_sequence"), 64)
		lon, e2 := strconv.ParseFloat(get("shape_pt_lon"), 64)
		lat, e3 := strconv.ParseFloat(get("shape_pt_lat"), 64)
		if e1 != nil || e2 != nil || e3 != nil {
			return fmt.Errorf("bad shape point for %s", id)
		}
		if _, ok := by[id]; !ok {
			order = append(order, id)
		}
		by[id] = append(by[id], spt{seq, lon, lat})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("%s: shapes.txt: %w", path, err)
	}

	out := make([][]geo.LL, 0, len(order))
	for _, id := range order {
		pts := by[id]
		// (seq, lon, lat) tuple sort, exactly like Python's pts.sort()
		sort.Slice(pts, func(i, j int) bool {
			if pts[i].seq != pts[j].seq {
				return pts[i].seq < pts[j].seq
			}
			if pts[i].lon != pts[j].lon {
				return pts[i].lon < pts[j].lon
			}
			return pts[i].lat < pts[j].lat
		})
		poly := make([]geo.LL, len(pts))
		for i, p := range pts {
			poly[i] = geo.LL{Lon: p.lon, Lat: p.lat}
		}
		out = append(out, poly)
	}
	return out, nil
}

// eachRowStrict streams a CSV member with groups.py's row semantics:
// header cells stripped, rows shorter than the header skipped whole,
// every value stripped, duplicate header names resolved last-wins (like
// dict(zip(head, row))). fn may return an error to abort the file.
func eachRowStrict(open opener, fn func(get func(string) string) error) error {
	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(rc)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	header, err := r.Read()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	if len(header) > 0 && len(header[0]) > 2 && header[0][0] == 0xEF {
		header[0] = header[0][3:]
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(h)] = i
	}
	for {
		row, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if len(row) < len(header) {
			continue // blank or truncated line
		}
		if err := fn(func(col string) string {
			i, ok := idx[col]
			if !ok || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}); err != nil {
			return err
		}
	}
}
