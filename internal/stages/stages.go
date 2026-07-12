// Package stages holds the pipeline stage contracts — ALL STUBS after the
// 2026-07-13 reset. The previous three architectures (corridor-state graph,
// support-graph averaging, string-trace) are deleted; docs/FRESH-START.md
// is the complete brief for the reimplementation. Infrastructure that
// SURVIVES the reset and must be reused: internal/geo (cross-sections,
// grid, arc-walking — every rule in it was paid for), internal/gtfs +
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

// Network is the bundled line graph every stage after BUNDLE operates on:
// edges carry the set of routes travelling them and meet exactly at nodes.
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
	Routes   []string // route ids riding this edge
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

// Bundle merges the GTFS pattern shapes into an overlap-free line graph
// (route bundling FIRST — Transit-app order). Continuity law: every input
// pattern must map to a CONNECTED walk in the result.
func Bundle(patterns []gtfs.Pattern, strands []bundle.Strand, frame geo.Frame) (*Network, error) {
	return nil, ErrNotImplemented
}

// Order assigns each edge's COLOR GROUPS to slots minimizing crossings at
// nodes (same-color routes share one ribbon — trunk rule).
func Order(n *Network) (map[int][]string, error) {
	return nil, ErrNotImplemented
}

// Fair cuts node areas per zoom band and reconnects each color between its
// slot positions (node-front model; circular arcs preserve parallelism).
func Fair(n *Network, slots map[int][]string) ([]Segment, error) {
	return nil, ErrNotImplemented
}
