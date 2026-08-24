package yards

import (
	"container/heap"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

const (
	walkStepM      = cellM / 2 // membership sampling pitch along every track
	entranceMergeM = 40.0      // proximity that merges crossings into one entrance
	entranceCos    = 0.866     // ~30° heading agreement for a merge
	termMergeM     = 70.0      // buffer stops sit ragged; a bundle end spreads wider
	termMinTracks  = 3         // a BUNCH of tracks ending — not every dead-end stub
	spineSmoothS   = 25.0      // heading low-pass sigma — rule 6, flowing geometry
	anchorSnapM    = 5.0       // crossing point → graph node tolerance
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
func (ix *Index) buildEntrancesAndSpines(tracks []Track, eff []int) {
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
	// level-gated on EFFECTIVE levels: a subway under a surface yard is
	// outside it, but a 150 m tunnel=yes underpass fragment of a surface
	// corridor is not a subway (it cost the Bushwick branch its entrance
	// and region 66 its whole spine graph).
	regionAt := func(p geo.Pt, lvl int) int {
		id := ix.regionIdxAt(p)
		if id < 0 || ix.regions[id].Level != lvl || rings[id] == nil {
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
		prevReg := regionAt(t.Line.AtArc(0), eff[ti])
		openAt := math.Inf(-1)
		if prevReg >= 0 {
			openAt = 0
		}
		for s := walkStepM; ; s += walkStepM {
			if s > total {
				s = total
			}
			reg := regionAt(t.Line.AtArc(s), eff[ti])
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
		for _, pc := range pieces[ri] {
			if pts := subPts(tracks[pc.ti].Line, pc.a0, pc.a1); len(pts) >= 2 {
				r.Steel = append(r.Steel, geo.NewLine(pts))
			}
		}
		if len(pieces[ri]) == 0 {
			continue
		}
		g := buildYardGraph(tracks, pieces[ri])
		ents, members := clusterEntrances(crossings[ri], rings[ri].Len(), tracks)
		// anchors: the graph nodes where each entrance's tracks pierce
		anchors := make([][]int, len(ents))
		taken := map[int]bool{}
		for ei := range ents {
			seen := map[int]bool{}
			for _, ci := range members[ei] {
				n, ok := g.nearestNode(crossings[ri][ci].pt, anchorSnapM)
				if !ok || seen[n] {
					continue
				}
				seen[n] = true
				anchors[ei] = append(anchors[ei], n)
				taken[n] = true
			}
			sort.Ints(anchors[ei])
		}
		// terminals: bundles of track that simply end inside the yard
		tEnts, tAnchors := terminalEntrances(g, tracks, taken)
		ents = append(ents, tEnts...)
		anchors = append(anchors, tAnchors...)
		r.Entrances = ents
		r.Spines, r.SkelNodes, r.Skel = buildSpines(g, ents, anchors)
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
	// Cluster by EUCLIDEAN proximity, not ring arc: the tracks of one
	// throat pierce within metres of each other, but the outline between
	// them wiggles, so an arc window left Jamaica's throat as five
	// separate entrances each pulling its own centerline. Union-find, so
	// a wide throat chains across its whole fan.
	parent := make([]int, len(cs))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := range cs {
		for j := i + 1; j < len(cs); j++ {
			if cs[i].pt.Dist(cs[j].pt) <= entranceMergeM &&
				cs[i].heading.Dot(cs[j].heading) >= entranceCos {
				if a, b := find(i), find(j); a != b {
					if a < b {
						parent[b] = a
					} else {
						parent[a] = b
					}
				}
			}
		}
	}
	byRoot := map[int][]int{}
	for i := range cs {
		r := find(i)
		byRoot[r] = append(byRoot[r], i)
	}
	roots := make([]int, 0, len(byRoot))
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Ints(roots)
	clusters := make([][]int, 0, len(roots))
	for _, r := range roots {
		clusters = append(clusters, byRoot[r])
	}
	_ = ringLen
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
		// Where several parallel tracks pierce together, the entrance is
		// the AVERAGE centerpoint of their crossings — one node in the
		// middle of the bundle, not a snap onto whichever rail happened
		// to sit nearest the median. The centerline then leaves the
		// entrance down the middle, which is what a corridor drawing of
		// a multi-track throat means.
		var sum, hsum geo.Pt
		ids := map[string]bool{}
		for _, ci := range cl {
			sum = sum.Add(cs[ci].pt)
			hsum = hsum.Add(cs[ci].heading)
			ids[tracks[cs[ci].ti].ID] = true
		}
		e := Entrance{
			Pt:      sum.Scale(1 / float64(len(cl))),
			Heading: hsum.Unit(),
		}
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
	nodes  map[[2]int]int
	pos    []geo.Pt
	adj    [][]int // node → edge indices
	edges  []spineEdge
	bycell map[[2]int][]int // 4 m cells → node ids, for nearestNode
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
	c := [2]int{int(math.Floor(p.X / 4)), int(math.Floor(p.Y / 4))}
	if g.bycell == nil {
		g.bycell = map[[2]int][]int{}
	}
	g.bycell[c] = append(g.bycell[c], id)
	return id
}

// nearestNode finds the graph node closest to p within maxD. Boundary
// crossings are computed on the outline ring while piece ends come from
// arc interpolation, so the two agree to centimetres, not exactly.
func (g *spineGraph) nearestNode(p geo.Pt, maxD float64) (int, bool) {
	r := int(math.Ceil(maxD/4)) + 1
	c := [2]int{int(math.Floor(p.X / 4)), int(math.Floor(p.Y / 4))}
	best, bestD := -1, maxD
	for dx := -r; dx <= r; dx++ {
		for dy := -r; dy <= r; dy++ {
			for _, n := range g.bycell[[2]int{c[0] + dx, c[1] + dy}] {
				if d := g.pos[n].Dist(p); d < bestD || (d == bestD && n < best) {
					best, bestD = n, d
				}
			}
		}
	}
	return best, best >= 0
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
func (q pq) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *pq) Push(x any)   { *q = append(*q, x.(pqItem)) }
func (q *pq) Pop() any     { old := *q; n := len(old); it := old[n-1]; *q = old[:n-1]; return it }

// buildYardGraph turns the in-region track pieces into a graph: nodes at
// shared vertices (OSM switches are exact shared nodes, quantized here to
// 25 cm), edges the runs between them. Every edge IS real steel, which is
// what lets a centerline path-match a track end to end.
func buildYardGraph(tracks []Track, pcs []piece) *spineGraph {
	g := &spineGraph{nodes: map[[2]int]int{}}
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
	return g
}

// terminalEntrances finds bundles of track that END inside the yard — a
// rail terminal's platform ends, a storage fan's buffers — and makes each
// bundle one entrance at its averaged endpoint, exactly as if the group
// were leaving the yard. Only a BUNCH counts (termMinTracks): every
// storage track in a yard dead-ends somewhere, and one entrance per stub
// would run a centerline down every siding.
func terminalEntrances(g *spineGraph, tracks []Track, taken map[int]bool) ([]Entrance, [][]int) {
	var cands []int
	for n := range g.pos {
		if len(g.adj[n]) == 1 && !taken[n] {
			cands = append(cands, n)
		}
	}
	if len(cands) < termMinTracks {
		return nil, nil
	}
	sort.Ints(cands)
	head := make(map[int]geo.Pt, len(cands))
	for _, n := range cands {
		e := g.edges[g.adj[n][0]]
		pts := e.pts
		// outward: along the track, away from the yard's interior
		if e.b == n {
			i := max(0, len(pts)-4)
			head[n] = pts[len(pts)-1].Sub(pts[i]).Unit()
		} else {
			i := min(len(pts)-1, 3)
			head[n] = pts[0].Sub(pts[i]).Unit()
		}
	}
	parent := map[int]int{}
	var find func(int) int
	find = func(x int) int {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i, a := range cands {
		for _, b := range cands[i+1:] {
			if g.pos[a].Dist(g.pos[b]) <= termMergeM && head[a].Dot(head[b]) >= entranceCos {
				if ra, rb := find(a), find(b); ra != rb {
					if ra < rb {
						parent[rb] = ra
					} else {
						parent[ra] = rb
					}
				}
			}
		}
	}
	byRoot := map[int][]int{}
	for _, n := range cands {
		r := find(n)
		byRoot[r] = append(byRoot[r], n)
	}
	roots := make([]int, 0, len(byRoot))
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Ints(roots)
	var ents []Entrance
	var anchors [][]int
	for _, r := range roots {
		group := byRoot[r]
		if len(group) < termMinTracks {
			continue
		}
		sort.Ints(group)
		var sum, hsum geo.Pt
		for _, n := range group {
			sum = sum.Add(g.pos[n])
			hsum = hsum.Add(head[n])
		}
		ents = append(ents, Entrance{
			Pt:       sum.Scale(1 / float64(len(group))),
			Heading:  hsum.Unit(),
			Terminal: true,
		})
		anchors = append(anchors, group)
	}
	return ents, anchors
}

// buildSpines lays the region's centerlines. The centerlines form a TREE
// over the entrances grown through the yard's own steel — never an
// all-pairs bundle, which is what wound lassos around Jamaica: in a tree
// every path is unique, so a centerline cannot double back on itself.
//
// The rules it keeps, in the owner's numbering:
//  1. every entrance is a node — anchors are the graph nodes where its
//     member tracks pierce the outline;
//  2. that node sits at the AVERAGE centerpoint of those crossings
//     (clusterEntrances), and every anchor of one entrance contracts to
//     that single node, so parallel tracks converge into one entry;
//  3. every centerline edge lies on a path between entrances (Prim only
//     ever adds one), so none floats free;
//  4. every entrance is connected — the growth continues while any
//     entrance remains reachable, and an entrance alone in its component
//     still gets a stub down its own steel;
//  5. every edge is a real track run from the graph, so the whole
//     centerline path-matches steel end to end with no track jumping;
//  6. runs are heading-smoothed with their ends pinned, so the geometry
//     flows through the yard instead of stepping switch to switch.
func buildSpines(g *spineGraph, ents []Entrance, anchors [][]int) ([]Spine, []SkelNode, []SkelEdge) {
	if len(ents) == 0 || len(g.edges) == 0 {
		return nil, nil, nil
	}
	entOf := map[int]int{}
	for ei := range ents {
		for _, n := range anchors[ei] {
			if _, taken := entOf[n]; !taken {
				entOf[n] = ei
			}
		}
	}

	kept := map[int]bool{}
	done := make([]bool, len(ents))
	for {
		seed := -1
		for ei := range ents {
			if !done[ei] && len(anchors[ei]) > 0 {
				seed = ei
				break
			}
		}
		if seed < 0 {
			break
		}
		done[seed] = true
		inTree := map[int]bool{}
		tree := append([]int{}, anchors[seed]...)
		for _, n := range tree {
			inTree[n] = true
		}
		grown := false
		for {
			dist, prevE := dijkstraMulti(g, tree)
			bestEnt, bestNode, bestD := -1, -1, math.Inf(1)
			for ei := range ents {
				if done[ei] {
					continue
				}
				for _, n := range anchors[ei] {
					if dist[n] < bestD {
						bestEnt, bestNode, bestD = ei, n, dist[n]
					}
				}
			}
			if bestEnt < 0 {
				break
			}
			for n := bestNode; !inTree[n]; {
				ei := prevE[n]
				if ei < 0 {
					break
				}
				kept[ei] = true
				inTree[n] = true
				tree = append(tree, n)
				n = g.edges[ei].a + g.edges[ei].b - n
			}
			done[bestEnt], grown = true, true
			for _, n := range anchors[bestEnt] {
				if !inTree[n] {
					inTree[n] = true
					tree = append(tree, n)
				}
			}
		}
		if !grown {
			// Rule 4 for a lone entrance: no partner to reach, so run a
			// stub down its own steel to the far end of its component.
			dist, prevE := dijkstraMulti(g, anchors[seed])
			far, farD := -1, 0.0
			for n := range dist {
				if !math.IsInf(dist[n], 1) && dist[n] > farD {
					far, farD = n, dist[n]
				}
			}
			for n := far; n >= 0 && !inTree[n]; {
				ei := prevE[n]
				if ei < 0 {
					break
				}
				kept[ei] = true
				inTree[n] = true
				n = g.edges[ei].a + g.edges[ei].b - n
			}
		}
	}

	skN, skE := contractSkeleton(g, ents, entOf, kept)
	return spinesFromSkeleton(skN, skE), skN, skE
}

// contractSkeleton reduces the kept edge set to runs between skeleton
// nodes (entrances, and any vertex whose kept-degree isn't 2 — forks and
// dead ends). Every anchor of one entrance contracts to ONE node at the
// entrance's averaged point, so parallel tracks converge there; runs are
// pinned to their node positions BEFORE smoothing, which lets the
// heading low-pass absorb the lateral pull into a flowing curve instead
// of leaving a kink at the second vertex.
func contractSkeleton(g *spineGraph, ents []Entrance, entOf map[int]int, kept map[int]bool) ([]SkelNode, []SkelEdge) {
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
	isSkel := func(n int) bool {
		_, ent := entOf[n]
		return ent || len(adj[n]) != 2
	}
	var nodes []SkelNode
	nodeID := map[int]int{}
	entNodeID := map[int]int{}
	skelNode := func(n int) int {
		if id, ok := nodeID[n]; ok {
			return id
		}
		if ei, ok := entOf[n]; ok {
			if id, have := entNodeID[ei]; have {
				nodeID[n] = id
				return id
			}
			id := len(nodes)
			nodes = append(nodes, SkelNode{Pt: ents[ei].Pt, Entrance: ei})
			entNodeID[ei], nodeID[n] = id, id
			return id
		}
		id := len(nodes)
		nodes = append(nodes, SkelNode{Pt: g.pos[n], Entrance: -1})
		nodeID[n] = id
		return id
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
			a, b := skelNode(start), skelNode(cur)
			if a == b {
				continue // a loop back onto one node draws no corridor
			}
			run[0], run[len(run)-1] = nodes[a].Pt, nodes[b].Pt
			sm := geo.SmoothTurning(run, spineSmoothS)
			sm[0], sm[len(sm)-1] = nodes[a].Pt, nodes[b].Pt
			edges = append(edges, SkelEdge{A: a, B: b, Line: geo.NewLine(sm)})
		}
	}
	return nodes, forest(edges)
}

// forest drops any run that closes a cycle, keeping the shorter way
// round. The kept graph is a tree by construction, but contraction can
// still close one: when two anchors of ONE entrance both sit in the tree,
// merging them into that entrance's single node turns the path between
// them into a loop — which is exactly the lasso that wound around
// Jamaica's throat. A dropped edge never disconnects anything (both its
// ends are already joined), so every entrance keeps its centerline.
func forest(edges []SkelEdge) []SkelEdge {
	if len(edges) < 2 {
		return edges
	}
	order := make([]int, len(edges))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return edges[order[i]].Line.Len() < edges[order[j]].Line.Len()
	})
	parent := map[int]int{}
	var find func(int) int
	find = func(x int) int {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	keep := make([]bool, len(edges))
	for _, i := range order {
		a, b := find(edges[i].A), find(edges[i].B)
		if a == b {
			continue
		}
		parent[a] = b
		keep[i] = true
	}
	out := edges[:0]
	for i, e := range edges {
		if keep[i] {
			out = append(out, e)
		}
	}
	return out
}

// spinesFromSkeleton reads the entrance-to-entrance corridors out of the
// skeleton: BFS from each entrance node, never expanding THROUGH another
// entrance, so each spine is one throat-to-throat run with no entrance in
// the middle. In a tree these paths are unique, so no pair is ambiguous.
func spinesFromSkeleton(nodes []SkelNode, edges []SkelEdge) []Spine {
	if len(edges) == 0 {
		return nil
	}
	adj := make([][]int, len(nodes))
	for i, e := range edges {
		adj[e.A] = append(adj[e.A], i)
		adj[e.B] = append(adj[e.B], i)
	}
	var out []Spine
	seen := map[[2]int]bool{}
	for s := range nodes {
		if nodes[s].Entrance < 0 {
			continue
		}
		prev := make([]int, len(nodes))
		for i := range prev {
			prev[i] = -1
		}
		visited := make([]bool, len(nodes))
		visited[s] = true
		queue := []int{s}
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			for _, ei := range adj[n] {
				e := edges[ei]
				nx := e.A + e.B - n
				if visited[nx] {
					continue
				}
				visited[nx] = true
				prev[nx] = ei
				if nodes[nx].Entrance >= 0 {
					a, b := nodes[s].Entrance, nodes[nx].Entrance
					key := [2]int{min(a, b), max(a, b)}
					if !seen[key] {
						seen[key] = true
						var chain []int
						for c := nx; c != s; {
							pe := prev[c]
							chain = append(chain, pe)
							c = edges[pe].A + edges[pe].B - c
						}
						var pts []geo.Pt
						cur := s
						for k := len(chain) - 1; k >= 0; k-- {
							e := edges[chain[k]]
							seg := e.Line.Pts
							if e.B == cur {
								seg = reversePts(seg)
								cur = e.A
							} else {
								cur = e.B
							}
							if len(pts) == 0 {
								pts = append(pts, seg...)
							} else {
								pts = append(pts, seg[1:]...)
							}
						}
						if len(pts) >= 2 {
							out = append(out, Spine{From: a, To: b, Line: geo.NewLine(pts)})
						}
					}
					continue // never expand THROUGH an entrance
				}
				queue = append(queue, nx)
			}
		}
	}
	return out
}

func reversePts(pts []geo.Pt) []geo.Pt {
	out := make([]geo.Pt, len(pts))
	for i, p := range pts {
		out[len(pts)-1-i] = p
	}
	return out
}

// dijkstraMulti returns per-node best cost from the nearest source and
// the edge used to reach each node; deterministic (heap ties break on
// node id, edge fans are appended in construction order).
func dijkstraMulti(g *spineGraph, srcs []int) ([]float64, []int) {
	dist := make([]float64, len(g.pos))
	prevE := make([]int, len(g.pos))
	for i := range dist {
		dist[i] = math.Inf(1)
		prevE[i] = -1
	}
	q := &pq{}
	for _, s := range srcs {
		if dist[s] != 0 {
			dist[s] = 0
			*q = append(*q, pqItem{s, 0})
		}
	}
	heap.Init(q)
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
