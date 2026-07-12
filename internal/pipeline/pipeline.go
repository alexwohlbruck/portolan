// Package pipeline wires the seven stages end-to-end. Both the CLI and the
// atlas workbench call it — the atlas "rebuild" button and `portolan chart`
// are the same code path.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alexwohlbruck/portolan/internal/berth"
	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/fair"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/order"
	"github.com/alexwohlbruck/portolan/internal/osm"
	"github.com/alexwohlbruck/portolan/internal/sketch"
)

type ChartOpts struct {
	GTFS  string  // GTFS zip; empty = tracks-only (corridor graph out)
	Rail  string  // OSM rail extract GeoJSON
	Out   string  // output GeoJSON (segments; <out>.graph.geojson also written)
	Cover float64 // pattern trip-coverage fraction (default 0.99)
}

// Chart runs CHART→SOUND→BUNDLE→BERTH→ORDER→FAIR→EMIT. logf receives
// progress lines (the atlas streams them to the browser).
func Chart(o ChartOpts, logf func(string, ...any)) error {
	if o.Cover == 0 {
		o.Cover = 0.99
	}
	t0 := time.Now()

	feed := &gtfs.Feed{}
	if o.GTFS != "" {
		var err error
		feed, err = gtfs.Load(o.GTFS, o.Cover)
		if err != nil {
			return err
		}
	} else {
		logf("chart: no GTFS; tracks-only build")
	}
	ways, err := osm.Load(o.Rail)
	if err != nil {
		return err
	}
	logf("chart: %d routes, %d patterns, %d rail ways (%.1fs)",
		len(feed.Routes), len(feed.Patterns), len(ways), time.Since(t0).Seconds())
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

	g := bundle.BuildGraph(tracks, bundle.DefaultGraphParams())
	logf("bundle: %d strands, %d corridors, %d nodes (%.1fs)",
		len(g.Strands), len(g.Corridors), len(g.Nodes), time.Since(t0).Seconds())
	if err := writeGraphGeoJSON(o.Out+".graph.geojson", g, frame); err != nil {
		return err
	}
	if len(feed.Patterns) == 0 {
		logf("chart: wrote %s.graph.geojson only (no GTFS) (%.1fs)",
			o.Out, time.Since(t0).Seconds())
		return nil
	}

	// rail modes only (route_type 0 tram / 1 subway / 2 rail / 1xx extended)
	var rail []gtfs.Pattern
	for _, pat := range feed.Patterns {
		t := pat.Route.Type
		if t == 0 || t == 1 || t == 2 || (t >= 100 && t < 200) {
			rail = append(rail, pat)
		}
	}
	logf("berth: %d rail patterns of %d total", len(rail), len(feed.Patterns))
	br := berth.MatchAll(g, rail, frame, berth.DefaultParams())
	nb := 0
	for _, bs := range br.Berths {
		nb += len(bs)
	}
	logf("berth: %d matches, %d berths, %d moves (%.1fs)",
		len(br.Matches), nb, len(br.Moves), time.Since(t0).Seconds())
	slots := order.Assign(g, br, 4)
	logf("order: slots on %d corridors (%.1fs)", len(slots), time.Since(t0).Seconds())
	segs := fair.Build(g, br, slots, fair.DefaultBands())
	logf("fair: %d segments across %d bands (%.1fs)",
		len(segs), len(fair.DefaultBands()), time.Since(t0).Seconds())
	if err := WriteSegmentsGeoJSON(o.Out, segs, frame); err != nil {
		return err
	}
	logf("chart: wrote %s (%.1fs total)", o.Out, time.Since(t0).Seconds())
	return nil
}

type SoundOpts struct {
	Network string // drawn network JSON
	Build   string // segments GeoJSON
}

// Sound scores a build against the drawn network and returns the result.
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
	Geom  struct {
		Type   string       `json:"type"`
		Coords [][2]float64 `json:"coordinates"`
	} `json:"geometry"`
}

type collection struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

// WriteSegmentsGeoJSON emits per-ribbon features in the parchment
// transit_line_segments contract (kind / route_color / offset_px /
// off_from_px / off_to_px, offsets in the feature's own travel frame).
func WriteSegmentsGeoJSON(path string, segs []fair.Segment, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for si, s := range segs {
		var f feature
		f.Type = "Feature"
		f.Props = map[string]any{
			"seg": si, "kind": s.Kind,
			"color": s.Color, "route_color": s.Color,
			"routes": strings.Join(s.Routes, ","), "label": s.Label,
			"route_type": s.RouteType,
			"slot":       s.Slot, "nslots": s.NSlots,
			"offset_px":   round2(s.OffsetPx),
			"off_from_px": round2(s.OffFromPx),
			"off_to_px":   round2(s.OffToPx),
			"corridor":    s.Corridor, "to_corridor": s.ToCorr,
			"band_min": s.Band.MinZoom, "band_max": s.Band.MaxZoom,
			"len_m": int(s.Line.Len()),
		}
		f.Geom.Type = "LineString"
		step := 8.0
		if s.Kind == "steady" {
			step = 6.0
		}
		for _, p := range s.Line.Resample(step) {
			ll := frame.ToLL(p)
			f.Geom.Coords = append(f.Geom.Coords, [2]float64{ll.Lon, ll.Lat})
		}
		if len(f.Geom.Coords) >= 2 {
			fc.Features = append(fc.Features, f)
		}
	}
	raw, err := json.Marshal(fc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func writeGraphGeoJSON(path string, g *bundle.Graph, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for _, c := range g.Corridors {
		var f feature
		f.Type = "Feature"
		f.Props = map[string]any{
			"kind": "corridor", "corridor": c.ID,
			"strands": len(c.Strands),
			"node_a":  c.NodeA, "node_b": c.NodeB,
			"len_m": int(c.Centerline.Len()),
		}
		f.Geom.Type = "LineString"
		for _, p := range c.Centerline.Resample(8) {
			ll := frame.ToLL(p)
			f.Geom.Coords = append(f.Geom.Coords, [2]float64{ll.Lon, ll.Lat})
		}
		if len(f.Geom.Coords) >= 2 {
			fc.Features = append(fc.Features, f)
		}
	}
	raw, err := json.Marshal(fc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// LoadBuildFeatures reads a segments GeoJSON for scoring (z15 band only).
func LoadBuildFeatures(path string, frame geo.Frame) ([]sketch.BuildFeature, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc collection
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, err
	}
	var out []sketch.BuildFeature
	for _, f := range fc.Features {
		if f.Geom.Type != "LineString" || len(f.Geom.Coords) < 2 {
			continue
		}
		if bm, ok := f.Props["band_max"].(float64); ok && bm < 15 {
			continue
		}
		if bm, ok := f.Props["band_min"].(float64); ok && bm > 15 {
			continue
		}
		pts := make([]geo.Pt, len(f.Geom.Coords))
		for i, c := range f.Geom.Coords {
			pts[i] = frame.ToXY(geo.LL{Lon: c[0], Lat: c[1]})
		}
		color, _ := f.Props["color"].(string)
		out = append(out, sketch.BuildFeature{Color: color, Line: geo.NewLine(pts)})
	}
	return out, nil
}

func round2(v float64) float64 { return float64(int(v*100+0.5*sign(v))) / 100 }

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
