// Package stages holds the pipeline stage contracts — ALL STUBS after the
// 2026-07-13 reset. The process is owner-specified (docs/FRESH-START.md):
// MATCH (path-match each GTFS route onto real OSM ways) → SPLIT (junction
// detection, divide into segments, assign routes per segment) → ORDER
// (parallel-line slot optimization) → FAIR (smooth junction curvature) →
// emit (scaffolding in internal/pipeline; MapLibre fork renders offset
// transitions). Infrastructure that SURVIVES the reset and must be reused:
// internal/geo (cross-sections, grid, arc-walking), internal/gtfs +
// internal/osm loaders, internal/bundle (Chain strands, MedianStrand,
// Refine — the owner's centerline rules, unit-tested), internal/sketch
// (the scorer + gates incl. duplication), and the atlas workbench.
package stages

import (
	"errors"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

var ErrNotImplemented = errors.New("stage not implemented — see docs/FRESH-START.md")

// Path is one GTFS route pattern matched onto real OSM geometry: a
// continuous walk over ways, never leaving them.
type Path struct {
	Pattern gtfs.Pattern
	Line    *geo.Line
	WayIDs  []string // the OSM ways walked, in order
}

// Network is the segment graph every stage after SPLIT operates on: edges
// carry the set of routes travelling them and meet exactly at junctions.
type Network struct {
	Nodes []Node
	Edges []Edge
}

type Node struct {
	At  geo.Pt
	Adj []int
}

type Edge struct {
	From, To int
	Pts      []geo.Pt
	Routes   []string // route ids riding this segment
	Tracks   int      // physical track count, if derived
}

// Segment is one emitted ribbon feature (parchment transit_line_segments
// contract: kind steady|transition|bridge, color-trunked, travel-frame
// offsets in px, zoom bands).
type Segment struct {
	Kind      string
	Color     string
	Routes    []string
	Label     string
	RouteType int
	Slot      int
	NSlots    int
	OffsetPx  float64
	OffFromPx float64
	OffToPx   float64
	BandMin   int
	BandMax   int
	Line      *geo.Line
}

// Match path-matches each GTFS pattern onto the mode-appropriate OSM layer
// (rails for trains, roads for buses, sea routes for ferries). Owner's
// rules: routes on similar paths MUST land on the same matched path; no
// jumping between paths — always follow real ways, with crossover segments
// (short ways connecting longer ways: switches, median turns) penalized;
// mainlines only — spurs and yards ignored except at station terminals.
func Match(patterns []gtfs.Pattern, ways []bundle.Track, frame geo.Frame) ([]Path, error) {
	return nil, ErrNotImplemented
}

// Split detects junctions where matched paths intersect or diverge,
// divides the paths into segments at each junction, and assigns each
// segment the set of routes riding it.
func Split(paths []Path) (*Network, error) {
	return nil, ErrNotImplemented
}

// Order assigns each segment's COLOR GROUPS to parallel-line slots
// minimizing crossings at junctions (same-color routes share one ribbon).
func Order(n *Network) (map[int][]string, error) {
	return nil, ErrNotImplemented
}

// Fair draws the junction connections with smooth curvature between slot
// positions (circular arcs preserve parallelism) and cuts zoom bands.
func Fair(n *Network, slots map[int][]string) ([]Segment, error) {
	return nil, ErrNotImplemented
}
