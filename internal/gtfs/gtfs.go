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
	"strings"
	"sync"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

type Route struct {
	ID        string
	ShortName string
	LongName  string
	Color     string // hex without '#', GTFS convention
	Type      int    // GTFS route_type
	Agency    string // agency_id — trunk-key fallback for colorless regional
	// SortOrder: route_sort_order, the feed's own presentation order
	// (MBTA, TriMet and PATH ship it); -1 when absent.
	SortOrder int
}

// Pattern is one distinct (route, shape) service pattern.
type Pattern struct {
	Route   Route
	ShapeID string
	Trips   int      // how many trips ride this shape (for coverage pruning)
	Shape   []geo.LL // ordered shape points
	StopIDs []string // every stop this shape serves, sorted (stations stage)
	// TermA/TermB: the pattern's first/last STOP locations. Shapes (and
	// the trim margin) overrun the terminal by tail trackage; these are
	// where service actually ends — the terminal-cut pass cuts here.
	TermA, TermB geo.LL
}

// Stop is one stops.txt record — platform or parent station. Only what
// the stations stage needs: where it is, what to call it, and how to
// group platforms under one roof.
type Stop struct {
	Name   string
	LL     geo.LL
	Parent string // parent_station, "" when the stop stands alone
}

type Feed struct {
	Routes   map[string]Route
	Patterns []Pattern
	Agencies map[string]string // agency_id → agency_name (labels for agency trunks)
	Stops    map[string]Stop   // stop_id → stop, platforms AND parent stations
	// Transfers: stop-id pairs a rider can transfer between without
	// paying again (transfers.txt rows with type ≠ 3). This is the
	// ground truth for which same-named stations are one complex and
	// which just share a name across the street (NYC's Rector St).
	Transfers [][2]string
}

// Load reads a GTFS zip and returns patterns covering ≥ coverFrac of each
// route's trips (agencies ship dozens of one-off shape variants; they are
// noise — LESSONS: prune, don't draw them all).
func Load(path string, coverFrac float64) (*Feed, error) {
	return LoadFiltered(path, coverFrac, nil)
}

// LoadFiltered is Load restricted to routes the keep predicate accepts
// (nil keeps everything). Filtering happens at the trips pass, so the
// stop_times sweep — by far the largest file — skips foreign trips with
// one map miss, and shapes only parse when a kept trip references them.
// Per-route coverage pruning is independent per route and downstream
// consumers re-filter by route type, so the kept routes' patterns are
// identical to an unfiltered load's.
func LoadFiltered(path string, coverFrac float64, keep func(Route) bool) (*Feed, error) {
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

	feed := &Feed{Routes: map[string]Route{}, Agencies: map[string]string{},
		Stops: map[string]Stop{}}
	if af, ok := files["agency.txt"]; ok {
		if err := eachRowCols(af, []string{"agency_id", "agency_name"},
			func(v []string) { feed.Agencies[v[0]] = v[1] }); err != nil {
			return nil, err
		}
	}

	// phase 1: routes ∥ stops (independent files, own maps)
	rf, err := need("routes.txt")
	if err != nil {
		return nil, err
	}
	stopLL := map[string]geo.LL{}
	var wg sync.WaitGroup
	var routesErr, stopsErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		routesErr = eachRowCols(rf, []string{"route_id", "route_type",
			"route_short_name", "route_long_name", "route_color", "agency_id",
			"route_sort_order"},
			func(v []string) {
				t, _ := strconv.Atoi(v[1])
				so := -1
				if v[6] != "" {
					if n, err := strconv.Atoi(v[6]); err == nil {
						so = n
					}
				}
				feed.Routes[v[0]] = Route{
					ID: v[0], ShortName: v[2], LongName: v[3],
					Color: v[4], Type: t, Agency: v[5], SortOrder: so,
				}
			})
	}()
	// terminal stops per shape: service ends at the last stop — anything
	// beyond it (relay loops, turnaround pockets: the Bowling Green
	// balloon, the E 146 St lasso) is train movement, not transit, and
	// must not draw. stops.txt + a stop_times sweep give each shape its
	// first/last stop location.
	if sf, err := need("stops.txt"); err == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopsErr = eachRowCols(sf,
				[]string{"stop_id", "stop_lat", "stop_lon", "stop_name", "parent_station"},
				func(v []string) {
					lat, e1 := strconv.ParseFloat(v[1], 64)
					lon, e2 := strconv.ParseFloat(v[2], 64)
					if e1 == nil && e2 == nil {
						stopLL[v[0]] = geo.LL{Lon: lon, Lat: lat}
						feed.Stops[v[0]] = Stop{Name: v[3],
							LL: geo.LL{Lon: lon, Lat: lat}, Parent: v[4]}
					}
				})
		}()
	}
	wg.Wait()
	if routesErr != nil {
		return nil, routesErr
	}
	if stopsErr != nil {
		return nil, stopsErr
	}
	// transfers.txt is small and optional — read it inline
	if tf, ok := files["transfers.txt"]; ok {
		if err := eachRowCols(tf, []string{"from_stop_id", "to_stop_id", "transfer_type"},
			func(v []string) {
				if v[0] != "" && v[1] != "" && v[2] != "3" {
					feed.Transfers = append(feed.Transfers, [2]string{v[0], v[1]})
				}
			}); err != nil {
			return nil, err
		}
	}
	// agency_id is optional in routes.txt when the feed has one agency
	// (LIRR ships without the column entirely) — backfill it, or the
	// agency trunk key has nothing to hold onto
	if len(feed.Agencies) == 1 {
		var only string
		for id := range feed.Agencies {
			only = id
		}
		for id, r := range feed.Routes {
			if r.Agency == "" {
				r.Agency = only
				feed.Routes[id] = r
			}
		}
	}

	// phase 2: trips (needs Routes for the keep predicate)
	tf, err := need("trips.txt")
	if err != nil {
		return nil, err
	}
	type key struct{ route, shape string }
	tripCount := map[key]int{}
	tripShape := map[string]string{}
	if err := eachRowCols(tf, []string{"route_id", "shape_id", "trip_id"},
		func(v []string) {
			if keep != nil && !keep(feed.Routes[v[0]]) {
				return
			}
			if v[1] != "" {
				tripCount[key{v[0], v[1]}]++
				tripShape[v[2]] = v[1]
			}
		}); err != nil {
		return nil, err
	}
	shapeNeeded := map[string]bool{}
	for k := range tripCount {
		shapeNeeded[k.shape] = true
	}

	// phase 3: stop_times ∥ shapes (both keyed off the kept trips)
	type ends struct {
		firstSeq, lastSeq   int
		firstStop, lastStop string
		ok                  bool
	}
	shapeEnds := map[string]*ends{}
	shapeStops := map[string]map[string]bool{}
	type spt struct {
		seq int
		ll  geo.LL
	}
	shapes := map[string][]spt{}
	var stErr, shErr error
	if stf, err := need("stop_times.txt"); err == nil && len(stopLL) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stErr = eachRowCols(stf, []string{"trip_id", "stop_sequence", "stop_id"},
				func(v []string) {
					shape, ok := tripShape[v[0]]
					if !ok {
						return
					}
					e := shapeEnds[shape]
					if e == nil {
						e = &ends{}
						shapeEnds[shape] = e
					}
					seq, err := strconv.Atoi(v[1])
					if err != nil {
						return
					}
					sid := v[2]
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
				})
		}()
	}
	sf2, err := need("shapes.txt")
	if err != nil {
		wg.Wait() // the stop_times goroutine must not outlive the zip close
		return nil, err
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		shErr = eachRowCols(sf2, []string{"shape_id", "shape_pt_lat",
			"shape_pt_lon", "shape_pt_sequence"},
			func(v []string) {
				if keep != nil && !shapeNeeded[v[0]] {
					return
				}
				lat, e1 := strconv.ParseFloat(v[1], 64)
				lon, e2 := strconv.ParseFloat(v[2], 64)
				seq, e3 := strconv.Atoi(v[3])
				if e1 == nil && e2 == nil && e3 == nil {
					shapes[v[0]] = append(shapes[v[0]], spt{seq, geo.LL{Lon: lon, Lat: lat}})
				}
			})
	}()
	wg.Wait()
	if stErr != nil {
		return nil, stErr
	}
	if shErr != nil {
		return nil, shErr
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
		var sids []string
		for sid := range shapeStops[k.shape] {
			sids = append(sids, sid)
		}
		sort.Strings(sids)
		pat := Pattern{
			Route: r, ShapeID: k.shape, Trips: n, Shape: pts, StopIDs: sids,
		}
		if e := shapeEnds[k.shape]; e != nil && e.ok {
			pat.TermA, pat.TermB = stopLL[e.firstStop], stopLL[e.lastStop]
		}
		byRoute[k.route] = append(byRoute[k.route], pat)
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

// eachRowCols streams a CSV member resolving the wanted columns to indices
// once, with csv.ReuseRecord (field strings are substrings of a fresh
// per-record backing string, so retaining them is safe). Missing columns
// and short rows read as "" — the same fallback eachRow's get() applies.
func eachRowCols(f *zip.File, cols []string, fn func(vals []string)) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(rc)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil {
		return err
	}
	if len(header) > 0 && len(header[0]) > 2 && header[0][0] == 0xEF {
		header[0] = header[0][3:]
	}
	idxs := make([]int, len(cols))
	for j, c := range cols {
		idxs[j] = -1
		for i, h := range header {
			// Metra ships "route_id, route_short_name, …" — spaces after
			// every comma, in headers AND values
			if strings.TrimSpace(h) == c {
				idxs[j] = i
				break
			}
		}
	}
	vals := make([]string, len(cols))
	for {
		row, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		for j, ix := range idxs {
			if ix < 0 || ix >= len(row) {
				vals[j] = ""
			} else {
				vals[j] = strings.TrimSpace(row[ix])
			}
		}
		fn(vals)
	}
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
		fn(func(col string) string {
			i, ok := idx[col]
			if !ok || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		})
	}
}
