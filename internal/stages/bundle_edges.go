package stages

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Edge-level bundling — owner's law 3 applied to the segment graph. Two
// non-gap edges sustained-parallel within the kiss-rule mate range
// (≤ mergeDist for ≥ mergeRun of arc) are one VISUAL corridor over that
// interval, whatever routes or colors they carry: cut both edges at the
// interval bounds (a node exists exactly where bundle membership changes),
// replace the overlapping halves with ONE edge carrying the union route set
// on the midpoint geometry, and let refinement settle it on the physical
// group median. This is what turns a terminal balloon's two approach legs
// into a lollipop stick, and stacks cross-color corridors (Chicago Blue
// under the El) into one ribbon group. Brief kisses (< mergeRun) remain
// unrepresentable, by construction.

var (
	mergeDist = 12.0 // kiss-rule mate range (dial: split_merge_dist)
	mergeRun  = 60.0 // sustained-parallelism minimum (dial: split_merge_run)
	// Cross-SERVICE corridors merge only when they have already refined
	// onto the same group median — sustained co-location this tight is
	// impossible for genuinely distinct corridors (kiss-class pairs sit
	// 12–18 m apart; the South Ferry concentric loops ~10 m), so this is
	// the safe trigger for visual bundling across colors: A/C+G on
	// Schermerhorn, F+A/C on Jay St, the Chicago Loop inner/outer pair.
	coMergeDist = 4.0 // (dial: split_co_merge_dist)

	// corridor-tail-from-anchor gates (cross-service trench corridors)
	corrAnchor = 8.0   // lines must converge this close at one point
	corrBand   = 25.0  // ...then stay within this band
	corrHead   = 0.985 // ...with headings agreeing (|cos|, ~10°)
	corrRun    = 250.0 // ...for at least this much arc
)

const (
	mergeSnap   = 30.0 // interval ends this close to an edge end snap to it
	mergeRounds = 600  // safety bound (each round applies ONE merge)
	mergeDS     = 8.0  // a-sample spacing for all pair evaluations
)

// looseTwins gates the short-fragment twin merge to a second phase: it is
// cleanup for residue around junction fans, and letting it fire early
// reroutes the greedy cascade away from the big banded corridor merges
// (DeKalb lost its full trunk to an eager 56 m twin once).
var looseTwins = false

func bundleParallelEdges(net *Network) int {
	mergeDist = dial("split_merge_dist", 12)
	mergeRun = dial("split_merge_run", 60)
	coMergeDist = dial("split_co_merge_dist", 4)
	merges := 0
	st := newBundleState(net)
	looseTwins = false
	for round := 0; round < mergeRounds; round++ {
		if !mergeOnePair(net, st) {
			break
		}
		merges++
	}
	looseTwins = true
	for round := 0; round < mergeRounds; round++ {
		if !mergeOnePair(net, st) {
			break
		}
		merges++
	}
	rebuildAdj(net)
	return merges
}

func rebuildAdj(net *Network) {
	for ni := range net.Nodes {
		net.Nodes[ni].Adj = nil
	}
	for ei, e := range net.Edges {
		net.Nodes[e.From].Adj = append(net.Nodes[e.From].Adj, ei)
		if e.To != e.From {
			net.Nodes[e.To].Adj = append(net.Nodes[e.To].Adj, ei)
		}
	}
}

// level index: vertical class lookup for merge rules. A stacked pair (el
// over subway) flattens to one trench in plan view but is NOT one
// corridor — Apple keeps the Astoria el separate from Queens Blvd.
var lvlLines []*geo.Line
var lvlClass []int
var lvlWays []string
var lvlGrid *geo.Grid
var lvlWayRoutes map[string]map[string]bool

func setLevelIndex(tracks []bundle.Track) {
	lvlLines = lvlLines[:0]
	lvlClass = lvlClass[:0]
	lvlWays = lvlWays[:0]
	for _, t := range tracks {
		lvlLines = append(lvlLines, t.Line)
		lvlClass = append(lvlClass, t.Level)
		lvlWays = append(lvlWays, t.ID)
	}
	lvlGrid = geo.NewGrid(lvlLines, 64)
}

func setWayRouteIndex(m map[string]map[string]bool) { lvlWayRoutes = m }

// levelAt: vertical class near q, resolved by SERVICE — with stacked
// alignments the nearest track in 2D is arbitrary, so among tracks within
// reach prefer the nearest one actually ridden by the given routes.
func levelAt(q geo.Pt, routes []string) (int, bool) {
	best, bi := 15.0, -1
	bestOwn, biOwn := 15.0, -1
	lvlGrid.Near(q, 15, func(ti int) {
		d := lvlLines[ti].DistTo(q)
		if d < best {
			best, bi = d, ti
		}
		if d < bestOwn && lvlWayRoutes != nil {
			rs := lvlWayRoutes[lvlWays[ti]]
			for _, r := range routes {
				if rs[r] {
					bestOwn, biOwn = d, ti
					break
				}
			}
		}
	})
	if biOwn >= 0 {
		return lvlClass[biOwn], true
	}
	if bi < 0 {
		return 0, false
	}
	return lvlClass[bi], true
}

// sameLevel: majority-vote vertical agreement of two lines over a span of
// a-samples (b probed at the a-sample's projection).
// lvlOK gates DISJOINT-route merges on vertical agreement: same-service
// doubling is one corridor by definition, but a cross-service pair must
// not merge across levels (el over subway).
func lvlOK(net *Network, a, b int, la, lb *geo.Line, samples []geo.Pt, i, j int) bool {
	if edgeRoutesIntersect(net.Edges[a].Routes, net.Edges[b].Routes) {
		return true
	}
	return sameLevel(net, a, b, lb, samples, i, j)
}

func sameLevel(net *Network, a, b int, lb *geo.Line, samples []geo.Pt, i, j int) bool {
	if lvlGrid == nil {
		return true
	}
	agree, differ := 0, 0
	for _, k := range []int{i + (j-i)/4, i + (j-i)/2, i + 3*(j-i)/4} {
		if k < 0 || k >= len(samples) {
			continue
		}
		ca, oka := levelAt(samples[k], net.Edges[a].Routes)
		arcB, _ := lb.ProjectArc(samples[k])
		cb, okb := levelAt(lb.AtArc(arcB), net.Edges[b].Routes)
		if !oka || !okb {
			continue
		}
		if ca == cb {
			agree++
		} else {
			differ++
		}
	}
	return differ <= agree
}

// bundleState carries exact bookkeeping across mergeOnePair rounds. Each
// round used to rebuild every line, resample every edge and re-evaluate
// every candidate pair from scratch, apply ONE merge and start over — the
// pipeline's single most expensive loop. Three invariants make the caching
// exact:
//
//   - a NEGATIVE pair evaluation mutates nothing and depends only on the
//     two edges' frozen content (Pts/Routes/Gap, keyed by id — content
//     changes mint fresh ids), their CURRENT From/To node ids (welds
//     change them, so they are part of the memo key) and the looseTwins
//     phase — skip it for good once seen;
//   - an edge's candidate list (nearby edges + per-sample counts) depends
//     only on pair geometries, so it survives every round whose merge
//     stayed out of the edge's 25 m sample neighborhood;
//   - the level index and dials are constant per invocation.
type bundleState struct {
	nextID  int64
	ids     []int64 // parallel to net.Edges
	lines   map[int64]*geo.Line
	samples map[int64][]geo.Pt // Resample(mergeDS)
	cand    map[int64][]candEntry
	neg     map[pairKey]struct{}
	fresh   []int64    // ids minted by the last merge
	gone    []int64    // ids retired by the last merge
	dirty   [][]geo.Pt // removed/new sample sets: neighborhoods to re-enumerate
	// grid indexes every current edge by content id. A merge touches two
	// edges and mints a handful, so it is patched, not rebuilt — rebuilding
	// cost Θ(rounds × E), and rounds grow with E.
	grid  *geo.MutGrid
	idIdx map[int64]int
}

type candEntry struct {
	id    int64
	count int
}

type pairKey struct {
	ida, idb int64
	fa, ta   int
	fb, tb   int
	loose    bool
}

func newBundleState(net *Network) *bundleState {
	st := &bundleState{
		lines:   map[int64]*geo.Line{},
		samples: map[int64][]geo.Pt{},
		cand:    map[int64][]candEntry{},
		neg:     map[pairKey]struct{}{},
		idIdx:   map[int64]int{},
	}
	st.ids = make([]int64, len(net.Edges))
	for i := range st.ids {
		st.ids[i] = st.mint()
	}
	return st
}

func (st *bundleState) mint() int64 { st.nextID++; return st.nextID }

func (st *bundleState) beginRound(net *Network) {
	for i, id := range st.ids {
		if st.lines[id] == nil {
			l := geo.NewLine(net.Edges[i].Pts)
			st.lines[id] = l
			st.samples[id] = l.Resample(mergeDS)
		}
	}
	if st.grid == nil {
		st.grid = geo.NewMutGrid(64)
		for _, id := range st.ids {
			st.grid.Add(id, st.lines[id])
		}
	} else {
		// patch to the post-merge edge set — the same index a rebuild
		// would produce, since surviving edges' geometry never changes
		for _, id := range st.gone {
			st.grid.Remove(id)
		}
		for _, id := range st.fresh {
			st.grid.Add(id, st.lines[id])
		}
		st.gone = st.gone[:0]
	}
	clear(st.idIdx)
	for i, id := range st.ids {
		st.idIdx[id] = i
	}
	// new geometry invalidates neighborhoods exactly like removed geometry
	for _, id := range st.fresh {
		st.dirty = append(st.dirty, st.samples[id])
	}
	st.fresh = st.fresh[:0]
	// An edge's cached candidate list changes only if geometry appeared or
	// vanished within 25 m of one of its samples. Any such sample lies on
	// the edge's polyline within 25+6 m of some sample of the changed
	// geometry (Resample keeps endpoints and rounds the count, so arc gaps
	// reach 12 m on lines just over 10 m — slack 6 m, not mergeDS/2) —
	// reach 31 marks a superset of the affected edges, and
	// over-invalidation merely recomputes identical lists.
	if len(st.dirty) > 0 {
		aff := map[int64]bool{}
		for _, pts := range st.dirty {
			for _, q := range pts {
				st.grid.Near(q, 31, func(id int64) { aff[id] = true })
			}
		}
		for id := range aff {
			delete(st.cand, id)
		}
		st.dirty = st.dirty[:0]
	}
}

// candFor returns edge a's candidate list — every edge with ≥1 a-sample
// within 25 m, with the sample count — computing it (identically to the
// old per-round enumeration) only when no valid cached list exists.
func (st *bundleState) candFor(a int) []candEntry {
	id := st.ids[a]
	if c, ok := st.cand[id]; ok {
		return c
	}
	counts := map[int64]int{}
	for _, q := range st.samples[id] {
		// gap bridges DO merge into a real corridor they shadow — a
		// bridged service running alongside ridden track is the same
		// visual corridor (the FX express bridge on Culver); genuine
		// gaps have no parallel corridor and are untouched
		// candidate reach covers the banded-corridor width, not just
		// the kiss range — the DeKalb bypass rides 10–25 m out
		st.grid.Near(q, 25, func(b int64) {
			if b != id {
				counts[b]++
			}
		})
	}
	list := make([]candEntry, 0, len(counts))
	for b, n := range counts {
		list = append(list, candEntry{b, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	st.cand[id] = list
	return list
}

// mergeOnePair finds one sustained-parallel edge pair and applies the cut +
// merge. Returns false when the network has no more mergeable pairs. The
// scan runs in ascending edge order with candidates in ascending edge
// order — identical to the original full re-evaluation — with negative
// pair decisions memoized (see bundleState).
func mergeOnePair(net *Network, st *bundleState) bool {
	st.beginRound(net)
	lineOf := func(e int) *geo.Line { return st.lines[st.ids[e]] }
	const ds = mergeDS
	// sharedEnds: which of a's ends (From-side, To-side) coincide with an
	// end of b — by node id or position. A shared junction is STRUCTURAL
	// proof two edges belong to one corridor there, which licenses merges
	// the distance gates alone must refuse (the 4 m cross-service gate
	// protects concentric loops — but those never share endpoints).
	sharedMatrix := func(a, b int) [2][2]bool {
		const tol = 8.0
		ea, eb := net.Edges[a], net.Edges[b]
		la, lb := lineOf(a), lineOf(b)
		aN := [2]int{ea.From, ea.To}
		aP := [2]geo.Pt{la.Pts[0], la.Pts[len(la.Pts)-1]}
		bN := [2]int{eb.From, eb.To}
		bP := [2]geo.Pt{lb.Pts[0], lb.Pts[len(lb.Pts)-1]}
		var out [2][2]bool
		for i := 0; i < 2; i++ {
			for j := 0; j < 2; j++ {
				if aN[i] == bN[j] || aP[i].Dist(bP[j]) <= tol {
					out[i][j] = true
				}
			}
		}
		return out
	}
	endPt := func(e, end int) geo.Pt {
		l := lineOf(e)
		if end == 0 {
			return l.Pts[0]
		}
		return l.Pts[len(l.Pts)-1]
	}
	// banded: topology-anchored corridor test — some shared end (i,j) with
	// the OPPOSITE ends in the same junction cluster, comparable lengths,
	// and the whole of a within the corridor band of b.
	banded := func(a, b int, mat [2][2]bool) bool {
		const bandMax, bandEndTol = 25.0, 30.0
		if net.Edges[a].Gap || net.Edges[b].Gap {
			return false
		}
		la, lb := lineOf(a).Len(), lineOf(b).Len()
		if la < mergeRun || lb < mergeRun ||
			math.Abs(la-lb) > math.Max(40, 0.25*math.Max(la, lb)) {
			return false
		}
		ok := false
		for i := 0; i < 2 && !ok; i++ {
			for j := 0; j < 2 && !ok; j++ {
				if mat[i][j] && endPt(a, 1-i).Dist(endPt(b, 1-j)) <= bandEndTol {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
		in := 0
		samples := lineOf(a).Resample(12)
		for _, q := range samples {
			if lineOf(b).WithinLE(q, bandMax) {
				in++
			}
		}
		return float64(in) >= 0.85*float64(len(samples))
	}
	for a := range net.Edges {
		if net.Edges[a].Gap || lineOf(a).Len() < 20 {
			continue
		}
		samples := st.samples[st.ids[a]]
		type bc struct{ idx, count int }
		var bs []bc
		for _, ce := range st.candFor(a) {
			if bi, ok := st.idIdx[ce.id]; ok {
				bs = append(bs, bc{bi, ce.count})
			}
		}
		sort.Slice(bs, func(i, j int) bool { return bs[i].idx < bs[j].idx })
		for _, cb := range bs {
			b := cb.idx
			key := pairKey{st.ids[a], st.ids[b],
				net.Edges[a].From, net.Edges[a].To,
				net.Edges[b].From, net.Edges[b].To, looseTwins}
			if _, seen := st.neg[key]; seen {
				continue
			}
			la, lb := lineOf(a), lineOf(b)
			mat := sharedMatrix(a, b)
			shared := [2]bool{mat[0][0] || mat[0][1], mat[1][0] || mat[1][1]}

			// NODE-PAIR TWINS: both ends of a coincide with ends of b —
			// two parallel drawings of one corridor segment (junction
			// furniture leaves ~50 m pairs). The sustained-run minimum
			// exists to reject brief kisses; a twin is not a kiss — merge
			// whole-edge whatever its length, if genuinely co-located.
			// Short fragments get a looser end tolerance: a 50 m sliver
			// pair at a fan meets its mates 10–20 m apart (the ends land
			// on parallel tracks of one throat), and after the big
			// corridor merges these slivers are the last braid source.
			twin := shared[0] && shared[1]
			farMax := mergeDist
			la2, lb2 := la.Len(), lb.Len()
			if la2 < 80 && lb2 < 80 {
				// short twins carry longitudinal end slack (their refined
				// endpoints drift meters from the shared nodes) — that
				// overhang is not lateral separation; allow it in the
				// co-location test
				farMax = math.Max(mergeDist, math.Min(20, 0.4*math.Min(la2, lb2)))
				if !twin && looseTwins && !net.Edges[b].Gap {
					tol := math.Min(20, 0.4*math.Min(la2, lb2))
					aP0, aP1 := la.Pts[0], la.Pts[len(la.Pts)-1]
					bP0, bP1 := lb.Pts[0], lb.Pts[len(lb.Pts)-1]
					twin = (aP0.Dist(bP0) <= tol && aP1.Dist(bP1) <= tol) ||
						(aP0.Dist(bP1) <= tol && aP1.Dist(bP0) <= tol)
				}
			}
			if twin && !net.Edges[b].Gap {
				// far <= farMax  ⟺  every sample within farMax of b
				within := true
				for _, q := range samples {
					if !lb.WithinLE(q, farMax) {
						within = false
						break
					}
				}
				if within &&
					lvlOK(net, a, b, la, lb, samples, 0, len(samples)-1) &&
					applyMerge(net, st, a, b, 0, la.Len(), 10) {
					return true
				}
			}

			// BANDED CORRIDOR (Apple's law: one bundle per corridor,
			// whatever the track layout): two edges that share one
			// junction node AND whose far ends land in the same junction
			// cluster are one corridor by TOPOLOGY — the DeKalb express
			// bypass beside the station tracks, the Chicago Loop's
			// inner/outer legs. They ride distinct track pairs a band
			// apart (10–25 m), far beyond the co-location gate, but with
			// both ends anchored there is nothing else they can be.
			// Distinct corridors that merely kiss (Rector) or nest
			// (South Ferry loops) never share junctions.
			if banded(a, b, mat) &&
				lvlOK(net, a, b, la, lb, samples, 0, len(samples)-1) {
				if applyMerge(net, st, a, b, 0, la.Len(), 10) {
					return true
				}
			}

			// CORRIDOR TAIL FROM A GEOMETRIC ANCHOR: a cross-service pair
			// whose lines converge to touching at one point and then run
			// PARALLEL in a sustained band is one corridor from the anchor
			// on — adjacent track groups in a shared trench (A/C beside G
			// on Fulton) sit a full track-group apart, beyond every kiss
			// gate, and share no junction node because their services
			// never interline. Transversal crossings are rejected twice:
			// headings must agree throughout, and the tail's far quarter
			// must hold a STABLE separation (a crossing's profile is a V,
			// never a plateau).
			// A qualifying run needs ≥ corrRun/ds samples within
			// corrBand — and corrBand equals the candidate reach, so the
			// cached count is a provably-necessary cheap gate.
			if float64(cb.count)*ds >= corrRun &&
				!net.Edges[a].Gap && !net.Edges[b].Gap &&
				!edgeRoutesIntersect(net.Edges[a].Routes, net.Edges[b].Routes) &&
				la.Len() >= corrRun {
				dists := make([]float64, len(samples))
				arcs := make([]float64, len(samples))
				for i, q := range samples {
					// exact whenever the value can be ≤ corrBand; +Inf
					// stands in when the true distance exceeds the cap
					// (the comparisons below agree either way)
					arcB, d, ok := lb.ProjectArcCapped(q, corrBand+1)
					if !ok {
						dists[i] = math.Inf(1)
						continue
					}
					dists[i], arcs[i] = d, arcB
				}
				// heading agreement, computed only where it is read
				// (dists ≤ corrBand — the arc is exact there)
				headok := make([]bool, len(samples))
				headSet := make([]bool, len(samples))
				headAt := func(i int) bool {
					if !headSet[i] {
						ta := la.TangentAtArc(float64(i)*ds, 15)
						tb := lb.TangentAtArc(arcs[i], 15)
						headok[i] = math.Abs(ta.Dot(tb)) >= corrHead
						headSet[i] = true
					}
					return headok[i]
				}
				for i := 0; i < len(samples); {
					if dists[i] > corrBand || !headAt(i) {
						i++
						continue
					}
					j, anchor := i, -1
					for j < len(samples) && dists[j] <= corrBand && headAt(j) {
						if dists[j] <= corrAnchor &&
							(anchor < 0 || dists[j] < dists[anchor]) {
							anchor = j
						}
						j++
					}
					if anchor >= 0 && float64(j-i)*ds >= corrRun {
						q := (j - i) / 4
						lo, hi := i, j
						if anchor-i < (j-i)/2 {
							lo = j - q // anchor at start → far tail is the end
						} else {
							hi = i + q
						}
						mn, mx := math.Inf(1), 0.0
						for k := lo; k < hi; k++ {
							mn = math.Min(mn, dists[k])
							mx = math.Max(mx, dists[k])
						}
						if mx-mn <= 6 &&
							lvlOK(net, a, b, la, lb, samples, i, j) &&
							applyMerge(net, st, a, b, float64(i)*ds, float64(j-1)*ds) {
							return true
						}
					}
					i = j
				}
			}

			cnt := cb.count
			if float64(cnt)*ds < mergeRun {
				st.neg[key] = struct{}{}
				continue
			}
			// SAME-SERVICE doubling (directional splits, terminal V-legs)
			// merges within the kiss range; CROSS-SERVICE corridors merge
			// only when already co-located on one refined median —
			// distinct corridors (concentric loops, kiss-class pairs)
			// never sit this close sustained.
			disjoint := !edgeRoutesIntersect(net.Edges[a].Routes, net.Edges[b].Routes)
			reach := mergeDist
			if disjoint {
				reach = coMergeDist
			}

			// ANCHORED CONVERGENCE TAIL: a cross-service pair sharing one
			// endpoint is one corridor FROM THAT POINT — the bundle must
			// begin where the tracks meet, not at some downstream seam
			// where the medians happen to squeeze under the 4 m gate
			// (that late start is what braided G with A/C on Fulton and
			// blue/orange below W 4 St). From the shared end, take the
			// run within the full kiss range.
			if disjoint && (shared[0] || shared[1]) && la.Len() >= mergeRun {
				try := func(fromStart bool) bool {
					run := 0
					if fromStart {
						for _, q := range samples {
							if !lb.WithinLE(q, mergeDist) {
								break
							}
							run++
						}
						if float64(run)*ds < mergeRun {
							return false
						}
						if !lvlOK(net, a, b, la, lb, samples, 0, run-1) {
							return false
						}
						return applyMerge(net, st, a, b, 0, float64(run-1)*ds)
					}
					for i := len(samples) - 1; i >= 0; i-- {
						if !lb.WithinLE(samples[i], mergeDist) {
							break
						}
						run++
					}
					if float64(run)*ds < mergeRun {
						return false
					}
					if !lvlOK(net, a, b, la, lb, samples, len(samples)-run, len(samples)-1) {
						return false
					}
					return applyMerge(net, st, a, b,
						float64(len(samples)-run)*ds, la.Len())
				}
				if shared[0] && try(true) {
					return true
				}
				if shared[1] && try(false) {
					return true
				}
			}

			// longest sustained run of a-samples within reach of b
			best, cur, curStart, bestStart := 0, 0, 0, 0
			for i, q := range samples {
				if lb.WithinLE(q, reach) {
					if cur == 0 {
						curStart = i
					}
					cur++
					if cur > best {
						best, bestStart = cur, curStart
					}
				} else {
					cur = 0
				}
			}
			if float64(best)*ds < mergeRun {
				st.neg[key] = struct{}{}
				continue
			}
			a1 := float64(bestStart) * ds
			a2 := float64(bestStart+best-1) * ds
			if !lvlOK(net, a, b, la, lb, samples, bestStart, bestStart+best-1) {
				st.neg[key] = struct{}{}
				continue
			}
			if applyMerge(net, st, a, b, a1, a2) {
				return true
			}
			st.neg[key] = struct{}{}
		}
	}
	return false
}

// mergeLog, when set (tests), traces every applied merge.
var mergeLog func(format string, args ...any)

// applyMerge cuts edges a and b at the interval bounds and replaces the
// overlapping middles with one union edge on the midpoint geometry.
// minSpan overrides the default minimum merged length (node-pair twins
// merge however short they are).
func applyMerge(net *Network, st *bundleState, a, b int, a1, a2 float64, minSpan ...float64) bool {
	minLen := mergeRun * 0.8
	if len(minSpan) > 0 {
		minLen = minSpan[0]
	}
	la := st.lines[st.ids[a]]
	lb := st.lines[st.ids[b]]
	// snap interval ends onto edge ends
	if a1 < mergeSnap {
		a1 = 0
	}
	if a2 > la.Len()-mergeSnap {
		a2 = la.Len()
	}
	if a2-a1 < minLen {
		return false
	}
	// corresponding interval on b (may run reversed — loop legs do), walked
	// with LOCALLY-constrained projection: on a ring the global nearest
	// point is ambiguous (both balloon sides are "near"), and a wandering
	// correspondence swallows geometry
	tb1, _ := lb.ProjectArc(la.AtArc(a1))
	cur := tb1
	for s := a1 + 10; s <= a2; s += 10 {
		cur = localProjectArc(lb, la.AtArc(s), cur, 45)
	}
	tb2 := localProjectArc(lb, la.AtArc(a2), cur, 45)
	rev := tb1 > tb2
	if rev {
		tb1, tb2 = tb2, tb1
	}
	if tb1 < mergeSnap {
		tb1 = 0
	}
	if tb2 > lb.Len()-mergeSnap {
		tb2 = lb.Len()
	}
	if tb2-tb1 < math.Min(mergeRun*0.5, minLen) {
		return false
	}
	// span correspondence: a's interval and its projection onto b must have
	// comparable arc lengths. On a ring (balloon loop) ProjectArc is
	// ambiguous — both sides of the loop are "near" — and a mismatched span
	// would silently swallow the difference (this ate the Bowling Green
	// balloon before the guard).
	spanA, spanB := a2-a1, tb2-tb1
	if math.Abs(spanA-spanB) > math.Max(30, 0.3*spanA) {
		return false
	}

	// midpoint geometry of the overlap; a gap bridge contributes NO
	// geometry — real track always wins over shape chords (LESSONS #8)
	blendT := 0.5
	if net.Edges[b].Gap {
		blendT = 0
	}
	mid := bundle.SubLine(la, a1, a2)
	var mpts []geo.Pt
	for _, q := range mid.Resample(6) {
		arc, d, ok := lb.ProjectArcCapped(q, mergeDist*1.5+1)
		if !ok || d > mergeDist*1.5 {
			mpts = append(mpts, q)
			continue
		}
		mpts = append(mpts, geo.Lerp(q, lb.AtArc(arc), blendT))
	}
	if len(mpts) < 2 {
		return false
	}

	// nodes at the interval bounds: reuse edge end nodes when the cut is at
	// an end, else mint new ones at the merged geometry's ends
	endNode := func(e int, atStart bool) int {
		if atStart {
			return net.Edges[e].From
		}
		return net.Edges[e].To
	}
	nodeFor := func(pt geo.Pt, aCutAtEnd, bCutAtEnd int) int {
		// aCutAtEnd/bCutAtEnd: -1 none, 0 From-side end, 1 To-side end
		if aCutAtEnd >= 0 {
			return endNode(a, aCutAtEnd == 0)
		}
		if bCutAtEnd >= 0 {
			return endNode(b, bCutAtEnd == 0)
		}
		net.Nodes = append(net.Nodes, Node{At: pt})
		return len(net.Nodes) - 1
	}
	aStartEnd, aEndEnd := -1, -1
	if a1 == 0 {
		aStartEnd = 0
	}
	if a2 == la.Len() {
		aEndEnd = 1
	}
	bLow, bHigh := -1, -1
	if tb1 == 0 {
		bLow = 0
	}
	if tb2 == lb.Len() {
		bHigh = 1
	}
	// b's low-arc side corresponds to a's start when not reversed
	bAtAStart, bAtAEnd := bLow, bHigh
	if rev {
		bAtAStart, bAtAEnd = bHigh, bLow
	}
	n1 := nodeFor(mpts[0], aStartEnd, bAtAStart)
	n2 := nodeFor(mpts[len(mpts)-1], aEndEnd, bAtAEnd)
	if n1 == n2 && a2-a1 < 120 {
		return false // degenerate: both bounds landed on one node
	}

	// weld: when both a and b end at the interval bound, b's node folds
	// into a's
	weld := func(keep, drop int) {
		if keep == drop || drop < 0 {
			return
		}
		for ei := range net.Edges {
			if net.Edges[ei].From == drop {
				net.Edges[ei].From = keep
			}
			if net.Edges[ei].To == drop {
				net.Edges[ei].To = keep
			}
		}
	}
	if aStartEnd >= 0 && bAtAStart >= 0 {
		weld(n1, endNode(b, bAtAStart == 0))
	}
	if aEndEnd >= 0 && bAtAEnd >= 0 {
		weld(n2, endNode(b, bAtAEnd == 0))
	}

	routes := unionRoutes(net.Edges[a].Routes, net.Edges[b].Routes)
	tracks := max(net.Edges[a].Tracks+net.Edges[b].Tracks, 1)

	var newEdges []Edge
	addPart := func(src int, l *geo.Line, from, to float64, nFrom, nTo int) {
		if to-from < 1 {
			return
		}
		sub := bundle.SubLine(l, from, to)
		if sub.Len() < 1 {
			return
		}
		newEdges = append(newEdges, Edge{
			From: nFrom, To: nTo, Pts: sub.Pts,
			Routes: net.Edges[src].Routes, Tracks: net.Edges[src].Tracks,
			Gap: net.Edges[src].Gap,
		})
	}
	// keep the outer parts of a and b
	addPart(a, la, 0, a1, net.Edges[a].From, n1)
	addPart(a, la, a2, la.Len(), n2, net.Edges[a].To)
	if !rev {
		addPart(b, lb, 0, tb1, net.Edges[b].From, n1)
		addPart(b, lb, tb2, lb.Len(), n2, net.Edges[b].To)
	} else {
		addPart(b, lb, 0, tb1, net.Edges[b].From, n2)
		addPart(b, lb, tb2, lb.Len(), n1, net.Edges[b].To)
	}
	merged := Edge{From: n1, To: n2, Pts: mpts, Routes: routes, Tracks: tracks,
		Gap: net.Edges[a].Gap && net.Edges[b].Gap}

	if mergeLog != nil {
		mergeLog("merge a=%v(len %.0f, itv %.0f-%.0f) b=%v(len %.0f, itv %.0f-%.0f rev=%v) parts=%d mergedLen=%.0f at (%.0f,%.0f)",
			net.Edges[a].Routes, la.Len(), a1, a2,
			net.Edges[b].Routes, lb.Len(), tb1, tb2, rev,
			len(newEdges), geo.NewLine(mpts).Len(), mpts[0].X, mpts[0].Y)
	}
	// splice: drop a and b, add the parts + merged corridor — and mirror
	// the id bookkeeping (survivors keep ids; parts + merged mint fresh
	// ones; the removed and added sample sets drive cand invalidation)
	oldA, oldB := st.ids[a], st.ids[b]
	st.dirty = append(st.dirty, st.samples[oldA], st.samples[oldB])
	st.gone = append(st.gone, oldA, oldB)
	var out []Edge
	newIDs := make([]int64, 0, len(net.Edges)+len(newEdges)-1)
	for ei, e := range net.Edges {
		if ei != a && ei != b {
			out = append(out, e)
			newIDs = append(newIDs, st.ids[ei])
		}
	}
	out = append(out, newEdges...)
	for range newEdges {
		id := st.mint()
		newIDs = append(newIDs, id)
		st.fresh = append(st.fresh, id)
	}
	out = append(out, merged)
	mid2 := st.mint()
	newIDs = append(newIDs, mid2)
	st.fresh = append(st.fresh, mid2)
	net.Edges = out
	st.ids = newIDs
	delete(st.lines, oldA)
	delete(st.lines, oldB)
	delete(st.samples, oldA)
	delete(st.samples, oldB)
	delete(st.cand, oldA)
	delete(st.cand, oldB)
	return true
}

// localProjectArc finds the arc position on l nearest to q, searching only
// within ±win of prevArc — a monotone local correspondence for parallel
// walks, immune to ring ambiguity.
func localProjectArc(l *geo.Line, q geo.Pt, prevArc, win float64) float64 {
	best, bestArc := math.Inf(1), prevArc
	lo := math.Max(0, prevArc-win)
	hi := math.Min(l.Len(), prevArc+win)
	for s := lo; s <= hi; s += 4 {
		if d := l.AtArc(s).Dist(q); d < best {
			best, bestArc = d, s
		}
	}
	return bestArc
}

func edgeRoutesIntersect(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func unionRoutes(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range append(append([]string{}, a...), b...) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
