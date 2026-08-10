package corridor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// maxReport caps how many offenders one error names. Ten is enough to
// see the pattern and few enough to read; the count in the message says
// how many more there were.
const maxReport = 10

// Network resolves the graph's topology and projects it into the frame.
//
// Node identity is resolved in one of two ways, and which one applied is
// reported back so a caller can tell whether it got the exact contract
// or the tolerant one:
//
//   - EXPLICIT — the edge carries `from`/`to` naming node features. This
//     is what writeNetwork emits and what callers should emit. A
//     dangling reference is an error, never a silent drop.
//   - SNAPPED — the edge carries neither, so each endpoint joins the
//     nearest node within SnapTol, and endpoints with no node in reach
//     cluster into new ones. Documented, but lossy by construction: two
//     junctions a metre apart become one.
//
// Mixing is allowed per edge. An edge that names one end and not the
// other snaps only the unnamed end.
func (g *Graph) Network(frame geo.Frame) (*stages.Network, error) {
	idx := make(map[string]int, len(g.Nodes))
	var dupes []string
	net := &stages.Network{}
	for _, n := range g.Nodes {
		if _, seen := idx[n.ID]; seen {
			dupes = append(dupes, n.ID)
			continue
		}
		idx[n.ID] = len(net.Nodes)
		net.Nodes = append(net.Nodes, stages.Node{At: frame.ToXY(n.At)})
	}
	if len(dupes) > 0 {
		return nil, fmt.Errorf("corridor: %d duplicate node ids: %s",
			len(dupes), listOf(dupes))
	}

	// snapping index over the declared nodes, in frame metres. Built
	// whether or not it gets used — it is O(nodes) and the alternative
	// is deciding twice.
	snap := newPointIndex(net.Nodes, SnapTol)

	var dangling []string
	resolve := func(e Edge, id string, at geo.LL, end string) (int, error) {
		if id != "" {
			i, ok := idx[id]
			if !ok {
				dangling = append(dangling, fmt.Sprintf("%s.%s→%q", e.ID, end, id))
				return -1, nil
			}
			return i, nil
		}
		p := frame.ToXY(at)
		if i, ok := snap.nearest(p); ok {
			return i, nil
		}
		// no node in reach: invent one. This is the only place the
		// reader adds topology the caller did not state, and it is
		// counted so the caller hears about it.
		net.Nodes = append(net.Nodes, stages.Node{At: p})
		i := len(net.Nodes) - 1
		snap.add(i, p)
		g.Synthesized++
		return i, nil
	}

	for _, e := range g.Edges {
		from, err := resolve(e, e.From, e.Pts[0], "from")
		if err != nil {
			return nil, err
		}
		to, err := resolve(e, e.To, e.Pts[len(e.Pts)-1], "to")
		if err != nil {
			return nil, err
		}
		if from < 0 || to < 0 {
			continue // dangling; reported in full below
		}
		pts := make([]geo.Pt, len(e.Pts))
		for i, ll := range e.Pts {
			pts[i] = frame.ToXY(ll)
		}
		net.Edges = append(net.Edges, stages.Edge{
			From: from, To: to, Pts: pts,
			Routes: append([]string(nil), e.Routes...),
			Tracks: e.Tracks, Gap: e.Gap, OneWay: e.OneWay,
		})
	}
	// A dangling reference means the caller's graph and the reader's
	// disagree about what exists. Guessing past it draws a map that is
	// quietly not the one that was asked for, so it fails.
	if len(dangling) > 0 {
		return nil, fmt.Errorf("corridor: %d edge endpoints reference nodes that do not exist: %s",
			len(dangling), listOf(dangling))
	}
	stages.RebuildAdj(net)
	return net, nil
}

// Validate checks the graph against the feed it will be drawn with, and
// reports what a caller cannot see from the geometry alone. Everything
// it returns an error for is a contradiction the caller has to fix;
// everything merely surprising goes to logf, because a network can be
// legitimately odd (see Components).
func (g *Graph) Validate(net *stages.Network, routes map[string]gtfs.Route,
	frame geo.Frame, logf func(string, ...any)) error {

	// route ids on an edge but not in routes.txt. This is the mismatch
	// that costs the most debugging time: the map builds, the ribbon is
	// simply absent, and nothing says why.
	unknown := map[string]int{}
	for _, e := range g.Edges {
		for _, rid := range e.Routes {
			if _, ok := routes[rid]; !ok {
				unknown[rid]++
			}
		}
	}
	if len(unknown) > 0 {
		ids := make([]string, 0, len(unknown))
		for id := range unknown {
			ids = append(ids, fmt.Sprintf("%q(%d edges)", id, unknown[id]))
		}
		sort.Strings(ids)
		return fmt.Errorf("corridor: %d route ids ride corridors but are not in routes.txt: %s",
			len(unknown), listOf(ids))
	}
	// an edge nobody rides draws nothing. Not fatal — a caller may be
	// exporting a corridor for a route that is out of the window, or
	// staging one that has no service yet — but it is never intentional
	// in a finished network.
	empty := 0
	for _, e := range g.Edges {
		if len(e.Routes) == 0 {
			empty++
		}
	}
	if empty > 0 {
		logf("corridors: %d of %d edges carry no routes and will not draw", empty, len(g.Edges))
	}
	if g.Synthesized > 0 {
		logf("corridors: %d nodes synthesized from unreferenced endpoints (snap tolerance %.1f m) — "+
			"emit from/to on every edge for an exact graph", g.Synthesized, SnapTol)
	}
	// There is deliberately NO complaint about routes.txt entries that
	// ride no corridor. The graph is the authority on what draws, and a
	// real feed carries a whole bus network the caller never meant to
	// chart — Atlanta's is 81 of 86 routes. Reporting those as missing
	// would bury the one report that matters (the unknown ids above) in
	// noise about routes nobody asked for.
	//
	// Components are REPORTED, not rejected. A disconnected graph is
	// usually a mistake and occasionally the truth — a ferry that
	// touches no track, a light rail system in two halves, a bbox that
	// cut a city in two — and rejecting it would make those networks
	// unchartable for no gain. The count and a sample location per
	// component are enough to recognise an accident on sight.
	comps := components(net)
	if len(comps) > 1 {
		var parts []string
		for _, c := range comps[:min(len(comps), maxReport)] {
			// lat,lon, not frame metres: the point of the sample is that
			// a caller can paste it into a map and see the accident
			ll := frame.ToLL(net.Nodes[c.sample].At)
			parts = append(parts, fmt.Sprintf("%d edges near %.5f,%.5f", c.edges, ll.Lat, ll.Lon))
		}
		logf("corridors: graph is in %d disconnected components: %s",
			len(comps), strings.Join(parts, "; "))
	}
	// divided corridors: two edges between the same node pair. Legal
	// when they are a one-way pair (the caller models a track per
	// direction) and a duplicate otherwise.
	type pair struct{ a, b int }
	byPair := map[pair][]int{}
	for ei, e := range net.Edges {
		k := pair{e.From, e.To}
		if k.a > k.b {
			k.a, k.b = k.b, k.a
		}
		byPair[k] = append(byPair[k], ei)
	}
	divided, dup := 0, 0
	for _, eis := range byPair {
		if len(eis) < 2 {
			continue
		}
		oneway := true
		for _, ei := range eis {
			if net.Edges[ei].OneWay == "" {
				oneway = false
			}
		}
		if oneway {
			divided++
		} else {
			dup++
		}
	}
	if divided > 0 {
		logf("corridors: %d divided corridors (a one-way edge per direction)", divided)
	}
	if dup > 0 {
		logf("corridors: %d node pairs joined by more than one two-way edge — "+
			"mark a divided corridor's edges oneway=forward|backward, or collapse them", dup)
	}
	return nil
}

type comp struct {
	edges  int
	sample int // a node index in the component
}

// components walks the graph over its edges. Order is by descending
// size then by lowest node index, so the report is stable across runs.
func components(net *stages.Network) []comp {
	seen := make([]bool, len(net.Nodes))
	var out []comp
	for start := range net.Nodes {
		if seen[start] || len(net.Nodes[start].Adj) == 0 {
			continue
		}
		c := comp{sample: start}
		edges := map[int]bool{}
		stack := []int{start}
		seen[start] = true
		for len(stack) > 0 {
			ni := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, ei := range net.Nodes[ni].Adj {
				edges[ei] = true
				for _, other := range []int{net.Edges[ei].From, net.Edges[ei].To} {
					if !seen[other] {
						seen[other] = true
						stack = append(stack, other)
					}
				}
			}
		}
		c.edges = len(edges)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].edges != out[j].edges {
			return out[i].edges > out[j].edges
		}
		return out[i].sample < out[j].sample
	})
	return out
}

func listOf(ss []string) string {
	if len(ss) <= maxReport {
		return strings.Join(ss, ", ")
	}
	return fmt.Sprintf("%s … and %d more", strings.Join(ss[:maxReport], ", "), len(ss)-maxReport)
}

// pointIndex is a uniform grid over the declared nodes, cell-sized to
// the snap tolerance so a nearest query touches nine cells. A corridor
// graph is small by construction, but a throat packs many nodes into a
// few metres and a linear scan there is the one place this would show.
type pointIndex struct {
	tol   float64
	cells map[[2]int][]int
	at    []geo.Pt
}

func newPointIndex(nodes []stages.Node, tol float64) *pointIndex {
	ix := &pointIndex{tol: tol, cells: map[[2]int][]int{}, at: make([]geo.Pt, len(nodes))}
	for i, n := range nodes {
		ix.at[i] = n.At
		ix.cells[ix.key(n.At)] = append(ix.cells[ix.key(n.At)], i)
	}
	return ix
}

func (ix *pointIndex) key(p geo.Pt) [2]int {
	return [2]int{int(p.X / ix.tol), int(p.Y / ix.tol)}
}

func (ix *pointIndex) add(i int, p geo.Pt) {
	for len(ix.at) <= i {
		ix.at = append(ix.at, geo.Pt{})
	}
	ix.at[i] = p
	ix.cells[ix.key(p)] = append(ix.cells[ix.key(p)], i)
}

// nearest returns the closest node within tol. Ties break to the lowest
// index: two nodes equidistant from an endpoint must resolve the same
// way on every run or the whole build stops being deterministic.
func (ix *pointIndex) nearest(p geo.Pt) (int, bool) {
	k := ix.key(p)
	best, bestD := -1, ix.tol
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			for _, i := range ix.cells[[2]int{k[0] + dx, k[1] + dy}] {
				if d := ix.at[i].Dist(p); d < bestD || (d == bestD && i < best) {
					best, bestD = i, d
				}
			}
		}
	}
	return best, best >= 0
}
