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

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

var ErrNotImplemented = errors.New("stage not implemented — see docs/FRESH-START.md")

// Path is one GTFS route pattern matched onto real OSM geometry: a
// continuous walk over ways, never leaving them.
type Path struct {
	Pattern gtfs.Pattern
	Line    *geo.Line
	WayIDs  []string   // the OSM ways walked, in order ("gap" marks bridges)
	Steps   []PathStep // the exact walk, for SPLIT
}

// PathStep is one hop of a matched walk: a directed graph piece, or a gap
// bridge carrying shape geometry where OSM has no track.
type PathStep struct {
	Piece int  // track-graph piece id, -1 for a gap
	Rev   bool // ridden against the piece's storage orientation
	Gap   *geo.Line
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
	Gap      bool     // shape-bridged (no OSM track) — render dashed
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
	Mode      string // mode class name (internal/mode) — style hierarchy key
	Slot      int
	NSlots    int
	OffsetPx  float64
	OffFromPx float64
	OffToPx   float64
	BandMin   int
	BandMax   int
	Line      *geo.Line
}
