// Package pipeline wires the stages end-to-end. RESET 2026-07-13: all
// algorithm stages are stubs (internal/stages); this file keeps the working
// scaffolding — loaders, frame, stage dumps, scorer plumbing, emit format —
// so the workbench and gates run against whatever the stages produce.
// The reimplementation brief is docs/FRESH-START.md.
package pipeline

import (
	"strconv"
	"encoding/json"
	"fmt"
	"os"
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
	GTFS  string
	Rail  string
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
	tracks := make([]bundle.Track, len(ways))
	for i, w := range ways {
		pts := make([]geo.Pt, len(w.Coords))
		for j, ll := range w.Coords {
			pts[j] = frame.ToXY(ll)
		}
		tracks[i] = bundle.Track{ID: w.ID, Line: geo.NewLine(pts),
			Level: levelClass(w.Tags)}
	}
	lvls := map[string]int{}
	for _, t := range tracks {
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
	logf("chart: %d rail ways → %d strands (%.1fs)",
		len(ways), len(strands), time.Since(t0).Seconds())
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
		return mode.Of(r.Type).Drawable()
	}
	feed, err := gtfs.LoadFiltered(o.GTFS, loadCover, drawable)
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
	if o.Scenario != "" {
		si, err := gtfs.LoadService(o.GTFS)
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
	paths, err := stages.Match(rail, tracks, frame)
	if err != nil {
		return fmt.Errorf("MATCH: %w", err)
	}
	logf("match: %d patterns → %d paths (%.1fs)",
		len(rail), len(paths), time.Since(t0).Seconds())
	if err := writePaths(o.Out+".paths.geojson", paths, frame); err != nil {
		return err
	}
	net, err := stages.Split(paths, tracks)
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
	segs, err := stages.Fair(net, slots, feed.Routes, paths)
	if err != nil {
		return fmt.Errorf("FAIR: %w", err)
	}
	logf("fair: %d segments (%.1fs)", len(segs), time.Since(t0).Seconds())
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
			"slot":       s.Slot, "nslots": s.NSlots,
			"offset_px":   s.OffsetPx,
			"off_from_px": s.OffFromPx,
			"off_to_px":   s.OffToPx,
			"band_min": s.BandMin, "band_max": s.BandMax,
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
