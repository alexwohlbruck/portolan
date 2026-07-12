// Package pipeline wires the stages end-to-end. Both the CLI and the atlas
// workbench call it — the atlas "rebuild" button and `portolan chart` are
// the same code path.
//
// v2 architecture (docs/CENTERLINE.md): GTFS pattern paths → SUPPORT-GRAPH
// construction (Brosi & Bast map construction: merge-by-averaging, rounds
// to convergence, intersection smoothing, turn restrictions) → median-strand
// REFINEMENT against OSM tracks → color-trunked ORDER → banded FAIR → EMIT.
// Routes are inserted as continuous paths and merged by node-sharing, so
// they can never break; a continuity gate verifies anyway.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
	"github.com/alexwohlbruck/portolan/internal/support"
)

// Dials are every tuning parameter, exposed in the atlas UI. Flat JSON so
// the panel can be generated and the rebuild button can POST overrides.
type Dials struct {
	// support-graph construction (docs/CENTERLINE.md, paper defaults)
	SampleL   float64 `json:"sample_l"`
	MergeD    float64 `json:"merge_d"`
	ConvGap   float64 `json:"conv_gap"`
	MaxRounds float64 `json:"max_rounds"`
	SmoothD   float64 `json:"smooth_d"`
	TurnGapN  float64 `json:"turn_gap_n"`
	// track refinement (median strands, cross-sections)
	RefineReach float64 `json:"refine_reach"`
	StrandGap   float64 `json:"strand_gap"`
	RefineIters float64 `json:"refine_iters"`
	FinishSigma float64 `json:"finish_sigma"`
	JoinTol     float64 `json:"join_tol"`
	// fairing
	BandBase float64 `json:"band_base"`
	// gtfs
	Cover float64 `json:"cover"`
}

func DefaultDials() Dials {
	sp := support.DefaultParams()
	rp := bundle.DefaultParams()
	return Dials{
		SampleL: sp.SampleL, MergeD: sp.MergeD, ConvGap: sp.ConvGap,
		MaxRounds: float64(sp.MaxRounds), SmoothD: sp.SmoothD,
		TurnGapN:    float64(sp.TurnGapN),
		RefineReach: rp.Reach, StrandGap: rp.StrandGap,
		RefineIters: float64(rp.Iters), FinishSigma: rp.FinishSigma,
		JoinTol:  1.0,
		BandBase: 140,
		Cover:    0.99,
	}
}

func (d Dials) supportParams() support.Params {
	p := support.DefaultParams()
	p.SampleL = d.SampleL
	p.MergeD = d.MergeD
	p.ConvGap = d.ConvGap
	p.MaxRounds = int(d.MaxRounds)
	p.SmoothD = d.SmoothD
	p.TurnGapN = int(d.TurnGapN)
	return p
}

func (d Dials) refineParams() bundle.Params {
	p := bundle.DefaultParams()
	p.Reach = d.RefineReach
	p.StrandGap = d.StrandGap
	p.Iters = int(d.RefineIters)
	p.FinishSigma = d.FinishSigma
	p.ThroughFrac = 0
	return p
}

func (d Dials) bands() []fair.Band {
	b := d.BandBase
	return []fair.Band{
		{MinZoom: 15, MaxZoom: 24, CutM: b},
		{MinZoom: 14, MaxZoom: 14, CutM: b * 2},
		{MinZoom: 13, MaxZoom: 13, CutM: b * 4},
		{MinZoom: 0, MaxZoom: 12, CutM: b * 8},
	}
}

type ChartOpts struct {
	GTFS  string // GTFS zip
	Rail  string // OSM rail extract GeoJSON
	Out   string // output GeoJSON (stage dumps written alongside)
	Dials *Dials
}

// Chart runs the full pipeline, writing stage dumps for the workbench:
// <out>.strands.geojson, <out>.support.geojson, <out>.graph.geojson (refined
// edges), <out>.nodes.geojson, and <out> (final segments).
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
	strandLines := make([]*geo.Line, len(strands))
	for i, s := range strands {
		strandLines[i] = s.Line
	}
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
	// rail modes only; patterns become support-graph input paths
	var paths []support.Path
	meta := map[string]gtfs.Pattern{}
	for _, pat := range feed.Patterns {
		t := pat.Route.Type
		if !(t == 0 || t == 1 || t == 2 || (t >= 100 && t < 200)) {
			continue
		}
		pts := make([]geo.Pt, len(pat.Shape))
		for i, ll := range pat.Shape {
			pts[i] = frame.ToXY(ll)
		}
		pid := pat.Route.ID + "|" + pat.ShapeID
		meta[pid] = pat
		paths = append(paths, support.Path{
			ID: pid, Route: pat.Route.ID, Color: routeColor(pat.Route),
			Label: pat.Route.ShortName, Type: t, Pts: pts,
		})
	}
	logf("chart: %d rail patterns of %d total (%.1fs)",
		len(paths), len(feed.Patterns), time.Since(t0).Seconds())

	// SUPPORT GRAPH — the documented map construction
	sg := support.Build(paths, d.supportParams(), logf)
	if err := writeSupport(o.Out+".support.geojson", sg, frame); err != nil {
		return err
	}

	// REFINEMENT — pull every edge onto the OSM track-bundle median
	grid := geo.NewGrid(strandLines, 64)
	rp := d.refineParams()
	refined := 0
	for _, e := range sg.Edges {
		l := e.Line()
		if l.Len() < 40 {
			continue
		}
		var members []*geo.Line
		seen := map[int]bool{}
		for _, q := range l.Resample(40) {
			grid.Near(q, rp.Reach, func(si int) {
				if !seen[si] {
					seen[si] = true
					members = append(members, strandLines[si])
				}
			})
		}
		if len(members) == 0 {
			continue // track data gap: the pattern geometry stands (bridge)
		}
		nl := bundle.Refine(l, members, rp)
		// ease back into the node endpoints (offset-scaled ramps — a hard
		// pin dumps the whole median offset into one seam kink)
		nl = bundle.TieEnds(nl, e.Pts[0], e.Pts[len(e.Pts)-1])
		e.Pts = nl.Pts
		refined++
	}
	logf("refine: %d/%d edges pulled onto track medians (%.1fs)",
		refined, len(sg.Edges), time.Since(t0).Seconds())

	// adapter → existing ORDER/FAIR machinery
	g, br := adapt(sg, meta)
	if err := writeGraphGeoJSON(o.Out+".graph.geojson", g, frame); err != nil {
		return err
	}
	if err := writeNodes(o.Out+".nodes.geojson", g, frame); err != nil {
		return err
	}

	// CONTINUITY GATE — a route may never break (owner's law #1)
	broken := continuityGate(sg, logf)
	if broken > 0 {
		logf("GATE: %d patterns have unconnected adjacencies — routes broken", broken)
	} else {
		logf("GATE: all pattern paths continuous")
	}

	slots := order.Assign(g, br, 4)
	logf("order: slots on %d edges (%.1fs)", len(slots), time.Since(t0).Seconds())
	segs := fair.Build(g, br, slots, d.bands())
	logf("fair: %d segments across %d bands (%.1fs)",
		len(segs), 4, time.Since(t0).Seconds())
	if err := WriteSegmentsGeoJSON(o.Out, segs, frame); err != nil {
		return err
	}
	logf("chart: wrote %s (%.1fs total)", o.Out, time.Since(t0).Seconds())
	return nil
}

// adapt converts the support graph into the corridor-graph + berth shapes
// the ORDER/FAIR stages consume. Moves come from the turn-restriction test:
// a route continues e→f only where one of its patterns' occupancy intervals
// are adjacent (Chicago-Loop rule).
func adapt(sg *support.Graph, meta map[string]gtfs.Pattern) (*bundle.Graph, *berth.Result) {
	g := &bundle.Graph{}
	for _, n := range sg.Nodes {
		g.Nodes = append(g.Nodes, bundle.Node{ID: len(g.Nodes), At: n.At})
	}
	for ei, e := range sg.Edges {
		g.Corridors = append(g.Corridors, bundle.Corridor{
			ID: ei, Centerline: e.Line(), NodeA: e.From, NodeB: e.To,
		})
		g.Nodes[e.From].Corridors = append(g.Nodes[e.From].Corridors, ei)
		if e.To != e.From {
			g.Nodes[e.To].Corridors = append(g.Nodes[e.To].Corridors, ei)
		}
	}
	br := &berth.Result{Berths: map[int][]berth.Berth{}, Moves: map[[2]int]map[string]bool{}}
	for ei, e := range sg.Edges {
		seen := map[string]bool{}
		for pid := range e.Occupancy {
			pat, ok := meta[pid]
			if !ok || seen[pat.Route.ID] {
				continue
			}
			seen[pat.Route.ID] = true
			br.Berths[ei] = append(br.Berths[ei], berth.Berth{
				RouteID: pat.Route.ID, Color: routeColor(pat.Route),
				Label: pat.Route.ShortName, Type: pat.Route.Type,
			})
		}
		sort.Slice(br.Berths[ei], func(a, b int) bool {
			x, y := br.Berths[ei][a], br.Berths[ei][b]
			if x.Color != y.Color {
				return x.Color < y.Color
			}
			return x.RouteID < y.RouteID
		})
	}
	turnGap := support.DefaultParams().TurnGapN
	for ni := range sg.Nodes {
		adj := sg.Nodes[ni].Adj
		for i := 0; i < len(adj); i++ {
			for j := 0; j < len(adj); j++ {
				if i == j {
					continue
				}
				a, b := adj[i], adj[j]
				ea, eb := sg.Edges[a], sg.Edges[b]
				for pid := range ea.Occupancy {
					pat, ok := meta[pid]
					if !ok {
						continue
					}
					if sg.Connects(pid, ea, eb, turnGap*4) {
						if br.Moves[[2]int{a, b}] == nil {
							br.Moves[[2]int{a, b}] = map[string]bool{}
						}
						br.Moves[[2]int{a, b}][pat.Route.ID] = true
					}
				}
			}
		}
	}
	return g, br
}

// continuityGate: a route may never break (owner's law #1). The test is
// order-free: the subgraph induced by a pattern's occupied edges must be
// ONE connected component.
func continuityGate(sg *support.Graph, logf func(string, ...any)) int {
	broken := 0
	for _, pa := range sg.Paths {
		parent := map[int]int{}
		var find func(int) int
		find = func(x int) int {
			if parent[x] != x {
				parent[x] = find(parent[x])
			}
			return parent[x]
		}
		seen := false
		for _, e := range sg.Edges {
			if _, ok := e.Occupancy[pa.ID]; !ok {
				continue
			}
			seen = true
			for _, n := range []int{e.From, e.To} {
				if _, ok := parent[n]; !ok {
					parent[n] = n
				}
			}
			parent[find(e.From)] = find(e.To)
		}
		if !seen {
			continue
		}
		comps := map[int]bool{}
		for n := range parent {
			comps[find(n)] = true
		}
		if len(comps) > 1 {
			broken++
			if broken <= 4 {
				logf("  broken: %s in %d components", pa.ID, len(comps))
			}
		}
	}
	return broken
}

var typeColor = map[int]string{
	0: "999933", 1: "333399", 2: "663300", 3: "336699", 4: "006666",
}

func routeColor(r gtfs.Route) string {
	if r.Color != "" {
		return r.Color
	}
	if c, ok := typeColor[r.Type]; ok {
		return c
	}
	return "555555"
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

func lineCoords(l *geo.Line, step float64, frame geo.Frame) json.RawMessage {
	var cs [][2]float64
	for _, p := range l.Resample(step) {
		ll := frame.ToLL(p)
		cs = append(cs, [2]float64{ll.Lon, ll.Lat})
	}
	raw, _ := json.Marshal(cs)
	return raw
}

func lineFeature(props map[string]any, l *geo.Line, step float64, frame geo.Frame) (feature, bool) {
	raw := lineCoords(l, step, frame)
	var f feature
	f.Type = "Feature"
	f.Props = props
	f.Geom = geomJSON{Type: "LineString", Coords: raw}
	return f, len(raw) > 10
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

func writeSupport(path string, sg *support.Graph, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for ei, e := range sg.Edges {
		if f, ok := lineFeature(map[string]any{
			"kind": "support", "edge": ei, "npaths": len(e.Occupancy),
			"node_a": e.From, "node_b": e.To, "len_m": int(e.Line().Len()),
		}, e.Line(), 8, frame); ok {
			fc.Features = append(fc.Features, f)
		}
	}
	return writeFC(path, fc)
}

func writeNodes(path string, g *bundle.Graph, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for _, n := range g.Nodes {
		ll := frame.ToLL(n.At)
		raw, _ := json.Marshal([2]float64{ll.Lon, ll.Lat})
		fc.Features = append(fc.Features, feature{
			Type:  "Feature",
			Props: map[string]any{"kind": "node", "node": n.ID, "degree": len(n.Corridors)},
			Geom:  geomJSON{Type: "Point", Coords: raw},
		})
	}
	return writeFC(path, fc)
}

func writeGraphGeoJSON(path string, g *bundle.Graph, frame geo.Frame) error {
	fc := collection{Type: "FeatureCollection"}
	for _, c := range g.Corridors {
		if f, ok := lineFeature(map[string]any{
			"kind": "corridor", "corridor": c.ID,
			"node_a": c.NodeA, "node_b": c.NodeB,
			"len_m": int(c.Centerline.Len()),
		}, c.Centerline, 8, frame); ok {
			fc.Features = append(fc.Features, f)
		}
	}
	return writeFC(path, fc)
}

// WriteSegmentsGeoJSON emits per-ribbon features in the parchment
// transit_line_segments contract.
func WriteSegmentsGeoJSON(path string, segs []fair.Segment, frame geo.Frame) error {
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
			"offset_px":   round2(s.OffsetPx),
			"off_from_px": round2(s.OffFromPx),
			"off_to_px":   round2(s.OffToPx),
			"corridor": s.Corridor, "to_corridor": s.ToCorr,
			"band_min": s.Band.MinZoom, "band_max": s.Band.MaxZoom,
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

func round2(v float64) float64 { return float64(int(v*100+0.5*sign(v))) / 100 }

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
