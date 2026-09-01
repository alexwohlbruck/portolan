package yards

import (
	"container/heap"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Yard centerlines: the corridor a hand-drawn map would run through a
// yard, one line per through route rather than one per rail.
//
// Topology and geometry are separated, because the two failed attempts
// before this one each got one of them right and the other wrong:
//
//   - A shortest path over the track graph rides whichever individual
//     rail the switches offer, so it wanders diagonally across a ladder.
//     Right topology, wrong geometry.
//   - A bundle trace — march forward, step to the middle of whatever
//     runs with you — has the right geometry but no topology: it does
//     not know which entrances it is meant to join, and independent
//     traces have to be stitched afterwards.
//
// So: the TRACK GRAPH picks which steel carries a centerline (a Steiner
// tree over the entrance nodes, which is also what makes a second route
// reuse the first one's trunk instead of drawing beside it), and the
// CROSS-SECTION centres the result laterally inside its bundle. Every
// edge in the tree is real steel, which is what lets the result
// path-match a track end to end.
const (
	ctrStepM     = 12.0 // resample pitch while centring
	ctrReachM    = 40.0 // cross-section half-reach: how wide a bundle to average
	ctrParallel  = 0.80 // |cos| for a crossing to count as part of the bundle
	ctrGain      = 0.7  // damping on the lateral correction — no hunting
	ctrTaperM    = 40.0 // the shift fades to nothing at a pinned end
	ctrPasses    = 3    // centring iterations
	ctrShiftSig  = 36.0 // low-pass on the lateral shift profile — anti-fold
	ctrSmoothSig = 24.0 // arc low-pass sigma — rule 6, flowing geometry
	ctrMinM      = 60.0 // shorter than this is furniture, not a corridor
	// foldCos: a direction change sharper than 60° over a 12 m window is
	// a JUNCTION, not a curve. Real yard track cannot do it — the tightest
	// yard radius turns about 15° over 12 m — so anything sharper came
	// from the graph, and the honest drawing is two corridors meeting
	// rather than one line with a kink in it.
	foldCos     = 0.5
	foldDedupeM = 3.0 // collapse a noisy hairpin, never a real second fold
)

// Centerline is one corridor through a yard. Ends carry the entrance
// index at each end, or -1 where the chain ends at an interior junction.
type Centerline struct {
	Pts  []geo.Pt
	Ends [2]int
}

// ---- shortest paths over the yard track graph ---------------------------

type pqItem struct {
	n int
	d float64
}
type pq []pqItem

func (q pq) Len() int           { return len(q) }
func (q pq) Less(i, j int) bool { return q[i].d < q[j].d }
func (q pq) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *pq) Push(x any)        { *q = append(*q, x.(pqItem)) }
func (q *pq) Pop() any          { old := *q; n := len(old); it := old[n-1]; *q = old[:n-1]; return it }

// dijkstra runs multi-source shortest paths, returning per node the cost
// to the nearest source and the edge it arrived by. Sources are seeded in
// sorted order so equal-cost ties resolve identically every run.
func (g *spineGraph) dijkstra(src map[int]bool) (dist []float64, via []int) {
	n := len(g.pos)
	dist = make([]float64, n)
	via = make([]int, n)
	for i := range dist {
		dist[i], via[i] = math.Inf(1), -1
	}
	srcs := make([]int, 0, len(src))
	for s := range src {
		if s >= 0 && s < n {
			srcs = append(srcs, s)
		}
	}
	sort.Ints(srcs)
	h := &pq{}
	for _, s := range srcs {
		dist[s] = 0
		*h = append(*h, pqItem{s, 0})
	}
	heap.Init(h)
	for h.Len() > 0 {
		it := heap.Pop(h).(pqItem)
		if it.d > dist[it.n]+1e-9 {
			continue
		}
		for _, ei := range g.adj[it.n] {
			e := g.edges[ei]
			o := e.a
			if o == it.n {
				o = e.b
			}
			if nd := it.d + e.cost; nd < dist[o]-1e-9 {
				dist[o], via[o] = nd, ei
				heap.Push(h, pqItem{o, nd})
			}
		}
	}
	return dist, via
}

// steinerEdges picks the steel that carries centerlines: a Prim-style
// Steiner forest over the entrance node sets. Seeded at the widest throat,
// it then repeatedly attaches the entrance CLOSEST TO WHAT IS ALREADY
// DRAWN — which is step 5 of the spec ("try to find an existing centerline
// to use; if no obvious candidate exists, draw a new path") falling out of
// the construction rather than needing a separate reuse test.
//
// A forest, not a tree: a region can hold detached groups of track, and an
// unreachable entrance seeds its own component instead of being dropped.
func steinerEdges(g *spineGraph, terms [][]int) map[int]bool {
	var live []int
	for i := range terms {
		if len(terms[i]) > 0 {
			live = append(live, i)
		}
	}
	if len(live) == 0 {
		return nil
	}
	seed := live[0]
	for _, i := range live {
		if len(terms[i]) > len(terms[seed]) {
			seed = i
		}
	}
	// Two different notions, and conflating them cost rule 4 twice over.
	// inTree is "reachable at no cost" — every rail of a connected throat,
	// so a later path can leave the yard by whichever one its component
	// happens to hold. touched is "an edge of the tree actually reaches
	// this node", which is the only thing that makes an entrance served.
	inTree, touched := map[int]bool{}, map[int]bool{}
	for _, n := range terms[seed] {
		inTree[n] = true
	}
	done := map[int]bool{seed: true}
	seeded := map[int]bool{seed: true}
	edges := map[int]bool{}

	for {
		for _, ti := range live {
			if done[ti] {
				continue
			}
			for _, n := range terms[ti] {
				if touched[n] {
					done[ti] = true
					break
				}
			}
		}
		left := 0
		for _, ti := range live {
			if !done[ti] {
				left++
			}
		}
		if left == 0 {
			break
		}
		dist, via := g.dijkstra(inTree)
		best, bestN, bestD := -1, -1, math.Inf(1)
		for _, ti := range live {
			if done[ti] {
				continue
			}
			for _, n := range terms[ti] {
				// dist 0 means "already a source but no steel reached it";
				// picking it would add no edges and silently drop the
				// entrance, which is exactly how 453 of them vanished
				if n < len(dist) && dist[n] > 1e-9 && dist[n] < bestD {
					best, bestN, bestD = ti, n, dist[n]
				}
			}
		}
		if best < 0 {
			// nothing left is connectable to the current forest: open a
			// new component at the first entrance that has not seeded one
			pick := -1
			for _, ti := range live {
				if !done[ti] {
					pick = ti
					break
				}
			}
			if pick < 0 {
				break
			}
			if seeded[pick] {
				done[pick] = true // its component holds no other entrance
				continue
			}
			seeded[pick] = true
			for _, n := range terms[pick] {
				inTree[n] = true
			}
			continue
		}
		for n := bestN; via[n] >= 0; {
			ei := via[n]
			edges[ei] = true
			e := g.edges[ei]
			o := e.a
			if o == n {
				o = e.b
			}
			touched[n], touched[o] = true, true
			inTree[n], inTree[o] = true, true
			n = o
		}
		for _, n := range terms[best] {
			inTree[n] = true
		}
		done[best] = true
	}
	return edges
}

// ---- turning the chosen steel into chains -------------------------------

type chain struct {
	pts  []geo.Pt
	a, b int // end node ids
}

// chainsOf splits the chosen edge set at every junction, dead end, and
// forced break, so each chain is a run of steel with nothing branching off
// it. Junction nodes are shared between chains, which is what keeps the
// drawn network welded once each chain is centred with its ends pinned.
//
// breaks holds the entrance anchor nodes. Without them an entrance the
// tree merely PASSES THROUGH ends no chain, and rule 4 — every entry/exit
// must have a centerline — quietly failed for 45% of them.
func chainsOf(g *spineGraph, edges map[int]bool, breaks map[int]bool) []chain {
	if len(edges) == 0 {
		return nil
	}
	eis := make([]int, 0, len(edges))
	for ei := range edges {
		eis = append(eis, ei)
	}
	sort.Ints(eis)

	deg := map[int]int{}
	for _, ei := range eis {
		e := g.edges[ei]
		deg[e.a]++
		deg[e.b]++
	}
	nodes := make([]int, 0, len(deg))
	for n := range deg {
		nodes = append(nodes, n)
	}
	sort.Ints(nodes)

	used := make(map[int]bool, len(eis))
	var out []chain
	walk := func(start, first int) {
		cur, e, prev := start, first, -1
		pts := []geo.Pt{g.pos[start]}
		for {
			used[e] = true
			ed := g.edges[e]
			seg := ed.pts
			if ed.b == cur && ed.a != cur {
				seg = reversePts(seg)
			}
			pts = append(pts, seg[1:]...)
			nxt := ed.a
			if nxt == cur {
				nxt = ed.b
			}
			prev, cur = cur, nxt
			if deg[cur] != 2 || breaks[cur] {
				break
			}
			// A corridor never retraces itself. Where two edges join the
			// same node pair — OSM maps a track twice, or a clip leaves two
			// pieces over one alignment — continuing onto the second walks
			// the first straight back, an exact 180° fold mid-chain.
			next := -1
			for _, e2 := range g.adj[cur] {
				if !edges[e2] || used[e2] {
					continue
				}
				far := g.edges[e2].a
				if far == cur {
					far = g.edges[e2].b
				}
				if far == prev {
					continue
				}
				next = e2
				break
			}
			if next < 0 {
				break
			}
			e = next
		}
		out = append(out, chain{pts: pts, a: start, b: cur})
	}
	for _, n := range nodes {
		if deg[n] == 2 && !breaks[n] {
			continue
		}
		for _, ei := range g.adj[n] {
			if edges[ei] && !used[ei] {
				walk(n, ei)
			}
		}
	}
	// anything left is a pure cycle — a balloon loop with no junction on it
	for _, ei := range eis {
		if !used[ei] {
			walk(g.edges[ei].a, ei)
		}
	}
	return splitFolds(out)
}

// splitFolds cuts a chain wherever it reverses onto itself. A corridor
// through a yard never doubles back, whatever the track graph did to get
// there — a stub the tree walked into and out of, or two nodes a
// quantisation apart carrying edges over one alignment. Left in, the
// hairpin survives centring and smoothing as a 180° spike (the smoothing
// only rounds its tip, which is what the collapsing 12 → 2 m sample
// spacing at the fold was).
func splitFolds(chs []chain) []chain {
	var out []chain
	for _, ch := range chs {
		cuts := foldCuts(ch.pts)
		if len(cuts) == 0 {
			out = append(out, ch)
			continue
		}
		bounds := append([]int{0}, cuts...)
		bounds = append(bounds, len(ch.pts)-1)
		for k := 0; k+1 < len(bounds); k++ {
			seg := ch.pts[bounds[k] : bounds[k+1]+1]
			if len(seg) < 2 {
				continue
			}
			// only the outer ends keep their graph node; a cut end is not
			// a node and must never pin to an entrance
			c := chain{pts: seg, a: -1, b: -1}
			if k == 0 {
				c.a = ch.a
			}
			if k == len(bounds)-2 {
				c.b = ch.b
			}
			out = append(out, c)
		}
	}
	return out
}

// foldCuts finds the vertices where a polyline reverses onto itself,
// comparing the direction over the WINDOW behind each vertex with the one
// ahead of it. Adjacent-segment angles miss the real case: OSM lays yard
// track at one- and two-metre vertices, so a hairpin turns through 180°
// over a dozen gentle steps that never individually look sharp — and then
// the uniform 12 m resample every gate measures on collapses it into the
// spike.
func foldCuts(pts []geo.Pt) []int {
	n := len(pts)
	if n < 3 {
		return nil
	}
	arc := make([]float64, n)
	for i := 1; i < n; i++ {
		arc[i] = arc[i-1] + pts[i].Dist(pts[i-1])
	}
	var cuts []int
	last := math.Inf(-1)
	for i := 1; i < n-1; i++ {
		back := i
		for back > 0 && arc[i]-arc[back] < ctrStepM {
			back--
		}
		fwd := i
		for fwd < n-1 && arc[fwd]-arc[i] < ctrStepM {
			fwd++
		}
		a, b := pts[i].Sub(pts[back]), pts[fwd].Sub(pts[i])
		if a.Norm() < 1e-9 || b.Norm() < 1e-9 {
			continue
		}
		if a.Unit().Dot(b.Unit()) >= foldCos {
			continue
		}
		// The dedupe window must sit WELL under the output sampling step.
		// Resample lays points at total/n, a hair under ctrStepM — so a
		// window of ctrStepM swallowed every fold after the first, and
		// chains with two reversals shipped with the second one in them.
		if arc[i]-last < foldDedupeM {
			continue // a noisy hairpin is one fold, not a run of them
		}
		cuts = append(cuts, i)
		last = arc[i]
	}
	return cuts
}

func reversePts(p []geo.Pt) []geo.Pt {
	out := make([]geo.Pt, len(p))
	for i, q := range p {
		out[len(p)-1-i] = q
	}
	return out
}

// ---- centring ----------------------------------------------------------

// centerRun slides each sample sideways onto the middle of the bundle it
// runs in: stand on the path, look along it, take a PERPENDICULAR section
// of the yard's steel (never a nearest-point projection — LESSONS #2),
// keep the crossings running roughly with us, and step to their median.
//
// The median, not the mean: a section that clips one stray siding at the
// edge of its reach should not drag the corridor towards it.
//
// Both ends are pinned and the shift tapers to nothing over ctrTaperM, so
// chains still meet exactly at the junctions they share.
func centerRun(pts []geo.Pt, steel []*geo.Line, grid *geo.Grid, ring []geo.Pt) []geo.Pt {
	if len(pts) < 3 || grid == nil {
		return pts
	}
	cur := geo.NewLine(pts).Resample(ctrStepM)
	if len(cur) < 3 {
		return pts
	}
	for pass := 0; pass < ctrPasses; pass++ {
		arc := make([]float64, len(cur))
		for i := 1; i < len(cur); i++ {
			arc[i] = arc[i-1] + cur[i].Dist(cur[i-1])
		}
		total := arc[len(arc)-1]
		tans := make([]geo.Pt, len(cur))
		shift := make([]float64, len(cur))
		var offs []float64
		for i := 1; i < len(cur)-1; i++ {
			tan := cur[i+1].Sub(cur[i-1]).Unit()
			if tan.Norm() < 0.5 {
				continue
			}
			tans[i] = tan
			offs = offs[:0]
			grid.Near(cur[i], ctrReachM, func(li int) {
				for _, c := range steel[li].CrossSection(cur[i], tan, ctrReachM) {
					if c.Parallel >= ctrParallel {
						offs = append(offs, c.Offset)
					}
				}
			})
			if len(offs) == 0 {
				continue
			}
			sort.Float64s(offs)
			med := offs[len(offs)/2]
			if len(offs)%2 == 0 {
				med = (offs[len(offs)/2-1] + offs[len(offs)/2]) / 2
			}
			shift[i] = med * ctrGain
		}
		// Low-pass the SHIFT PROFILE before applying it, not the points
		// afterwards. Where two bundles diverge, one sample's section
		// finds the left bundle and the next sample's finds the right,
		// and the raw medians jump tens of metres between neighbours 12 m
		// apart — applied directly that folds the line back on itself,
		// which is where every 179° reversal came from.
		shift = smoothScalar(shift, arc, ctrShiftSig)
		next := make([]geo.Pt, len(cur))
		copy(next, cur)
		for i := 1; i < len(cur)-1; i++ {
			if tans[i].Norm() < 0.5 {
				continue
			}
			taper := math.Min(1, math.Min(arc[i], total-arc[i])/ctrTaperM)
			s := math.Max(-ctrReachM, math.Min(ctrReachM, shift[i])) * taper
			p := cur[i].Add(tans[i].Perp().Scale(s))
			// never centre a corridor out of its own yard
			if len(ring) >= 3 && !pointInRing(ring, p) {
				continue
			}
			next[i] = p
		}
		cur = next
	}
	return geo.GaussianArc(cur, ctrSmoothSig)
}

// smoothScalar low-passes a per-sample value along the arc it was
// measured on (ends held at zero, matching the pinned taper).
func smoothScalar(v, arc []float64, sigma float64) []float64 {
	if sigma <= 0 || len(v) < 3 {
		return v
	}
	out := make([]float64, len(v))
	win := 3 * sigma
	for i := range v {
		var sw, sv float64
		for j := i; j >= 0 && arc[i]-arc[j] <= win; j-- {
			w := math.Exp(-(arc[i] - arc[j]) * (arc[i] - arc[j]) / (2 * sigma * sigma))
			sv += v[j] * w
			sw += w
		}
		for j := i + 1; j < len(v) && arc[j]-arc[i] <= win; j++ {
			w := math.Exp(-(arc[j] - arc[i]) * (arc[j] - arc[i]) / (2 * sigma * sigma))
			sv += v[j] * w
			sw += w
		}
		if sw > 0 {
			out[i] = sv / sw
		}
	}
	return out
}

// ---- assembly ----------------------------------------------------------

// buildCenterlines runs steps 1-6 of the spec for one region: choose the
// steel (Steiner forest over the entrance nodes), cut it into chains,
// centre each chain in its bundle, smooth it, and pin every chain that
// reaches an entrance to that entrance's averaged node.
func buildCenterlines(g *spineGraph, ents []Entrance, anchors [][]int,
	steel []*geo.Line, ring []geo.Pt) []Centerline {

	edges := steinerEdges(g, anchors)
	if len(edges) == 0 {
		return nil
	}
	// node → entrance, so a chain end can be pinned to the averaged
	// centrepoint of the throat rather than to whichever rail it used
	entOf := map[int]int{}
	breaks := map[int]bool{}
	for ei := range anchors {
		for _, n := range anchors[ei] {
			breaks[n] = true
			if _, taken := entOf[n]; !taken {
				entOf[n] = ei
			}
		}
	}
	var grid *geo.Grid
	if len(steel) > 0 {
		grid = geo.NewGrid(steel, 64)
	}
	var out []Centerline
	for _, ch := range chainsOf(g, edges, breaks) {
		if len(ch.pts) < 2 {
			continue
		}
		pts := centerRun(ch.pts, steel, grid, ring)
		if len(pts) < 2 {
			continue
		}
		ea, okA := entOf[ch.a]
		eb, okB := entOf[ch.b]
		if okA {
			pts = pinTo(pts, ents[ea].Pt, true)
		}
		if okB {
			pts = pinTo(pts, ents[eb].Pt, false)
		}
		// One uniform sampling for everything that leaves here. A chain
		// too short to be centred otherwise keeps OSM's native vertices,
		// and yard track is laid at sub-metre spacing: two 0.8 m spans
		// meeting at a switch read as a 177° spike that means nothing
		// (docs/LESSONS.md #20).
		if l := geo.NewLine(pts); l.Len() > ctrStepM {
			pts = l.Resample(ctrStepM)
		}
		if !okA && !okB && lineLen(pts) < ctrMinM {
			continue // an interior stub joining nothing: furniture
		}
		// Last guarantee: nothing leaves with a fold in it. Everything
		// upstream tries not to make one; this is what makes rule 6 hold
		// whatever the track graph did.
		bounds := append([]int{0}, foldCuts(pts)...)
		bounds = append(bounds, len(pts)-1)
		for k := 0; k+1 < len(bounds); k++ {
			seg := pts[bounds[k] : bounds[k+1]+1]
			if len(seg) < 2 {
				continue
			}
			cl := Centerline{Pts: seg, Ends: [2]int{-1, -1}}
			if k == 0 && okA {
				cl.Ends[0] = ea
			}
			if k == len(bounds)-2 && okB {
				cl.Ends[1] = eb
			}
			out = append(out, cl)
		}
	}
	return out
}

// pinTo slides a chain end onto its entrance node — the averaged
// centrepoint of the throat, which can sit tens of metres off whichever
// rail the chain actually used — decaying the move along the chain so a
// SHORT chain translates instead of spiking. Replacing the endpoint
// outright put a 180° hairpin in every chain shorter than the two pins
// that grabbed it.
func pinTo(pts []geo.Pt, target geo.Pt, atStart bool) []geo.Pt {
	n := len(pts)
	if n < 2 {
		return pts
	}
	idx := 0
	if !atStart {
		idx = n - 1
	}
	d := target.Sub(pts[idx])
	if d.Norm() < 1e-9 {
		return pts
	}
	arc := make([]float64, n)
	for i := 1; i < n; i++ {
		arc[i] = arc[i-1] + pts[i].Dist(pts[i-1])
	}
	total := arc[n-1]
	reach := math.Min(ctrTaperM, total)
	if reach < 1e-9 {
		return pts
	}
	out := make([]geo.Pt, n)
	copy(out, pts)
	for i := 0; i < n; i++ {
		s := arc[i]
		if !atStart {
			s = total - arc[i]
		}
		if s >= reach {
			continue
		}
		out[i] = pts[i].Add(d.Scale(1 - s/reach))
	}
	return out
}

func lineLen(pts []geo.Pt) float64 {
	t := 0.0
	for i := 1; i < len(pts); i++ {
		t += pts[i].Dist(pts[i-1])
	}
	return t
}
