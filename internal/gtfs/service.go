// Service scenarios: the calendar view of a feed. NYC (and most big
// systems) runs different service by time of day and day of week — routes
// short-turn, swap tracks, or vanish entirely at night and on weekends.
// Instead of drawing the union of everything that ever runs, we derive the
// distinct SERVICE SCENARIOS a rider can actually experience, and each one
// gets its own full pipeline layout — so toggling time in the viewer swaps
// between complete, coherent maps: lines stay bundled and connected because
// the layout was optimized for exactly that set.
//
// Two scenarios are THE SAME MAP when they draw the same ink: per color,
// the dilated raster footprint of their patterns. That equivalence is what
// keeps the scenario count small — trip mixes churn every hour (put-ins,
// short-turns, opposite-direction shape pairs, expresses sharing the local
// corridor) without changing what is drawn, while a night reroute or a
// weekend-suspended branch changes the ink and starts a new scenario.
//
// Generic by construction: no route names, no city-specific buckets — the
// scenarios fall out of weekly day masks, stop_times.txt trip spans and
// shapes.txt geometry.
//
// Calendars come from calendar.txt where a feed has one. Where it does NOT
// — the MTA railroads (LIRR, Metro-North) ship calendar_dates.txt only,
// with a separate service_id per date — the weekly mask is derived from
// the dates themselves: each added date votes for its weekday, weighted by
// how often it recurs. A service running ten Wednesdays reads as Wednesday
// service; a one-off holiday date carries weight 1 and dies against the
// per-route floor in groupPatterns. For feeds that DO have calendar.txt,
// calendar_dates is still ignored — the map shows regular service, not
// holidays.
package gtfs

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PatKey identifies one (route, shape) service pattern.
type PatKey struct{ Route, Shape string }

// ServiceInfo holds, for every pattern, how many of its trips are in
// service during each (day-of-week, hour) cell. Day 0 = Monday. Trips that
// run past midnight (GTFS hours ≥ 24) spill into the following day's cells.
//
// Two pattern sets live here and the difference is load-bearing.
// Activity covers every DRAWABLE pattern — buses, ferries, the lot — and
// is what a chosen scenario is SELECTED with, so a Saturday map drops the
// weekday-only bus as readily as the weekday-only subway line. derive is
// the rail-family subset that scenario DERIVATION runs on: hundreds of bus
// patterns churning their own timetables would shatter the week into
// dozens of near-identical maps, and the rail ink is what a rider reads a
// service change off anyway.
type ServiceInfo struct {
	Activity map[PatKey]*[7][24]int
	derive   map[PatKey]bool
	// shapeOf maps a pattern to its geometry key: shape ids are only
	// unique WITHIN a feed, and a multi-feed city merges several.
	shapeOf    map[PatKey]string
	shapes     map[string][]spt2 // downsampled shape polylines, local meters
	routeColor map[string]string
}

// ServiceOpts injects the mode policy the gtfs package must not know
// (internal/mode imports gtfs, so the dependency only runs one way).
// Drawable selects the patterns a scenario can contain; Derive selects
// the rail-family subset the scenarios are derived from. Nil means "all".
type ServiceOpts struct {
	Drawable func(routeType int) bool
	Derive   func(routeType int) bool
}

func (o ServiceOpts) drawable(t int) bool {
	return o.Drawable == nil || o.Drawable(t)
}

func (o ServiceOpts) derive(t int) bool {
	return o.Derive == nil || o.Derive(t)
}

type spt2 struct{ x, y float64 }

// LoadService reads one feed. See LoadServiceFeeds for the comma-list form
// a multi-feed city needs.
func LoadService(path string) (*ServiceInfo, error) {
	return LoadServiceFeeds(path, ServiceOpts{})
}

// LoadServiceFeeds loads every feed in a comma list (primary first, overlay
// feeds after) and merges them into one weekly picture. Overlay route ids
// carry the same f<i>: prefix the pipeline's loader applies, so the pattern
// keys a scenario names are the very keys the built map is filtered by —
// without that, a scenario derived from the primary feed silently dropped
// every overlay pattern and NYC's "Saturday" map came out subway-only.
func LoadServiceFeeds(paths string, opts ServiceOpts) (*ServiceInfo, error) {
	si := &ServiceInfo{
		Activity: map[PatKey]*[7][24]int{}, derive: map[PatKey]bool{},
		shapeOf: map[PatKey]string{}, shapes: map[string][]spt2{},
		routeColor: map[string]string{},
	}
	parts := strings.Split(paths, ",")
	var firstErr error
	loaded := 0
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		pre := ""
		if i > 0 {
			pre = fmt.Sprintf("f%d:", i)
		}
		if err := si.loadOne(p, pre, opts); err != nil {
			// one unusable overlay must not cost the whole city its
			// scenarios — the primary feed is the one that has to work.
			if i == 0 {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		loaded++
	}
	if loaded == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("gtfs: no feeds in %q", paths)
	}
	if len(si.Activity) == 0 {
		return nil, fmt.Errorf("gtfs: %s has no timed trips with calendar service", paths)
	}
	si.buildFrame()
	return si, firstErr
}

func (si *ServiceInfo) loadOne(path, pre string, opts ServiceOpts) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
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

	// service_id → per-weekday weight (Mon=0 … Sun=6). calendar.txt is a
	// plain weekly mask (weight 1 per active day); a feed without one is
	// read off calendar_dates.txt, where the weight is how many times that
	// weekday actually recurs — see the package comment.
	svcDays := map[string]*[7]int{}
	if cf, ok := files["calendar.txt"]; ok {
		dayCols := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
		if err := eachRow(cf, func(get func(string) string) {
			var days [7]int
			any := false
			for i, c := range dayCols {
				if get(c) == "1" {
					days[i], any = 1, true
				}
			}
			if any {
				svcDays[get("service_id")] = &days
			}
		}); err != nil {
			return err
		}
	}
	if cd, ok := files["calendar_dates.txt"]; ok {
		// snapshot FIRST: the guard below must mean "calendar.txt owns
		// this service", not "some earlier dated row already created it" —
		// checking the live map stopped accumulation after one date and
		// flattened every weekday weight to 1.
		fromCalendar := make(map[string]bool, len(svcDays))
		for sid := range svcDays {
			fromCalendar[sid] = true
		}
		if err := eachRow(cd, func(get func(string) string) {
			sid := get("service_id")
			if fromCalendar[sid] {
				return // calendar.txt owns this service; exceptions are holidays
			}
			if get("exception_type") != "1" {
				return
			}
			t, err := time.Parse("20060102", strings.TrimSpace(get("date")))
			if err != nil {
				return
			}
			d := (int(t.Weekday()) + 6) % 7 // time.Sunday=0 → our Mon=0
			days := svcDays[sid]
			if days == nil {
				days = &[7]int{}
				svcDays[sid] = days
			}
			days[d]++
		}); err != nil {
			return err
		}
	}
	if len(svcDays) == 0 {
		return fmt.Errorf("gtfs: %s has neither calendar.txt nor dated service", path)
	}

	// routes.txt → colors (ribbons are drawn per color; a rush express
	// sharing the local's color and corridor draws the same ink)
	rf, err := need("routes.txt")
	if err != nil {
		return err
	}
	drawable, derive := map[string]bool{}, map[string]bool{}
	if err := eachRow(rf, func(get func(string) string) {
		id := pre + get("route_id")
		c := get("route_color")
		if c == "" {
			c = id
		}
		si.routeColor[id] = c
		t, _ := strconv.Atoi(get("route_type"))
		drawable[id] = opts.drawable(t)
		derive[id] = opts.derive(t)
	}); err != nil {
		return err
	}

	// trips.txt → trip_id → (pattern, service days)
	tf, err := need("trips.txt")
	if err != nil {
		return err
	}
	type tripInfo struct {
		key  PatKey
		days *[7]int
	}
	trips := map[string]tripInfo{}
	if err := eachRow(tf, func(get func(string) string) {
		days, ok := svcDays[get("service_id")]
		shape := get("shape_id")
		route := pre + get("route_id")
		if !ok || shape == "" || !drawable[route] {
			return
		}
		trips[get("trip_id")] = tripInfo{PatKey{route, shape}, days}
	}); err != nil {
		return err
	}

	// stop_times.txt → trip time span in seconds (GTFS allows hours ≥ 24)
	sf, err := need("stop_times.txt")
	if err != nil {
		return err
	}
	type span struct {
		min, max int
		freq     bool // span came from frequencies.txt, not the template
	}
	spans := map[string]*span{}
	if err := eachRow(sf, func(get func(string) string) {
		id := get("trip_id")
		if _, ok := trips[id]; !ok {
			return
		}
		for _, col := range []string{"arrival_time", "departure_time"} {
			sec, ok := parseGTFSTime(get(col))
			if !ok {
				continue
			}
			sp := spans[id]
			if sp == nil {
				sp = &span{min: sec, max: sec}
				spans[id] = sp
			}
			if sec < sp.min {
				sp.min = sec
			}
			if sec > sp.max {
				sp.max = sec
			}
		}
	}); err != nil {
		return err
	}

	// frequencies.txt overrides the span: a headway-based feed stores ONE
	// template trip in stop_times (JFK's AirTrain templates at 00:00:00)
	// and states the real service window here. Read literally, a 24-hour
	// service showed up in the late-night scenario and nowhere else. The
	// window runs from the first start_time to the last end_time plus one
	// trip's running time — the final departure still completes its run.
	if ff, ok := files["frequencies.txt"]; ok {
		if err := eachRow(ff, func(get func(string) string) {
			sp, ok := spans[get("trip_id")]
			if !ok {
				return
			}
			start, ok1 := parseGTFSTime(get("start_time"))
			end, ok2 := parseGTFSTime(get("end_time"))
			if !ok1 || !ok2 || end < start {
				return
			}
			dur := sp.max - sp.min
			if sp.freq {
				sp.min, sp.max = min(sp.min, start), max(sp.max, end+dur)
				return
			}
			sp.freq = true
			sp.min, sp.max = start, end+dur
		}); err != nil {
			return err
		}
	}

	for id, ti := range trips {
		sp, ok := spans[id]
		if !ok {
			continue
		}
		act := si.Activity[ti.key]
		if act == nil {
			act = &[7][24]int{}
			si.Activity[ti.key] = act
			si.shapeOf[ti.key] = pre + ti.key.Shape
			// derivation is the PRIMARY feed's rail only. The MTA railroads
			// issue a service_id per date, so their pattern mix churns from
			// Tuesday to Wednesday without the map changing — folded into
			// derivation they shattered NYC's week into 36 ragged scenarios
			// ("Tue 10–16,20–22; Wed 10–15") where the subway alone yields
			// 15 clean ones. Overlay modes still get SELECTED per scenario,
			// so they appear and vanish on the backbone's boundaries.
			if pre == "" && derive[ti.key.Route] {
				si.derive[ti.key] = true
			}
		}
		h0, h1 := sp.min/3600, (sp.max-1)/3600
		if sp.max <= sp.min {
			h1 = h0
		}
		for d := 0; d < 7; d++ {
			if ti.days[d] == 0 {
				continue
			}
			for hh := h0; hh <= h1; hh++ {
				act[(d+hh/24)%7][hh%24] += ti.days[d]
			}
		}
	}

	// shapes.txt → downsampled polylines in local meters
	shf, err := need("shapes.txt")
	if err != nil {
		return err
	}
	type raw struct {
		seq      int
		lon, lat float64
	}
	rawShapes := map[string][]raw{}
	if err := eachRow(shf, func(get func(string) string) {
		lat, e1 := strconv.ParseFloat(get("shape_pt_lat"), 64)
		lon, e2 := strconv.ParseFloat(get("shape_pt_lon"), 64)
		seq, e3 := strconv.Atoi(get("shape_pt_sequence"))
		if e1 == nil && e2 == nil && e3 == nil {
			id := pre + get("shape_id")
			rawShapes[id] = append(rawShapes[id], raw{seq, lon, lat})
		}
	}); err != nil {
		return err
	}
	// geometry stays in DEGREES here: the projection anchor has to be one
	// city-wide constant, and later feeds aren't loaded yet. buildFrame
	// projects once every feed is in.
	for id, pts := range rawShapes {
		sort.Slice(pts, func(i, j int) bool { return pts[i].seq < pts[j].seq })
		out := make([]spt2, 0, len(pts))
		for _, p := range pts {
			out = append(out, spt2{p.lon, p.lat})
		}
		si.shapes[id] = out
	}
	return nil
}

// buildFrame projects every feed's shapes into one local-meter frame and
// downsamples them. The anchor is the lexicographically first shape id, not
// whatever map iteration served up first: scenario IDs are re-derived in
// other processes (the atlas derives, the CLI builds) and a wandering grid
// origin would hand the same service a different id.
func (si *ServiceInfo) buildFrame() {
	var lat0 float64
	anchorID := ""
	for id, pts := range si.shapes {
		if len(pts) > 0 && (anchorID == "" || id < anchorID) {
			anchorID, lat0 = id, pts[0].y
		}
	}
	kx := 111320.0 * cosDeg(lat0)
	const ky = 111320.0
	for id, pts := range si.shapes {
		var out []spt2
		for i, p := range pts {
			q := spt2{p.x * kx, p.y * ky}
			if i == 0 || i == len(pts)-1 || dist2(q, out[len(out)-1]) >= 40*40 {
				out = append(out, q)
			}
		}
		if len(out) >= 2 {
			si.shapes[id] = out
		} else {
			delete(si.shapes, id)
		}
	}
}

func cosDeg(d float64) float64 {
	x := d * 3.14159265358979 / 180
	// cheap cosine is fine at city scale
	return 1 - x*x/2 + x*x*x*x/24
}

func dist2(a, b spt2) float64 {
	dx, dy := a.x-b.x, a.y-b.y
	return dx*dx + dy*dy
}

// parseGTFSTime parses "HH:MM:SS" (hours may exceed 24) into seconds.
func parseGTFSTime(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	sec, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || h < 0 {
		return 0, false
	}
	return h*3600 + m*60 + sec, true
}

// Scenario is one distinct drawn map, covering every (day, hour) cell
// where service renders that map.
type Scenario struct {
	ID       string `json:"id"`    // stable short hash of the pattern set
	Label    string `json:"label"` // "Mon–Fri 06–22" style
	Patterns int    `json:"patterns"`
	Cells    [7][24]bool     `json:"-"`
	Keys     map[PatKey]bool `json:"-"`
}

// floorFrac: within a scenario, a pattern must carry at least this share
// of its route's activity to be drawn. The union build's coverage pruning
// achieves the same thing via whole-week trip counts; per-hour the counts
// are too small for a 99% cover to cut anything (1% of one hour is less
// than one trip), so one-off variants need an explicit floor.
var floorFrac = 0.05

// Drawn-map raster: per color, pattern polylines dilated onto a coarse
// grid. rasterDilate makes opposite-direction track pairs (≤ ~40 m apart)
// occupy identical cells, so directionality and same-corridor expresses
// never split scenarios; a real branch or reroute occupies cells nothing
// else covers.
var (
	rasterCell   = 100.0 // grid pitch, meters
	rasterDilate = 60.0  // mark every cell within this of a sample point
	mergeMaxDiff = 12    // per-color symdiff cells tolerated as jitter
)

type gridCell struct{ cx, cy int32 }

type raster map[string]map[gridCell]bool // color → occupied cells

func (si *ServiceInfo) rasterize(keys map[PatKey]bool) raster {
	r := raster{}
	for key := range keys {
		color := si.routeColor[key.Route]
		if color == "" {
			color = key.Route
		}
		cells := r[color]
		if cells == nil {
			cells = map[gridCell]bool{}
			r[color] = cells
		}
		for _, p := range si.shapes[si.shapeOf[key]] {
			cx0, cy0 := int32(p.x/rasterCell), int32(p.y/rasterCell)
			for dx := int32(-1); dx <= 1; dx++ {
				for dy := int32(-1); dy <= 1; dy++ {
					c := gridCell{cx0 + dx, cy0 + dy}
					// distance from p to the cell's square
					x0, y0 := float64(c.cx)*rasterCell, float64(c.cy)*rasterCell
					ddx := clampDist(p.x, x0, x0+rasterCell)
					ddy := clampDist(p.y, y0, y0+rasterCell)
					if ddx*ddx+ddy*ddy <= rasterDilate*rasterDilate {
						cells[c] = true
					}
				}
			}
		}
	}
	return r
}

func clampDist(v, lo, hi float64) float64 {
	if v < lo {
		return lo - v
	}
	if v > hi {
		return v - hi
	}
	return 0
}

// sameMap: no color's footprint differs by more than the jitter allowance.
func sameMap(a, b raster) bool {
	for color := range b {
		if _, ok := a[color]; !ok {
			if len(b[color]) > mergeMaxDiff {
				return false
			}
		}
	}
	for color, ca := range a {
		cb := b[color] // nil is fine — counts all of ca as diff
		diff := 0
		for c := range ca {
			if !cb[c] {
				diff++
				if diff > mergeMaxDiff {
					return false
				}
			}
		}
		for c := range cb {
			if !ca[c] {
				diff++
				if diff > mergeMaxDiff {
					return false
				}
			}
		}
	}
	return true
}

// symdiffTotal: total differing cells across colors (for nearest-neighbor
// absorption of tiny transitional groups).
func symdiffTotal(a, b raster) int {
	colors := map[string]bool{}
	for c := range a {
		colors[c] = true
	}
	for c := range b {
		colors[c] = true
	}
	total := 0
	for color := range colors {
		ca, cb := a[color], b[color]
		for c := range ca {
			if !cb[c] {
				total++
			}
		}
		for c := range cb {
			if !ca[c] {
				total++
			}
		}
	}
	return total
}

// minCells: groups smaller than this (lone transitional hours — 5 am
// ramp-up mixes) are absorbed into their drawn-nearest neighbor rather
// than surfacing as one-hour scenarios.
var minCells = 3

// BuildScenarios collapses the 7×24 service grid into distinct drawn maps.
// Hour cells chain together along the week ring while consecutive hours
// draw the same ink; the resulting blocks then merge pairwise under the
// same test, and tiny transitional blocks are absorbed into their nearest
// neighbor. Cells with no service at all map to no scenario.
func BuildScenarios(si *ServiceInfo, coverFrac float64) []Scenario {
	// per-hour pattern sets and rasters, deduped by exact set (Mon–Fri
	// share service ids, so most cells repeat)
	type hourGroup struct {
		keys map[PatKey]bool
		ras  raster
	}
	groupOf := [7 * 24]int{} // hour index → group index, -1 = no service
	var hgroups []hourGroup
	byKeyHash := map[string]int{}
	for i := 0; i < 7*24; i++ {
		var one [7][24]bool
		one[i/24][i%24] = true
		keys := groupPatterns(si, one, coverFrac, true)
		if len(keys) == 0 {
			groupOf[i] = -1
			continue
		}
		kh := keyHash(keys)
		gi, ok := byKeyHash[kh]
		if !ok {
			gi = len(hgroups)
			hgroups = append(hgroups, hourGroup{keys, si.rasterize(keys)})
			byKeyHash[kh] = gi
		}
		groupOf[i] = gi
	}

	// phase 1: union-find along the week's consecutive hours
	parent := make([]int, len(hgroups))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := 0; i < 7*24; i++ {
		j := (i + 1) % (7 * 24)
		gi, gj := groupOf[i], groupOf[j]
		if gi < 0 || gj < 0 || find(gi) == find(gj) {
			continue
		}
		if sameMap(hgroups[gi].ras, hgroups[gj].ras) {
			parent[find(gi)] = find(gj)
		}
	}

	// components → scenarios with aggregate pattern sets
	comp := map[int]*Scenario{}
	for i := 0; i < 7*24; i++ {
		if groupOf[i] < 0 {
			continue
		}
		root := find(groupOf[i])
		sc := comp[root]
		if sc == nil {
			sc = &Scenario{}
			comp[root] = sc
		}
		sc.Cells[i/24][i%24] = true
	}
	scens := make([]*Scenario, 0, len(comp))
	for _, sc := range comp {
		scens = append(scens, sc)
	}
	sort.Slice(scens, func(i, j int) bool {
		return firstCell(scens[i].Cells) < firstCell(scens[j].Cells)
	})
	rasters := make([]raster, len(scens))
	refresh := func(i int) {
		scens[i].Keys = groupPatterns(si, scens[i].Cells, coverFrac, true)
		rasters[i] = si.rasterize(scens[i].Keys)
	}
	for i := range scens {
		refresh(i)
	}

	// phase 2: pairwise merge of the (few) blocks, then absorb tiny ones
	merge := func(i, j int) { // j into i
		for d := 0; d < 7; d++ {
			for h := 0; h < 24; h++ {
				scens[i].Cells[d][h] = scens[i].Cells[d][h] || scens[j].Cells[d][h]
			}
		}
		scens = append(scens[:j], scens[j+1:]...)
		rasters = append(rasters[:j], rasters[j+1:]...)
		refresh(i)
	}
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(scens) && !changed; i++ {
			for j := i + 1; j < len(scens); j++ {
				if sameMap(rasters[i], rasters[j]) {
					merge(i, j)
					changed = true
					break
				}
			}
		}
	}
	for {
		small, sn := -1, 0
		for i := range scens {
			if n := cellCount(scens[i].Cells); n < minCells && (small < 0 || n < sn) {
				small, sn = i, n
			}
		}
		if small < 0 || len(scens) < 2 {
			break
		}
		near, nd := -1, 0
		for i := range scens {
			if i == small {
				continue
			}
			if d := symdiffTotal(rasters[small], rasters[i]); near < 0 || d < nd {
				near, nd = i, d
			}
		}
		if near > small {
			near, small = small, near // merge keeps the lower index
		}
		merge(near, small)
	}

	out := make([]Scenario, 0, len(scens))
	for _, sc := range scens {
		sc.ID = keyHash(sc.Keys)[:8]
		sc.Patterns = len(sc.Keys)
		sc.Label = cellsLabel(sc.Cells)
		out = append(out, *sc)
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := cellCount(out[i].Cells), cellCount(out[j].Cells)
		if ci != cj {
			return ci > cj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func keyHash(keys map[PatKey]bool) string {
	list := make([]string, 0, len(keys))
	for k := range keys {
		list = append(list, k.Route+"|"+k.Shape)
	}
	sort.Strings(list)
	sum := sha1.Sum([]byte(strings.Join(list, "\n")))
	return hex.EncodeToString(sum[:])
}

// Select is the pattern set a chosen scenario actually DRAWS: every
// drawable pattern — bus, ferry, commuter rail, the lot — that carries
// meaningful service in the scenario's cells. Derivation deliberately
// looks at less than this (see ServiceInfo), so selection has to be its
// own pass; filtering the build by the derivation set is what once made
// NYC's Saturday map subway-only.
func (si *ServiceInfo) Select(cells [7][24]bool, coverFrac float64) map[PatKey]bool {
	return groupPatterns(si, cells, coverFrac, false)
}

// groupPatterns: the pattern set over a cell group's aggregated activity —
// most-active first until coverFrac, minus sub-floorFrac noise (a route's
// top pattern is always kept). deriveOnly restricts to the rail-family
// subset scenarios are derived from.
func groupPatterns(si *ServiceInfo, cells [7][24]bool, coverFrac float64, deriveOnly bool) map[PatKey]bool {
	type pa struct {
		key PatKey
		n   int
	}
	byRoute := map[string][]pa{}
	for key, act := range si.Activity {
		if deriveOnly && !si.derive[key] {
			continue
		}
		n := 0
		for d := 0; d < 7; d++ {
			for h := 0; h < 24; h++ {
				if cells[d][h] {
					n += act[d][h]
				}
			}
		}
		if n > 0 {
			byRoute[key.Route] = append(byRoute[key.Route], pa{key, n})
		}
	}
	keys := map[PatKey]bool{}
	for _, pats := range byRoute {
		sort.Slice(pats, func(i, j int) bool {
			if pats[i].n != pats[j].n {
				return pats[i].n > pats[j].n
			}
			return pats[i].key.Shape < pats[j].key.Shape
		})
		total := 0
		for _, p := range pats {
			total += p.n
		}
		acc := 0
		for i, p := range pats {
			if i > 0 && float64(p.n) < floorFrac*float64(total) {
				break
			}
			keys[p.key] = true
			acc += p.n
			if float64(acc) >= coverFrac*float64(total) {
				break
			}
		}
	}
	return keys
}

func firstCell(cells [7][24]bool) int {
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			if cells[d][h] {
				return d*24 + h
			}
		}
	}
	return 7 * 24
}

func cellCount(cells [7][24]bool) int {
	n := 0
	for d := range cells {
		for h := range cells[d] {
			if cells[d][h] {
				n++
			}
		}
	}
	return n
}

var dayNames = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// cellsLabel renders a compact human label for a cell set: when every
// active day shares the same hours, "Mon–Fri 06–22"; otherwise a per-day
// breakdown joined with ";".
func cellsLabel(cells [7][24]bool) string {
	perDay := map[string][]int{} // hour-signature → days
	var order []string
	for d := 0; d < 7; d++ {
		sig := ""
		for h := 0; h < 24; h++ {
			if cells[d][h] {
				sig += fmt.Sprintf("%d,", h)
			}
		}
		if sig == "" {
			continue
		}
		if _, ok := perDay[sig]; !ok {
			order = append(order, sig)
		}
		perDay[sig] = append(perDay[sig], d)
	}
	var parts []string
	for _, sig := range order {
		days := perDay[sig]
		var hours []int
		for _, f := range strings.Split(strings.TrimSuffix(sig, ","), ",") {
			h, _ := strconv.Atoi(f)
			hours = append(hours, h)
		}
		parts = append(parts, dayRange(days)+" "+hourRanges(hours))
	}
	return strings.Join(parts, "; ")
}

func dayRange(days []int) string {
	var runs []string
	for i := 0; i < len(days); {
		j := i
		for j+1 < len(days) && days[j+1] == days[j]+1 {
			j++
		}
		if j > i {
			runs = append(runs, dayNames[days[i]]+"–"+dayNames[days[j]])
		} else {
			runs = append(runs, dayNames[days[i]])
		}
		i = j + 1
	}
	return strings.Join(runs, ",")
}

// hourRanges compresses sorted hours into "06–22" spans (end exclusive);
// a run that wraps midnight renders as "22–02".
func hourRanges(hours []int) string {
	if len(hours) == 24 {
		return "all day"
	}
	in := [24]bool{}
	for _, h := range hours {
		in[h] = true
	}
	// find runs on the 24h ring
	var runs [][2]int
	for h := 0; h < 24; h++ {
		if !in[h] || in[(h+23)%24] {
			continue // not a run start
		}
		end := h
		for in[(end+1)%24] {
			end = (end + 1) % 24
		}
		runs = append(runs, [2]int{h, (end + 1) % 24})
	}
	var parts []string
	for _, r := range runs {
		parts = append(parts, fmt.Sprintf("%02d–%02d", r[0], r[1]))
	}
	return strings.Join(parts, ",")
}
