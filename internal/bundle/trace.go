package bundle

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/support"
)

// StringTrace is the owner's bundling algorithm, literally: lay a string
// down each track group. Sweep a SEED strand end to end — the centerline is
// that single uninterrupted walk, moved laterally to the median strand of
// each cross-section (1 track → follow, 2 → midpoint, 3 → center,
// 4 → middle-two). The group at each sample is the set of strands running
// alongside (heading-agreed, offset-stable — the kiss rule); where that set
// changes persistently, the group splits/joins. Swept strands are consumed;
// remaining stretches seed later sweeps (branches). Tangling is
// structurally impossible: geometry never merges points, it follows one
// continuous string per group.
type TraceParams struct {
	Step        float64 // sweep sampling (m)
	Reach       float64 // cross-section half-width (m)
	MinParallel float64 // |cos| heading agreement
	OffStable   float64 // max offset drift over ±30m (m) — crossings sweep through
	MinState    float64 // group-set change must persist this long (m)
	MinSeed     float64 // shortest unconsumed run worth sweeping (m)
	NodeTol     float64 // endpoint clustering radius (m)
	SmoothSigma float64 // final low-pass (m)
}

func DefaultTraceParams() TraceParams {
	return TraceParams{
		Step: 10, Reach: 20, MinParallel: math.Cos(25 * math.Pi / 180),
		OffStable: 3.5, MinState: 60, MinSeed: 150, NodeTol: 30,
		SmoothSigma: 8,
	}
}

// StringTrace returns a support.Graph so the attribution/order/fair stages
// consume it unchanged. Edge.Tracks carries the group's track count.
func StringTrace(strands []Strand, p TraceParams, logf func(string, ...any)) *support.Graph {
	lines := make([]*geo.Line, len(strands))
	for i, s := range strands {
		lines[i] = s.Line
	}
	grid := geo.NewGrid(lines, 64)

	// consumption mask per strand at Step resolution
	consumed := make([][]bool, len(strands))
	for i, l := range lines {
		consumed[i] = make([]bool, int(l.Len()/p.Step)+2)
	}
	markConsumed := func(si int, arc float64) {
		k := int(arc / p.Step)
		for _, kk := range []int{k - 1, k, k + 1} {
			if kk >= 0 && kk < len(consumed[si]) {
				consumed[si][kk] = true
			}
		}
	}
	longestRun := func(si int) (float64, float64) {
		best, bl := -1.0, 0.0
		cur := -1.0
		for k, c := range consumed[si] {
			if !c {
				if cur < 0 {
					cur = float64(k) * p.Step
				}
			} else if cur >= 0 {
				if l := float64(k)*p.Step - cur; l > bl {
					best, bl = cur, l
				}
				cur = -1
			}
		}
		if cur >= 0 {
			if l := float64(len(consumed[si]))*p.Step - cur; l > bl {
				best, bl = cur, l
			}
		}
		return best, bl
	}

	type sweepPt struct {
		pt     geo.Pt
		count  int
		group  []int // sorted alongside strand ids
	}

	g := &support.Graph{}
	var endpoints []endpoint

	for {
		// seed = strand with the longest unconsumed run
		seed, sFrom, sLen := -1, 0.0, 0.0
		for si := range strands {
			if from, l := longestRun(si); l > sLen {
				seed, sFrom, sLen = si, from, l
			}
		}
		if seed < 0 || sLen < p.MinSeed {
			break
		}
		l := lines[seed]
		sTo := math.Min(sFrom+sLen, l.Len())

		// ---- sweep the string
		var pts []sweepPt
		for arc := sFrom; arc <= sTo; arc += p.Step {
			pt := l.AtArc(arc)
			tan := l.TangentAtArc(arc, p.Step)
			nrm := tan.Perp()
			// offset-stable alongside set votes in the median (kiss rule:
			// crossings sweep through); consumption is GENEROUS — anything
			// near and parallel is covered by this string, or it reseeds a
			// duplicate sweep down the same group (the dup% leak)
			offs := []float64{0}
			var group []int
			type cand struct {
				oi  int
				off float64
			}
			var cands []cand
			grid.Near(pt, p.Reach, func(oi int) {
				if oi == seed {
					return
				}
				cs := lines[oi].CrossSection(pt, tan, p.Reach)
				for _, c := range cs {
					if c.Parallel >= p.MinParallel {
						cands = append(cands, cand{oi, c.Offset})
						break
					}
				}
				o, ok := stableOffset(lines[oi], pt, tan, nrm, p)
				if !ok {
					return
				}
				offs = append(offs, o)
				group = append(group, oi)
			})
			sort.Ints(group)
			sort.Float64s(offs)
			// consume only within the voted group's offset ENVELOPE (+5m):
			// wider consumption swallows SEPARATE corridors nearby (781
			// route bridges); narrower re-seeds duplicates (dup% leak)
			lo, hi := offs[0]-5, offs[len(offs)-1]+5
			var consume []int
			for _, c := range cands {
				if c.off >= lo && c.off <= hi {
					consume = append(consume, c.oi)
				}
			}
			o := MedianStrand(Strands(offs, 4.5))
			pts = append(pts, sweepPt{
				pt:    pt.Add(nrm.Scale(o)),
				count: len(offs), group: group,
			})
			markConsumed(seed, arc)
			for _, oi := range consume {
				oarc, _ := lines[oi].ProjectArc(pt)
				markConsumed(oi, oarc)
				markConsumed(oi, oarc-p.Step)
				markConsumed(oi, oarc+p.Step)
			}
		}
		if len(pts) < 2 {
			continue
		}

		// ---- segment by persistent group-set change (split/join points)
		minRun := int(p.MinState / p.Step)
		var cuts []int
		cur := 0
		k := 1
		for k < len(pts) {
			if sameGroup(pts[k].group, pts[cur].group) {
				k++
				continue
			}
			hit, tot := 0, 0
			for j := k; j < len(pts) && j < k+minRun; j++ {
				tot++
				if sameGroup(pts[j].group, pts[k].group) {
					hit++
				}
			}
			if tot > 0 && float64(hit) >= 0.8*float64(tot) {
				cuts = append(cuts, k)
				cur = k
			}
			k++
		}
		// ---- emit edges
		emit := func(from, to int) {
			if to-from < 1 {
				return
			}
			seg := make([]geo.Pt, 0, to-from+1)
			counts := map[int]int{}
			for _, sp := range pts[from : to+1] {
				seg = append(seg, sp.pt)
				counts[sp.count]++
			}
			bestC, bestN := 1, 0
			for c, n := range counts {
				if n > bestN {
					bestC, bestN = c, n
				}
			}
			seg = geo.GaussianArc(geo.NewLine(seg).Densify(8), p.SmoothSigma)
			ei := len(g.Edges)
			g.Edges = append(g.Edges, &support.Edge{
				Pts: seg, Tracks: bestC,
				Occupancy: map[string]support.Interval{},
			})
			endpoints = append(endpoints,
				endpoint{seg[0], ei, true},
				endpoint{seg[len(seg)-1], ei, false})
		}
		prev := 0
		for _, c := range cuts {
			emit(prev, c)
			prev = c
		}
		emit(prev, len(pts)-1)
	}

	// ---- nodes: cluster edge endpoints; attach branch ends onto trunk edges
	nodeOf := clusterEndpoints(g, endpoints, p.NodeTol)
	attachDanglers(g, nodeOf, p)
	g.RebuildAdj()
	total := 0.0
	for _, e := range g.Edges {
		total += e.Line().Len()
	}
	if logf != nil {
		logf("trace: %d group edges, %d nodes, %.1f km", len(g.Edges), len(g.Nodes), total/1000)
	}
	return g
}

// stableOffset returns the signed cross-section offset of strand ol at pt if
// it runs ALONGSIDE (heading-agreed, offset stable over ±30m along the
// sweep tangent) — the kiss/crossing filter.
func stableOffset(ol *geo.Line, pt, tan, nrm geo.Pt, p TraceParams) (float64, bool) {
	cs := ol.CrossSection(pt, tan, p.Reach)
	if len(cs) == 0 {
		return 0, false
	}
	best := cs[0]
	for _, c := range cs[1:] {
		if math.Abs(c.Offset) < math.Abs(best.Offset) {
			best = c
		}
	}
	if best.Parallel < p.MinParallel {
		return 0, false
	}
	// stability probes ±30m along the tangent direction
	for _, s := range []float64{-30, 30} {
		q := pt.Add(tan.Scale(s))
		qs := ol.CrossSection(q, tan, p.Reach)
		ok := false
		for _, c := range qs {
			if math.Abs(c.Offset-best.Offset) <= p.OffStable && c.Parallel >= p.MinParallel {
				ok = true
				break
			}
		}
		if !ok {
			return 0, false
		}
	}
	return best.Offset, true
}

func sameGroup(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type endpoint struct {
	pt   geo.Pt
	edge int
	atA  bool
}

func clusterEndpoints(g *support.Graph, eps []endpoint, tol float64) []int {
	nodeOf := make([]int, len(eps))
	for i := range nodeOf {
		nodeOf[i] = -1
	}
	for i, e := range eps {
		if nodeOf[i] >= 0 {
			continue
		}
		nid := len(g.Nodes)
		sum, n := e.pt, 1
		nodeOf[i] = nid
		for j := i + 1; j < len(eps); j++ {
			if nodeOf[j] < 0 && eps[j].pt.Dist(e.pt) <= tol {
				nodeOf[j] = nid
				sum = sum.Add(eps[j].pt)
				n++
			}
		}
		g.Nodes = append(g.Nodes, support.Node{At: sum.Scale(1 / float64(n))})
	}
	for i, e := range eps {
		nid := nodeOf[i]
		if e.atA {
			g.Edges[e.edge].From = nid
		} else {
			g.Edges[e.edge].To = nid
		}
	}
	return nodeOf
}

// attachDanglers joins a branch endpoint onto the trunk it forks from. The
// node sits ON THE TRUNK at the projection point — the trunk is never bent
// toward the branch tip (that drew perpendicular H-bars at every fork); the
// BRANCH eases into the node instead.
func attachDanglers(g *support.Graph, _ []int, p TraceParams) {
	for pass := 0; pass < 4; pass++ {
		deg := make(map[int]int)
		for _, e := range g.Edges {
			deg[e.From]++
			deg[e.To]++
		}
		changed := false
		for ni := range g.Nodes {
			if deg[ni] != 1 {
				continue
			}
			at := g.Nodes[ni].At
			bestE, bestArc, bestD := -1, 0.0, p.NodeTol
			for ei, e := range g.Edges {
				if e.From == ni || e.To == ni {
					continue
				}
				l := e.Line()
				arc, d := l.ProjectArc(at)
				if d < bestD && arc > p.NodeTol && arc < l.Len()-p.NodeTol {
					bestE, bestArc, bestD = ei, arc, d
				}
			}
			if bestE < 0 {
				continue
			}
			e := g.Edges[bestE]
			l := e.Line()
			proj := l.AtArc(bestArc)
			g.Nodes[ni].At = proj // fork node lives ON the trunk
			a := SubLine(l, 0, bestArc)
			b := SubLine(l, bestArc, l.Len())
			g.Edges[bestE] = &support.Edge{From: e.From, To: ni,
				Pts: a.Pts, Tracks: e.Tracks, Occupancy: map[string]support.Interval{}}
			g.Edges = append(g.Edges, &support.Edge{From: ni, To: e.To,
				Pts: b.Pts, Tracks: e.Tracks, Occupancy: map[string]support.Interval{}})
			changed = true
		}
		if !changed {
			break
		}
	}
	// every edge end eases into its node over an offset-scaled ramp — the
	// median steps sideways where a group's set changes; a hard pin turns
	// that step into a jog/kink at every fork (smooth Y instead)
	for _, e := range g.Edges {
		if len(e.Pts) < 3 {
			continue
		}
		na, nb := g.Nodes[e.From].At, g.Nodes[e.To].At
		e.Pts = TieEnds(geo.NewLine(e.Pts), na, nb).Pts
	}
}
