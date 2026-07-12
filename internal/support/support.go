// Package support implements the SUPPORT-GRAPH map construction of
// Brosi & Bast, "Large-Scale Generation of Transit Maps from OpenStreetMap
// Data" (Cartographic Journal 2024) — the documented, planet-scale-proven
// algorithm for collapsing overlapping line geometries into a shared line
// graph. See docs/CENTERLINE.md for the research trail and why it replaces
// the corridor-state machinery.
//
// Every pattern is inserted as ONE CONTINUOUS PATH and merged by
// node-sharing, so routes can never break (owner's law #1); labels ride
// along during merging, so bundling is structural (law #3); node positions
// average every merged pass, and a median-strand refinement post-pass pulls
// edges onto the exact track-bundle centerline (law #2).
package support

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Path is one input line geometry (a GTFS pattern's course).
type Path struct {
	ID    string // pattern id (unique per input path)
	Route string
	Color string
	Label string
	Type  int
	Pts   []geo.Pt
}

// Params — defaults from the paper; all exposed as atlas tuning dials.
type Params struct {
	SampleL    float64 // dense sampling step (paper: 5 m)
	MergeD     float64 // node merge radius d̂
	ConvGap    float64 // round convergence: |1 - L'/L| threshold (paper: 0.002)
	MaxRounds  int
	CreepAlpha float64 // line-creep guard (paper: sin 45°)
	SmoothD    float64 // intersection smoothing crop distance (≈ d̂)
	TurnGapN   int     // max sample-index gap for a line to CONNECT e→f at a node
	// ContractAll contracts EVERY degree-2 node regardless of label sets —
	// for the track-centerline network, where input identity (arbitrary OSM
	// strand splits) must not fragment the merged geometry; nodes belong
	// only at physical forks.
	ContractAll bool
}

func DefaultParams() Params {
	return Params{
		SampleL:    5.0,
		MergeD:     17.5,
		ConvGap:    0.002,
		MaxRounds:  8,
		CreepAlpha: math.Sin(math.Pi / 4),
		SmoothD:    17.5,
		TurnGapN:   12,
	}
}

// Graph is the free line graph: edges labelled with the patterns (and
// routes) travelling through them, meeting exactly at nodes.
type Graph struct {
	Nodes []Node
	Edges []*Edge
	Paths []Path // the inputs, for turn checks and continuity gates
}

type Node struct {
	At  geo.Pt
	Adj []int // edge indices
}

// Edge is one merged segment. Occupancy maps pattern id → the interval of
// that pattern's sample indices that merged into this edge (interval
// adjacency at shared nodes = the turn-restriction test).
type Edge struct {
	From, To  int
	Pts       []geo.Pt
	Occupancy map[string]Interval
}

type Interval struct{ Lo, Hi int }

func (e *Edge) Line() *geo.Line { return geo.NewLine(e.Pts) }

// ---- construction ---------------------------------------------------------

type builder struct {
	p     Params
	cell  float64
	grid  map[[2]int][]int32
	pos   []geo.Pt
	alive []bool
	// mini-segment adjacency: seg = (a,b, occupancy)
	segs   map[[2]int32]map[string]Interval
	nAdded int
}

func newBuilder(p Params) *builder {
	return &builder{
		p: p, cell: p.MergeD,
		grid: map[[2]int][]int32{},
		segs: map[[2]int32]map[string]Interval{},
	}
}

func (b *builder) key(pt geo.Pt) [2]int {
	return [2]int{int(math.Floor(pt.X / b.cell)), int(math.Floor(pt.Y / b.cell))}
}

func (b *builder) addNode(pt geo.Pt) int32 {
	id := int32(len(b.pos))
	b.pos = append(b.pos, pt)
	b.alive = append(b.alive, true)
	b.grid[b.key(pt)] = append(b.grid[b.key(pt)], id)
	return id
}

// nearest returns the nearest alive node within MergeD, excluding blocked.
func (b *builder) nearest(pt geo.Pt, blocked map[int32]bool) int32 {
	k := b.key(pt)
	best, bestD := int32(-1), b.p.MergeD
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			for _, id := range b.grid[[2]int{k[0] + dx, k[1] + dy}] {
				if !b.alive[id] || blocked[id] {
					continue
				}
				if d := b.pos[id].Dist(pt); d < bestD {
					best, bestD = id, d
				}
			}
		}
	}
	return best
}

func (b *builder) moveNode(id int32, to geo.Pt) {
	old := b.key(b.pos[id])
	b.pos[id] = to
	nk := b.key(to)
	if nk != old {
		lst := b.grid[old]
		for i, v := range lst {
			if v == id {
				b.grid[old] = append(lst[:i], lst[i+1:]...)
				break
			}
		}
		b.grid[nk] = append(b.grid[nk], id)
	}
}

func segKey(a, c int32) [2]int32 {
	if a > c {
		a, c = c, a
	}
	return [2]int32{a, c}
}

func (b *builder) addSeg(a, c int32, path string, sample int) {
	if a == c {
		return
	}
	k := segKey(a, c)
	m := b.segs[k]
	if m == nil {
		m = map[string]Interval{}
		b.segs[k] = m
	}
	iv, ok := m[path]
	if !ok {
		m[path] = Interval{sample, sample}
	} else {
		if sample < iv.Lo {
			iv.Lo = sample
		}
		if sample > iv.Hi {
			iv.Hi = sample
		}
		m[path] = iv
	}
}

// input is one polyline to insert, carrying the occupancy provenance of
// everything already merged into it — labels flow THROUGH construction
// rounds (paper: L(f) = L(f) ∪ L(e)); nothing is ever re-attributed after
// the fact (post-hoc nearest-edge attribution reintroduces flicker).
type input struct {
	pts []geo.Pt
	occ map[string]Interval
}

// insert one polyline into the growing graph (paper: Figure 6).
func (b *builder) insert(in input) {
	samples := geo.NewLine(in.pts).Densify(b.p.SampleL)
	if len(samples) < 2 {
		return
	}
	p1 := samples[0]
	pl := samples[len(samples)-1]
	n := len(samples) - 1
	blockN := int(b.p.MergeD/b.p.SampleL) + 1
	var recent []int32
	blocked := map[int32]bool{}
	var prev int32 = -1
	for k, pt := range samples {
		v := b.nearest(pt, blocked)
		if v >= 0 {
			// line-creep guard: refuse merges that are farther than the
			// edge's own endpoints by factor α (obtuse interlacing fix).
			// NEVER applied within a merge radius of the edge's own ends —
			// there dist(pk,p1)≈0 blocks EVERYTHING and every input spawns
			// fresh coincident nodes at its endpoints, shattering the graph
			// (a 105-component Z train taught us this).
			d1, dl := pt.Dist(p1), pt.Dist(pl)
			if d1 > b.p.MergeD && dl > b.p.MergeD {
				dv := b.pos[v].Dist(pt)
				if b.p.CreepAlpha*d1 <= dv || b.p.CreepAlpha*dl <= dv {
					v = -1
				}
			}
		}
		if v >= 0 {
			b.moveNode(v, geo.Lerp(b.pos[v], pt, 0.5))
		} else {
			v = b.addNode(pt)
		}
		if prev >= 0 && prev != v {
			frac := float64(k) / float64(n)
			for pid, iv := range in.occ {
				idx := iv.Lo + int(float64(iv.Hi-iv.Lo)*frac)
				b.addSeg(prev, v, pid, idx)
			}
		}
		prev = v
		recent = append(recent, v)
		blocked[v] = true
		if len(recent) > blockN {
			delete(blocked, recent[0])
			recent = recent[1:]
		}
	}
}

// Build runs repeated construction rounds until convergence, then contracts
// chains, smooths intersections, and returns the free line graph.
func Build(paths []Path, p Params, logf func(string, ...any)) *Graph {
	// longest-first insertion (paper)
	sorted := append([]Path(nil), paths...)
	sort.Slice(sorted, func(i, j int) bool {
		return geo.NewLine(sorted[i].Pts).Len() > geo.NewLine(sorted[j].Pts).Len()
	})
	inputLen := 0.0
	for _, pa := range sorted {
		inputLen += geo.NewLine(pa.Pts).Len()
	}

	var g *Graph
	prevLen := inputLen
	var cur []input
	for _, pa := range sorted {
		nSamples := len(geo.NewLine(pa.Pts).Densify(p.SampleL)) - 1
		cur = append(cur, input{
			pts: pa.Pts,
			occ: map[string]Interval{pa.ID: {0, nSamples}},
		})
	}
	for round := 0; round < p.MaxRounds; round++ {
		b := newBuilder(p)
		for _, in := range cur {
			b.insert(in)
		}
		g = b.finish(paths)
		g.contractArtifacts(p)
		totalLen := 0.0
		for _, e := range g.Edges {
			totalLen += e.Line().Len()
		}
		gap := math.Abs(1 - totalLen/prevLen)
		if logf != nil {
			logf("support: round %d — %d nodes, %d edges, %.1f km (gap %.4f)",
				round+1, len(g.Nodes), len(g.Edges), totalLen/1000, gap)
		}
		if gap < p.ConvGap {
			break
		}
		prevLen = totalLen
		// next round: the merged edges become the inputs, occupancy intact
		cur = nil
		for _, e := range g.Edges {
			cur = append(cur, input{pts: e.Pts, occ: e.Occupancy})
		}
	}
	g.smoothIntersections(p)
	return g
}

// finish converts builder state into a contracted graph.
func (b *builder) finish(originals []Path) *Graph {
	// adjacency
	adj := map[int32][][2]int32{}
	for k := range b.segs {
		adj[k[0]] = append(adj[k[0]], k)
		adj[k[1]] = append(adj[k[1]], k)
	}
	g := &Graph{Paths: originals}
	// walk maximal chains through degree-2 nodes with EQUAL occupancy keys
	visited := map[[2]int32]bool{}
	nodeID := map[int32]int{}
	getNode := func(v int32) int {
		if id, ok := nodeID[v]; ok {
			return id
		}
		id := len(g.Nodes)
		nodeID[v] = id
		g.Nodes = append(g.Nodes, Node{At: b.pos[v]})
		return id
	}
	// contraction compares ROUTE sets (paper: "matching lines"), never raw
	// pattern-interval keys — a single sampling dropout on one pattern must
	// not block contraction (it bead-strings the graph with micro-edges)
	routeOf := func(pid string) string {
		for i := 0; i < len(pid); i++ {
			if pid[i] == '|' {
				return pid[:i]
			}
		}
		return pid
	}
	routeSet := func(m map[string]Interval) map[string]bool {
		s := map[string]bool{}
		for pid := range m {
			s[routeOf(pid)] = true
		}
		return s
	}
	sameLines := func(a, c [2]int32) bool {
		if b.p.ContractAll {
			return true
		}
		ra, rc := routeSet(b.segs[a]), routeSet(b.segs[c])
		if len(ra) != len(rc) {
			return false
		}
		for k := range ra {
			if !rc[k] {
				return false
			}
		}
		return true
	}
	nextThrough := func(v int32, from [2]int32) ([2]int32, bool) {
		ks := adj[v]
		if len(ks) != 2 {
			return [2]int32{}, false
		}
		var other [2]int32
		if ks[0] == from {
			other = ks[1]
		} else {
			other = ks[0]
		}
		if !sameLines(from, other) {
			return [2]int32{}, false
		}
		return other, true
	}
	otherEnd := func(k [2]int32, v int32) int32 {
		if k[0] == v {
			return k[1]
		}
		return k[0]
	}
	for k := range b.segs {
		if visited[k] {
			continue
		}
		// walk backward to chain start
		start, sk := k[0], k
		for {
			prevSeg, ok := nextThrough(start, sk)
			if !ok || visited[prevSeg] {
				break
			}
			sk = prevSeg
			start = otherEnd(sk, start)
			if sk == k {
				break // ring
			}
		}
		// walk forward collecting
		occ := map[string]Interval{}
		pts := []geo.Pt{b.pos[start]}
		v := start
		seg := sk
		for {
			if visited[seg] {
				break
			}
			visited[seg] = true
			for pid, iv := range b.segs[seg] {
				cur, ok := occ[pid]
				if !ok {
					occ[pid] = iv
				} else {
					if iv.Lo < cur.Lo {
						cur.Lo = iv.Lo
					}
					if iv.Hi > cur.Hi {
						cur.Hi = iv.Hi
					}
					occ[pid] = cur
				}
			}
			v = otherEnd(seg, v)
			pts = append(pts, b.pos[v])
			nxt, ok := nextThrough(v, seg)
			if !ok {
				break
			}
			seg = nxt
		}
		e := &Edge{From: getNode(start), To: getNode(v), Pts: pts, Occupancy: occ}
		g.Edges = append(g.Edges, e)
	}
	g.rebuildAdj()
	return g
}

func (g *Graph) rebuildAdj() {
	for i := range g.Nodes {
		g.Nodes[i].Adj = nil
	}
	for ei, e := range g.Edges {
		if e == nil {
			continue
		}
		g.Nodes[e.From].Adj = append(g.Nodes[e.From].Adj, ei)
		if e.To != e.From {
			g.Nodes[e.To].Adj = append(g.Nodes[e.To].Adj, ei)
		}
	}
}

// contractArtifacts removes edges shorter than the sampling rate that are
// shorter than their surroundings (paper: reproduced-forever artifacts).
func (g *Graph) contractArtifacts(p Params) {
	for {
		removed := false
		for ei, e := range g.Edges {
			if e == nil || e.From == e.To {
				continue
			}
			l := e.Line().Len()
			if l >= p.SampleL*1.5 {
				continue
			}
			shorterThanAdj := true
			for _, n := range []int{e.From, e.To} {
				for _, oi := range g.Nodes[n].Adj {
					if oi != ei && g.Edges[oi] != nil &&
						g.Edges[oi].Line().Len() < l {
						shorterThanAdj = false
					}
				}
			}
			if !shorterThanAdj {
				continue
			}
			// merge To into From at midpoint
			mid := geo.Lerp(e.Pts[0], e.Pts[len(e.Pts)-1], 0.5)
			from, to := e.From, e.To
			g.Nodes[from].At = mid
			for oi, o := range g.Edges {
				if o == nil || oi == ei {
					continue
				}
				if o.From == to {
					o.From = from
					o.Pts[0] = mid
				}
				if o.To == to {
					o.To = from
					o.Pts[len(o.Pts)-1] = mid
				}
			}
			g.Edges[ei] = nil
			removed = true
		}
		if !removed {
			break
		}
	}
	var kept []*Edge
	for _, e := range g.Edges {
		if e != nil && len(e.Pts) >= 2 {
			kept = append(kept, e)
		}
	}
	g.Edges = kept
	g.rebuildAdj()
}

// smoothIntersections implements the paper's node-area cleanup: crop each
// adjacent edge at distance d̂ from the node, move the node to the average
// of the crop points, reconnect (Figure 8 — "centerline thrown at
// junctions" fix).
func (g *Graph) smoothIntersections(p Params) {
	for ni := range g.Nodes {
		n := &g.Nodes[ni]
		if len(n.Adj) < 3 {
			continue
		}
		type cropped struct {
			ei   int
			atA  bool
			pts  []geo.Pt
			end  geo.Pt
		}
		var crops []cropped
		var sum geo.Pt
		for _, ei := range n.Adj {
			e := g.Edges[ei]
			l := e.Line()
			if l.Len() < p.SmoothD*1.5 {
				continue // short junction furniture: leave
			}
			atA := e.From == ni
			var sub *geo.Line
			if atA {
				sub = subLine(l, p.SmoothD, l.Len())
			} else {
				sub = subLine(l, 0, l.Len()-p.SmoothD)
			}
			var end geo.Pt
			if atA {
				end = sub.Pts[0]
			} else {
				end = sub.Pts[len(sub.Pts)-1]
			}
			crops = append(crops, cropped{ei, atA, sub.Pts, end})
			sum = sum.Add(end)
		}
		if len(crops) < 2 {
			continue
		}
		avg := sum.Scale(1 / float64(len(crops)))
		n.At = avg
		for _, c := range crops {
			e := g.Edges[c.ei]
			if c.atA {
				e.Pts = append([]geo.Pt{avg}, c.pts...)
			} else {
				e.Pts = append(append([]geo.Pt(nil), c.pts...), avg)
			}
		}
		// short edges at this node keep endpoints pinned to the moved node
		for _, ei := range n.Adj {
			e := g.Edges[ei]
			if e.From == ni {
				e.Pts[0] = avg
			}
			if e.To == ni {
				e.Pts[len(e.Pts)-1] = avg
			}
		}
	}
}

func subLine(l *geo.Line, from, to float64) *geo.Line {
	if from > to {
		from, to = to, from
	}
	from = math.Max(0, from)
	to = math.Min(l.Len(), to)
	pts := []geo.Pt{l.AtArc(from)}
	arc := 0.0
	for i := 1; i < len(l.Pts); i++ {
		arc += l.Pts[i].Dist(l.Pts[i-1])
		if arc > from && arc < to {
			pts = append(pts, l.Pts[i])
		}
	}
	pts = append(pts, l.AtArc(to))
	return geo.NewLine(pts)
}

// PruneStubs iteratively removes dead-end edges shorter than minLen (spur
// tracks and merge residue; genuine terminals are longer), then re-contracts
// pass-through nodes so the network stays clean.
func (g *Graph) PruneStubs(minLen float64) {
	for {
		removed := false
		deg := make([]int, len(g.Nodes))
		for _, e := range g.Edges {
			if e == nil {
				continue
			}
			deg[e.From]++
			if e.To != e.From {
				deg[e.To]++
			}
		}
		for ei, e := range g.Edges {
			if e == nil || e.From == e.To {
				continue
			}
			if (deg[e.From] == 1 || deg[e.To] == 1) && e.Line().Len() < minLen {
				g.Edges[ei] = nil
				removed = true
			}
		}
		if !removed {
			break
		}
	}
	var kept []*Edge
	for _, e := range g.Edges {
		if e != nil {
			kept = append(kept, e)
		}
	}
	g.Edges = kept
	g.rebuildAdj()
	g.contractDeg2()
}

// contractJunctionSlivers merges edges shorter than maxLen whose BOTH
// endpoints are forks (degree ≥3) — near-coincident junction nodes.
func (g *Graph) contractJunctionSlivers(maxLen float64) {
	for {
		g.rebuildAdj()
		deg := make([]int, len(g.Nodes))
		for _, e := range g.Edges {
			if e == nil {
				continue
			}
			deg[e.From]++
			if e.To != e.From {
				deg[e.To]++
			}
		}
		merged := false
		for ei, e := range g.Edges {
			if e == nil || e.From == e.To {
				continue
			}
			if e.Line().Len() >= maxLen || deg[e.From] < 3 || deg[e.To] < 3 {
				continue
			}
			mid := geo.Lerp(e.Pts[0], e.Pts[len(e.Pts)-1], 0.5)
			from, to := e.From, e.To
			g.Nodes[from].At = mid
			for oi, o := range g.Edges {
				if o == nil || oi == ei {
					continue
				}
				if o.From == to {
					o.From = from
					o.Pts[0] = mid
				} else if o.From == from {
					o.Pts[0] = mid
				}
				if o.To == to {
					o.To = from
					o.Pts[len(o.Pts)-1] = mid
				} else if o.To == from {
					o.Pts[len(o.Pts)-1] = mid
				}
			}
			g.Edges[ei] = nil
			merged = true
			break
		}
		if !merged {
			break
		}
	}
	var kept []*Edge
	for _, e := range g.Edges {
		if e != nil && len(e.Pts) >= 2 {
			kept = append(kept, e)
		}
	}
	g.Edges = kept
	g.rebuildAdj()
}

// contractDeg2 merges edge pairs through pass-through nodes (geometry
// concat, occupancy union).
func (g *Graph) contractDeg2() {
	for {
		g.rebuildAdj()
		merged := false
		for ni := range g.Nodes {
			if len(g.Nodes[ni].Adj) != 2 {
				continue
			}
			ai, bi := g.Nodes[ni].Adj[0], g.Nodes[ni].Adj[1]
			if ai == bi {
				continue
			}
			a, b := g.Edges[ai], g.Edges[bi]
			if a == nil || b == nil || a.From == a.To || b.From == b.To {
				continue
			}
			// orient a to END at ni, b to START at ni
			apts := append([]geo.Pt(nil), a.Pts...)
			if a.From == ni {
				apts = reversePts(apts)
			}
			bpts := append([]geo.Pt(nil), b.Pts...)
			if b.To == ni {
				bpts = reversePts(bpts)
			}
			occ := map[string]Interval{}
			for pid, iv := range a.Occupancy {
				occ[pid] = iv
			}
			for pid, iv := range b.Occupancy {
				if cur, ok := occ[pid]; ok {
					if iv.Lo < cur.Lo {
						cur.Lo = iv.Lo
					}
					if iv.Hi > cur.Hi {
						cur.Hi = iv.Hi
					}
					occ[pid] = cur
				} else {
					occ[pid] = iv
				}
			}
			var from, to int
			if a.From == ni {
				from = a.To
			} else {
				from = a.From
			}
			if b.To == ni {
				to = b.From
			} else {
				to = b.To
			}
			g.Edges[ai] = &Edge{From: from, To: to,
				Pts: append(apts, bpts[1:]...), Occupancy: occ}
			g.Edges[bi] = nil
			var kept []*Edge
			for _, e := range g.Edges {
				if e != nil {
					kept = append(kept, e)
				}
			}
			g.Edges = kept
			merged = true
			break
		}
		if !merged {
			break
		}
	}
	g.rebuildAdj()
}

func reversePts(pts []geo.Pt) []geo.Pt {
	out := make([]geo.Pt, len(pts))
	for i, p := range pts {
		out[len(pts)-1-i] = p
	}
	return out
}

// TrackCenterlines merges physical track strands into the VISUAL bundle
// centerline network — the single smooth line each track group appears as
// when zoomed out. Nodes fall only at physical forks; spur stubs pruned.
func TrackCenterlines(strandPaths []Path, p Params, pruneLen float64,
	logf func(string, ...any)) *Graph {
	p.ContractAll = true
	g := Build(strandPaths, p, logf)
	g.PruneStubs(pruneLen)
	// NOTE: naive junction-sliver contraction (merge short fork-fork edges
	// to a midpoint) folds neighboring geometry into 180° hairpins — the
	// triangle-junction fix needs node-front consolidation instead
	// (Brosi Fig 8/18); tracked, not shipped
	// pruning + re-contraction create concat joints the paper's
	// intersection smoothing never saw — smooth the node areas again, then
	// low-pass each edge (endpoints pinned): "smooth and consistent"
	g.smoothIntersections(p)
	for _, e := range g.Edges {
		if e.Line().Len() < 30 {
			continue
		}
		e.Pts = geo.GaussianArc(geo.NewLine(e.Pts).Densify(8), 8)
	}
	return g
}

// Connects reports whether path pid genuinely continues from edge e to edge
// f at their shared node: its occupancy intervals must be ADJACENT in the
// path's own sample order (the turn-restriction test — a line whose
// original course does not pass through the node never connects there;
// Chicago-Loop rule).
func (g *Graph) Connects(pid string, e, f *Edge, turnGapN int) bool {
	a, ok1 := e.Occupancy[pid]
	c, ok2 := f.Occupancy[pid]
	if !ok1 || !ok2 {
		return false
	}
	gap := a.Lo - c.Hi
	if c.Lo > a.Hi {
		gap = c.Lo - a.Hi
	}
	if gap < 0 {
		gap = 0
	}
	return gap <= turnGapN
}
