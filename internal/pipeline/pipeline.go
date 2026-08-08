// Package pipeline wires the stages end-to-end. RESET 2026-07-13: all
// algorithm stages are stubs (internal/stages); this file keeps the working
// scaffolding — loaders, frame, stage dumps, scorer plumbing, emit format —
// so the workbench and gates run against whatever the stages produce.
// The reimplementation brief is docs/FRESH-START.md.
package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
	"github.com/alexwohlbruck/portolan/internal/osm"
	"github.com/alexwohlbruck/portolan/internal/sketch"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// Dials — tuning parameters surfaced in the atlas UI. Stage authors: add
// dials here (flat json floats, stage-prefixed so the panel groups them)
// and read them via stages.Tuning — they appear in the panel automatically.
type Dials struct {
	JoinTol float64 `json:"join_tol"`
	Cover   float64 `json:"cover"`

	MatchReach      float64 `json:"match_reach"`
	MatchCands      float64 `json:"match_cands"`
	MatchWHead      float64 `json:"match_w_head"`
	MatchWTurn      float64 `json:"match_w_turn"`
	MatchBonusRoute float64 `json:"match_bonus_route"`
	MatchBonusColor float64 `json:"match_bonus_color"`
	MatchBonusOther float64 `json:"match_bonus_other"`
	MatchGapCost    float64 `json:"match_gap_cost"`
	MatchGapFree    float64 `json:"match_gap_free"`

	SplitMinRefine   float64 `json:"split_min_refine"`
	SplitMateMax     float64 `json:"split_mate_max"`
	SplitMateRun     float64 `json:"split_mate_run"`
	SplitMergeDist   float64 `json:"split_merge_dist"`
	SplitMergeRun    float64 `json:"split_merge_run"`
	SplitCoMergeDist float64 `json:"split_co_merge_dist"`

	FairCutBase float64 `json:"fair_cut_base"`
	FairGapPx   float64 `json:"fair_gap_px"`
	FairMaxTurn float64 `json:"fair_max_turn"`
	FairFilletR float64 `json:"fair_fillet_r"`
}

func DefaultDials() Dials {
	return Dials{
		JoinTol: 1.0, Cover: 0.99,
		MatchReach: 90, MatchCands: 14, MatchWHead: 35, MatchWTurn: 0.8,
		MatchBonusRoute: 25, MatchBonusColor: 18, MatchBonusOther: 12,
		MatchGapCost: 75, MatchGapFree: 45,
		SplitMinRefine: 40, SplitMateMax: 12, SplitMateRun: 60,
		SplitMergeDist: 12, SplitMergeRun: 60, SplitCoMergeDist: 4,
		FairCutBase: 60, FairGapPx: 6, FairMaxTurn: 30, FairFilletR: 30,
	}
}

// tuning flattens the dials for the stages (json tags become tuning keys).
func (d Dials) tuning() stages.Tuning {
	raw, _ := json.Marshal(d)
	t := stages.Tuning{}
	json.Unmarshal(raw, &t)
	return t
}

// levelClass maps OSM structure tags to a coarse vertical class. Bridges
// and viaducts flatten onto tunnels in plan view; a stacked el-over-subway
// pair is NOT one corridor, and the merge rules need to know.
func levelClass(tags map[string]string) int {
	if v := tags["bridge"]; v != "" && v != "no" {
		return 1
	}
	if v := tags["tunnel"]; v != "" && v != "no" {
		return -1
	}
	if v := tags["layer"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			switch {
			case n > 0:
				return 1
			case n < 0:
				return -1
			}
		}
	}
	return 0
}

type ChartOpts struct {
	GTFS string
	Rail string
	// Streets: optional street extract (osm.LoadStreets). Bus patterns are
	// drawn only when set; the streets join the MATCH graph but never the
	// strand pool.
	Streets string
	// LineAgencies: regional agencies whose routes keep per-line identity
	// instead of collapsing into one agency trunk (Paris RER A–E). Config
	// curation — see mode.SetLineAgencies.
	LineAgencies []string
	// BBox [w,s,e,n]: clip pattern shapes to the city window. A national
	// feed's Amtrak shape would otherwise leave the extract and draw a
	// gap chord across the continent.
	BBox  []float64
	Out   string
	Dials *Dials
	// Scenario: build the layout for one service scenario (gtfs.Scenario
	// ID) instead of the all-service union — the patterns are restricted
	// to that scenario's set and everything downstream lays out only what
	// actually runs then.
	Scenario string
}

// Chart: CHART (load, proven) → MATCH → SPLIT → ORDER → FAIR (stubs) → EMIT.
func Chart(o ChartOpts, logf func(string, ...any)) error {
	d := DefaultDials()
	if o.Dials != nil {
		d = *o.Dials
	}
	stages.SetTuning(d.tuning())
	t0 := time.Now()

	ways, err := osm.Load(o.Rail)
	if err != nil {
		return err
	}
	if len(ways) == 0 {
		return fmt.Errorf("no regular-service rail ways in %s", o.Rail)
	}
	frame := FrameOf(ways)
	xover := map[string]bool{}
	for _, w := range ways {
		if w.Tags["service"] == "crossover" {
			xover[w.ID] = true
		}
	}
	stages.SetCrossoverWays(xover)
	toTracks := func(ws []osm.Way) []bundle.Track {
		ts := make([]bundle.Track, len(ws))
		for i, w := range ws {
			pts := make([]geo.Pt, len(w.Coords))
			for j, ll := range w.Coords {
				pts[j] = frame.ToXY(ll)
			}
			ts[i] = bundle.Track{ID: w.ID, Line: geo.NewLine(pts),
				Level: levelClass(w.Tags)}
		}
		return ts
	}
	tracks := toTracks(ways)
	// streets are a separate opt-in layer for bus matching: they join the
	// class/level maps and the MATCH graph, but never the strand pool —
	// a street way IS the drawn road centerline already, and 100k street
	// lines through bundling would be pure cost.
	var streetTracks []bundle.Track
	if o.Streets != "" {
		sways, serr := osm.LoadStreets(o.Streets)
		if serr != nil {
			return serr
		}
		streetTracks = toTracks(sways)
		ways = append(ways, sways...)
	}
	lvls := map[string]int{}
	for _, t := range tracks {
		if t.Level != 0 {
			lvls[t.ID] = t.Level
		}
	}
	for _, t := range streetTracks {
		if t.Level != 0 {
			lvls[t.ID] = t.Level
		}
	}
	stages.SetWayLevels(lvls)
	cls := map[string]string{}
	for _, w := range ways {
		cls[w.ID] = w.Tags["railway"]
	}
	stages.SetWayRailClass(cls)
	strands := bundle.Chain(tracks, d.JoinTol)
	logf("chart: %d rail ways (+%d street) → %d strands (%.1fs)",
		len(tracks), len(streetTracks), len(strands), time.Since(t0).Seconds())
	if err := writeStrands(o.Out+".strands.geojson", strands, frame); err != nil {
		return err
	}
	if o.GTFS == "" {
		logf("chart: no GTFS — strands dump only")
		return nil
	}
	loadCover := d.Cover
	if o.Scenario != "" {
		loadCover = 1.01 // scenario selection replaces union pruning
	}
	// rail filter at the load: the stop_times sweep (5.8M rows on Chicago,
	// ~95% bus) skips foreign trips with one map miss, and bus shapes never
	// parse. The kept patterns are identical — the same predicate re-runs
	// below on what used to be the full set.
	drawable := func(r gtfs.Route) bool {
		c := mode.Of(r.Type)
		if c == mode.Bus {
			return o.Streets != ""
		}
		return c.Drawable()
	}
	feed, err := loadFeeds(o.GTFS, loadCover, drawable)
	if err != nil {
		return err
	}
	var rail []gtfs.Pattern
	for _, pat := range feed.Patterns {
		if drawable(pat.Route) {
			rail = append(rail, pat)
		}
	}
	logf("chart: %d drawable patterns of %d total", len(rail), len(feed.Patterns))
	if len(o.BBox) == 4 {
		before := len(rail)
		rail = clipPatterns(rail, o.BBox)
		logf("chart: bbox clip: %d patterns → %d in-window pieces", before, len(rail))
	}
	if o.Scenario != "" {
		si, err := gtfs.LoadService(firstFeed(o.GTFS))
		if err != nil {
			return fmt.Errorf("scenario build: %w", err)
		}
		var sc *gtfs.Scenario
		for _, s := range gtfs.BuildScenarios(si, d.Cover) {
			if s.ID == o.Scenario {
				sc = &s
				break
			}
		}
		if sc == nil {
			return fmt.Errorf("unknown scenario %q", o.Scenario)
		}
		var keep []gtfs.Pattern
		for _, pat := range rail {
			if sc.Keys[gtfs.PatKey{Route: pat.Route.ID, Shape: pat.ShapeID}] {
				keep = append(keep, pat)
			}
		}
		rail = keep
		logf("scenario %s (%s): %d rail patterns", sc.ID, sc.Label, len(rail))
		if len(rail) == 0 {
			return fmt.Errorf("scenario %s has no rail patterns", sc.ID)
		}
	}

	if v := os.Getenv("PORTOLAN_DBG3"); v != "" {
		var la, lo float64
		fmt.Sscanf(v, "%f,%f", &la, &lo)
		stages.SetDbg3(frame.ToXY(geo.LL{Lat: la, Lon: lo}))
	}
	stages.SetFerryRoutes(routeSetOf(feed.Routes, mode.Ferry))
	la := map[string]bool{}
	for _, a := range o.LineAgencies {
		la[strings.TrimSpace(a)] = true
	}
	mode.SetLineAgencies(la)
	stages.SetAgencyNames(feed.Agencies)
	matchTracks := tracks
	if len(streetTracks) > 0 {
		matchTracks = append(append([]bundle.Track{}, tracks...), streetTracks...)
	}
	paths, err := stages.Match(rail, matchTracks, frame)
	if err != nil {
		return fmt.Errorf("MATCH: %w", err)
	}
	logf("match: %d patterns → %d paths (%.1fs)",
		len(rail), len(paths), time.Since(t0).Seconds())
	if err := writePaths(o.Out+".paths.geojson", paths, frame); err != nil {
		return err
	}
	// buses stop here: a matched bus path IS the deliverable (street
	// centerline walk), so bus paths skip SPLIT/ORDER/FAIR entirely and
	// emit directly — path matching and nothing more.
	var busPaths, railPaths []stages.Path
	for _, p := range paths {
		if mode.Of(p.Pattern.Route.Type) == mode.Bus {
			busPaths = append(busPaths, p)
		} else {
			railPaths = append(railPaths, p)
		}
	}
	// SPLIT must see the SAME track slice as MATCH: path steps index into
	// the match graph's pieces, and buildTrackGraphCached only returns that
	// graph for the identical slice. (A rail+used-streets subset here
	// panicked on piece ids past the smaller graph's range.)
	net, err := stages.Split(railPaths, matchTracks)
	if err != nil {
		return fmt.Errorf("SPLIT: %w", err)
	}
	logf("split: %d nodes, %d edges (%.1fs)",
		len(net.Nodes), len(net.Edges), time.Since(t0).Seconds())
	if err := writeNetwork(o.Out, net, frame); err != nil {
		return err
	}
	slots, err := stages.Order(net, feed.Routes)
	if err != nil {
		return fmt.Errorf("ORDER: %w", err)
	}
	logf("order: slots on %d edges (%.1fs)", len(slots), time.Since(t0).Seconds())
	segs, err := stages.Fair(net, slots, feed.Routes, railPaths)
	if err != nil {
		return fmt.Errorf("FAIR: %w", err)
	}
	logf("fair: %d segments (%.1fs)", len(segs), time.Since(t0).Seconds())
	if len(busPaths) > 0 {
		bsegs := stages.BusSegments(busPaths)
		logf("bus: %d paths → %d deduped segments", len(busPaths), len(bsegs))
		segs = append(segs, bsegs...)
	}
	return WriteSegmentsGeoJSON(o.Out, segs, frame)
}

type SoundOpts struct {
	Network string
	Build   string
}

func Sound(o SoundOpts) (*sketch.Result, error) {
	net, err := sketch.LoadNetwork(o.Network)
	if err != nil {
		return nil, err
	}
	if len(net.Lines) == 0 {
		return nil, fmt.Errorf("network is empty")
	}
	frame := geo.NewFrame(geo.LL{
		Lon: net.Lines[0].Coords[0][0], Lat: net.Lines[0].Coords[0][1],
	})
	feats, err := LoadBuildFeatures(o.Build, frame)
	if err != nil {
		return nil, err
	}
	return sketch.Score(net, feats, frame), nil
}

func FrameOf(ways []osm.Way) geo.Frame {
	minLon, minLat := 180.0, 90.0
	maxLon, maxLat := -180.0, -90.0
	for _, w := range ways {
		for _, c := range w.Coords {
			minLon, maxLon = min(minLon, c.Lon), max(maxLon, c.Lon)
			minLat, maxLat = min(minLat, c.Lat), max(maxLat, c.Lat)
		}
	}
	return geo.NewFrame(geo.LL{Lon: (minLon + maxLon) / 2, Lat: (minLat + maxLat) / 2})
}

type feature struct {
	Type  string         `json:"type"`
	Props map[string]any `json:"properties"`
	Geom  geomJSON       `json:"geometry"`
}

type geomJSON struct {
	Type   string          `json:"type"`
	Coords json.RawMessage `json:"coordinates"`
}

type collection struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

func lineFeature(props map[string]any, l *geo.Line, step float64, frame geo.Frame) (feature, bool) {
	var cs [][2]float64
	for _, p := range l.Resample(step) {
		ll := frame.ToLL(p)
		cs = append(cs, [2]float64{ll.Lon, ll.Lat})
	}
	raw, _ := json.Marshal(cs)
	return feature{Type: "Feature", Props: props,
		Geom: geomJSON{Type: "LineString", Coords: raw}}, len(cs) >= 2
}

func writeFC(path string, fc collection) error {
	raw, err := json.Marshal(fc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func writeStrands(path string, strands []bundle.Strand, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for _, s := range strands {
		if f, ok := lineFeature(map[string]any{
			"kind": "strand", "strand": s.ID, "len_m": int(s.Line.Len()),
		}, s.Line, 10, frame); ok {
			fc.Features = append(fc.Features, f)
		}
	}
	return writeFC(path, fc)
}

func writePaths(path string, paths []stages.Path, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for _, p := range paths {
		gaps := 0
		for _, w := range p.WayIDs {
			if w == "gap" {
				gaps++
			}
		}
		if f, ok := lineFeature(map[string]any{
			"kind": "path", "route": p.Pattern.Route.ID,
			"label": p.Pattern.Route.ShortName,
			"color": p.Pattern.Route.Color, "shape": p.Pattern.ShapeID,
			"trips": p.Pattern.Trips, "gaps": gaps,
			"len_m": int(p.Line.Len()),
		}, p.Line, 8, frame); ok {
			fc.Features = append(fc.Features, f)
		}
	}
	return writeFC(path, fc)
}

// writeNetwork dumps the SPLIT graph: refined segment centerlines
// (trackcenter layer) and junction nodes with degree (nodes layer).
func writeNetwork(out string, net *stages.Network, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for ei, e := range net.Edges {
		l := geo.NewLine(e.Pts)
		if f, ok := lineFeature(map[string]any{
			"kind": "segment", "edge": ei,
			"routes": strings.Join(e.Routes, ","), "nroutes": len(e.Routes),
			"tracks": e.Tracks, "gap": e.Gap, "len_m": int(l.Len()),
		}, l, 8, frame); ok {
			fc.Features = append(fc.Features, f)
		}
	}
	if err := writeFC(out+".trackcenter.geojson", fc); err != nil {
		return err
	}
	nfc := collection{Type: "FeatureCollection"}
	for _, n := range net.Nodes {
		ll := frame.ToLL(n.At)
		raw, _ := json.Marshal([2]float64{ll.Lon, ll.Lat})
		nfc.Features = append(nfc.Features, feature{
			Type:  "Feature",
			Props: map[string]any{"degree": len(n.Adj)},
			Geom:  geomJSON{Type: "Point", Coords: raw},
		})
	}
	return writeFC(out+".nodes.geojson", nfc)
}

// WriteSegmentsGeoJSON emits the parchment transit_line_segments contract.
func WriteSegmentsGeoJSON(path string, segs []stages.Segment, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for si, s := range segs {
		// FAIR emits visually-smoothed polylines — resampling at a coarse
		// step would re-facet every rounded corner. Emit vertices as-is.
		if f, ok := vertexFeature(map[string]any{
			"seg": si, "kind": s.Kind,
			"color": s.Color, "route_color": s.Color,
			"routes": strings.Join(s.Routes, ","), "label": s.Label,
			"route_type": s.RouteType, "mode": s.Mode,
			"slot": s.Slot, "nslots": s.NSlots,
			"offset_px":   s.OffsetPx,
			"off_from_px": s.OffFromPx,
			"off_to_px":   s.OffToPx,
			"band_min":    s.BandMin, "band_max": s.BandMax,
			"len_m": int(s.Line.Len()),
		}, s.Line, frame); ok {
			fc.Features = append(fc.Features, f)
		}
	}
	return writeFC(path, fc)
}

// vertexFeature emits a line's own vertices (no resampling).
func vertexFeature(props map[string]any, l *geo.Line, frame geo.Frame) (feature, bool) {
	var cs [][2]float64
	for _, p := range l.Pts {
		ll := frame.ToLL(p)
		cs = append(cs, [2]float64{ll.Lon, ll.Lat})
	}
	raw, _ := json.Marshal(cs)
	return feature{Type: "Feature", Props: props,
		Geom: geomJSON{Type: "LineString", Coords: raw}}, len(cs) >= 2
}

// LoadBuildFeatures reads a segments GeoJSON for scoring (z15 band only).
func LoadBuildFeatures(path string, frame geo.Frame) ([]sketch.BuildFeature, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc struct {
		Features []struct {
			Props    map[string]any `json:"properties"`
			Geometry struct {
				Type   string       `json:"type"`
				Coords [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, err
	}
	var out []sketch.BuildFeature
	for _, f := range fc.Features {
		if f.Geometry.Type != "LineString" || len(f.Geometry.Coords) < 2 {
			continue
		}
		if bm, ok := f.Props["band_max"].(float64); ok && bm < 15 {
			continue
		}
		if bm, ok := f.Props["band_min"].(float64); ok && bm > 15 {
			continue
		}
		// score the modes the sketches actually draw (rapid transit).
		// The gates are blind to mode and the sketch is subway-only:
		// a one-way bus couplet carrying one route on two streets fires
		// the dup lens, LIRR's Atlantic Branch "covers" the green line,
		// and street corners near corridors read as spikes — a metro-only
		// subset of the same build scores exactly the rail-only baseline.
		// When a sketch grows non-rapid lines, its lines need a mode field
		// and this filter follows it.
		if md, ok := f.Props["mode"].(string); ok && md != "metro" && md != "tram" {
			continue
		}
		pts := make([]geo.Pt, len(f.Geometry.Coords))
		for i, c := range f.Geometry.Coords {
			pts[i] = frame.ToXY(geo.LL{Lon: c[0], Lat: c[1]})
		}
		color, _ := f.Props["color"].(string)
		var rts []string
		if rs, _ := f.Props["routes"].(string); rs != "" {
			rts = strings.Split(rs, ",")
		}
		out = append(out, sketch.BuildFeature{Color: color, Routes: rts, Line: geo.NewLine(pts)})
	}
	return out, nil
}

// firstFeed: ChartOpts.GTFS is a comma list (primary feed first, overlay
// feeds after). Scenario derivation stays a primary-feed concept.
func firstFeed(paths string) string {
	if i := strings.IndexByte(paths, ','); i >= 0 {
		return strings.TrimSpace(paths[:i])
	}
	return strings.TrimSpace(paths)
}

// loadFeeds loads and merges every feed in the comma list. Overlay route
// ids are prefixed f<i>: unconditionally — deterministic, and route ids
// only surface in the routes prop; labels come from short names.
func loadFeeds(paths string, cover float64, keep func(gtfs.Route) bool) (*gtfs.Feed, error) {
	parts := strings.Split(paths, ",")
	base, err := gtfs.LoadFiltered(strings.TrimSpace(parts[0]), cover, keep)
	if err != nil {
		return nil, err
	}
	for i, p := range parts[1:] {
		p = strings.TrimSpace(p)
		f, err := gtfs.LoadFiltered(p, cover, keep)
		if err != nil {
			return nil, fmt.Errorf("overlay feed %s: %w", p, err)
		}
		pre := fmt.Sprintf("f%d:", i+1)
		for id, r := range f.Routes {
			r.ID = pre + id
			if r.Agency != "" {
				r.Agency = pre + r.Agency // "1" is MNR in one feed, someone else in the next
			}
			base.Routes[r.ID] = r
		}
		for _, pat := range f.Patterns {
			pat.Route.ID = pre + pat.Route.ID
			if pat.Route.Agency != "" {
				pat.Route.Agency = pre + pat.Route.Agency
			}
			base.Patterns = append(base.Patterns, pat)
		}
		for id, name := range f.Agencies {
			base.Agencies[pre+id] = name
		}
	}
	return base, nil
}

func routeSetOf(routes map[string]gtfs.Route, c mode.Class) map[string]bool {
	m := map[string]bool{}
	for id, r := range routes {
		if mode.Of(r.Type) == c {
			m[id] = true
		}
	}
	return m
}

// clipPatterns cuts every shape to the window (plus margin): each maximal
// in-window run ≥1 km becomes its own pattern piece, ending at the window
// edge the way Apple draws a line running off the map. Without this a
// national feed (Amtrak) chords across the continent as gap bridges.
func clipPatterns(pats []gtfs.Pattern, bbox []float64) []gtfs.Pattern {
	const margin = 0.02 // ~2 km
	w, s, e, n := bbox[0]-margin, bbox[1]-margin, bbox[2]+margin, bbox[3]+margin
	inside := func(ll geo.LL) bool {
		return ll.Lon >= w && ll.Lon <= e && ll.Lat >= s && ll.Lat <= n
	}
	var out []gtfs.Pattern
	for _, pat := range pats {
		runs := [][]geo.LL{}
		var cur []geo.LL
		for _, ll := range pat.Shape {
			if inside(ll) {
				cur = append(cur, ll)
			} else if len(cur) > 0 {
				runs = append(runs, cur)
				cur = nil
			}
		}
		if len(cur) > 0 {
			runs = append(runs, cur)
		}
		if len(runs) == 1 && len(runs[0]) == len(pat.Shape) {
			out = append(out, pat) // wholly inside — untouched
			continue
		}
		for ri, run := range runs {
			if len(run) < 2 || runLenKm(run) < 1.0 {
				continue
			}
			np := pat
			np.Shape = run
			np.ShapeID = fmt.Sprintf("%s#clip%d", pat.ShapeID, ri)
			out = append(out, np)
		}
	}
	return out
}

func runLenKm(run []geo.LL) float64 {
	km := 0.0
	midLat := run[0].Lat * math.Pi / 180
	for i := 1; i < len(run); i++ {
		dx := (run[i].Lon - run[i-1].Lon) * 111.32 * math.Cos(midLat)
		dy := (run[i].Lat - run[i-1].Lat) * 110.54
		km += math.Hypot(dx, dy)
	}
	return km
}
