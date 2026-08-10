package corridor

import (
	"fmt"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// Traversal — which junction MOVEMENTS a route makes.
//
// FAIR does not need a map-match. It needs one thing from the matched
// walks, and only one: at a junction where two of a route's legs meet,
// does the route actually ride from one to the other? A loop circulator
// carries its id on three legs of a corner and rides two of the three
// pairings; the third must not get a turn drawn. Everything else FAIR
// takes from the network itself.
//
// That makes the requirement much smaller than MATCH's. Reading FAIR's
// gate (`ridesPair`), a route with no walk at all is given the benefit
// of the doubt and every pairing is attested. So a walk is only load-
// bearing where a route's OWN subgraph has a node of degree ≥ 3 — a
// fork, or a corner the route crosses twice. Below that degree every
// movement the route could make is one it does make, and deriving the
// walk and supplying nothing are the same map.
//
// Hence the ladder, best evidence first:
//
//  1. shapes.txt lying on the corridors — the walk IS the shape, no
//     search at all, because the contract says the coordinates are
//     already on the graph. Deviation is measured and reported.
//  2. ordered stop sequences (trips.txt + stop_times.txt) — each stop
//     snaps to a corridor and consecutive stops join by the shortest
//     ride over the route's own subgraph.
//  3. structure — a route whose subgraph is a simple path or a simple
//     cycle has exactly one traversal, so it is read straight off.
//  4. nothing — reported per route, and FAIR falls back to attesting
//     every pairing, which is right below degree 3 and a guess above it.

// shapeOnCorridorTol is how far a shape coordinate may sit from the
// corridor it rides before the shape stops being evidence about this
// graph. Generous on purpose: the check is there to catch a shape from
// a DIFFERENT network (the caller exported mismatched files), not to
// police rounding.
const shapeOnCorridorTol = 30.0

// Traversals builds the walks FAIR attests movements from. It never
// fails the build: evidence that is missing is reported, because a
// corridor graph that draws with one phantom turn is more useful than
// one that refuses to draw, and the report says exactly which route to
// give a stop sequence to.
func (g *Graph) Traversals(net *stages.Network, feed *gtfs.Feed, frame geo.Frame,
	logf func(string, ...any)) []stages.Path {

	byRoute := map[string][]int{}
	for ei, e := range net.Edges {
		for _, rid := range e.Routes {
			byRoute[rid] = append(byRoute[rid], ei)
		}
	}
	pats := map[string][]gtfs.Pattern{}
	if feed != nil {
		for _, p := range feed.Patterns {
			pats[p.Route.ID] = append(pats[p.Route.ID], p)
		}
	}
	var stopLL map[string]geo.LL
	if feed != nil {
		stopLL = make(map[string]geo.LL, len(feed.Stops))
		for id, s := range feed.Stops {
			stopLL[id] = s.LL
		}
	}

	rids := make([]string, 0, len(byRoute))
	for rid := range byRoute {
		rids = append(rids, rid)
	}
	sort.Strings(rids) // emission order is part of the build's determinism
	lines := edgeLines(net)

	var out []stages.Path
	var fromShape, fromStops, fromStruct int
	var unattested []string
	var strayed []string
	for _, rid := range rids {
		sub := newSubgraph(net, lines, byRoute[rid])
		var got []stages.Path

		for _, p := range pats[rid] {
			if len(p.Shape) < 2 {
				continue
			}
			pts := make([]geo.Pt, len(p.Shape))
			for i, ll := range p.Shape {
				pts[i] = frame.ToXY(ll)
			}
			if d, off := sub.firstStray(pts, shapeOnCorridorTol); off {
				strayed = append(strayed, fmt.Sprintf("%s/%s(%.0fm)", rid, p.ShapeID, d))
				continue
			}
			got = append(got, stages.Path{Pattern: p, Line: geo.NewLine(pts)})
		}
		if len(got) > 0 {
			fromShape += len(got)
			out = append(out, got...)
			continue
		}

		for _, p := range pats[rid] {
			if len(p.StopSeq) < 2 {
				continue
			}
			if l := sub.rideThrough(p.StopSeq, stopLL, frame); l != nil {
				got = append(got, stages.Path{Pattern: p, Line: l})
			}
		}
		if len(got) > 0 {
			fromStops += len(got)
			out = append(out, got...)
			continue
		}

		r := gtfs.Route{ID: rid}
		if feed != nil {
			if fr, ok := feed.Routes[rid]; ok {
				r = fr
			}
		}
		walks, complete := sub.structural()
		for i, w := range walks {
			got = append(got, stages.Path{
				Pattern: gtfs.Pattern{Route: r,
					ShapeID: fmt.Sprintf("corridor:%s#%d", rid, i)},
				Line: w,
			})
		}
		if !complete {
			// degree ≥ 3 somewhere in this route's own subgraph and no
			// shape or stop order to say which way round it goes
			unattested = append(unattested, rid)
			got = nil
		}
		fromStruct += len(got)
		out = append(out, got...)
	}

	logf("corridors: traversals — %d from shapes, %d from stop order, %d derived from structure",
		fromShape, fromStops, fromStruct)
	if len(strayed) > 0 {
		logf("corridors: %d shapes stray more than %.0f m from the corridors they name and were "+
			"ignored as traversal evidence: %s", len(strayed), shapeOnCorridorTol, listOf(strayed))
	}
	if len(unattested) > 0 {
		sort.Strings(unattested)
		logf("corridors: %d routes fork or cross themselves and have no shape or stop order to say "+
			"how — every junction movement will be drawn, including any the route does not make. "+
			"Give these routes stop_times.txt in riding order: %s",
			len(unattested), listOf(unattested))
	}
	return out
}

// subgraph is one route's own corridors, and nothing else. Every walk is
// searched inside it: a route can only ride what it is listed on, so
// restricting the search is both faster and the only correct reading.
//
// lines is the WHOLE network's geometry, shared by every subgraph, not a
// per-route copy. geo.Line builds a lazy segment index on first
// proximity query, and rebuilding that index once per route that rides
// the corridor was most of what made traversal slow — five routes over
// one 11 km trunk indexed it five times.
type subgraph struct {
	net   *stages.Network
	edges []int
	adj   map[int][]int // node → incident edges, route's own only
	lines []*geo.Line
}

// edgeLines wraps every edge once, for sharing across subgraphs.
func edgeLines(net *stages.Network) []*geo.Line {
	ls := make([]*geo.Line, len(net.Edges))
	for ei := range net.Edges {
		ls[ei] = geo.NewLine(net.Edges[ei].Pts)
	}
	return ls
}

func newSubgraph(net *stages.Network, lines []*geo.Line, edges []int) *subgraph {
	s := &subgraph{net: net, edges: edges, adj: map[int][]int{}, lines: lines}
	for _, ei := range edges {
		e := net.Edges[ei]
		s.adj[e.From] = append(s.adj[e.From], ei)
		if e.To != e.From {
			s.adj[e.To] = append(s.adj[e.To], ei)
		}
	}
	return s
}

// firstStray reports the first sample that sits further than tol from
// every corridor in the subgraph, and how far off it was.
//
// The question is "is this shape describing THIS graph", which is a
// yes/no — so the hot loop asks the INDEXED predicate (Within walks a
// grid of candidate segments) and only pays for an exact distance on
// the sample that already failed, to put a number in the report. The
// naive form — exact distance from every sample to every corridor —
// was 87 s on a network with eight corridors, because an authored edge
// can be 11 km of vertices and a shape thousands of points.
func (s *subgraph) firstStray(pts []geo.Pt, tol float64) (float64, bool) {
	for _, p := range pts {
		on := false
		for _, ei := range s.edges {
			if s.lines[ei].WithinLE(p, tol) {
				on = true
				break
			}
		}
		if on {
			continue
		}
		best := math.Inf(1)
		for _, ei := range s.edges {
			if d := s.lines[ei].DistTo(p); d < best {
				best = d
			}
		}
		return best, true
	}
	return 0, false
}

// nearestEdge returns the subgraph edge closest to p.
func (s *subgraph) nearestEdge(p geo.Pt) (int, float64, bool) {
	best, bestD := -1, math.Inf(1)
	for _, ei := range s.edges {
		// ties break to the lower edge index: two corridors equidistant
		// from a stop must resolve the same way on every run
		if d := s.lines[ei].DistTo(p); d < bestD {
			best, bestD = ei, d
		}
	}
	return best, bestD, best >= 0
}

// rideThrough turns an ordered stop list into a walk: each stop snaps to
// the corridor nearest it, and consecutive stops join by the shortest
// ride over this route's own edges. Shortest is a choice, and the right
// one — every candidate path is over corridors the route is listed on,
// so the shortest is the one a timetable would describe.
//
// The walk emits whole edges. Overshooting the first and last stop by
// the remainder of their edges is deliberate and harmless: FAIR probes
// junction legs, and a route is listed on that edge either way.
func (s *subgraph) rideThrough(seq []string, stopLL map[string]geo.LL, frame geo.Frame) *geo.Line {
	if len(s.edges) == 0 {
		return nil
	}
	var chain []int
	prev := -1
	for _, sid := range seq {
		ll, ok := stopLL[sid]
		if !ok {
			continue
		}
		ei, _, ok := s.nearestEdge(frame.ToXY(ll))
		if !ok {
			continue
		}
		if ei == prev {
			continue
		}
		if prev < 0 {
			chain = append(chain, ei)
			prev = ei
			continue
		}
		hop := s.shortestEdgePath(prev, ei)
		if hop == nil {
			// the route's own subgraph does not connect these two stops.
			// A gap in the authored graph, not something to paper over —
			// the walk restarts, which costs an attestation and invents
			// nothing.
			chain = append(chain, ei)
			prev = ei
			continue
		}
		chain = append(chain, hop...)
		prev = ei
	}
	if len(chain) == 0 {
		return nil
	}
	return s.stitch(chain)
}

// shortestEdgePath is Dijkstra over the subgraph's nodes, returning the
// edges to ride from a to b EXCLUDING a and including b.
func (s *subgraph) shortestEdgePath(a, b int) []int {
	if a == b {
		return nil
	}
	const inf = math.MaxFloat64
	dist := map[int]float64{}
	via := map[int]int{} // node → edge ridden to reach it
	start := []int{s.net.Edges[a].From, s.net.Edges[a].To}
	for _, n := range start {
		dist[n] = 0
	}
	// simple O(V²) selection: a route's subgraph is tens of nodes, and a
	// heap here would be more code than the whole search saves
	done := map[int]bool{}
	for {
		cur, curD := -1, inf
		for n, d := range dist {
			if !done[n] && d < curD {
				cur, curD = n, d
			}
		}
		if cur < 0 {
			break
		}
		done[cur] = true
		for _, ei := range s.adj[cur] {
			e := s.net.Edges[ei]
			other := e.From
			if other == cur {
				other = e.To
			}
			nd := curD + s.lines[ei].Len()
			if d, ok := dist[other]; !ok || nd < d {
				dist[other] = nd
				via[other] = ei
			}
		}
	}
	// finish at whichever end of b is cheaper to reach
	endA, endB := s.net.Edges[b].From, s.net.Edges[b].To
	end, endD := -1, inf
	for _, n := range []int{endA, endB} {
		if d, ok := dist[n]; ok && d < endD {
			end, endD = n, d
		}
	}
	if end < 0 {
		return nil
	}
	var rev []int
	seen := map[int]bool{}
	for n := end; ; {
		ei, ok := via[n]
		if !ok || seen[ei] {
			break
		}
		seen[ei] = true
		rev = append(rev, ei)
		e := s.net.Edges[ei]
		if e.From == n {
			n = e.To
		} else {
			n = e.From
		}
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return append(rev, b)
}

// stitch concatenates an edge chain into one polyline, orienting each
// edge to continue from the last point rather than trusting From/To —
// an authored graph is under no obligation to store its edges pointing
// the way anyone rides them.
func (s *subgraph) stitch(chain []int) *geo.Line {
	var pts []geo.Pt
	for _, ei := range chain {
		ep := s.net.Edges[ei].Pts
		if len(ep) < 2 {
			continue
		}
		if len(pts) == 0 {
			// orient the first edge towards the second, so a two-edge
			// walk does not start by running backwards
			if len(chain) > 1 {
				nxt := s.net.Edges[chain[1]]
				if ep[0].Dist(nxt.Pts[0]) < ep[len(ep)-1].Dist(nxt.Pts[0]) ||
					ep[0].Dist(nxt.Pts[len(nxt.Pts)-1]) < ep[len(ep)-1].Dist(nxt.Pts[len(nxt.Pts)-1]) {
					ep = reversed(ep)
				}
			}
			pts = append(pts, ep...)
			continue
		}
		tail := pts[len(pts)-1]
		if tail.Dist(ep[len(ep)-1]) < tail.Dist(ep[0]) {
			ep = reversed(ep)
		}
		if tail.Dist(ep[0]) < 0.5 {
			ep = ep[1:] // shared node: do not repeat the vertex
		}
		pts = append(pts, ep...)
	}
	if len(pts) < 2 {
		return nil
	}
	return geo.NewLine(pts)
}

func reversed(p []geo.Pt) []geo.Pt {
	out := make([]geo.Pt, len(p))
	for i := range p {
		out[i] = p[len(p)-1-i]
	}
	return out
}

// structural reads the walk straight off the shape of the subgraph. It
// returns one line per connected component, and complete=false when any
// component has a node of degree ≥ 3 — a fork, or a corner the route
// crosses twice. Those have more than one traversal and structure alone
// cannot say which, so no walk is invented for them.
func (s *subgraph) structural() ([]*geo.Line, bool) {
	complete := true
	for _, eis := range s.adj {
		if len(eis) > 2 {
			complete = false
		}
	}
	if !complete {
		return nil, false
	}
	// Every node is degree ≤ 2, so each component is a simple path or a
	// simple ring and one forward walk covers it — provided a path is
	// started at one of its two ends. Hence two passes: the first only
	// starts on an edge that HAS an end, so every path is walked from
	// its terminus; whatever is still unseen afterwards is a ring, which
	// has no end and may be started anywhere.
	seen := map[int]bool{}
	order := append([]int(nil), s.edges...)
	sort.Ints(order) // component order is part of the build's determinism

	// walkFrom rides away from the start edge towards node `at`, taking
	// the one unridden edge at each node until the component runs out.
	walkFrom := func(ei, at int) []int {
		chain := []int{ei}
		seen[ei] = true
		for {
			nxt := -1
			for _, cand := range s.adj[at] {
				if !seen[cand] {
					nxt = cand
					break
				}
			}
			if nxt < 0 {
				return chain
			}
			seen[nxt] = true
			chain = append(chain, nxt)
			e := s.net.Edges[nxt]
			if e.From == at {
				at = e.To
			} else {
				at = e.From
			}
		}
	}

	var out []*geo.Line
	for pass := 0; pass < 2; pass++ {
		for _, ei := range order {
			if seen[ei] {
				continue
			}
			e := s.net.Edges[ei]
			var at int
			switch {
			case len(s.adj[e.From]) == 1:
				at = e.To
			case len(s.adj[e.To]) == 1:
				at = e.From
			case pass == 0:
				continue // no end here — a ring, or mid-path; pass 2
			default:
				at = e.To
			}
			if l := s.stitch(walkFrom(ei, at)); l != nil {
				out = append(out, l)
			}
		}
	}
	return out, true
}
