// portolan — automatic transit line maps from GTFS feeds.
//
//	portolan chart --gtfs feed.zip --rail rail.geojson --out build.geojson
//	portolan sound --network sketches/nyc.json --build build.geojson
//	portolan atlas --sketches ./sketches --addr 127.0.0.1:8765
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/alexwohlbruck/portolan/internal/atlas"
	"github.com/alexwohlbruck/portolan/internal/berth"
	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/fair"
	"github.com/alexwohlbruck/portolan/internal/order"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/osm"
	"github.com/alexwohlbruck/portolan/internal/sketch"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "chart":
		chart(os.Args[2:])
	case "sound":
		sound(os.Args[2:])
	case "atlas":
		atlasCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: portolan chart|sound|atlas [flags] (see README.md)")
	os.Exit(2)
}

// chart runs the pipeline. IMPLEMENTED: CHART (load) → SOUND (soundings) →
// BUNDLE (centerlines), emitted as GeoJSON for review. NEXT: BERTH → ORDER →
// FAIR → EMIT per docs/ALGORITHM.md — attribution is not wired yet, so the
// output is bundle centerlines, not per-route ribbons.
func chart(args []string) {
	fs := flag.NewFlagSet("chart", flag.ExitOnError)
	gtfsPath := fs.String("gtfs", "", "GTFS zip")
	railPath := fs.String("rail", "", "OSM rail extract (GeoJSON)")
	out := fs.String("out", "build.geojson", "output GeoJSON")
	cover := fs.Float64("cover", 0.99, "pattern trip-coverage fraction")
	fs.Parse(args)
	if *railPath == "" {
		fs.Usage()
		os.Exit(2)
	}
	t0 := time.Now()

	feed := &gtfs.Feed{}
	if *gtfsPath != "" {
		var err error
		feed, err = gtfs.Load(*gtfsPath, *cover)
		die(err)
	} else {
		fmt.Fprintln(os.Stderr, "chart: no --gtfs; tracks-only build (BERTH will need it)")
	}
	ways, err := osm.Load(*railPath)
	die(err)
	fmt.Fprintf(os.Stderr, "chart: %d routes, %d patterns, %d rail ways (%.1fs)\n",
		len(feed.Routes), len(feed.Patterns), len(ways), time.Since(t0).Seconds())
	if len(ways) == 0 {
		die(fmt.Errorf("no regular-service rail ways in %s", *railPath))
	}

	// one local metric frame for everything (LESSONS #11)
	frame := frameOf(ways)
	tracks := make([]bundle.Track, len(ways))
	for i, w := range ways {
		pts := make([]geo.Pt, len(w.Coords))
		for j, ll := range w.Coords {
			pts[j] = frame.ToXY(ll)
		}
		tracks[i] = bundle.Track{ID: w.ID, Line: geo.NewLine(pts)}
	}

	g := bundle.BuildGraph(tracks, bundle.DefaultGraphParams())
	fmt.Fprintf(os.Stderr, "bundle: %d strands, %d corridors, %d nodes (%.1fs)\n",
		len(g.Strands), len(g.Corridors), len(g.Nodes), time.Since(t0).Seconds())

	if len(feed.Patterns) == 0 {
		die(writeGraphGeoJSON(*out, g, frame))
		fmt.Fprintf(os.Stderr, "chart: wrote %s (corridor graph only; %.1fs)\n",
			*out, time.Since(t0).Seconds())
		return
	}
	// rail modes only: bus shapes matched onto rail corridors are garbage.
	// (GTFS route_type: 0 tram, 1 subway, 2 rail; extended 1xx rail codes.)
	var rail []gtfs.Pattern
	for _, pat := range feed.Patterns {
		t := pat.Route.Type
		if t == 0 || t == 1 || t == 2 || (t >= 100 && t < 200) {
			rail = append(rail, pat)
		}
	}
	fmt.Fprintf(os.Stderr, "berth: %d rail patterns of %d total\n",
		len(rail), len(feed.Patterns))
	br := berth.MatchAll(g, rail, frame, berth.DefaultParams())
	nb := 0
	for _, bs := range br.Berths {
		nb += len(bs)
	}
	fmt.Fprintf(os.Stderr, "berth: %d matches, %d berths, %d moves (%.1fs)\n",
		len(br.Matches), nb, len(br.Moves), time.Since(t0).Seconds())
	slots := order.Assign(g, br, 4)
	fmt.Fprintf(os.Stderr, "order: slots on %d corridors (%.1fs)\n",
		len(slots), time.Since(t0).Seconds())
	segs := fair.Build(g, br, slots, fair.DefaultBands())
	fmt.Fprintf(os.Stderr, "fair: %d segments across %d bands (%.1fs)\n",
		len(segs), len(fair.DefaultBands()), time.Since(t0).Seconds())
	die(writeSegmentsGeoJSON(*out, segs, frame))
	fmt.Fprintf(os.Stderr, "chart: wrote %s (%.1fs total)\n", *out, time.Since(t0).Seconds())
}

func sound(args []string) {
	fs := flag.NewFlagSet("sound", flag.ExitOnError)
	netPath := fs.String("network", "", "drawn network JSON (ground truth)")
	buildPath := fs.String("build", "", "build GeoJSON to grade")
	fs.Parse(args)
	if *netPath == "" || *buildPath == "" {
		fs.Usage()
		os.Exit(2)
	}
	net, err := sketch.LoadNetwork(*netPath)
	die(err)
	if len(net.Lines) == 0 {
		die(fmt.Errorf("network is empty"))
	}
	frame := geo.NewFrame(geo.LL{
		Lon: net.Lines[0].Coords[0][0], Lat: net.Lines[0].Coords[0][1],
	})
	feats, err := loadBuild(*buildPath, frame)
	die(err)
	res := sketch.Score(net, feats, frame)
	res.Print()
	if res.Failures > 0 {
		os.Exit(1)
	}
}

func atlasCmd(args []string) {
	fs := flag.NewFlagSet("atlas", flag.ExitOnError)
	dir := fs.String("sketches", "sketches", "sketch storage directory")
	addr := fs.String("addr", "127.0.0.1:8765", "listen address")
	fs.Parse(args)
	die((&atlas.Server{Dir: *dir}).ListenAndServe(*addr))
}

func frameOf(ways []osm.Way) geo.Frame {
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

func writeSegmentsGeoJSON(path string, segs []fair.Segment, frame geo.Frame) error {
	type feature struct {
		Type  string         `json:"type"`
		Props map[string]any `json:"properties"`
		Geom  struct {
			Type   string       `json:"type"`
			Coords [][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	var fc struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}
	fc.Type = "FeatureCollection"
	for si, s := range segs {
		var f feature
		f.Type = "Feature"
		f.Props = map[string]any{
			"seg": si, "kind": s.Kind, "color": s.Color,
			"routes": s.Routes, "labels": s.Labels,
			"slots": s.Slots, "nslots": s.NSlots,
			"corridor": s.Corridor, "to_corridor": s.ToCorr,
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
	type feature struct {
		Type  string         `json:"type"`
		Props map[string]any `json:"properties"`
		Geom  struct {
			Type   string       `json:"type"`
			Coords [][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	var fc struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}
	fc.Type = "FeatureCollection"
	for _, c := range g.Corridors {
		var f feature
		f.Type = "Feature"
		f.Props = map[string]any{
			"corridor": c.ID, "strands": len(c.Strands),
			"node_a": c.NodeA, "node_b": c.NodeB,
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

func writeGeoJSON(path string, bundles []bundle.Bundle, frame geo.Frame) error {
	type feature struct {
		Type  string         `json:"type"`
		Props map[string]any `json:"properties"`
		Geom  struct {
			Type   string       `json:"type"`
			Coords [][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	var fc struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}
	fc.Type = "FeatureCollection"
	for bi, b := range bundles {
		var f feature
		f.Type = "Feature"
		f.Props = map[string]any{
			"bundle": bi, "tracks": len(b.Tracks),
			"len_m": int(b.Centerline.Len()),
		}
		f.Geom.Type = "LineString"
		for _, p := range b.Centerline.Resample(8) {
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

func loadBuild(path string, frame geo.Frame) ([]sketch.BuildFeature, error) {
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
		// score the base band only (z15 lives in band_min<=15<=band_max)
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

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "portolan:", err)
		os.Exit(1)
	}
}
