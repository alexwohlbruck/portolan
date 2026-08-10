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

// agencyNames: agency_id → display name (gtfs agency.txt), for labelling
// agency-trunked regional groups. Set by the pipeline before FAIR.
var agencyNames map[string]string

func SetAgencyNames(m map[string]string) { agencyNames = m }

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
	// OneWay: which way this edge is ridden, when the corridor is
	// DIVIDED — a caller whose model splits a corridor into a track per
	// direction hands over both, and both draw. "" (everything the OSM
	// path produces) is an undivided corridor carrying both directions
	// on one centerline; "forward" is From→To only, "backward" To→From.
	// Two edges between the same node pair with opposite OneWay are a
	// divided corridor, NOT a duplicate — the corridor validator knows
	// the difference, and ORDER slots each of the pair on its own.
	OneWay string
	// Acts: per-route weekly activity ON THIS EDGE — the OR of the masks
	// of the patterns that actually ride it (docs/DYNAMIC-SERVICE.md).
	// This is where short-turns live: the tail beyond a short-turn
	// terminal carries only the full-length pattern's hours, so dynamic
	// rendering can put the tail to sleep while the core stays lit. Nil
	// when the pipeline ran without service info.
	Acts map[string]gtfs.Mask168
}

// RebuildAdj recomputes every node's incident-edge list. Adjacency is an
// index INTO Edges, and a self-loop is listed once — anything building a
// Network from outside this package (internal/corridor) must land on the
// same invariant, so it calls this rather than reimplementing it.
func RebuildAdj(net *Network) { rebuildAdj(net) }

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
	// Acts: hex Mask168 per route, aligned with Routes — the hours each
	// member actually rides THIS segment (empty when no service info).
	Acts []string
	Line *geo.Line
}
