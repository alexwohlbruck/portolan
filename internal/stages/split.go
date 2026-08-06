package stages

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
)

// dbgRPt reports whether the line passes near the PORTOLAN_DBGR "x,y"
// frame point (debug only).
func dbgRPt(l *geo.Line) bool {
	v := os.Getenv("PORTOLAN_DBGR")
	if v == "" {
		return false
	}
	var x, y float64
	fmt.Sscanf(v, "%f,%f", &x, &y)
	return l.DistTo(geo.Pt{X: x, Y: y}) < 60
}

// SPLIT — owner's steps 2–3. Because MATCH converged co-running routes onto
// identical walks, junctions are exact graph facts: a network node exists
// where the used subgraph branches, where the route set changes, or where a
// gap bridge lands. Everything between contracts into one segment carrying
// its route set. Each segment is then moved onto its physical track-GROUP
// median (owner's law 2) with bundle.Refine, membership scoped by the kiss
// rule (sustained parallelism, never proximity alone), and each junction
// node is placed at the least-squares meet of its corridors' centerlines
// (LESSONS #10).

type splitParams struct {
	JoinTol   float64 // strand chaining tolerance
	MinRefine float64 // edges shorter than this keep raw track geometry
	MateMax   float64 // strand counts as corridor mate within this offset
	MateRun   float64 // ...sustained at least this much arc (the kiss rule)
	RayNear   float64 // end-ray window inner inset (skip pinned-end bend)
	RayFar    float64 // end-ray window outer inset
	MeetMax   float64 // max distance a meet may move a node
}

func defaultSplitParams() splitParams {
	return splitParams{
		JoinTol:   1.0,
		MinRefine: dial("split_min_refine", 40),
		MateMax:   dial("split_mate_max", 12),
		MateRun:   dial("split_mate_run", 60),
		RayNear:   15,
		RayFar:    45,
		MeetMax:   25,
	}
}

// Split detects junctions where matched paths intersect or diverge,
// divides the paths into segments at each junction, and assigns each
// segment the set of routes riding it.
func Split(paths []Path, tracks []bundle.Track) (*Network, error) {
	setLevelIndex(tracks)
	g := buildTrackGraphCached(tracks)
	p := defaultSplitParams()

	// ---- usage: route set per used piece
	pieceRoutes := make([]map[string]bool, len(g.pieces))
	for _, pa := range paths {
		rid := pa.Pattern.Route.ID
		for _, st := range pa.Steps {
			if st.Piece < 0 {
				continue
			}
			if pieceRoutes[st.Piece] == nil {
				pieceRoutes[st.Piece] = map[string]bool{}
			}
			pieceRoutes[st.Piece][rid] = true
		}
	}

	// ---- unified edge list: used pieces + deduped gap bridges
	type uedge struct {
		from, to int
		line     *geo.Line // oriented from→to
		routes   map[string]bool
		gap      bool
	}
	var ue []uedge
	for piece, rs := range pieceRoutes {
		if rs == nil {
			continue
		}
		e := g.edges[2*piece]
		ue = append(ue, uedge{e.From, e.To, g.pieces[piece], rs, false})
	}

	// synthetic nodes for gap ends that anchor to no graph node
	nodePos := make([]geo.Pt, len(g.nodes))
	for i, n := range g.nodes {
		nodePos[i] = n.At
	}
	synthOf := func(pt geo.Pt) int {
		for i := len(g.nodes); i < len(nodePos); i++ {
			if nodePos[i].Dist(pt) < 10 {
				return i
			}
		}
		nodePos = append(nodePos, pt)
		return len(nodePos) - 1
	}
	gapDedup := map[[2]int]int{} // normalized node pair → ue index
	for _, pa := range paths {
		rid := pa.Pattern.Route.ID
		for si, st := range pa.Steps {
			if st.Piece >= 0 || st.Gap == nil || st.Gap.Len() < 1 {
				continue
			}
			from, to := -1, -1
			if si > 0 && pa.Steps[si-1].Piece >= 0 {
				pe := g.edges[2*pa.Steps[si-1].Piece]
				from = pe.From
				if !pa.Steps[si-1].Rev {
					from = pe.To
				}
			}
			if si < len(pa.Steps)-1 && pa.Steps[si+1].Piece >= 0 {
				ne := g.edges[2*pa.Steps[si+1].Piece]
				to = ne.To
				if !pa.Steps[si+1].Rev {
					to = ne.From
				}
			}
			if from < 0 {
				from = synthOf(st.Gap.Pts[0])
			}
			if to < 0 {
				to = synthOf(st.Gap.Pts[len(st.Gap.Pts)-1])
			}
			key := [2]int{min(from, to), max(from, to)}
			if ui, ok := gapDedup[key]; ok {
				ue[ui].routes[rid] = true
				continue
			}
			gapDedup[key] = len(ue)
			ue = append(ue, uedge{from, to, st.Gap,
				map[string]bool{rid: true}, true})
		}
	}

	// ---- adjacency over the used subgraph
	type incid struct{ edge, other int }
	adj := map[int][]incid{}
	for ei, e := range ue {
		adj[e.from] = append(adj[e.from], incid{ei, e.to})
		adj[e.to] = append(adj[e.to], incid{ei, e.from})
	}
	routeKey := func(rs map[string]bool) string {
		ids := make([]string, 0, len(rs))
		for r := range rs {
			ids = append(ids, r)
		}
		sort.Strings(ids)
		k := ""
		for _, r := range ids {
			k += r + ","
		}
		return k
	}
	// outward tangent of a unified edge standing at one of its nodes
	outward := func(ei, node int) geo.Pt {
		l := ue[ei].line
		win := math.Min(15, l.Len())
		if ue[ei].from == node {
			return l.AtArc(win).Sub(l.Pts[0]).Unit()
		}
		return l.AtArc(l.Len() - win).Sub(l.Pts[len(l.Pts)-1]).Unit()
	}
	isNet := map[int]bool{}
	for n, incs := range adj {
		switch {
		case len(incs) != 2:
			isNet[n] = true
		case ue[incs[0].edge].gap || ue[incs[1].edge].gap:
			isNet[n] = true
		case routeKey(ue[incs[0].edge].routes) != routeKey(ue[incs[1].edge].routes):
			isNet[n] = true
		case outward(incs[0].edge, n).Dot(outward(incs[1].edge, n)) > 0.5:
			// tangent reversal: both branches leave the same way — a fork
			// event (switchback / relay V), never a pass-through; a
			// contracted reversal doubles the centerline over itself
			isNet[n] = true
		}
	}

	// ---- contract degree-2 chains into segments
	net := &Network{}
	nodeIdx := map[int]int{}
	netNode := func(n int) int {
		if i, ok := nodeIdx[n]; ok {
			return i
		}
		net.Nodes = append(net.Nodes, Node{At: nodePos[n]})
		nodeIdx[n] = len(net.Nodes) - 1
		return len(net.Nodes) - 1
	}
	visited := make([]bool, len(ue))
	appendOriented := func(pts []geo.Pt, e uedge, fromNode int) []geo.Pt {
		seg := e.line.Pts
		if e.from != fromNode {
			seg = reversedPts(seg)
		}
		for _, q := range seg {
			if len(pts) > 0 && pts[len(pts)-1].Dist(q) < 1e-9 {
				continue
			}
			pts = append(pts, q)
		}
		return pts
	}
	walk := func(start int, first incid) {
		if visited[first.edge] {
			return
		}
		var pts []geo.Pt
		e0 := ue[first.edge]
		pts = appendOriented(pts, e0, start)
		visited[first.edge] = true
		curEdge, cur := first.edge, first.other
		for !isNet[cur] {
			incs := adj[cur]
			nx := incs[0]
			if nx.edge == curEdge {
				nx = incs[1]
			}
			if visited[nx.edge] {
				break // safety: malformed ring
			}
			pts = appendOriented(pts, ue[nx.edge], cur)
			visited[nx.edge] = true
			curEdge, cur = nx.edge, nx.other
		}
		routes := make([]string, 0, len(e0.routes))
		for r := range e0.routes {
			routes = append(routes, r)
		}
		sort.Strings(routes)
		net.Edges = append(net.Edges, Edge{
			From: netNode(start), To: netNode(cur),
			Pts: pts, Routes: routes, Gap: e0.gap,
		})
	}
	var netNodes []int
	for n := range adj {
		if isNet[n] {
			netNodes = append(netNodes, n)
		}
	}
	sort.Ints(netNodes) // determinism
	for _, n := range netNodes {
		for _, inc := range adj[n] {
			walk(n, inc)
		}
	}
	for ei := range ue { // isolated all-degree-2 rings
		if !visited[ei] {
			isNet[ue[ei].from] = true
			walk(ue[ei].from, incid{ei, ue[ei].to})
		}
	}
	for ei, e := range net.Edges {
		net.Nodes[e.From].Adj = append(net.Nodes[e.From].Adj, ei)
		if e.To != e.From {
			net.Nodes[e.To].Adj = append(net.Nodes[e.To].Adj, ei)
		}
	}

	// ---- law 2: move each segment onto its track-group median.
	// Only RIDDEN ways vote: the median settles on the tracks service
	// actually uses. Unridden trackage alongside — turnaround loops (the
	// South Ferry arc dragged the 1 sideways), yard ladders, dead spurs —
	// is steel, not service, and must not pull a centerline.
	usedWays := map[string]bool{}
	wayRoutes := map[string]map[string]bool{}
	for piece, rs := range pieceRoutes {
		if rs != nil {
			w := g.edges[2*piece].Way
			usedWays[w] = true
			if wayRoutes[w] == nil {
				wayRoutes[w] = map[string]bool{}
			}
			for rid := range rs {
				wayRoutes[w][rid] = true
			}
		}
	}
	setWayRouteIndex(wayRoutes)
	var riddenTracks []bundle.Track
	for _, t := range tracks {
		if usedWays[t.ID] {
			riddenTracks = append(riddenTracks, t)
		}
	}
	if len(riddenTracks) == 0 {
		riddenTracks = tracks
	}
	// refinement mates come from RIDDEN-ONLY strands: chaining over all
	// tracks glues turnaround arcs into revenue strands, and a mixed
	// strand keeps the dead trackage voting in the median (the 1 kept
	// getting dragged toward the South Ferry loop through exactly that).
	// Refinement mates are the ridden strands. Route-affinity filtering of
	// mates was tried twice (2026-08-05, per-route chains and per-strand
	// route sets) to fix the Battery-curve contamination; both correctly
	// isolated corridors but ALSO stopped co-located cross-service groups
	// (A/C+G at Hoyt, the DeKalb yellows) from converging onto a shared
	// median, breaking corridor co-merges. The contamination is handled
	// inside Refine instead: one vote per strand per cross-section, so
	// loop limbs and terminal return legs chained into a strand cannot
	// stack votes here.
	// DIRECTIONAL TWINS: MATCH's convergence bonus puts both directions of
	// a route on one track, leaving the paired other-direction rail
	// "unridden" — but the drawn centerline must sit on the PAIR's center
	// (the Lake corridor rode its north rail exactly). A twin is admitted
	// per edge only when it runs parallel within track gauge for nearly
	// the edge's whole length; turnaround arcs and yard ladders never do,
	// so the South Ferry law holds.
	var unriddenTracks []bundle.Track
	for _, t := range tracks {
		if !usedWays[t.ID] {
			unriddenTracks = append(unriddenTracks, t)
		}
	}
	twinStrands := bundle.Chain(unriddenTracks, p.JoinTol)
	twinLines := make([]*geo.Line, len(twinStrands))
	for i, st := range twinStrands {
		twinLines[i] = st.Line
	}
	tgrid := geo.NewGrid(twinLines, 64)

	strands := bundle.Chain(riddenTracks, p.JoinTol)
	strandLines := make([]*geo.Line, len(strands))
	for i, st := range strands {
		strandLines[i] = st.Line
	}
	sgrid := geo.NewGrid(strandLines, 64)
	rp := bundle.DefaultParams()
	refineEdges(net, strandLines, sgrid, twinLines, tgrid, p, rp)

	// ---- law 3: edges sustained-parallel within mate range are ONE visual
	// corridor — cut at the membership bounds and merge (lollipop sticks,
	// stacked cross-color corridors); then settle merged middles on their
	// group medians
	// bundle ↔ refine to a fixpoint: every refinement moves centerlines
	// by meters, which reveals merges the previous pass measured too far
	// apart (the DeKalb fan twins needed three passes). Terminates when a
	// pass merges nothing; capped as a safety bound.
	for i := 0; i < 4; i++ {
		if bundleParallelEdges(net) == 0 {
			break
		}
		refineEdges(net, strandLines, sgrid, twinLines, tgrid, p, rp)
	}

	// ---- law 16: fold rings into lollipop sticks. An edge that runs a
	// deep excursion and returns to (nearly) its start — the Bowling
	// Green loop, terminal balloons, relay lassos — is two directional
	// passes of ONE drawn line: fold it in half onto the midpoint of its
	// halves, with the tip as a new terminal node. Apple draws exactly
	// the folded line (the 4/5 around the Battery is a single curve).
	foldRings(net)
	refineEdges(net, strandLines, sgrid, twinLines, tgrid, p, rp)
	dropShadowStubs(net)
	// a gap edge whose ENDPOINTS nearly coincide is not missing track —
	// it is a deviating shape's bulge pinched between two matched anchors
	// (the O'Hare approach, where the GTFS shape leaves the subway for
	// ~100 m). Weld the nodes and drop the gap; chain contraction then
	// fuses the halves into one continuous line on real track.
	for ei := range net.Edges {
		e := &net.Edges[ei]
		if !e.Gap || len(e.Pts) == 0 || e.From == e.To {
			continue
		}
		if net.Nodes[e.From].At.Dist(net.Nodes[e.To].At) > 20 {
			continue
		}
		keep, drop := e.From, e.To
		for j := range net.Edges {
			if net.Edges[j].From == drop {
				net.Edges[j].From = keep
			}
			if net.Edges[j].To == drop {
				net.Edges[j].To = keep
			}
		}
		e.Pts = nil
	}
	// node welds during ring deletion can collapse a small leftover
	// fragment's two endpoints into one node, leaving a self-loop far
	// below renderable balloon size — unreachable by any other sweep.
	{
		kept := net.Edges[:0]
		for _, e := range net.Edges {
			if len(e.Pts) == 0 {
				continue // swept above (healed gaps)
			}
			if e.From == e.To && geo.NewLine(e.Pts).Len() < 250 {
				continue
			}
			kept = append(kept, e)
		}
		net.Edges = kept
	}
	// merge cuts, ring deletion and stub sweeps leave same-route chains
	// hanging on degree-2 nodes — pure furniture that fragments FAIR into
	// redundant transitions (the Bowling Green fork tine). A deg-2 node
	// between identical route sets carries zero information: contract.
	contractChains(net)
	// fragments were median-refined separately, so their joints survive
	// contraction as kinks mid-edge — refine the merged edges as whole
	// strands once more.
	refineEdges(net, strandLines, sgrid, twinLines, tgrid, p, rp)
	// ---- junction nodes at the meet of their corridors' centerlines
	for ni := range net.Nodes {
		placeNodeAtMeet(net, ni, p)
	}
	// a stub shorter than the junction cuts that will consume it, strung
	// between two junction nodes, is interior connective tissue (Tower 18
	// crossing legs) — its body can never render, and leaving it splits
	// every turning movement into fragment-hopping transitions. Collapse
	// it to a single node at its midpoint.
	for {
		rebuildAdj(net)
		target := -1
		for ei, e := range net.Edges {
			if e.Gap || len(e.Pts) == 0 || e.From == e.To {
				continue
			}
			if geo.NewLine(e.Pts).Len() >= 60 {
				continue
			}
			if len(net.Nodes[e.From].Adj) < 3 || len(net.Nodes[e.To].Adj) < 3 {
				continue
			}
			target = ei
			break
		}
		if target < 0 {
			break
		}
		e := &net.Edges[target]
		mid := geo.Lerp(e.Pts[0], e.Pts[len(e.Pts)-1], 0.5)
		keep, drop := e.From, e.To
		net.Nodes[keep].At = mid
		for j := range net.Edges {
			if net.Edges[j].From == drop {
				net.Edges[j].From = keep
			}
			if net.Edges[j].To == drop {
				net.Edges[j].To = keep
			}
		}
		var out []Edge
		for j, e2 := range net.Edges {
			if j != target {
				out = append(out, e2)
			}
		}
		net.Edges = out
	}
	// leg welds can turn a remaining short edge into a self-loop on the
	// merged junction node — sweep those, then contract the deg-2
	// same-route joints the collapse exposed.
	{
		kept := net.Edges[:0]
		for _, e := range net.Edges {
			if !e.Gap && e.From == e.To && len(e.Pts) > 0 &&
				geo.NewLine(e.Pts).Len() < 250 {
				continue
			}
			kept = append(kept, e)
		}
		net.Edges = kept
	}
	rebuildAdj(net)
	trimEdgeEnds(net)
	contractChains(net)
	// final refinement: edges created or reshaped by late merge/weld/
	// contraction rounds otherwise keep their seed geometry verbatim —
	// the Lake corridor east of Garvey rode its seed's own track (the
	// north rail, 6-decimal identical) instead of the pair's center.
	// Refine moves interior vertices only, so the trimmed ends hold.
	refineEdges(net, strandLines, sgrid, twinLines, tgrid, p, rp)
	// the junction-leg collapse re-welded nodes to LEG MIDPOINTS,
	// overwriting the earlier least-squares meets — Tower 18's node sat
	// 15 m north of the Lake axis and every corridor tail kinked up to
	// reach it. Re-meet on the final refined centerlines, then re-trim:
	// with the node back on the axes' crossing, each tail's closest
	// approach lands before its weld bend and the bend is cut away —
	// straight centerline endings.
	rebuildAdj(net)
	for ni := range net.Nodes {
		placeNodeAtMeet(net, ni, p)
	}
	trimEdgeEnds(net)
	compactNodes(net)
	return net, nil
}

// trimEdgeEnds cuts each edge end back to its closest approach to its
// node: welds and meets move nodes but leave edge geometry pointing at
// the OLD endpoints, so tails overshoot or bend toward stale positions
// (the SE Loop corner drew its turn as a dive-and-reverse V through two
// overshoots).
func trimEdgeEnds(net *Network) {
	for ei := range net.Edges {
		e := &net.Edges[ei]
		if len(e.Pts) < 2 {
			continue
		}
		l := geo.NewLine(e.Pts)
		for end := 0; end < 2; end++ {
			var node geo.Pt
			if end == 0 {
				node = net.Nodes[e.From].At
			} else {
				node = net.Nodes[e.To].At
			}
			var tip geo.Pt
			if end == 0 {
				tip = e.Pts[0]
			} else {
				tip = e.Pts[len(e.Pts)-1]
			}
			if tip.Dist(node) < 3 {
				continue
			}
			win := math.Min(60, l.Len()/3)
			bestArc, bestD := -1.0, math.Inf(1)
			for arc := 0.0; arc <= win; arc += 2 {
				pos := arc
				if end == 1 {
					pos = l.Len() - arc
				}
				if d := l.AtArc(pos).Dist(node); d < bestD {
					bestD, bestArc = d, pos
				}
			}
			if bestArc < 0 || bestD >= tip.Dist(node)-1 {
				continue
			}
			if end == 0 {
				l = bundle.SubLine(l, bestArc, l.Len())
			} else {
				l = bundle.SubLine(l, 0, bestArc)
			}
			e.Pts = l.Pts
			l = geo.NewLine(e.Pts)
		}
	}
}

// compactNodes drops degree-0 nodes — weld and sweep leftovers that no
// edge references (edges get compacted after every sweep; nodes never
// were, so orphans accumulated at every junction weld). Pure cleanup:
// they are invisible to drawing but clutter the network and the debug
// node layer (Tower 18 showed four "junctions" — one real, three
// orphans).
func compactNodes(net *Network) {
	used := make([]bool, len(net.Nodes))
	for _, e := range net.Edges {
		used[e.From] = true
		used[e.To] = true
	}
	remap := make([]int, len(net.Nodes))
	var kept []Node
	for i, nd := range net.Nodes {
		if used[i] {
			remap[i] = len(kept)
			kept = append(kept, nd)
		} else {
			remap[i] = -1
		}
	}
	for ei := range net.Edges {
		net.Edges[ei].From = remap[net.Edges[ei].From]
		net.Edges[ei].To = remap[net.Edges[ei].To]
	}
	net.Nodes = kept
	rebuildAdj(net)
}


// dropShadowStubs removes dangling slivers that duplicate coverage: a
// deg-1 fragment lying entirely alongside another edge that carries a
// superset of its routes is fold/relay residue (the Bowling Green ring
// fold leaves approach slivers shadowing the folded curve), never a real
// terminus — a true terminal stub has no superset twin beside it.

// blendSeam smooths vertices within radius (m of arc) of the seam between
// pts[:seam] and pts[seam:] by iterated neighbor averaging; vertices
// outside the window are pinned.
func blendSeam(pts []geo.Pt, seam int, radius float64) {
	if seam <= 0 || seam >= len(pts) {
		return
	}
	arc := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		arc[i] = arc[i-1] + pts[i].Dist(pts[i-1])
	}
	sa := arc[seam]
	lo, hi := seam, seam
	for lo > 1 && sa-arc[lo-1] < radius {
		lo--
	}
	for hi < len(pts)-2 && arc[hi+1]-sa < radius {
		hi++
	}
	for round := 0; round < 12; round++ {
		for i := lo; i <= hi; i++ {
			mid := geo.Lerp(pts[i-1], pts[i+1], 0.5)
			pts[i] = geo.Lerp(pts[i], mid, 0.5)
		}
	}
}

func contractChains(net *Network) {
	sameSet := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		m := map[string]bool{}
		for _, r := range a {
			m[r] = true
		}
		for _, r := range b {
			if !m[r] {
				return false
			}
		}
		return true
	}
	for {
		rebuildAdj(net)
		target := -1
		for ni := range net.Nodes {
			adj := net.Nodes[ni].Adj
			if len(adj) != 2 || adj[0] == adj[1] {
				continue
			}
			a, b := &net.Edges[adj[0]], &net.Edges[adj[1]]
			if a.Gap || b.Gap || !sameSet(a.Routes, b.Routes) {
				continue
			}
			// keep genuine reversal events (relay V) as nodes
			la, lb := geo.NewLine(a.Pts), geo.NewLine(b.Pts)
			ta := outwardTangent(la, a.From == ni)
			tb := outwardTangent(lb, b.From == ni)
			if ta.Dot(tb) > 0.5 {
				continue
			}
			target = ni
			break
		}
		if target < 0 {
			break
		}
		adj := net.Nodes[target].Adj
		ea, eb := adj[0], adj[1]
		a, b := net.Edges[ea], net.Edges[eb]
		// orient a INTO the node, b OUT of it
		ap := a.Pts
		if a.From == target {
			ap = reversedPts(ap)
		}
		bp := b.Pts
		if b.To == target {
			bp = reversedPts(bp)
		}
		// both halves can OVERSHOOT into the junction region their weld
		// deleted; concatenating the raw tails folds the polyline back on
		// itself (the NE Loop corner drew a 12 m retrace scribble). Join
		// at the closest-approach pair of the two tail regions instead.
		ai, bj := len(ap)-1, 0
		bestD := math.Inf(1)
		aArc, bArc := 0.0, 0.0
		for i := len(ap) - 1; i > 0; i-- {
			if i < len(ap)-1 {
				aArc += ap[i].Dist(ap[i+1])
			}
			if aArc > 40 {
				break
			}
			bArc = 0
			for j := 0; j < len(bp)-1; j++ {
				if j > 0 {
					bArc += bp[j].Dist(bp[j-1])
				}
				if bArc > 40 {
					break
				}
				if d := ap[i].Dist(bp[j]); d < bestD {
					bestD, ai, bj = d, i, j
				}
			}
		}
		pts := append(append([]geo.Pt{}, ap[:ai+1]...), bp[bj:]...)
		// the halves were shaped independently (separate refinement mates,
		// cut adjustments), so the joint carries a heading/alignment jump
		// that survives downstream smoothing as a visible kink — ease the
		// seam neighborhood locally, leaving the rest of both halves
		// untouched.
		blendSeam(pts, ai+1, 35)
		from := a.From
		if a.From == target {
			from = a.To
		}
		to := b.To
		if b.To == target {
			to = b.From
		}
		merged := Edge{From: from, To: to, Pts: pts, Routes: a.Routes,
			Tracks: max(a.Tracks, b.Tracks)}
		var out []Edge
		for ei, e := range net.Edges {
			if ei != ea && ei != eb {
				out = append(out, e)
			}
		}
		net.Edges = append(out, merged)
	}
	rebuildAdj(net)
}

// outwardTangent: unit direction leaving the given end of a line.
func outwardTangent(l *geo.Line, atStart bool) geo.Pt {
	win := math.Min(15, l.Len())
	if atStart {
		return l.AtArc(win).Sub(l.Pts[0]).Unit()
	}
	return l.AtArc(l.Len() - win).Sub(l.Pts[len(l.Pts)-1]).Unit()
}

func dropShadowStubs(net *Network) {
	for {
		rebuildAdj(net)
		drop := -1
		for ei, e := range net.Edges {
			if e.Gap || len(e.Pts) < 2 {
				continue
			}
			l := geo.NewLine(e.Pts)
			if l.Len() > 260 {
				continue
			}
			if len(net.Nodes[e.From].Adj) > 1 && len(net.Nodes[e.To].Adj) > 1 {
				continue // not dangling
			}
			for oj, o := range net.Edges {
				if oj == ei || o.Gap || len(o.Pts) < 2 {
					continue
				}
				if !routesSuperset(o.Routes, e.Routes) {
					continue
				}
				ol := geo.NewLine(o.Pts)
				far := 0.0
				for _, q := range l.Resample(10) {
					if d := ol.DistTo(q); d > far {
						far = d
					}
				}
				if far <= 40 {
					drop = ei
					break
				}
			}
			if drop >= 0 {
				break
			}
		}
		if drop < 0 {
			break
		}
		net.Edges = append(net.Edges[:drop], net.Edges[drop+1:]...)
	}
	rebuildAdj(net)
}

func routesSuperset(sup, sub []string) bool {
	m := map[string]bool{}
	for _, r := range sup {
		m[r] = true
	}
	for _, r := range sub {
		if !m[r] {
			return false
		}
	}
	return true
}

func foldRings(net *Network) {
	for ei := range net.Edges {
		e := &net.Edges[ei]
		if e.Gap || len(e.Pts) < 5 {
			continue
		}
		l := geo.NewLine(e.Pts)
		if l.Len() < 250 || l.Len() > 1600 {
			continue
		}
		if e.Pts[0].Dist(e.Pts[len(e.Pts)-1]) > 45 {
			continue
		}
		// a ring whose services are all carried by the OTHER edges at
		// its throat adds no service at all — it is turnaround trackage
		// (owner's law: relay loops never render). Drop it. Only rings
		// that carry something unique fold into a lollipop stick.
		rebuildAdj(net)
		carried := map[string]bool{}
		for _, ni := range []int{e.From, e.To} {
			for _, oj := range net.Nodes[ni].Adj {
				if oj == ei {
					continue
				}
				for _, r := range net.Edges[oj].Routes {
					carried[r] = true
				}
			}
		}
		covered := true
		for _, r := range e.Routes {
			if !carried[r] {
				covered = false
				break
			}
		}
		if covered {
			near, drop := e.From, e.To
			if near != drop {
				for j := range net.Edges {
					if net.Edges[j].From == drop {
						net.Edges[j].From = near
					}
					if net.Edges[j].To == drop {
						net.Edges[j].To = near
					}
				}
			}
			e.Pts = nil // mark dead; swept below
			continue
		}
		// tip = farthest point from the throat
		throat := geo.Lerp(e.Pts[0], e.Pts[len(e.Pts)-1], 0.5)
		farArc, farD := 0.0, 0.0
		for _, q := range l.Resample(8) {
			if d := q.Dist(throat); d > farD {
				farD = d
				a, _ := l.ProjectArc(q)
				farArc = a
			}
		}
		if farD < 80 {
			continue
		}
		h1 := bundle.SubLine(l, 0, farArc)
		h2 := bundle.SubLine(l, farArc, l.Len())
		// walk both halves tip-ward by arc fraction, midpointing
		n := int(math.Max(8, h1.Len()/8))
		stick := make([]geo.Pt, 0, n+1)
		for i := 0; i <= n; i++ {
			t := float64(i) / float64(n)
			p1 := h1.AtArc(t * h1.Len())
			p2 := h2.AtArc((1 - t) * h2.Len())
			stick = append(stick, geo.Lerp(p1, p2, 0.5))
		}
		// weld the far node into the near one, re-point the edge at a new
		// tip node
		near, drop := e.From, e.To
		if near != drop {
			for j := range net.Edges {
				if net.Edges[j].From == drop {
					net.Edges[j].From = near
				}
				if net.Edges[j].To == drop {
					net.Edges[j].To = near
				}
			}
		}
		net.Nodes = append(net.Nodes, Node{At: stick[len(stick)-1]})
		e.From = near
		e.To = len(net.Nodes) - 1
		e.Pts = stick
	}
	var kept []Edge
	for _, e := range net.Edges {
		if len(e.Pts) >= 2 {
			kept = append(kept, e)
		}
	}
	net.Edges = kept
	rebuildAdj(net)
}

func refineEdges(net *Network, strandLines []*geo.Line, sgrid *geo.Grid,
	twinLines []*geo.Line, tgrid *geo.Grid,
	p splitParams, rp bundle.Params) {

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i := range net.Edges {
		e := &net.Edges[i]
		if e.Gap || len(e.Pts) < 3 {
			continue
		}
		if geo.NewLine(e.Pts).Len() < p.MinRefine {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			cl := geo.NewLine(e.Pts)
			var members []*geo.Line
			for round := 0; round < 2; round++ {
				members = edgeMates(cl, strandLines, sgrid, p)
				if len(members) == 0 {
					break
				}
				members = append(members, edgeTwins(cl, twinLines, tgrid)...)
				cl = bundle.Refine(cl, members, rp)
			}
			if os.Getenv("PORTOLAN_DBGR") != "" && dbgRPt(cl) {
				mid := cl.AtArc(cl.Len() / 2)
				fmt.Printf("REFINE3 edge routes=%v members=%d\n", e.Routes, len(members))
				for mi, m := range members {
					fmt.Printf("  member %d: DistTo(mid)=%.1f len=%.0f\n", mi, m.DistTo(mid), m.Len())
				}
			}
			e.Pts = cl.Pts
			e.Tracks = trackCount(cl, members, rp)
		}()
	}
	wg.Wait()
}

// edgeTwins finds unridden DIRECTIONAL TWINS of a centerline: strands
// running parallel within track-pair gauge for nearly the edge's whole
// length. MATCH converges both directions of a route onto one track, so
// the paired rail is unridden steel — but the drawn line must sit on
// the pair's center. Turnaround arcs and yard ladders parallel an edge
// only briefly and never qualify (the South Ferry law holds).
func edgeTwins(cl *geo.Line, twinLines []*geo.Line, tgrid *geo.Grid) []*geo.Line {
	const ds = 12.0
	const twinMax = 6.0
	samples := cl.Resample(ds)
	counts := map[int]int{}
	for _, q := range samples {
		tgrid.Near(q, twinMax, func(si int) {
			if twinLines[si].Within(q, twinMax) {
				counts[si]++
			}
		})
	}
	need := int(0.8 * float64(len(samples)))
	if need < 3 {
		need = 3
	}
	var out []*geo.Line
	for si, c := range counts {
		if c >= need {
			out = append(out, twinLines[si])
		}
	}
	return out
}

// edgeMates finds the strands that form this segment's physical track group:
// sustained parallelism within MateMax for at least MateRun of arc (LESSONS
// #5 — the kiss rule; proximity alone never bundles).
// Only RIDDEN strands vote in the median — unridden trackage alongside
// (the South Ferry turnaround arc, yard ladders) is steel, not service.
func edgeMates(cl *geo.Line, strandLines []*geo.Line,
	sgrid *geo.Grid, p splitParams) []*geo.Line {

	const ds = 12.0
	samples := cl.Resample(ds)
	runs := map[int]int{}  // strand → current consecutive run
	bests := map[int]int{} // strand → best run
	for _, q := range samples {
		here := map[int]bool{}
		sgrid.Near(q, p.MateMax, func(si int) { here[si] = true })
		for si := range runs {
			if !here[si] {
				delete(runs, si)
			}
		}
		for si := range here {
			runs[si]++
			if runs[si] > bests[si] {
				bests[si] = runs[si]
			}
		}
	}
	need := int(math.Min(p.MateRun, 0.8*cl.Len()) / ds)
	if need < 2 {
		need = 2
	}
	var out []*geo.Line
	for si, b := range bests {
		if b >= need {
			out = append(out, strandLines[si])
		}
	}
	return out
}

// trackCount estimates the physical strand count of the group (median over a
// few cross-sections).
func trackCount(cl *geo.Line, members []*geo.Line, rp bundle.Params) int {
	if len(members) == 0 {
		return 1
	}
	var counts []int
	for _, f := range []float64{0.3, 0.5, 0.7} {
		at := cl.Len() * f
		q := cl.AtArc(at)
		tan := cl.TangentAtArc(at, 15)
		var offs []float64
		for _, m := range members {
			for _, c := range m.CrossSection(q, tan, rp.Reach) {
				if c.Parallel >= rp.MinParallel {
					offs = append(offs, c.Offset)
				}
			}
		}
		counts = append(counts, len(bundle.Strands(offs, rp.StrandGap)))
	}
	sort.Ints(counts)
	return counts[len(counts)/2]
}

// placeNodeAtMeet solves the least-squares meet of the end rays of the
// node's incident refined centerlines. Rays sample an interior window so the
// pinned-end bend from refinement doesn't own the tangent. Degenerate or
// far-flung solutions keep the physical switch position.
func placeNodeAtMeet(net *Network, ni int, p splitParams) {
	n := &net.Nodes[ni]
	var sumM [2][2]float64
	var sumB [2]float64
	rays := 0
	for _, ei := range n.Adj {
		e := net.Edges[ei]
		if e.Gap {
			continue
		}
		cl := geo.NewLine(e.Pts)
		if cl.Len() < p.RayFar+10 {
			continue
		}
		var a, b geo.Pt
		if e.To == ni {
			a, b = cl.AtArc(cl.Len()-p.RayFar), cl.AtArc(cl.Len()-p.RayNear)
		} else {
			a, b = cl.AtArc(p.RayFar), cl.AtArc(p.RayNear)
		}
		d := b.Sub(a).Unit()
		// (I - ddᵀ) accumulates the perpendicular projector of the ray
		m00, m01, m11 := 1-d.X*d.X, -d.X*d.Y, 1-d.Y*d.Y
		sumM[0][0] += m00
		sumM[0][1] += m01
		sumM[1][0] += m01
		sumM[1][1] += m11
		sumB[0] += m00*b.X + m01*b.Y
		sumB[1] += m01*b.X + m11*b.Y
		rays++
	}
	if rays < 2 {
		return
	}
	det := sumM[0][0]*sumM[1][1] - sumM[0][1]*sumM[1][0]
	if math.Abs(det) < 1e-3 {
		return
	}
	x := geo.Pt{
		X: (sumB[0]*sumM[1][1] - sumB[1]*sumM[0][1]) / det,
		Y: (sumM[0][0]*sumB[1] - sumM[1][0]*sumB[0]) / det,
	}
	if x.Dist(n.At) > p.MeetMax {
		return
	}
	n.At = x
}
