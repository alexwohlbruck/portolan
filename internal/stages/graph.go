package stages

import (
	"fmt"
	"math"
	"sync"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
)

// trackGraph is the welded OSM way graph that MATCH walks. Nodes exist where
// way endpoints coincide or where a way endpoint touches another way's
// interior vertex — OSM switches share that vertex, so the touched way is
// split there. Edges are way pieces between nodes; every piece appears as two
// directed edges.
type trackGraph struct {
	nodes   []tgNode
	edges   []tgEdge // directed; edges[2p] and edges[2p+1] are piece p
	pieces  []*geo.Line
	grid    *geo.Grid   // indexes pieces (line index == piece id)
	turn    [][]float64 // turnDeg per directed edge, aligned with To-node's Out
	isXover []bool      // crossoverWays[edge.Way], snapshotted at build
	isYard  []bool      // tagged yard steel, snapshotted at build
	lvl     []int       // wayLevels[edge.Way], snapshotted at build
}

type tgNode struct {
	At  geo.Pt
	Out []int // directed edges leaving this node
}

type tgEdge struct {
	Way   string // source OSM way id
	Piece int
	From  int
	To    int
	Line  *geo.Line // oriented From→To
}

func (g *trackGraph) rev(e int) int { return e ^ 1 }

const weldEps = 0.6 // m; OSM shared nodes survive GeoJSON at cm precision

// layerOf: rails, streets and seaways are disjoint physical layers — a
// road crossing a subway grate in plan view is not a junction, and a
// ferry lane hugging a bridge is not a track. Cross-layer welds grafted
// bus street nodes onto subway junctions and changed their cut and
// adjacency structure (the N·Q drawn break at 57th appeared the moment
// NYC buses landed). No weld, split, or snap crosses a layer line.
func layerOf(id string) string {
	switch wayRailClass[id] {
	case "street":
		return "street"
	case "seaway":
		return "seaway"
	}
	return "rail"
}

// Match and Split build the graph from the same tracks slice and already
// rely on the two builds being identical (shared piece ids) — build once.
// Keyed on slice identity: any other slice rebuilds.
var (
	graphCacheMu sync.Mutex
	cachedTracks []bundle.Track
	cachedGraph  *trackGraph
)

func buildTrackGraphCached(tracks []bundle.Track) *trackGraph {
	graphCacheMu.Lock()
	defer graphCacheMu.Unlock()
	if cachedGraph != nil && len(tracks) > 0 && len(tracks) == len(cachedTracks) &&
		&tracks[0] == &cachedTracks[0] {
		return cachedGraph
	}
	g := buildTrackGraph(tracks)
	cachedTracks, cachedGraph = tracks, g
	return g
}

// buildTrackGraph welds ways into a routable graph. Deterministic for a given
// track slice — MATCH and SPLIT rebuild the same graph and share piece ids.
func buildTrackGraph(tracks []bundle.Track) *trackGraph {
	tracks = snapEndpoints(tracks)
	cell := 4.0
	key := func(p geo.Pt) [2]int {
		return [2]int{int(math.Floor(p.X / cell)), int(math.Floor(p.Y / cell))}
	}
	// index every way endpoint
	type endRef struct{ way, end int }
	endBuckets := map[[2]int][]endRef{}
	endPt := func(r endRef) geo.Pt {
		pts := tracks[r.way].Line.Pts
		if r.end == 0 {
			return pts[0]
		}
		return pts[len(pts)-1]
	}
	for wi, t := range tracks {
		if len(t.Line.Pts) < 2 {
			continue
		}
		for e := 0; e <= 1; e++ {
			r := endRef{wi, e}
			endBuckets[key(endPt(r))] = append(endBuckets[key(endPt(r))], r)
		}
	}
	anyEndNear := func(p geo.Pt, notWay int) bool {
		k := key(p)
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, r := range endBuckets[[2]int{k[0] + dx, k[1] + dy}] {
					if r.way != notWay && endPt(r).Dist(p) <= weldEps &&
						layerOf(tracks[r.way].ID) == layerOf(tracks[notWay].ID) {
						return true
					}
				}
			}
		}
		return false
	}

	// split ways at interior vertices touched by another way's endpoint
	g := &trackGraph{}
	type rawPiece struct {
		way string
		pts []geo.Pt
	}
	var raw []rawPiece
	for wi, t := range tracks {
		pts := t.Line.Pts
		if len(pts) < 2 {
			continue
		}
		start := 0
		for vi := 1; vi < len(pts)-1; vi++ {
			if anyEndNear(pts[vi], wi) {
				raw = append(raw, rawPiece{t.ID, pts[start : vi+1]})
				start = vi
			}
		}
		raw = append(raw, rawPiece{t.ID, pts[start:]})
	}

	// weld piece endpoints into nodes
	nodeBuckets := map[[2]int][]int{}
	nodeLayer := []string{}
	nodeOf := func(p geo.Pt, layer string) int {
		k := key(p)
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, ni := range nodeBuckets[[2]int{k[0] + dx, k[1] + dy}] {
					if nodeLayer[ni] == layer && g.nodes[ni].At.Dist(p) <= weldEps {
						return ni
					}
				}
			}
		}
		g.nodes = append(g.nodes, tgNode{At: p})
		nodeLayer = append(nodeLayer, layer)
		ni := len(g.nodes) - 1
		nodeBuckets[key(p)] = append(nodeBuckets[key(p)], ni)
		return ni
	}
	for _, rp := range raw {
		line := geo.NewLine(rp.pts)
		if line.Len() < 1e-6 {
			continue
		}
		from := nodeOf(rp.pts[0], layerOf(rp.way))
		to := nodeOf(rp.pts[len(rp.pts)-1], layerOf(rp.way))
		p := len(g.pieces)
		g.pieces = append(g.pieces, line)
		fwd := tgEdge{Way: rp.way, Piece: p, From: from, To: to, Line: line}
		bwd := tgEdge{Way: rp.way, Piece: p, From: to, To: from,
			Line: geo.NewLine(reversedPts(rp.pts))}
		g.edges = append(g.edges, fwd, bwd)
		g.nodes[from].Out = append(g.nodes[from].Out, 2*p)
		g.nodes[to].Out = append(g.nodes[to].Out, 2*p+1)
	}
	g.healBreaks()
	g.grid = geo.NewGrid(g.pieces, 64)
	// walk() tables: per-expansion turn angles and way-class lookups are
	// pure functions of immutable graph geometry and the (already set)
	// crossover/level registries — computed once, identical values
	g.turn = make([][]float64, len(g.edges))
	g.isXover = make([]bool, len(g.edges))
	g.isYard = make([]bool, len(g.edges))
	g.lvl = make([]int, len(g.edges))
	for e := range g.edges {
		g.isXover[e] = crossoverWays[g.edges[e].Way]
		g.isYard[e] = isYardWay(g.edges[e].Way)
		g.lvl[e] = wayLevels[g.edges[e].Way]
		outs := g.nodes[g.edges[e].To].Out
		t := make([]float64, len(outs))
		for i, b := range outs {
			t[i] = g.turnDeg(e, b)
		}
		g.turn[e] = t
	}
	return g
}

// snapEndpoints fixes the weld class the vertex-touch machinery cannot see:
// a way ENDING ON another way's segment BETWEEN vertices (OSM switches drawn
// without a shared node — the whole Chrystie St connection and the Manhattan
// Bridge approach hung off the main graph by 0.4–1.2 m of exactly this).
// Two phases, both requiring tangent alignment (a junction continues a
// direction; a different-layer crossing meets at an angle):
//   - tight (1.5 m, unconditional): below any real track spacing, so a snap
//     this close can only be a drawn-apart junction;
//   - far (6 m, DIFFERENT components only): parallel tracks in one corridor
//     sit 3–5 m apart but share a component through their real junctions, so
//     the component gate leaves them alone while portal-scale misses (149 St
//     concourse: 3.4 m) reconnect.
//
// Snapped endpoints move exactly onto a vertex of the target way (inserted
// at the projection when none exists), so the ordinary weld picks them up.
// Pure function: input tracks are not mutated.
func snapEndpoints(tracks []bundle.Track) []bundle.Track {
	const (
		snapNear = 1.5
		snapFar  = 6.0
		alignDot = 0.7
	)
	pts := make([][]geo.Pt, len(tracks))
	lines := make([]*geo.Line, len(tracks))
	for i, t := range tracks {
		pts[i] = append([]geo.Pt(nil), t.Line.Pts...)
		lines[i] = t.Line
	}
	grid := geo.NewGrid(lines, 64) // geometry of a way never moves; only
	// vertices are inserted — the original-line grid stays valid for lookup

	// project p onto way wi (current pts); returns segment idx, point, dist
	project := func(wi int, p geo.Pt) (int, geo.Pt, float64) {
		best := math.Inf(1)
		bi, bq := -1, geo.Pt{}
		w := pts[wi]
		for si := 0; si+1 < len(w); si++ {
			a, b := w[si], w[si+1]
			v := b.Sub(a)
			l2 := v.Dot(v)
			t := 0.0
			if l2 > 0 {
				t = math.Max(0, math.Min(1, p.Sub(a).Dot(v)/l2))
			}
			q := geo.Pt{X: a.X + t*v.X, Y: a.Y + t*v.Y}
			if d := p.Dist(q); d < best {
				best, bi, bq = d, si, q
			}
		}
		return bi, bq, best
	}
	endTangent := func(wi, end int) geo.Pt {
		w := pts[wi]
		if end == 0 {
			return w[intMin(len(w)-1, 3)].Sub(w[0]).Unit()
		}
		return w[len(w)-1].Sub(w[intMax(0, len(w)-4)]).Unit()
	}
	segTangent := func(wi, si int) geo.Pt {
		return pts[wi][si+1].Sub(pts[wi][si]).Unit()
	}

	// union-find over ways, seeded by the welds the builder already makes:
	// endpoint within weldEps of another way's vertex
	parent := make([]int, len(tracks))
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
	vcell := 4.0
	vkey := func(p geo.Pt) [2]int {
		return [2]int{int(math.Floor(p.X / vcell)), int(math.Floor(p.Y / vcell))}
	}
	type vref struct{ way, vi int }
	vbuckets := map[[2]int][]vref{}
	for wi, w := range pts {
		for vi, p := range w {
			vbuckets[vkey(p)] = append(vbuckets[vkey(p)], vref{wi, vi})
		}
	}
	eachVertexNear := func(p geo.Pt, r float64, fn func(vref)) {
		k := vkey(p)
		reach := int(math.Ceil(r / vcell))
		for dx := -reach; dx <= reach; dx++ {
			for dy := -reach; dy <= reach; dy++ {
				for _, v := range vbuckets[[2]int{k[0] + dx, k[1] + dy}] {
					if pts[v.way][v.vi].Dist(p) <= r {
						fn(v)
					}
				}
			}
		}
	}
	for wi, w := range pts {
		if len(w) < 2 {
			continue
		}
		for _, p := range []geo.Pt{w[0], w[len(w)-1]} {
			eachVertexNear(p, weldEps, func(v vref) {
				if v.way != wi &&
					layerOf(tracks[v.way].ID) == layerOf(tracks[wi].ID) {
					parent[find(wi)] = find(v.way)
				}
			})
		}
	}

	// snap: move endpoint (wi, end) onto way tw at segment si / point q
	snapTo := func(wi, end, tw, si int, q geo.Pt) {
		w := pts[tw]
		// reuse a target vertex when the projection basically is one
		if q.Dist(w[si]) <= weldEps {
			q = w[si]
		} else if q.Dist(w[si+1]) <= weldEps {
			q = w[si+1]
		} else {
			nw := make([]geo.Pt, 0, len(w)+1)
			nw = append(nw, w[:si+1]...)
			nw = append(nw, q)
			nw = append(nw, w[si+1:]...)
			pts[tw] = nw
			vbuckets[vkey(q)] = append(vbuckets[vkey(q)], vref{tw, si + 1})
		}
		if end == 0 {
			pts[wi][0] = q
		} else {
			pts[wi][len(pts[wi])-1] = q
		}
		parent[find(wi)] = find(tw)
	}

	for pass, tol := range []float64{snapNear, snapFar} {
		for wi := range pts {
			if len(pts[wi]) < 2 {
				continue
			}
			for end := 0; end <= 1; end++ {
				p := pts[wi][0]
				if end == 1 {
					p = pts[wi][len(pts[wi])-1]
				}
				// already welded to something here?
				welded := false
				eachVertexNear(p, weldEps, func(v vref) {
					if v.way != wi {
						welded = true
					}
				})
				if welded {
					continue
				}
				et := endTangent(wi, end)
				bestD := math.Inf(1)
				bw, bsi, bq := -1, -1, geo.Pt{}
				grid.Near(p, tol, func(cand int) {
					if cand == wi || len(pts[cand]) < 2 ||
						layerOf(tracks[cand].ID) != layerOf(tracks[wi].ID) {
						return
					}
					if pass == 1 && find(cand) == find(wi) {
						return // far snaps only bridge components
					}
					si, q, d := project(cand, p)
					if si < 0 || d > tol || d >= bestD {
						return
					}
					if math.Abs(et.Dot(segTangent(cand, si))) < alignDot {
						return
					}
					bestD, bw, bsi, bq = d, cand, si, q
				})
				if bw >= 0 {
					snapTo(wi, end, bw, bsi, bq)
				}
			}
		}
	}

	out := make([]bundle.Track, len(tracks))
	for i, t := range tracks {
		out[i] = bundle.Track{ID: t.ID, Line: geo.NewLine(pts[i])}
	}
	return out
}

func intMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func intMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// healBreaks welds micro-breaks: OSM sometimes leaves a track's continuation
// a few meters short of the next way (Chambers St: 3.7 m). Two degree-1
// nodes weld when they are within healEps and their track ends FACE each
// other (a continuation). Parallel station-throat track ends do not face
// each other and never weld.
func (g *trackGraph) healBreaks() {
	const healEps = 5.0
	merged := map[int]bool{}
	var deg1 []int
	for ni, n := range g.nodes {
		if len(n.Out) == 1 {
			deg1 = append(deg1, ni)
		}
	}
	for i, a := range deg1 {
		if merged[a] {
			continue
		}
		for _, b := range deg1[i+1:] {
			if merged[b] || merged[a] {
				break
			}
			if g.nodes[a].At.Dist(g.nodes[b].At) > healEps {
				continue
			}
			ea, eb := g.nodes[a].Out[0], g.nodes[b].Out[0]
			if g.edges[ea].Piece == g.edges[eb].Piece {
				continue // a tiny isolated stub, not a break
			}
			// arriving direction at a vs departing direction from b
			if g.inTangent(g.rev(ea), 12).Dot(g.outTangent(eb, 12)) < 0.7 {
				continue
			}
			for ei := range g.edges {
				if g.edges[ei].From == b {
					g.edges[ei].From = a
				}
				if g.edges[ei].To == b {
					g.edges[ei].To = a
				}
			}
			g.nodes[a].Out = append(g.nodes[a].Out, g.nodes[b].Out...)
			g.nodes[b].Out = nil
			merged[a], merged[b] = true, true // both are degree-2 now
			break
		}
	}
}

func reversedPts(pts []geo.Pt) []geo.Pt {
	out := make([]geo.Pt, len(pts))
	for i, p := range pts {
		out[len(pts)-1-i] = p
	}
	return out
}

// pieceToken names a piece stably for Path.WayIDs ("wayid#piece").
func (g *trackGraph) pieceToken(piece int) string {
	return fmt.Sprintf("%s#%d", g.edges[2*piece].Way, piece)
}

// endTangent is the direction a directed edge leaves its From node (win m).
func (g *trackGraph) outTangent(e int, win float64) geo.Pt {
	l := g.edges[e].Line
	return l.AtArc(math.Min(win, l.Len())).Sub(l.Pts[0]).Unit()
}

// inTangent is the direction a directed edge arrives at its To node.
func (g *trackGraph) inTangent(e int, win float64) geo.Pt {
	l := g.edges[e].Line
	return l.Pts[len(l.Pts)-1].Sub(l.AtArc(math.Max(0, l.Len()-win))).Unit()
}

// turnDeg is the heading change from directed edge a into directed edge b at
// their shared node.
func (g *trackGraph) turnDeg(a, b int) float64 {
	ta := g.inTangent(a, 12)
	tb := g.outTangent(b, 12)
	d := math.Max(-1, math.Min(1, ta.Dot(tb)))
	return math.Acos(d) * 180 / math.Pi
}
