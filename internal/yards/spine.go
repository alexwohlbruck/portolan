package yards

import (
	"container/heap"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

const (
	walkStepM      = cellM / 2 // membership sampling pitch along every track
	entranceMergeM = 40.0      // ring-arc radius that merges boundary crossings
	entranceCos    = 0.866     // ~30° heading agreement for a merge
	spineSmoothS   = 15.0
	spineOverlap   = 0.8 // path length already covered by kept spines → dup
	spineCapFactor = 2   // max spines per region = factor × entrances
	nodeQuantM     = 0.25
)

// crossing is one spot where a track pierces a region's outline.
type crossing struct {
	ti      int
	arc     float64 // on the way, refined to the outline intersection
	pt      geo.Pt  // on the outline ring
	heading geo.Pt  // unit, pointing OUT of the region
	ringArc float64
}

// piece is a maximal in-region interval of one track.
type piece struct {
	ti     int
	a0, a1 float64
}

// buildEntrancesAndSpines runs the boundary pass: every track is walked
// once against the region masks, transitions refine to outline crossings,
// crossings cluster into entrances, and Dijkstra over the clipped in-yard
// track graph turns entrance pairs into spine centerlines.
func (ix *Index) buildEntrancesAndSpines(tracks []Track) {
	nr := len(ix.regions)
	crossings := make([][]crossing, nr)
	pieces := make([][]piece, nr)
	rings := make([]*geo.Line, nr)
	for ri, r := range ix.regions {
		if len(r.Outline) >= 3 {
			closed := append(append([]geo.Pt{}, r.Outline...), r.Outline[0])
			rings[ri] = geo.NewLine(closed)
		}
	}

	// regionAt answers with the region index a sample belongs to, or -1 —
	// level-gated: a subway under a surface yard is outside it.
	regionAt := func(p geo.Pt, lvl int) int {
		id, ok := ix.cellRegion[cellKey(p)]
		if !ok || ix.regions[id].Level != lvl || rings[id] == nil {
			return -1
		}
		return int(id)
	}

	for ti := range tracks {
		t := &tracks[ti]
		total := t.Line.Len()
		if total < 1e-9 {
			continue
		}
		prevArc := 0.0
		prevReg := regionAt(t.Line.AtArc(0), t.Level)
		openAt := math.Inf(-1)
		if prevReg >= 0 {
			openAt = 0
		}
		for s := walkStepM; ; s += walkStepM {
			if s > total {
				s = total
			}
			reg := regionAt(t.Line.AtArc(s), t.Level)
			if reg != prevReg {
				if prevReg >= 0 {
					c, ok := refineCrossing(t, ti, prevArc, s, rings[prevReg], false)
					end := s // fallback: cut at the first outside sample
					if ok {
						crossings[prevReg] = append(crossings[prevReg], c)
						end = c.arc
					}
					if !math.IsInf(openAt, -1) && end > openAt {
						pieces[prevReg] = append(pieces[prevReg], piece{ti, openAt, end})
					}
					openAt = math.Inf(-1)
				}
				if reg >= 0 {
					openAt = prevArc
					if c, ok := refineCrossing(t, ti, prevArc, s, rings[reg], true); ok {
						crossings[reg] = append(crossings[reg], c)
						openAt = c.arc
					}
				}
			}
			prevArc, prevReg = s, reg
			if s >= total {
				break
			}
		}
		if prevReg >= 0 && !math.IsInf(openAt, -1) && total > openAt {
			pieces[prevReg] = append(pieces[prevReg], piece{ti, openAt, total})
		}
	}

	for ri, r := range ix.regions {
		if rings[ri] == nil {
			continue
		}
		ents, members := clusterEntrances(crossings[ri], rings[ri].Len(), tracks)
		r.Entrances = ents
		r.Spines, r.SkelNodes, r.Skel = buildSpines(tracks, ents, members, crossings[ri], pieces[ri])
	}
}

// refineCrossing intersects the chord between two membership samples with
// the outline ring: entering keeps the first pierce along the walk,
// leaving the last, so the crossing lands on the OUTER skin. The heading
// points out of the region.
func refineCrossing(t *Track, ti int, a, b float64, ring *geo.Line, entering bool) (crossing, bool) {
	pa, pb := t.Line.AtArc(a), t.Line.AtArc(b)
	span := math.Max(pb.Sub(pa).Norm(), 1e-12)
	bestT := -1.0
	var bestPt geo.Pt
	for i := 1; i < len(ring.Pts); i++ {
		q, ok := geo.SegIntersect(pa, pb, ring.Pts[i-1], ring.Pts[i])
		if !ok {
			continue
		}
		tt := q.Sub(pa).Norm() / span
		if bestT < 0 || (entering && tt < bestT) || (!entering && tt > bestT) {
			bestT, bestPt = tt, q
		}
	}
	if bestT < 0 {
		return crossing{}, false
	}
	arc := a + (b-a)*math.Min(1, bestT)
	tan := t.Line.TangentAtArc(arc, tangentWinM)
	out := pb // the outside sample
	if entering {
		out = pa
	}
	if tan.Dot(out.Sub(bestPt)) < 0 {
		tan = tan.Scale(-1)
	}
	ringArc, _ := ring.ProjectArc(bestPt)
	return crossing{ti: ti, arc: arc, pt: bestPt, heading: tan, ringArc: ringArc}, true
}

// clusterEntrances merges crossings that pierce the outline close together
// (ring arc within entranceMergeM, wrap-around included) with agreeing
// outward headings. The representative point is the member crossing
// nearest the cluster's ring-arc median — a real pierce point, so spines
// can end exactly on it. Returns entrances in ring-arc order plus each
// entrance's member indices into cs (cs is sorted in place).
func clusterEntrances(cs []crossing, ringLen float64, tracks []Track) ([]Entrance, [][]int) {
	if len(cs) == 0 {
		return nil, nil
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].ringArc != cs[j].ringArc {
			return cs[i].ringArc < cs[j].ringArc
		}
		if cs[i].ti != cs[j].ti {
			return cs[i].ti < cs[j].ti
		}
		return cs[i].arc < cs[j].arc
	})
	var clusters [][]int
	for i := range cs {
		if n := len(clusters); n > 0 {
			last := clusters[n-1]
			prev := cs[last[len(last)-1]]
			if cs[i].ringArc-prev.ringArc <= entranceMergeM && cs[i].heading.Dot(prev.heading) >= entranceCos {
				clusters[n-1] = append(last, i)
				continue
			}
		}
		clusters = append(clusters, []int{i})
	}
	if len(clusters) > 1 {
		// Wrap-around: the ring's zero arc is an arbitrary corner.
		first, last := clusters[0], clusters[len(clusters)-1]
		a, b := cs[first[0]], cs[last[len(last)-1]]
		if a.ringArc+ringLen-b.ringArc <= entranceMergeM && a.heading.Dot(b.heading) >= entranceCos {
			clusters[0] = append(last, first...)
			clusters = clusters[:len(clusters)-1]
		}
	}
	type ent struct {
		med     float64
		e       Entrance
		members []int
	}
	ents := make([]ent, 0, len(clusters))
	for _, cl := range clusters {
		arcs := make([]float64, len(cl))
		for i, ci := range cl {
			arcs[i] = cs[ci].ringArc
		}
		sort.Float64s(arcs)
		med := arcs[len(arcs)/2]
		best, bestD := cl[0], math.Inf(1)
		for _, ci := range cl {
			if d := math.Abs(cs[ci].ringArc - med); d < bestD {
				best, bestD = ci, d
			}
		}
		ids := map[string]bool{}
		for _, ci := range cl {
			ids[tracks[cs[ci].ti].ID] = true
		}
		e := Entrance{Pt: cs[best].pt, Heading: cs[best].heading}
		for id := range ids {
			e.WayIDs = append(e.WayIDs, id)
		}
		sort.Strings(e.WayIDs)
		ents = append(ents, ent{med, e, cl})
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].med < ents[j].med })
	out := make([]Entrance, len(ents))
	members := make([][]int, len(ents))
	for i, e := range ents {
		out[i] = e.e
		members[i] = e.members
	}
	return out, members
}

// ---- the in-yard track graph and its spines ----

type spineEdge struct {
	a, b int // node ids
	cost float64
	arcL float64
	pts  []geo.Pt // a→b geometry
}

type spineGraph struct {
	nodes map[[2]int]int
	pos   []geo.Pt
	adj   [][]int // node → edge indices
	edges []spineEdge
}

func (g *spineGraph) node(p geo.Pt) int {
	k := [2]int{int(math.Round(p.X / nodeQuantM)), int(math.Round(p.Y / nodeQuantM))}
	if id, ok := g.nodes[k]; ok {
		return id
	}
	id := len(g.pos)
	g.nodes[k] = id
	g.pos = append(g.pos, p)
	g.adj = append(g.adj, nil)
	return id
}

func (g *spineGraph) addEdge(pts []geo.Pt) {
	if len(pts) < 2 {
		return
	}
	arcL, turn := 0.0, 0.0
	for i := 1; i < len(pts); i++ {
		arcL += pts[i].Dist(pts[i-1])
	}
	if arcL < 1e-9 {
		return
	}
	for i := 1; i < len(pts)-1; i++ {
		turn += geo.TurnDeg(pts[i-1], pts[i], pts[i+1])
	}
	a, b := g.node(pts[0]), g.node(pts[len(pts)-1])
	// Shortest AND straightest wins: the spine should ride the through
	// lead, not zigzag the ladder.
	e := spineEdge{a: a, b: b, cost: arcL * (1 + turn/180), arcL: arcL, pts: pts}
	g.adj[a] = append(g.adj[a], len(g.edges))
	g.adj[b] = append(g.adj[b], len(g.edges))
	g.edges = append(g.edges, e)
}

// subPts extracts the vertex run of a track between two arcs, exact
// endpoints included.
func subPts(l *geo.Line, a0, a1 float64) []geo.Pt {
	pts := []geo.Pt{l.AtArc(a0)}
	arcs := l.ArcTable()
	for i, arc := range arcs {
		if arc > a0+1e-9 && arc < a1-1e-9 {
			pts = append(pts, l.Pts[i])
		}
	}
	pts = append(pts, l.AtArc(a1))
	return pts
}

type pqItem struct {
	node int
	dist float64
}
type pq []pqItem

func (q pq) Len() int { return len(q) }
func (q pq) Less(i, j int) bool {
	if q[i].dist != q[j].dist {
		return q[i].dist < q[j].dist
	}
	return q[i].node < q[j].node
}
func (q pq) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *pq) Push(x any)        { *q = append(*q, x.(pqItem)) }
func (q *pq) Pop() any          { old := *q; n := len(old); it := old[n-1]; *q = old[:n-1]; return it }

// buildSpines connects entrance pairs through the yard's own steel, then
// contracts the union of kept paths into the region's skeleton.
func buildSpines(tracks []Track, ents []Entrance, members [][]int, cs []crossing, pcs []piece) ([]Spine, []SkelNode, []SkelEdge) {
	if len(ents) < 2 || len(pcs) == 0 {
		return nil, nil, nil
	}
	g := &spineGraph{nodes: map[[2]int]int{}}

	// Junction census: a vertex key seen by more than one piece is a
	// switch node; piece endpoints always are.
	seen := map[[2]int]int{}
	quant := func(p geo.Pt) [2]int {
		return [2]int{int(math.Round(p.X / nodeQuantM)), int(math.Round(p.Y / nodeQuantM))}
	}
	perPiece := make([][]geo.Pt, len(pcs))
	for pi, pc := range pcs {
		pts := subPts(tracks[pc.ti].Line, pc.a0, pc.a1)
		perPiece[pi] = pts
		for _, p := range pts {
			seen[quant(p)]++
		}
	}
	for _, pts := range perPiece {
		run := []geo.Pt{pts[0]}
		for i := 1; i < len(pts); i++ {
			run = append(run, pts[i])
			if i == len(pts)-1 || seen[quant(pts[i])] > 1 {
				g.addEdge(run)
				run = []geo.Pt{pts[i]}
			}
		}
	}
	// Entrance nodes weld to their member crossings' pierce points (the
	// piece endpoints) with straight connectors.
	entNode := make([]int, len(ents))
	for ei, e := range ents {
		entNode[ei] = g.node(e.Pt)
		for _, ci := range members[ei] {
			if cs[ci].pt != e.Pt {
				g.addEdge([]geo.Pt{e.Pt, cs[ci].pt})
			}
		}
	}

	var spines []Spine
	kept := map[int]bool{} // edge indices covered by kept spines
	maxSpines := spineCapFactor * len(ents)
	for i := 0; i < len(ents) && len(spines) < maxSpines; i++ {
		dist, prevE := dijkstra(g, entNode[i])
		for j := i + 1; j < len(ents) && len(spines) < maxSpines; j++ {
			if math.IsInf(dist[entNode[j]], 1) || entNode[j] == entNode[i] {
				continue
			}
			// Reconstruct edge path j←i.
			var path []int
			for n := entNode[j]; n != entNode[i]; {
				ei := prevE[n]
				path = append(path, ei)
				if g.edges[ei].a == n {
					n = g.edges[ei].b
				} else {
					n = g.edges[ei].a
				}
			}
			total, covered := 0.0, 0.0
			for _, ei := range path {
				total += g.edges[ei].arcL
				if kept[ei] {
					covered += g.edges[ei].arcL
				}
			}
			if total < 1e-9 || covered/total >= spineOverlap {
				continue
			}
			// Assemble geometry i→j (path is j→i).
			pts := []geo.Pt{ents[i].Pt}
			cur := entNode[i]
			for k := len(path) - 1; k >= 0; k-- {
				e := g.edges[path[k]]
				seg := e.pts
				if e.b == cur {
					seg = reversePts(seg)
					cur = e.a
				} else {
					cur = e.b
				}
				pts = append(pts, seg[1:]...)
				kept[path[k]] = true
			}
			sm := geo.SmoothTurning(pts, spineSmoothS)
			// The weld guarantee: spine ends bit-equal to the entrances.
			sm[0], sm[len(sm)-1] = ents[i].Pt, ents[j].Pt
			spines = append(spines, Spine{From: i, To: j, Line: geo.NewLine(sm)})
		}
	}
	skN, skE := contractSkeleton(g, entNode, kept)
	return spines, skN, skE
}

// contractSkeleton reduces the kept edge set to runs between skeleton
// nodes (entrances, and any vertex whose kept-degree isn't 2 — forks and
// dead ends). Runs are heading-smoothed with their end points pinned
// bit-equal to the node positions.
func contractSkeleton(g *spineGraph, entNode []int, kept map[int]bool) ([]SkelNode, []SkelEdge) {
	if len(kept) == 0 {
		return nil, nil
	}
	keptIDs := make([]int, 0, len(kept))
	for ei := range kept {
		keptIDs = append(keptIDs, ei)
	}
	sort.Ints(keptIDs)
	adj := map[int][]int{}
	for _, ei := range keptIDs {
		e := g.edges[ei]
		adj[e.a] = append(adj[e.a], ei)
		adj[e.b] = append(adj[e.b], ei)
	}
	entOf := map[int]int{}
	for i, n := range entNode {
		entOf[n] = i
	}
	isSkel := func(n int) bool {
		_, ent := entOf[n]
		return ent || len(adj[n]) != 2
	}
	var nodes []SkelNode
	nodeID := map[int]int{}
	skelNode := func(n int) int {
		if id, ok := nodeID[n]; ok {
			return id
		}
		ent, ok := entOf[n]
		if !ok {
			ent = -1
		}
		nodeID[n] = len(nodes)
		nodes = append(nodes, SkelNode{Pt: g.pos[n], Entrance: ent})
		return nodeID[n]
	}
	var edges []SkelEdge
	used := map[int]bool{}
	starts := make([]int, 0, len(adj))
	for n := range adj {
		if isSkel(n) {
			starts = append(starts, n)
		}
	}
	sort.Ints(starts)
	for _, start := range starts {
		for _, ei0 := range adj[start] {
			if used[ei0] {
				continue
			}
			run := []geo.Pt{g.pos[start]}
			cur, ei := start, ei0
			for {
				used[ei] = true
				e := g.edges[ei]
				seg := e.pts
				next := e.b
				if e.b == cur {
					seg = reversePts(seg)
					next = e.a
				}
				run = append(run, seg[1:]...)
				cur = next
				if isSkel(cur) {
					break
				}
				found := -1
				for _, nx := range adj[cur] {
					if !used[nx] {
						found = nx
						break
					}
				}
				if found < 0 {
					break
				}
				ei = found
			}
			if len(run) < 2 {
				continue
			}
			sm := geo.SmoothTurning(run, spineSmoothS)
			sm[0], sm[len(sm)-1] = g.pos[start], g.pos[cur]
			edges = append(edges, SkelEdge{A: skelNode(start), B: skelNode(cur), Line: geo.NewLine(sm)})
		}
	}
	return nodes, edges
}

func reversePts(pts []geo.Pt) []geo.Pt {
	out := make([]geo.Pt, len(pts))
	for i, p := range pts {
		out[len(pts)-1-i] = p
	}
	return out
}

// dijkstra returns per-node best cost from src and the edge used to reach
// each node; deterministic (heap ties break on node id, edge fans are
// appended in construction order).
func dijkstra(g *spineGraph, src int) ([]float64, []int) {
	dist := make([]float64, len(g.pos))
	prevE := make([]int, len(g.pos))
	for i := range dist {
		dist[i] = math.Inf(1)
		prevE[i] = -1
	}
	dist[src] = 0
	q := &pq{{src, 0}}
	for q.Len() > 0 {
		it := heap.Pop(q).(pqItem)
		if it.dist > dist[it.node] {
			continue
		}
		for _, ei := range g.adj[it.node] {
			e := g.edges[ei]
			to := e.b
			if to == it.node {
				to = e.a
			}
			if nd := it.dist + e.cost; nd < dist[to] {
				dist[to] = nd
				prevE[to] = ei
				heap.Push(q, pqItem{to, nd})
			}
		}
	}
	return dist, prevE
}
