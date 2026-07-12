// Package pipeline wires the stages end-to-end. RESET 2026-07-13: all
// algorithm stages are stubs (internal/stages); this file keeps the working
// scaffolding — loaders, frame, stage dumps, scorer plumbing, emit format —
// so the workbench and gates run against whatever the stages produce.
// The reimplementation brief is docs/FRESH-START.md.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/osm"
	"github.com/alexwohlbruck/portolan/internal/sketch"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// Dials — tuning parameters surfaced in the atlas UI. Stage authors: add
// dials here (flat json floats) and they appear in the panel automatically.
type Dials struct {
	JoinTol float64 `json:"join_tol"`
	Cover   float64 `json:"cover"`
}

func DefaultDials() Dials {
	return Dials{JoinTol: 1.0, Cover: 0.99}
}

type ChartOpts struct {
	GTFS  string
	Rail  string
	Out   string
	Dials *Dials
}

// Chart: CHART (load, proven) → BUNDLE → ORDER → FAIR (stubs) → EMIT.
func Chart(o ChartOpts, logf func(string, ...any)) error {
	d := DefaultDials()
	if o.Dials != nil {
		d = *o.Dials
	}
	t0 := time.Now()

	ways, err := osm.Load(o.Rail)
	if err != nil {
		return err
	}
	if len(ways) == 0 {
		return fmt.Errorf("no regular-service rail ways in %s", o.Rail)
	}
	frame := FrameOf(ways)
	tracks := make([]bundle.Track, len(ways))
	for i, w := range ways {
		pts := make([]geo.Pt, len(w.Coords))
		for j, ll := range w.Coords {
			pts[j] = frame.ToXY(ll)
		}
		tracks[i] = bundle.Track{ID: w.ID, Line: geo.NewLine(pts)}
	}
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
	feed, err := gtfs.Load(o.GTFS, d.Cover)
	if err != nil {
		return err
	}
	var rail []gtfs.Pattern
	for _, pat := range feed.Patterns {
		t := pat.Route.Type
		if t == 0 || t == 1 || t == 2 || (t >= 100 && t < 200) {
			rail = append(rail, pat)
		}
	}
	logf("chart: %d rail patterns of %d total", len(rail), len(feed.Patterns))

	net, err := stages.Bundle(rail, strands, frame)
	if err != nil {
		return fmt.Errorf("BUNDLE: %w", err)
	}
	slots, err := stages.Order(net)
	if err != nil {
		return fmt.Errorf("ORDER: %w", err)
	}
	segs, err := stages.Fair(net, slots)
	if err != nil {
		return fmt.Errorf("FAIR: %w", err)
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

// WriteSegmentsGeoJSON emits the parchment transit_line_segments contract.
func WriteSegmentsGeoJSON(path string, segs []stages.Segment, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for si, s := range segs {
		step := 8.0
		if s.Kind == "steady" {
			step = 6.0
		}
		if f, ok := lineFeature(map[string]any{
			"seg": si, "kind": s.Kind,
			"color": s.Color, "route_color": s.Color,
			"routes": strings.Join(s.Routes, ","), "label": s.Label,
			"route_type": s.RouteType,
			"slot":       s.Slot, "nslots": s.NSlots,
			"offset_px":   s.OffsetPx,
			"off_from_px": s.OffFromPx,
			"off_to_px":   s.OffToPx,
			"band_min": s.BandMin, "band_max": s.BandMax,
			"len_m": int(s.Line.Len()),
		}, s.Line, step, frame); ok {
			fc.Features = append(fc.Features, f)
		}
	}
	return writeFC(path, fc)
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
		out = append(out, sketch.BuildFeature{Color: color, Line: geo.NewLine(pts)})
	}
	return out, nil
}
