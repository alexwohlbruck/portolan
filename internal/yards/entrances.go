package yards

import (
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
	centerReachM   = 35.0      // cross-section half-reach when centering a run
	centerParallel = 0.85      // heading agreement for a track to count as bundle
	centerTaperM   = 45.0      // lateral shift fades to nothing at a pinned node
	centerStepM    = 12.0      // resample pitch while centering
	strandGapM     = 4.5       // bundle.DefaultParams().StrandGap — distinct tracks

	// bundle tracing — the centerline walk
	traceStepM      = 10.0    // march pitch
	traceReachM     = 40.0    // section half-reach: how wide a bundle to average
	traceParallel   = 0.80    // a track counts as bundle within ~37 deg
	traceCenterGain = 0.6     // damping on the lateral correction — no hunting
	traceTurnGain   = 0.35    // heading easing toward the steel's own tangent
	traceArriveM    = 45.0    // close enough to call it arrival at a node
	traceMaxM       = 12000.0 // runaway guard
	traceMinM       = 60.0    // shorter than this is furniture, not a corridor
	traceDupM       = 22.0    // ink this close counts as already drawn
	traceDupFrac    = 0.75    // ...and this much of a trace covered = duplicate
	spineSmoothS    = 25.0    // heading low-pass sigma — rule 6, flowing geometry
	anchorSnapM     = 5.0     // crossing point → graph node tolerance
	nodeQuantM      = 0.25
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

// buildEntrances runs the boundary pass: every track is walked once
// against the region masks, membership transitions refine to exact
// outline crossings, and crossings cluster into entrance nodes.
func (ix *Index) buildEntrances(tracks []Track, eff []int) {
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
		// r.Steel is NOT built from these pieces: the walk above is
		// level-gated, so a region with members at mixed effective levels
		// would draw almost none of its own track. Build sets it from
		// every member way clipped to the ring (memberSteel).
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
		_ = anchors
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
