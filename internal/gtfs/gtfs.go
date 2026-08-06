// Package gtfs reads GTFS static zips directly — no database. routes.txt,
// trips.txt, shapes.txt are all portolan needs for line geometry; stops come
// later with the stations stage.
package gtfs

import (
	"archive/zip"
	"encoding/csv"
	"fmt"
	"io"
	"math"
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
	tripShape := map[string]string{}
	if err := eachRow(tf, func(get func(string) string) {
		k := key{get("route_id"), get("shape_id")}
		if k.shape != "" {
			tripCount[k]++
			tripShape[get("trip_id")] = k.shape
		}
	}); err != nil {
		return nil, err
	}

	// terminal stops per shape: service ends at the last stop — anything
	// beyond it (relay loops, turnaround pockets: the Bowling Green
	// balloon, the E 146 St lasso) is train movement, not transit, and
	// must not draw. stops.txt + a stop_times sweep give each shape its
	// first/last stop location.
	stopLL := map[string]geo.LL{}
	if sf, err := need("stops.txt"); err == nil {
		if err := eachRow(sf, func(get func(string) string) {
			lat, e1 := strconv.ParseFloat(get("stop_lat"), 64)
			lon, e2 := strconv.ParseFloat(get("stop_lon"), 64)
			if e1 == nil && e2 == nil {
				stopLL[get("stop_id")] = geo.LL{Lon: lon, Lat: lat}
			}
		}); err != nil {
			return nil, err
		}
	}
	type ends struct {
		firstSeq, lastSeq   int
		firstStop, lastStop string
		ok                  bool
	}
	shapeEnds := map[string]*ends{}
	shapeStops := map[string]map[string]bool{}
	if stf, err := need("stop_times.txt"); err == nil && len(stopLL) > 0 {
		if err := eachRow(stf, func(get func(string) string) {
			shape, ok := tripShape[get("trip_id")]
			if !ok {
				return
			}
			e := shapeEnds[shape]
			if e == nil {
				e = &ends{}
				shapeEnds[shape] = e
			}
			seq, err := strconv.Atoi(get("stop_sequence"))
			if err != nil {
				return
			}
			sid := get("stop_id")
			if shapeStops[shape] == nil {
				shapeStops[shape] = map[string]bool{}
			}
			shapeStops[shape][sid] = true
			if !e.ok || seq < e.firstSeq {
				e.firstSeq, e.firstStop = seq, sid
			}
			if !e.ok || seq > e.lastSeq {
				e.lastSeq, e.lastStop = seq, sid
			}
			e.ok = true
		}); err != nil {
			return nil, err
		}
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
		if e := shapeEnds[k.shape]; e != nil && e.ok {
			pts = trimToStops(pts, stopLL[e.firstStop], stopLL[e.lastStop])
			var sll []geo.LL
			for sid := range shapeStops[k.shape] {
				if ll, ok := stopLL[sid]; ok {
					sll = append(sll, ll)
				}
			}
			pts = exciseLoops(pts, sll)
		}
		if len(pts) < 2 {
			continue
		}
		byRoute[k.route] = append(byRoute[k.route], Pattern{
			Route: r, ShapeID: k.shape, Trips: n, Shape: pts,
		})
	}
	// deterministic route order and trip-count tie-break: map iteration and
	// an unstable sort let equal-trip patterns swap runs, flipping which
	// shapes make the coverage cut (and every downstream emission order)
	routeIDs := make([]string, 0, len(byRoute))
	for rid := range byRoute {
		routeIDs = append(routeIDs, rid)
	}
	sort.Strings(routeIDs)
	for _, rid := range routeIDs {
		pats := byRoute[rid]
		sort.Slice(pats, func(i, j int) bool {
			if pats[i].Trips != pats[j].Trips {
				return pats[i].Trips > pats[j].Trips
			}
			return pats[i].ShapeID < pats[j].ShapeID
		})
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

// trimToStops cuts a shape polyline to the span between its terminal
// stops: the EARLIEST point where the shape first reaches each terminal
// (within snap distance), plus a small margin. Turnaround trackage beyond
// the last stop — relay loops, yard leads — never draws (it is not
// service), and on a loop shape the earliest-arrival rule cuts exactly
// the return half.
func trimToStops(pts []geo.LL, first, last geo.LL) []geo.LL {
	if len(pts) < 2 || (first == geo.LL{}) || (last == geo.LL{}) {
		return pts
	}
	kx := 111320 * math.Cos(pts[0].Lat*math.Pi/180)
	const ky = 111320.0
	d2 := func(a geo.LL, b geo.LL) float64 {
		dx, dy := (a.Lon-b.Lon)*kx, (a.Lat-b.Lat)*ky
		return dx*dx + dy*dy
	}
	arc := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		arc[i] = arc[i-1] + math.Sqrt(d2(pts[i], pts[i-1]))
	}
	find := func(stop geo.LL) float64 {
		const snap2 = 45.0 * 45.0
		best, bestArc := math.Inf(1), 0.0
		for i, p := range pts {
			d := d2(p, stop)
			if d <= snap2 {
				return arc[i]
			}
			if d < best {
				best, bestArc = d, arc[i]
			}
		}
		return bestArc
	}
	a1, a2 := find(first), find(last)
	if a2 < a1 {
		a1, a2 = a2, a1
	}
	const margin = 40.0
	a1 -= margin
	a2 += margin
	if a2-a1 < 500 {
		return pts
	}
	var out []geo.LL
	for i, p := range pts {
		if arc[i] >= a1 && arc[i] <= a2 {
			out = append(out, p)
		}
	}
	if len(out) < 2 {
		return pts
	}
	return out
}


// exciseLoops removes stop-less self-returning excursions from a shape: a
// stretch that leaves the line, rings around (250–2500 m) and comes back
// within closeTol of where it left, serving NO stop on the way, is
// turnaround trackage (the Bowling Green loop rides mid-shape on THROUGH
// Woodlawn–New Lots shapes; relay rings at E 146 St) — train movement,
// not transit. Revenue loops are protected by their stops (the Chicago
// Loop has stations all the way around).
func exciseLoops(pts []geo.LL, stops []geo.LL) []geo.LL {
	if len(pts) < 4 {
		return pts
	}
	kx := 111320 * math.Cos(pts[0].Lat*math.Pi/180)
	const ky = 111320.0
	d2 := func(a, b geo.LL) float64 {
		dx, dy := (a.Lon-b.Lon)*kx, (a.Lat-b.Lat)*ky
		return dx*dx + dy*dy
	}
	const (
		closeTol = 60.0
		minRing  = 250.0
		maxRing  = 2500.0
		stopTol  = 70.0
	)
	for pass := 0; pass < 4; pass++ {
		arc := make([]float64, len(pts))
		for i := 1; i < len(pts); i++ {
			arc[i] = arc[i-1] + math.Sqrt(d2(pts[i], pts[i-1]))
		}
		cut := false
		for i := 0; i < len(pts)-2 && !cut; i++ {
			for j := i + 2; j < len(pts); j++ {
				ring := arc[j] - arc[i]
				if ring < minRing {
					continue
				}
				if ring > maxRing {
					break
				}
				if d2(pts[i], pts[j]) > closeTol*closeTol {
					continue
				}
				served := false
				for k := i + 1; k < j && !served; k++ {
					for _, sll := range stops {
						if d2(pts[k], sll) <= stopTol*stopTol {
							served = true
							break
						}
					}
				}
				if served {
					continue
				}
				pts = append(pts[:i+1], pts[j:]...)
				cut = true
				break
			}
		}
		if !cut {
			break
		}
	}
	return pts
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
