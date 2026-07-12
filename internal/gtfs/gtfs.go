// Package gtfs reads GTFS static zips directly — no database. routes.txt,
// trips.txt, shapes.txt are all portolan needs for line geometry; stops come
// later with the stations stage.
package gtfs

import (
	"archive/zip"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

type Route struct {
	ID        string
	ShortName string
	LongName  string
	Color     string // hex without '#', GTFS convention
	Type      int    // GTFS route_type
}

// Pattern is one distinct (route, shape) service pattern.
type Pattern struct {
	Route   Route
	ShapeID string
	Trips   int      // how many trips ride this shape (for coverage pruning)
	Shape   []geo.LL // ordered shape points
}

type Feed struct {
	Routes   map[string]Route
	Patterns []Pattern
}

// Load reads a GTFS zip and returns patterns covering ≥ coverFrac of each
// route's trips (agencies ship dozens of one-off shape variants; they are
// noise — LESSONS: prune, don't draw them all).
func Load(path string, coverFrac float64) (*Feed, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	need := func(name string) (*zip.File, error) {
		if f, ok := files[name]; ok {
			return f, nil
		}
		return nil, fmt.Errorf("gtfs: %s missing %s", path, name)
	}

	feed := &Feed{Routes: map[string]Route{}}

	rf, err := need("routes.txt")
	if err != nil {
		return nil, err
	}
	if err := eachRow(rf, func(get func(string) string) {
		id := get("route_id")
		t, _ := strconv.Atoi(get("route_type"))
		feed.Routes[id] = Route{
			ID: id, ShortName: get("route_short_name"),
			LongName: get("route_long_name"),
			Color:    get("route_color"), Type: t,
		}
	}); err != nil {
		return nil, err
	}

	tf, err := need("trips.txt")
	if err != nil {
		return nil, err
	}
	type key struct{ route, shape string }
	tripCount := map[key]int{}
	if err := eachRow(tf, func(get func(string) string) {
		k := key{get("route_id"), get("shape_id")}
		if k.shape != "" {
			tripCount[k]++
		}
	}); err != nil {
		return nil, err
	}

	sf, err := need("shapes.txt")
	if err != nil {
		return nil, err
	}
	type spt struct {
		seq int
		ll  geo.LL
	}
	shapes := map[string][]spt{}
	if err := eachRow(sf, func(get func(string) string) {
		id := get("shape_id")
		lat, e1 := strconv.ParseFloat(get("shape_pt_lat"), 64)
		lon, e2 := strconv.ParseFloat(get("shape_pt_lon"), 64)
		seq, e3 := strconv.Atoi(get("shape_pt_sequence"))
		if e1 == nil && e2 == nil && e3 == nil {
			shapes[id] = append(shapes[id], spt{seq, geo.LL{Lon: lon, Lat: lat}})
		}
	}); err != nil {
		return nil, err
	}
	for id := range shapes {
		s := shapes[id]
		sort.Slice(s, func(i, j int) bool { return s[i].seq < s[j].seq })
		shapes[id] = s
	}

	// per route: keep the largest patterns until coverFrac of trips covered
	byRoute := map[string][]Pattern{}
	for k, n := range tripCount {
		r, ok := feed.Routes[k.route]
		if !ok {
			continue
		}
		raw, ok := shapes[k.shape]
		if !ok || len(raw) < 2 {
			continue
		}
		pts := make([]geo.LL, len(raw))
		for i, sp := range raw {
			pts[i] = sp.ll
		}
		byRoute[k.route] = append(byRoute[k.route], Pattern{
			Route: r, ShapeID: k.shape, Trips: n, Shape: pts,
		})
	}
	for _, pats := range byRoute {
		sort.Slice(pats, func(i, j int) bool { return pats[i].Trips > pats[j].Trips })
		total := 0
		for _, p := range pats {
			total += p.Trips
		}
		acc := 0
		for _, p := range pats {
			feed.Patterns = append(feed.Patterns, p)
			acc += p.Trips
			if float64(acc) >= coverFrac*float64(total) {
				break
			}
		}
	}
	return feed, nil
}

func eachRow(f *zip.File, fn func(get func(string) string)) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(rc)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	header, err := r.Read()
	if err != nil {
		return err
	}
	// strip BOM on first header cell
	if len(header) > 0 && len(header[0]) > 2 && header[0][0] == 0xEF {
		header[0] = header[0][3:]
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[h] = i
	}
	for {
		row, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		fn(func(col string) string {
			i, ok := idx[col]
			if !ok || i >= len(row) {
				return ""
			}
			return row[i]
		})
	}
}
