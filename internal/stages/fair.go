package stages

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
)

// FAIR — owner's step 5, the node-front model. Per zoom band each edge is
// cut back from its nodes (a small free area sized by the band; a curvy
// approach shrinks its cut — LESSONS #14), the body between cuts is emitted
// once per color group as a steady ribbon, and each node reconnects every
// continuing color between its slot positions with a transition feature —
// tail + head chained through the junction, corner rounded with a circular
// arc (strict parallelism, no degenerate cases — the Transit blog), offsets
// eased by the renderer along line-progress. A color that terminates at a
// node keeps its offset to the end of geometry: a steady stub into the
// node, never a collapse to the centerline (LESSONS #13 family).

type fairParams struct {
	CutBase     float64 // node-front half-size at z15 (doubles per band)
	GapPx       float64 // slot pitch in px at reference zoom
	MaxTurn     float64 // approach turn (deg) beyond which the cut shrinks
	FilletR     float64 // junction corner rounding radius at z15
	MinShortCut float64 // never cut below this
}

func defaultFairParams() fairParams {
	return fairParams{
		CutBase:     dial("fair_cut_base", 60),
		GapPx:       dial("fair_gap_px", 5),
		MaxTurn:     dial("fair_max_turn", 30),
		FilletR:     dial("fair_fillet_r", 30),
		MinShortCut: 12,
	}
}

// bands: emit one full copy per zoom band; the free area and fillet grow
// per zoom-out so on-screen junction size stays readable — but SUB-linear
// (was ×2 per band): full doubling cut too much of the map away at low
// zoom (owner). gap scales the slot pitch down as the map shrinks.
var fairBands = []struct {
	min, max int
	scale    float64
	gap      float64
}{
	{15, 24, 1, 1},
	{14, 15, 1.5, 0.8},
	{13, 14, 2.5, 0.65},
	{0, 13, 4, 0.5},
}

// Fair draws the junction connections with smooth curvature between slot
// positions (circular arcs preserve parallelism) and cuts zoom bands.
var dbg3Pt geo.Pt

func SetDbg3(p geo.Pt) { dbg3Pt = p }

func Fair(n *Network, slots map[int][]string, routes map[string]gtfs.Route, paths []Path) ([]Segment, error) {
	p := defaultFairParams()

	// the group key is the TRUNK KEY (docs/MODES.md): the color string
	// itself for color-trunked rail — identical to the old colorOf — but
	// an opaque token for agency-trunked regional and the singleton modes.
	// Anywhere a segment needs a DISPLAY color, resolve through hexOf,
	// never the key.
	colorOf := func(rid string) string {
		return mode.TrunkKey(routes[rid])
	}
	// per edge: color → routes of that color (sorted, stable)
	colorRoutes := make([]map[string][]string, len(n.Edges))
	for ei, e := range n.Edges {
		m := map[string][]string{}
		for _, r := range e.Routes {
			c := colorOf(r)
			m[c] = append(m[c], r)
		}
		for c := range m {
			sort.Strings(m[c])
		}
		colorRoutes[ei] = m
	}
	slotOf := func(ei int, color string) (int, int) {
		s := slots[ei]
		for i, c := range s {
			if c == color {
				return i, len(s)
			}
		}
		return -1, len(s)
	}
	// storage-frame px offset of a color on an edge; gapNow is the current
	// band's slot pitch (set at the top of each band iteration)
	gapNow := p.GapPx
	offsetPx := func(ei int, color string) float64 {
		s, ns := slotOf(ei, color)
		return (float64(s) - float64(ns-1)/2) * gapNow
	}
	// travel-frame px offset: traveling this edge storage-forward or not
	travelOffsetPx := func(ei int, color string, storage bool) float64 {
		o := offsetPx(ei, color)
		if !storage {
			return -o
		}
		return o
	}
	label := func(ei int, color string) string {
		rs := colorRoutes[ei][color]
		// a MULTI-route agency trunk is labelled as the agency — "Long
		// Island Rail Road", not four branch names and a +9. A lone route
		// keeps its own name (the SIR is not "MTA New York City Transit").
		if len(rs) > 1 && strings.HasPrefix(color, "agency:") {
			if n := agencyNames[strings.TrimPrefix(color, "agency:")]; n != "" {
				return n
			}
		}
		// a bus corridor can carry dozens of routes — the label is a
		// sample, not a roster
		const maxNames = 4
		out := ""
		for i, r := range rs {
			if i == maxNames && len(rs) > maxNames+1 {
				return fmt.Sprintf("%s +%d", out, len(rs)-maxNames)
			}
			sn := routes[r].ShortName
			if sn == "" {
				sn = routes[r].LongName // Amtrak names its trains, not numbers
			}
			if sn == "" {
				sn = r
			}
			if out != "" {
				out += "·"
			}
			out += sn
		}
		return out
	}
	routeType := func(ei int, color string) int {
		rs := colorRoutes[ei][color]
		if len(rs) == 0 {
			return 1
		}
		return routes[rs[0]].Type
	}
	// display color for a group: the first member's route_color. For a
	// color-trunked group this IS the key (unchanged behaviour); for
	// agency- and route-trunked groups the key is opaque and the members
	// supply the hex.
	hexOf := func(ei int, color string) string {
		// bus corridors paint one NEUTRAL color network-wide
		// (docs/MODES.md): the first-member color made each corridor a
		// different borough blue, so one continuous drawn trunk changed
		// color at every membership seam — chunked noise on the map and
		// phantom dangling ends in the continuity scan.
		if color == "bus" {
			return "888888"
		}
		rs := colorRoutes[ei][color]
		if len(rs) == 0 || routes[rs[0]].Color == "" {
			return "888888"
		}
		return routes[rs[0]].Color
	}
	// band floor per group: a mode invisible below its floor emits no
	// copy into those bands at all (docs/MODES.md, inferred pending the
	// Apple observation pass).
	belowFloor := func(ei int, color string, bandMin int) bool {
		return bandMin < mode.Of(routeType(ei, color)).BandFloor()
	}

	// matched walks per route id — the authority on which junction movements
	// a route actually makes. A loop circulator carries its id on three legs
	// of a corner but rides only two of the three pairings; the unridden
	// pairing must not get a turn drawn.
	patIdx := map[string][]int{}
	for i := range paths {
		rid := paths[i].Pattern.Route.ID
		patIdx[rid] = append(patIdx[rid], i)
	}
	elines := make([]*geo.Line, len(n.Edges))
	for ei, e := range n.Edges {
		elines[ei] = geo.NewLine(e.Pts)
	}
	const probeArc = 60.0 // how far down a leg to probe
	const probeTol = 28.0 // walk counts as passing within this of the probe
	probePt := func(ei, ni int) (geo.Pt, float64) {
		l := elines[ei]
		d := math.Min(probeArc, l.Len()/2)
		if n.Edges[ei].To == ni {
			return l.AtArc(l.Len() - d), d
		}
		return l.AtArc(d), d
	}
	// arcs along pattern pi where the walk passes the probe of (edge, node)
	passCache := map[[3]int][]float64{}
	passesNear := func(pi, ei, ni int, probe geo.Pt) []float64 {
		key := [3]int{pi, ei, ni}
		if arcs, ok := passCache[key]; ok {
			return arcs
		}
		var arcs []float64
		pts := paths[pi].Line.Pts
		arc, best, bestArc := 0.0, math.Inf(1), 0.0
		in := false
		for i := 1; i < len(pts); i++ {
			d := segDistPt(probe, pts[i-1], pts[i])
			if d < probeTol {
				in = true
				if d < best {
					best, bestArc = d, arc
				}
			} else if in {
				arcs = append(arcs, bestArc)
				in, best = false, math.Inf(1)
			}
			arc += pts[i].Sub(pts[i-1]).Norm()
		}
		if in {
			arcs = append(arcs, bestArc)
		}
		passCache[key] = arcs
		return arcs
	}
	// ridesPair: some shared route's walk passes both probes in one short
	// span (arc separation comparable to the probe spans, not a lap of a
	// loop). Attest-false only when the walks demonstrably pass BOTH legs
	// yet never consecutively — a walk that misses a probe entirely is a
	// geometry mismatch (merged medians drift), not evidence of a phantom.
	ridesPair := func(shared []string, a, b, ni int) bool {
		pa, da := probePt(a, ni)
		pb, db := probePt(b, ni)
		// slack covers the junction interior between the probe passes —
		// welded legs and station throats stretch it to ~300 m at DeKalb.
		// A phantom pairing (a circulator crossing its own path) separates
		// by half a loop lap, kilometres — orders of magnitude clear.
		sepMax := da + db + 400
		dbg := os.Getenv("PORTOLAN_DBG3") != "" && n.Nodes[ni].At.Dist(dbg3Pt) < 100
		for _, rid := range shared {
			pis := patIdx[rid]
			if len(pis) == 0 {
				return true
			}
			sawA, sawB := false, false
			for _, pi := range pis {
				aArcs := passesNear(pi, a, ni, pa)
				bArcs := passesNear(pi, b, ni, pb)
				sawA = sawA || len(aArcs) > 0
				sawB = sawB || len(bArcs) > 0
				if dbg {
					minSep := math.Inf(1)
					for _, x := range aArcs {
						for _, y := range bArcs {
							minSep = math.Min(minSep, math.Abs(x-y))
						}
					}
					fmt.Printf("  RIDE3 %s pat=%d passesA=%d passesB=%d minSep=%.0f sepMax=%.0f da=%.0f db=%.0f\n",
						rid, pi, len(aArcs), len(bArcs), minSep, sepMax, da, db)
				}
				for _, x := range aArcs {
					for _, y := range bArcs {
						if math.Abs(x-y) <= sepMax {
							return true
						}
					}
				}
			}
			if !sawA || !sawB {
				return true
			}
		}
		return false
	}

	// through-pairs per node/color: two incident edges sharing a route id of
	// that color really carry it through (walks are continuous — Law 1),
	// gated on the walk actually making that movement at this node
	type pair struct{ a, b int }
	throughs := make([]map[string][]pair, len(n.Nodes))
	for ni, nd := range n.Nodes {
		throughs[ni] = map[string][]pair{}
		for x := 0; x < len(nd.Adj); x++ {
			for y := x + 1; y < len(nd.Adj); y++ {
				a, b := nd.Adj[x], nd.Adj[y]
				if a == b {
					continue
				}
				for c, ras := range colorRoutes[a] {
					rbs, ok := colorRoutes[b][c]
					if !ok {
						continue
					}
					shared := sharedRoutes(ras, rbs)
					if len(shared) == 0 {
						continue
					}
					rides := ridesPair(shared, a, b, ni)
					if os.Getenv("PORTOLAN_DBG3") != "" && nd.At.Dist(dbg3Pt) < 100 {
						fmt.Printf("GATE3 node=%d color=%s a=e%d(%v) b=e%d(%v) shared=%v rides=%v\n",
							ni, c, a, n.Edges[a].Routes, b, n.Edges[b].Routes, shared, rides)
					}
					if !rides {
						continue
					}
					throughs[ni][c] = append(throughs[ni][c], pair{a, b})
				}
			}
		}
	}

	// drawn geometry rides ruler lines where the steel is straight: the
	// bundled median carries metre-scale waves (junction-adjacent member
	// contamination, strand flicker) that every offset color repeats, so
	// the whole bundle visibly wobbles. Near-straight runs collapse to
	// their chord; real curves fail the heading cone at the first bend
	// and keep every vertex. Shared per edge so all colors stay parallel.
	// then merge near-collinear chord joints: a slow multi-chord drift
	// (2-3 m over hundreds of metres) otherwise leaves chords meeting
	// mid-block — read as an extra split point in the bundle. Real curves
	// facet at several degrees per chord and are untouched.
	straightPts := make([][]geo.Pt, len(n.Edges))
	for ei, e := range n.Edges {
		tol := dial("fair_straight_tol", 2.2)
		// The street 2x-tolerance experiment is DEAD — do not retry: it
		// straightened Trade St but also chorded away Hawthorne Lane's
		// genuine gentle curve (sagitta > tol, heading deviation < any
		// cone — the cone cannot save a long shallow arc; only tol
		// decides). Pinch flattening lives in Refine's width bridging
		// now, which distinguishes furniture from geometry at the vote
		// level, where the information still exists.
		// pass 1 cone ~5.7°: wide enough to flatten corridor micro-waves,
		// tight enough that a real gradual curve keeps vertices every
		// ~11° of bend (a wider cone concentrated the Union Square elbow
		// into one sharp vertex). pass 2 merges ≤2° drift joints with
		// every dropped vertex re-checked against the final chord.
		straightPts[ei] = straightenCone(
			straightenCone(e.Pts, tol, 0.995),
			tol*1.6, 0.99939)
	}

	var segs []Segment
	for _, band := range fairBands {
		gapNow = p.GapPx * band.gap
		cut := make([][2]float64, len(n.Edges)) // per edge: cut at From, To
		lines := make([]*geo.Line, len(n.Edges))
		for ei := range n.Edges {
			lines[ei] = geo.NewLine(straightPts[ei])
			base := p.CutBase * band.scale
			for end := 0; end < 2; end++ {
				c := math.Min(base, lines[ei].Len()/3)
				// a curvy approach is real geometry — shrink the cut
				// rather than lay a synthetic connector across it; the
				// Bézier fallback turns compactly from whatever cut is
				// left, hugging the junction like the steel does
				for c > p.MinShortCut &&
					approachTurn(lines[ei], end == 1, c) > p.MaxTurn {
					c *= 0.7
				}
				cut[ei][end] = math.Max(c, 0)
			}
		}

		// transitions per node/color/pair; remember which (edge,end,color)
		// a transition already serves
		served := map[[3]int]bool{} // edge, end(0=From,1=To), colorIdx
		colorIdx := func(ei int, color string) int {
			s, _ := slotOf(ei, color)
			return s
		}
		// absorbed: (edge, colorIdx) fully covered by a chained-through
		// transition — its steady body must not draw
		absorbed := map[[2]int]bool{}
		// emitted: exact connections already drawn (a-end ↔ cur-end, color)
		emitted := map[[5]int]bool{}
		// uniqueThrough: the single same-color continuation of edge e at
		// node ni, or -1 (spec law "chain transitions through": a fragment
		// shorter than a fan is furniture; transitions pass through it in
		// one ease — per-node transitions on either side of a fragment
		// take different spans and CROSS, the circled DeKalb braids)
		uniqueThrough := func(ni int, e int, color string) int {
			out := -1
			for _, pr := range throughs[ni][color] {
				other := -1
				if pr.a == e {
					other = pr.b
				} else if pr.b == e {
					other = pr.a
				}
				if other < 0 {
					continue
				}
				if out >= 0 && out != other {
					return -1 // ambiguous
				}
				out = other
			}
			return out
		}
		fanLen := 1.4 * p.CutBase * band.scale
		// PLAN, then EMIT. Emitting as pairs are discovered is
		// order-dependent: a fragment's own direct transition can land
		// before the chained transition that covers the same passage,
		// leaving both drawn — a dead-end tine forking off the line (the
		// Bowling Green green fork). Planning first lets longer chains
		// claim their span; anything touching a claimed end is dropped.
		type cand struct {
			a, cur         int
			aAtTo, bAtFrom bool
			color          string
			mid            []geo.Pt
			mids           [][2]int
			bend           float64
		}
		// entry/exit travel tangents of a connection, measured at the OUTER
		// ends (steady side) — inner ends carry corner overshoot. 100 m out:
		// a gradual 90° sweep (the Dearborn subway peel) reads only ~29° at
		// 30 m and was classified straight — its Bézier then cut the corner
		// a block wide of the steel.
		pairTangents := func(a, cur int, aAtTo, bAtFrom bool) (geo.Pt, geo.Pt) {
			entryArc := math.Min(100, lines[a].Len()/3)
			exitArc := math.Min(100, lines[cur].Len()/3)
			var entry, exit geo.Pt
			if aAtTo {
				entry = lines[a].TangentAtArc(lines[a].Len()-entryArc, 10)
			} else {
				entry = lines[a].TangentAtArc(entryArc, 10).Scale(-1)
			}
			if bAtFrom {
				exit = lines[cur].TangentAtArc(exitArc, 10)
			} else {
				exit = lines[cur].TangentAtArc(lines[cur].Len()-exitArc, 10).Scale(-1)
			}
			return entry, exit
		}
		var cands []cand
		for ni := range n.Nodes {
			// sorted color order: map iteration here decides segment emission
			// order, which must not vary run to run
			colors := make([]string, 0, len(throughs[ni]))
			for color := range throughs[ni] {
				colors = append(colors, color)
			}
			sort.Strings(colors)
			for _, color := range colors {
				prs := throughs[ni][color]
				for _, pr := range prs {
					a, b := pr.a, pr.b
					aAtTo := n.Edges[a].To == ni
					mid := []geo.Pt{}
					var mids [][2]int
					cur, curNode := b, ni
					hops := 0
					for lines[cur].Len() < fanLen && hops < 3 {
						curAtFrom := n.Edges[cur].From == curNode
						far := n.Edges[cur].To
						if !curAtFrom {
							far = n.Edges[cur].From
						}
						nxt := uniqueThrough(far, cur, color)
						if nxt < 0 || nxt == a {
							break
						}
						seg := n.Edges[cur].Pts
						if !curAtFrom {
							seg = reversedPts(seg)
						}
						mid = append(mid, seg...)
						mids = append(mids, [2]int{cur, colorIdx(cur, color)})
						cur, curNode = nxt, far
						hops++
					}
					bAtFrom := n.Edges[cur].From == curNode
					en, ex := pairTangents(a, cur, aAtTo, bAtFrom)
					bend := math.Acos(math.Max(-1, math.Min(1, en.Dot(ex)))) * 180 / math.Pi
					cands = append(cands, cand{a, cur, aAtTo, bAtFrom, color, mid, mids, bend})
				}
			}
		}
		// per-color node fronts: a straight-through movement holds bundle
		// formation until a short cut — its slot swing happens inside the
		// junction — while a turning movement keeps the full span its curve
		// needs. Per (edge, end, color) the effective cut is the max any of
		// that color's movements needs, so every transition tail and the
		// steady body meet at the same arc.
		const straightBend = 30.0
		effCut := map[[3]int]float64{}
		cutFor := func(ei, end, ci int) float64 {
			if v, ok := effCut[[3]int{ei, end, ci}]; ok {
				return v
			}
			return cut[ei][end]
		}
		for _, c := range cands {
			endA, endB := boolIdx(c.aAtTo), boolIdx(!c.bAtFrom)
			cA, cB := cut[c.a][endA], cut[c.cur][endB]
			short := 1.5 * p.MinShortCut * band.scale
			if c.bend < straightBend {
				cA = math.Min(cA, short)
				cB = math.Min(cB, short)
			} else if c.bend > 40 {
				// a steel-traced turn needs only the span its connector
				// occupies — the full turning cut exists for SYNTHETIC
				// curves. Probing here (before cuts freeze) lets the turn
				// depart at the junction neck, and — because effCut takes
				// the per-color max — stops the turn's long cut from
				// dragging the same color's through-movement seam out with
				// it: the whole bundle then transitions in the same window.
				tailP := endPiece(lines[c.a], c.aAtTo, cA)
				headP := startPiece(lines[c.cur], c.bAtFrom, cB)
				if len(tailP) > 1 && len(headP) > 1 {
					p0, p3 := tailP[0], headP[len(headP)-1]
					near := tailP[len(tailP)-1]
					tlP, hlP := geo.NewLine(tailP), geo.NewLine(headP)
					if tc, needA, needB := trackCurveBetween(p0, p3, near, tlP, hlP,
						mode.Of(routeType(c.a, c.color)) == mode.Bus); tc != nil {
						cand := append(append([]geo.Pt{p0}, tc...), p3)
						allow := 25 + 20*math.Min(1, c.bend/90)
						if maxTurn12(smoothPolyline(geo.NewLine(cand))) <= allow {
							cA = math.Min(cA, needA+short)
							cB = math.Min(cB, needB+short)
						}
					}
				}
			}
			kA := [3]int{c.a, endA, colorIdx(c.a, c.color)}
			kB := [3]int{c.cur, endB, colorIdx(c.cur, c.color)}
			if v, ok := effCut[kA]; !ok || cA > v {
				effCut[kA] = cA
			}
			if v, ok := effCut[kB]; !ok || cB > v {
				effCut[kB] = cB
			}
		}
		// longest chains first: a chained connection claims the fragment
		// span before the fragment's own direct pairs are considered
		sort.SliceStable(cands, func(i, j int) bool {
			return len(cands[i].mids) > len(cands[j].mids)
		})
		for _, c := range cands {
			a, cur := c.a, c.cur
			if belowFloor(a, c.color, band.min) {
				continue // mode hidden in this band; its steady bodies skip too
			}
			if os.Getenv("PORTOLAN_DBG3") != "" && band.min == 15 {
				for _, e := range []int{a, cur} {
					pts2 := n.Edges[e].Pts
					if len(pts2) > 0 && pts2[len(pts2)-1].Dist(dbg3Pt) < 100 ||
						len(pts2) > 0 && pts2[0].Dist(dbg3Pt) < 100 {
						fmt.Printf("CAND3 %s a=e%d cur=e%d mids=%d aAtTo=%v bAtFrom=%v bend=%.0f\n",
							c.color, a, cur, len(c.mids), c.aAtTo, c.bAtFrom, c.bend)
						break
					}
				}
			}
			skip := absorbed[[2]int{a, colorIdx(a, c.color)}] ||
				absorbed[[2]int{cur, colorIdx(cur, c.color)}]
			for _, k := range c.mids {
				if absorbed[k] {
					skip = true
				}
			}
			ck := [5]int{a, boolIdx(c.aAtTo), cur, boolIdx(!c.bAtFrom), colorIdx(a, c.color)}
			rk := [5]int{cur, boolIdx(!c.bAtFrom), a, boolIdx(c.aAtTo), colorIdx(cur, c.color)}
			if skip || emitted[ck] || emitted[rk] {
				if os.Getenv("PORTOLAN_DBG3") != "" && band.min == 15 {
					fmt.Printf("  REJ3 skip=%v em=%v/%v\n", skip, emitted[ck], emitted[rk])
				}
				continue
			}
			tail := endPiece(lines[a], c.aAtTo, cutFor(a, boolIdx(c.aAtTo), colorIdx(a, c.color)))
			head := startPiece(lines[cur], c.bAtFrom, cutFor(cur, boolIdx(!c.bAtFrom), colorIdx(cur, c.color)))
			chain := chainPts(tail, c.mid)
			chain = chainPts(chain, head)
			if len(chain) < 2 {
				continue
			}
			// opposing TRAVEL directions are a topology decision, not a
			// smoothing problem (the Grand Central lesson): leave both
			// sides as steady stubs, never a hairpin connector. Measured
			// at the OUTER ends — entry direction into the connection vs
			// exit direction out of it — because the inner ends carry
			// corner overshoot (each edge owns half the curve at a 90°
			// street corner, and their meeting tangents oppose even
			// though the movement is a plain turn).
			tl, hl := geo.NewLine(tail), geo.NewLine(head)
			entry, exit := pairTangents(a, cur, c.aAtTo, c.bAtFrom)
			if entry.Dot(exit) < -0.5 {
				if os.Getenv("PORTOLAN_DBG3") != "" && band.min == 15 {
					fmt.Printf("  REJ3 hairpin\n")
				}
				continue
			}
			_ = tl
			_ = hl
			freeR := math.Min(p.FilletR*band.scale,
				0.8*math.Min(tl.Len(), hl.Len()))
			chain = blendFreeArea(chain, len(tail)-1, freeR)
			// a connector that cannot be DRAWN smoothly is a topology
			// decision, not a smoothing problem — the tangent guard above
			// catches net reversals, this catches everything else that
			// would emit as a visible kink.
			// allowance scales with how much the connection MUST turn: a
			// 90° street corner concentrates ~35-40° per 12 m at sane
			// radii, while a straight-through weave has no excuse.
			bend := c.bend
			allow := 25 + 20*math.Min(1, bend/90)
			// a TURNING movement follows the steel: the junction's
			// connector track is in the data — trace it between the cut
			// points instead of sweeping a synthetic curve wide of it
			onSteel := false
			if bend > 40 && len(tail) > 0 && len(head) > 0 {
				p0 := tail[0]
				p3 := head[len(head)-1]
				near := tail[len(tail)-1]
				tc, _, _ := trackCurveBetween(p0, p3, near, geo.NewLine(tail), geo.NewLine(head),
					mode.Of(routeType(a, c.color)) == mode.Bus)
				if os.Getenv("PORTOLAN_DBG3") != "" && band.min == 15 && near.Dist(dbg3Pt) < 150 {
					mt := -1.0
					if tc != nil {
						mt = maxTurn12(smoothPolyline(geo.NewLine(append(append([]geo.Pt{p0}, tc...), p3))))
					}
					fmt.Printf("TCURVE %s bend=%.0f tc=%v mt=%.0f allow=%.0f\n", c.color, bend, tc != nil, mt, allow)
				}
				if tc != nil {
					cand := append(append([]geo.Pt{p0}, tc...), p3)
					if maxTurn12(smoothPolyline(geo.NewLine(cand))) <= allow {
						chain = cand
						onSteel = true
					}
				}
			}
			// a straight-through pass has no excuse for ANY visible wiggle —
			// the junction node itself sits metres off the corridor axis
			// (weld position), so a pieced chain through it draws a tent.
			// Always offer the Bézier between the steady-side tangents and
			// take it whenever it beats the chain: the hand's line.
			// BUT straightness must hold along the CHAIN, not just at its
			// ends: at low bands the scaled cuts absorb whole junctions, and
			// an S-shaped chained movement (6 Av → CPW at 53 St) has parallel
			// end tangents while the steel between swings two real bends —
			// the Bézier would redraw hundreds of metres of real curve as a
			// lobe far off the tracks. Total accumulated turn tells a weld
			// tent (a few degrees) from a real S (100°+).
			chainTurn := 0.0
			{
				cl := geo.NewLine(chain)
				for arc := 12.0; arc <= cl.Len(); arc += 12 {
					t1 := cl.TangentAtArc(arc-12, 8)
					t2 := cl.TangentAtArc(arc, 8)
					d := math.Max(-1, math.Min(1, t1.Dot(t2)))
					chainTurn += math.Acos(d) * 180 / math.Pi
				}
			}
			chainStraight := bend < straightBend && chainTurn < 60
			retryAt := 25.0
			if chainStraight {
				retryAt = 0
			}
			if mt := maxTurn12(smoothPolyline(geo.NewLine(chain))); !onSteel && mt > retryAt {
				// the pieced chain drags junction-interior geometry (branch
				// ramps, weld curls) into the connector. A hand doesn't: it
				// draws one curve between the cut points, tangent to the
				// steadies. Retry as a cubic Bézier fitted to the
				// TRUSTWORTHY tangents — the steady-side ends of the cut
				// pieces — and only give up if even that can't draw.
				p0, p3 := tail[0], head[len(head)-1]
				t0 := geo.NewLine(tail).TangentAtArc(0, 12)
				t3 := geo.NewLine(head).TangentAtArc(geo.NewLine(head).Len(), 12)
				k := p0.Dist(p3) / 3
				p1 := p0.Add(t0.Scale(k))
				p2 := p3.Sub(t3.Scale(k))
				var bez []geo.Pt
				steps := int(math.Max(8, p0.Dist(p3)/4))
				for st := 0; st <= steps; st++ {
					t := float64(st) / float64(steps)
					u := 1 - t
					bez = append(bez, geo.Pt{
						X: u*u*u*p0.X + 3*u*u*t*p1.X + 3*u*t*t*p2.X + t*t*t*p3.X,
						Y: u*u*u*p0.Y + 3*u*u*t*p1.Y + 3*u*t*t*p2.Y + t*t*t*p3.Y,
					})
				}
				mb := maxTurn12(geo.NewLine(bez))
				// when the chain genuinely curves, the Bézier may only
				// STAND IN for it, never redraw it: bound how far it strays
				// from the pieced geometry before letting it win.
				bezOK := true
				if chainTurn >= 60 {
					bl := geo.NewLine(bez)
					for _, cp := range chain {
						if bl.DistTo(cp) > 40 {
							bezOK = false
							break
						}
					}
				}
				if bezOK && (mb < mt-5 || (chainStraight && mb < mt)) {
					chain = bez
					mt = mb
				}
				if mt > allow {
					if os.Getenv("PORTOLAN_DBG3") != "" && band.min == 15 {
						fmt.Printf("  REJ3 drawgate mt=%.0f bez=%.0f allow=%.0f bend=%.0f\n", mt, mb, allow, bend)
					}
					continue
				}
			}
			segs = append(segs, Segment{
				Kind:      "transition",
				Color:     hexOf(a, c.color),
				Routes:    colorRoutes[a][c.color],
				Label:     label(a, c.color),
				RouteType: routeType(a, c.color),
				Mode:      mode.Of(routeType(a, c.color)).String(),
				OffFromPx: travelOffsetPx(a, c.color, c.aAtTo),
				OffToPx:   travelOffsetPx(cur, c.color, c.bAtFrom),
				BandMin:   band.min,
				BandMax:   band.max,
				Line:      geo.NewLine(chain),
			})
			served[[3]int{a, boolIdx(c.aAtTo), colorIdx(a, c.color)}] = true
			served[[3]int{cur, boolIdx(!c.bAtFrom), colorIdx(cur, c.color)}] = true
			emitted[ck] = true
			emitted[rk] = true
			for _, k := range c.mids {
				absorbed[k] = true
			}
		}

		// steady bodies (and terminal stubs: unserved ends keep offset to
		// the end of geometry)
		for ei, e := range n.Edges {
			l := lines[ei]
			// sorted color order: emission order must not vary run to run
			colors := make([]string, 0, len(colorRoutes[ei]))
			for color := range colorRoutes[ei] {
				colors = append(colors, color)
			}
			sort.Strings(colors)
			for _, color := range colors {
				ci := colorIdx(ei, color)
				if absorbed[[2]int{ei, ci}] {
					continue // fully covered by a chained transition
				}
				if belowFloor(ei, color, band.min) {
					continue
				}
				from := cutFor(ei, 0, ci)
				if !served[[3]int{ei, 0, ci}] {
					from = 0
				}
				to := cutFor(ei, 1, ci)
				if !served[[3]int{ei, 1, ci}] {
					to = 0
				}
				body := bundle.SubLine(l, from, l.Len()-to)
				if body.Len() < 1 {
					continue
				}
				kind := "steady"
				if e.Gap {
					kind = "bridge"
				}
				s, ns := slotOf(ei, color)
				segs = append(segs, Segment{
					Kind:      kind,
					Color:     hexOf(ei, color),
					Routes:    colorRoutes[ei][color],
					Label:     label(ei, color),
					RouteType: routeType(ei, color),
					Mode:      mode.Of(routeType(ei, color)).String(),
					Slot:      s,
					NSlots:    ns,
					OffsetPx:  offsetPx(ei, color),
					BandMin:   band.min,
					BandMax:   band.max,
					Line:      body,
				})
			}
		}
	}
	// visual smoothing: the working geometry is polygonal at ~6 m vertex
	// spacing, which reads as facets at high zoom even when every turn
	// matches the steel. Simplify straight runs, then corner-cut the
	// remainder — endpoints pinned exactly, so segment-to-segment drawn
	// continuity is untouched.
	// The kit is MODE-SCALED: a tram's identity is hugging street
	// geometry — a metro-scale 25 m fillet erased the Atlanta streetcar's
	// Park Place dogleg into one sweep 20 m off the rails. Street-running
	// modes keep tight corners; grade-separated modes keep the wide kit.
	for i := range segs {
		s := 1.0
		switch mode.Of(segs[i].RouteType) {
		case mode.Tram, mode.Cable: // street-running: real corners survive
			s = 0.4
		}
		segs[i].Line = smoothPolylineScaled(segs[i].Line, s)
		// transition VERTEX DENSITY is load-bearing: the renderer
		// evaluates the line-progress offset ease per vertex, so a
		// geometrically-straight transition simplified to its two
		// endpoints draws its offset swing as one hard diagonal — the
		// smooth S lives entirely in the interior samples the collinear
		// simplify just deleted. Re-densify after smoothing; 12 m spacing
		// gives the cubic ease ~5+ samples on even the shortest seam.
		if segs[i].Kind == "transition" {
			segs[i].Line = geo.NewLine(segs[i].Line.Densify(12))
		}
	}
	return segs, nil
}

// smoothPolyline: collinear-run simplification followed by two rounds of
// endpoint-pinned Chaikin corner cutting.
// trackCurveBetween looks for the junction's connector track — the way
// that actually carries the movement between the two cut points — and
// returns its curve, laterally offset with a linear ramp so the ends
// meet the cut points. Through-tracks fail the span/offset tests; the
// connector is the way that projects both ends onto distinct arcs with
// small lateral offsets. Nil when no such way exists. The two floats
// are how much of each corridor piece the steel actually occupies —
// arc from the tail's node-side tip back to the curve's start, and
// from the head's tip forward to the curve's end — valid only when the
// curve is non-nil; the cut pass uses them to shrink turning cuts.
func trackCurveBetween(p0, p3, near geo.Pt, tl, hl *geo.Line, busOK bool) ([]geo.Pt, float64, float64) {
	if lvlGrid == nil {
		return nil, 0, 0
	}
	type fit struct {
		ti     int
		sa, sb float64
		o0, o1 float64
	}
	bestScore := math.Inf(1)
	var bestFit *fit
	lvlGrid.Near(near, 25, func(ti int) {
		t := lvlLines[ti]
		// class discipline: a bus movement connects over streets, a rail
		// movement never does — an el corner with a street below would
		// otherwise offer the road as its "connector steel"
		if (wayRailClass[lvlWays[ti]] == "street") != busOK {
			return
		}
		if t.Len() < 15 || t.DistTo(near) > 25 {
			return
		}
		sa, _ := t.ProjectArc(p0)
		sb, _ := t.ProjectArc(p3)
		if math.Abs(sb-sa) < 8 {
			return
		}
		// lateral gap measured against the CORRIDOR pieces at the
		// connector's own ends, in the tail→head oriented frame —
		// projecting the far cut points clamps and reads longitudinal
		// overshoot as huge lateral offsets
		endA := t.AtArc(sa) // tail-side end of the connector
		endB := t.AtArc(sb) // head-side end
		dirA := t.TangentAtArc(sa, 8)
		dirB := t.TangentAtArc(sb, 8)
		if sb < sa { // oriented frame runs sa→sb; flip tangents
			dirA = dirA.Scale(-1)
			dirB = dirB.Scale(-1)
		}
		pa, _ := tl.ProjectArc(endA)
		o0 := dirA.Cross(tl.AtArc(pa).Sub(endA))
		pb, _ := hl.ProjectArc(endB)
		o1 := dirB.Cross(hl.AtArc(pb).Sub(endB))
		if math.Abs(o0) > 15 || math.Abs(o1) > 15 {
			return
		}
		if sc := math.Abs(o0) + math.Abs(o1); sc < bestScore {
			bestScore = sc
			bestFit = &fit{ti, sa, sb, o0, o1}
		}
	})
	if bestFit == nil {
		return nil, 0, 0
	}
	t := lvlLines[bestFit.ti]
	sub := bundle.SubLine(t, math.Min(bestFit.sa, bestFit.sb), math.Max(bestFit.sa, bestFit.sb))
	pts := sub.Pts
	if bestFit.sb < bestFit.sa {
		pts = reversedPts(pts)
	}
	o0, o1 := bestFit.o0, bestFit.o1
	l := geo.NewLine(pts)
	// the lateral ramp o0→o1 keys on the connector's ACCUMULATED BEND,
	// not its arc: an arc-linear ramp settles the drawn line onto the
	// connector's pre-bend alignment before the curve — a visible dip on
	// every approach (the SE-corner notch). Bend-weighted, the line holds
	// the corridor offset until the steel starts turning and transfers
	// through the curve itself.
	// noise floor 0.5°/4m: the connector's gentle pre-corner divergence
	// (a few degrees over a block) must not start the transfer — only the
	// corner's sustained curvature moves the line off the corridor.
	turnAt := []float64{0}
	total := 0.0
	for arc := 4.0; arc <= l.Len(); arc += 4 {
		t1 := l.TangentAtArc(arc-4, 8)
		t2 := l.TangentAtArc(arc, 8)
		d := math.Max(-1, math.Min(1, t1.Dot(t2)))
		total += math.Max(0, math.Acos(d)-0.009)
		turnAt = append(turnAt, total)
	}
	// skip the connector's pre-bend LEAD-IN entirely: it runs alongside
	// the corridor but not ON it, so emitting it (even offset-ramped)
	// walks the drawn line off the bundle long before the turn — pink's
	// Tower 18 dive visibly split 87 m out. Emitting from the bend start
	// leaves the chain's first segment as a straight run from the cut
	// point to the curve: the line hugs the bundle until the steel bends.
	startArc := 0.0
	if total > 0.1 {
		for i, tv := range turnAt {
			if tv > 0.03 {
				startArc = math.Max(0, float64(i-1)*4)
				break
			}
		}
	}
	base := 0.0
	if total > 0.1 {
		base = turnAt[int(startArc/4)]
	}
	// o0 was measured at the connector's ORIGINAL start, back in the
	// lead-in. The lead-in converges toward the corridor, so applying that
	// stale offset at the bend start lands the curve's first point metres
	// off the corridor line — the chord from the cut point droops off the
	// bundle for its whole length. Re-measure at the emit start so the
	// curve begins exactly ON the corridor tail line.
	if startArc > 0 {
		es := l.AtArc(startArc)
		dir := l.TangentAtArc(startArc, 8)
		pa, _ := tl.ProjectArc(es)
		o0 = dir.Cross(tl.AtArc(pa).Sub(es))
		if math.Abs(o0) > 15 {
			return nil, 0, 0
		}
	}
	var out []geo.Pt
	i := 0
	for arc := 0.0; arc <= l.Len(); arc += 4 {
		if arc < startArc {
			i++
			continue
		}
		f := 1.0
		if total-base > 1e-9 {
			f = (turnAt[min(i, len(turnAt)-1)] - base) / (total - base)
		} else if l.Len()-startArc > 1e-9 {
			f = (arc - startArc) / (l.Len() - startArc)
		}
		o := o0*(1-f) + o1*f
		nrm := l.TangentAtArc(arc, 8).Perp()
		out = append(out, l.AtArc(arc).Add(nrm.Scale(o)))
		i++
	}
	if len(out) == 0 {
		return nil, 0, 0
	}
	// the connector rarely spans the whole gap between the cut points — the
	// caller bridges the rest with straight chords to p0/p3. When the
	// corridor's own line bends inside that span (the tip dives toward the
	// junction weld, or the approach curves), a chord SPREADS that bend
	// over the full cut — the drawn line peels off the bundle at the seam
	// instead of riding it to the neck. Ride the corridor pieces between
	// the cut points and the connector's ends instead: the transition then
	// hugs the bundle exactly until the steel takes over.
	qa, _ := tl.ProjectArc(out[0])
	var pre []geo.Pt
	for arc := 4.0; arc < qa-2; arc += 4 {
		pre = append(pre, tl.AtArc(arc))
	}
	qb, _ := hl.ProjectArc(out[len(out)-1])
	for arc := qb + 2; arc < hl.Len()-2; arc += 4 {
		out = append(out, hl.AtArc(arc))
	}
	if os.Getenv("PORTOLAN_DBG3") != "" && near.Dist(dbg3Pt) < 150 {
		fmt.Printf("TCB len=%.0f total=%.3f startArc=%.0f o0=%.1f o1=%.1f qa=%.0f/%.0f qb=%.0f/%.0f\n",
			l.Len(), total, startArc, o0, o1, qa, tl.Len(), qb, hl.Len())
	}
	return append(pre, out...), math.Max(0, tl.Len()-qa), math.Max(0, qb)
}

// trackParallelCorner finds a track (from the level index — every loaded
// track) passing the corner parallel to both arms and returns its curve
// between the arm projections, offset laterally by the drawn line's
// distance from it. Nil when no suitable track exists.
func trackParallelCorner(a, b, apex geo.Pt) []geo.Pt {
	if lvlGrid == nil {
		return nil
	}
	best := -1
	bestSpread := 10.0
	var bestOff float64
	lvlGrid.Near(apex, 25, func(ti int) {
		t := lvlLines[ti]
		if wayRailClass[lvlWays[ti]] == "street" {
			return // bus edges are street geometry already; rail never borrows a road corner
		}
		da := t.DistTo(a)
		db := t.DistTo(b)
		dx := t.DistTo(apex)
		if da > 25 || db > 25 || dx > 25 {
			return
		}
		// parallel means the offsets agree along the whole window
		spread := math.Max(da, math.Max(db, dx)) - math.Min(da, math.Min(db, dx))
		if spread < bestSpread {
			bestSpread = spread
			best = ti
			bestOff = (da + db) / 2
		}
	})
	if best < 0 {
		return nil
	}
	t := lvlLines[best]
	sa, _ := t.ProjectArc(a)
	sb, _ := t.ProjectArc(b)
	if math.Abs(sb-sa) < 10 || math.Abs(sb-sa) > 400 {
		return nil
	}
	sub := bundle.SubLine(t, math.Min(sa, sb), math.Max(sa, sb))
	pts := sub.Pts
	if sb < sa {
		pts = reversedPts(pts)
	}
	// signed offset: which side of the track is the drawn line on?
	arcA, _ := t.ProjectArc(a)
	tanA := t.TangentAtArc(arcA, 10)
	side := 1.0
	if tanA.Cross(a.Sub(t.AtArc(arcA))) < 0 {
		side = -1
	}
	if sb < sa {
		side = -side
	}
	o := side * bestOff
	l := geo.NewLine(pts)
	var out []geo.Pt
	step := 4.0
	for arc := 0.0; arc <= l.Len(); arc += step {
		q := l.AtArc(arc)
		nrm := l.TangentAtArc(arc, 8).Perp()
		out = append(out, q.Add(nrm.Scale(o)))
	}
	return out
}

// maxTurn12: worst turn at uniform 12 m sampling — the scale the eye
// (and the jaggedness gate) sees; dense vertices hide nothing here.
// straightenCone replaces near-straight runs with their chord: every
// interior point within tol of the chord AND every segment heading
// within a cone (coneDot = cos of the half-angle) of the chord heading.
// Endpoints never move; every dropped vertex is checked against the
// final chord, so deviation is hard-bounded by tol.
func straightenCone(pts []geo.Pt, tol, coneDot float64) []geo.Pt {
	if len(pts) < 3 {
		return pts
	}
	// per-segment norm/unit depend only on k, not the chord: compute once
	// (identical operands and guard order as the inline computation)
	segN := make([]float64, len(pts)-1)
	segU := make([]geo.Pt, len(pts)-1)
	for k := range segN {
		seg := pts[k+1].Sub(pts[k])
		segN[k] = seg.Norm()
		if segN[k] > 1e-9 {
			segU[k] = seg.Unit()
		}
	}
	out := []geo.Pt{pts[0]}
	i := 0
	for i < len(pts)-1 {
		best := i + 1
		for j := i + 2; j < len(pts); j++ {
			chord := pts[j].Sub(pts[i])
			cl := chord.Norm()
			if cl < 1e-9 {
				continue
			}
			u := chord.Scale(1 / cl)
			ok := true
			for k := i + 1; k < j && ok; k++ {
				if math.Abs(pts[k].Sub(pts[i]).Cross(u)) > tol {
					ok = false
				}
			}
			for k := i; k < j && ok; k++ {
				if segN[k] > 1e-9 && segU[k].Dot(u) < coneDot {
					ok = false
				}
			}
			if !ok {
				break
			}
			best = j
		}
		out = append(out, pts[best])
		i = best
	}
	return out
}

func maxTurn12(l *geo.Line) float64 {
	if l.Len() < 24 {
		return geo.MaxTurnDeg(l.Pts)
	}
	return geo.MaxTurnDeg(l.Resample(12))
}

func smoothPolyline(l *geo.Line) *geo.Line {
	return smoothPolylineScaled(l, 1)
}

// smoothPolylineScaled runs the smoothing kit with its fillet radius and
// Chaikin corner-cut cap scaled by s — street-running modes pass s < 1 so
// real street corners survive.
func smoothPolylineScaled(l *geo.Line, s float64) *geo.Line {
	out := smoothPolylineOnce(l, true, s)
	// insurance: fillet windows that overlap spatially can stitch a FOLD
	// into the line (a reversal sharper than anything in the input) —
	// fall back to plain corner cutting when that happens.
	if geo.MaxTurnDeg(out.Pts) > 100 {
		if alt := smoothPolylineOnce(l, false, s); geo.MaxTurnDeg(alt.Pts) < geo.MaxTurnDeg(out.Pts) {
			out = alt
		}
	}
	return out
}

func smoothPolylineOnce(l *geo.Line, fillets bool, s float64) *geo.Line {
	pts := l.Pts
	if len(pts) < 3 {
		return l
	}
	// drop vertices that deviate < 0.3 m from the chord of their
	// neighbors — straight runs collapse so corner cutting doesn't bloat
	// the output
	simp := []geo.Pt{pts[0]}
	for i := 1; i < len(pts)-1; i++ {
		if segDistPt(pts[i], simp[len(simp)-1], pts[i+1]) > 0.3 {
			simp = append(simp, pts[i])
		}
	}
	simp = append(simp, pts[len(pts)-1])
	// corner fillets stay clear of segment ends: the first/last stretch
	// of a steady is where transitions attach, computed on the raw line —
	// an arc there displaces the meeting geometry and the seam reads as
	// a disconnected stub.
	// (enforced inside filletCorners via endClear)
	// corner fillets: a REAL corner (sharp localized turn — the Loop's
	// 90° street corners) is drawn as a circular arc tangent to both
	// arms, the way a hand with a compass would round it. Gentle curves
	// (low per-vertex turns) never trigger and keep following the steel.
	if fillets && os.Getenv("PORTOLAN_NOFILLET") == "" {
		// minVertex 9 (was 15): chord straightening leaves corner curves as
		// ~11°-per-joint polylines — under 15° no window ever formed and the
		// capped Chaikin can't round long chords, so corners drew faceted
		// (the SW Loop chamfer). Cluster total ≥30° still gates: real
		// corners fillet, isolated drift joints don't.
		simp = filletCorners(simp, 25*s, 9, 30)
	}
	for round := 0; round < 3; round++ {
		if len(simp) < 3 {
			break
		}
		out := make([]geo.Pt, 0, 2*len(simp))
		out = append(out, simp[0])
		for i := 0; i+1 < len(simp); i++ {
			a, b := simp[i], simp[i+1]
			// corner-cut distance is CAPPED: Chaikin's 1/4 ratio assumes
			// dense (~6 m) vertices. On chord-straightened lines segments
			// run hundreds of metres and a plain 1/4 cut sweeps a real
			// curve elbow tens of metres off the steel (Union Square cut
			// 50 m west). Capping keeps dense behavior identical and
			// rounds sparse corners tightly, like a hand would.
			d := a.Dist(b)
			qf, rf := 0.25, 0.75
			if d > 32*s {
				qf = 8 * s / d
				rf = 1 - 8*s/d
			}
			q := geo.Lerp(a, b, qf)
			r := geo.Lerp(a, b, rf)
			if i == 0 {
				out = append(out, r)
			} else if i+2 == len(simp) {
				out = append(out, q)
			} else {
				out = append(out, q, r)
			}
		}
		out = append(out, simp[len(simp)-1])
		simp = out
	}
	return geo.NewLine(simp)
}

// filletCorners replaces clusters of consecutive sharp vertices (each
// turning >= minVertex deg, cluster total >= minTotal deg) with a
// circular arc of up to radius R tangent to the straightened arms — the
// way a hand with a compass rounds a corner. Every candidate is judged
// against the original window and the locally smoothest wins, so a
// wiggly seam that only looks like a corner is never made worse.
// Endpoints are never touched.
func filletCorners(pts []geo.Pt, r, minVertex, minTotal float64) []geo.Pt {
	n := len(pts)
	if n < 3 {
		return pts
	}
	turn := make([]float64, n)
	for i := 1; i < n-1; i++ {
		turn[i] = geo.TurnDeg(pts[i-1], pts[i], pts[i+1])
	}
	type window struct {
		lo, hi int
		sign   float64
		repl   []geo.Pt // nil = keep original
	}
	// clusters of sharp vertices; a chamfered corner (bend, straight
	// diagonal, bend) is ONE corner — merge clusters closer than 2R so a
	// single arc spans street-tangent to street-tangent instead of two
	// conflicting arcs fighting over the diagonal
	type cluster struct {
		i, j  int
		total float64
		apex  int
	}
	bendSign := func(k int) float64 {
		if k < 1 || k > n-2 {
			return 0
		}
		return pts[k].Sub(pts[k-1]).Cross(pts[k+1].Sub(pts[k]))
	}
	var cls []cluster
	{
		ci := 1
		for ci < n-1 {
			if turn[ci] < minVertex {
				ci++
				continue
			}
			cj, tot, ap := ci, 0.0, ci
			for cj < n-1 && turn[cj] >= minVertex {
				tot += turn[cj]
				if turn[cj] > turn[ap] {
					ap = cj
				}
				cj++
			}
			cls = append(cls, cluster{ci, cj, tot, ap})
			ci = cj
		}
		drop := make([]bool, len(cls))
		for x := 0; x+1 < len(cls); {
			if pts[cls[x+1].i].Dist(pts[cls[x].j-1]) < 2*r &&
				bendSign(cls[x].apex)*bendSign(cls[x+1].apex) > 0 {
				if turn[cls[x+1].apex] > turn[cls[x].apex] {
					cls[x].apex = cls[x+1].apex
				}
				cls[x].total += cls[x+1].total
				cls[x].j = cls[x+1].j
				drop = append(drop[:x+1], drop[x+2:]...)
				cls = append(cls[:x+1], cls[x+2:]...)
			} else {
				x++
			}
		}
		// adjacent OPPOSITE-sign clusters are an S-curve, not corners —
		// two tangent arcs meet at a kink there; leave the S alone
		for x := 0; x+1 < len(cls); x++ {
			if pts[cls[x+1].i].Dist(pts[cls[x].j-1]) < 2*r &&
				bendSign(cls[x].apex)*bendSign(cls[x+1].apex) < 0 {
				drop[x] = true
				drop[x+1] = true
			}
		}
		kept := cls[:0]
		for x, cl := range cls {
			if !drop[x] {
				kept = append(kept, cl)
			}
		}
		cls = kept
	}
	var wins []window
	for _, cl := range cls {
		i, j, total, apex := cl.i, cl.j, cl.total, cl.apex
		if total < minTotal {
			continue
		}
		lo, hi := i, j-1
		// keep clear of segment ends: transitions attach there on the
		// raw geometry
		arcToStart, arcToEnd := 0.0, 0.0
		for k := 1; k <= apex; k++ {
			arcToStart += pts[k].Dist(pts[k-1])
		}
		for k := apex + 1; k < n; k++ {
			arcToEnd += pts[k].Dist(pts[k-1])
		}
		if arcToStart < 30 || arcToEnd < 30 {
			continue
		}
		minLo := 1
		if len(wins) > 0 {
			minLo = wins[len(wins)-1].hi + 2
		}
		// swallow the corner's gentle shoulders too — a real track
		// corner tapers in and out, and an arc that replaces only the
		// apex leaves chord-and-bend residue on both sides
		for lo > minLo && (pts[lo-1].Dist(pts[apex]) < r ||
			(turn[lo-1] >= 5 && pts[lo-1].Dist(pts[apex]) < 1.8*r)) {
			lo--
		}
		for hi < n-2 && (pts[hi+1].Dist(pts[apex]) < r ||
			(turn[hi+1] >= 5 && pts[hi+1].Dist(pts[apex]) < 1.8*r)) {
			hi++
		}
		if lo < 1 || hi > n-2 {
			i = j
			continue
		}
		a, b := pts[lo-1], pts[hi+1]
		orig := append(append([]geo.Pt{a}, pts[lo:hi+1]...), b)
		var best []geo.Pt
		bestScore := maxTurn12(geo.NewLine(orig))
		// classical street-corner fillet: the arms are the ENTRY and EXIT
		// LINES (the segments crossing the window boundary — the streets
		// themselves, not chords across the curved cluster). The arc is
		// tangent to both lines at distance d from their intersection, so
		// its ends lie exactly ON the streets and the connecting chords
		// carry no lateral drift.
		u1 := pts[apex].Sub(a).Unit()
		if lo < apex {
			u1 = pts[lo].Sub(a).Unit()
		}
		u2 := b.Sub(pts[apex]).Unit()
		if hi > apex {
			u2 = b.Sub(pts[hi]).Unit()
		}
		cosT := u1.Dot(u2)
		theta := math.Acos(math.Max(-1, math.Min(1, cosT)))
		den := u1.Cross(u2)
		if theta > 0.05 && math.Abs(den) > 1e-9 {
			// virtual apex: intersection of line(a,u1) and line(b,u2)
			ab := b.Sub(a)
			t1 := ab.Cross(u2) / den
			vx := a.Add(u1.Scale(t1))
			armA := vx.Dist(a)
			armB := vx.Dist(b)
			if vx.Dist(pts[apex]) > 3*r {
				// nearly-straight arms: the line intersection shoots far
				// from the real bend and any arc built there lands outside
				// the window, stitching a RETRACE into the polyline
				continue
			}
			sign := 1.0
			if den < 0 {
				sign = -1
			}
			dBase := math.Min(r*math.Tan(theta/2),
				0.45*math.Min(armA, armB))
			for _, scale := range []float64{1, 0.5, 0.25} {
				d := dBase * scale
				rEff := d / math.Tan(theta/2)
				if rEff < 12 {
					break // below drawing scale — an arc this tight reads as a kink
				}
				p1 := vx.Sub(u1.Scale(d))
				nrm1 := geo.Pt{X: -u1.Y * sign, Y: u1.X * sign}
				c := p1.Add(nrm1.Scale(rEff))
				a0 := math.Atan2(p1.Y-c.Y, p1.X-c.X)
				steps := int(math.Max(4, theta*rEff/3))
				arc := make([]geo.Pt, 0, steps+1)
				for st := 0; st <= steps; st++ {
					ang := a0 + sign*theta*float64(st)/float64(steps)
					arc = append(arc, geo.Pt{X: c.X + rEff*math.Cos(ang), Y: c.Y + rEff*math.Sin(ang)})
				}
				// reject any arc whose joints reverse direction — an arc
				// overshooting the window boundary retraces when stitched
				if len(arc) >= 2 {
					inOK := arc[0].Sub(a).Dot(arc[1].Sub(arc[0])) > 0
					outOK := b.Sub(arc[len(arc)-1]).Dot(arc[len(arc)-1].Sub(arc[len(arc)-2])) > 0
					if !inOK || !outOK {
						continue
					}
				}
				cand := append(append([]geo.Pt{a}, arc...), b)
				if sc := maxTurn12(geo.NewLine(cand)); sc < bestScore {
					bestScore = sc
					best = arc
				}
			}
		}
		// fallback candidate: local corner cutting of the original
		// window — when no arc fits (curved arms), this still beats the
		// raw elbow
		{
			cc := append([]geo.Pt(nil), orig...)
			for round := 0; round < 3; round++ {
				if len(cc) < 3 {
					break
				}
				nxt := []geo.Pt{cc[0]}
				for x := 0; x+1 < len(cc); x++ {
					q := geo.Lerp(cc[x], cc[x+1], 0.25)
					rr := geo.Lerp(cc[x], cc[x+1], 0.75)
					if x == 0 {
						nxt = append(nxt, rr)
					} else if x+2 == len(cc) {
						nxt = append(nxt, q)
					} else {
						nxt = append(nxt, q, rr)
					}
				}
				nxt = append(nxt, cc[len(cc)-1])
				cc = nxt
			}
			if sc := maxTurn12(geo.NewLine(cc)); sc < bestScore {
				bestScore = sc
				best = cc[1 : len(cc)-1]
			}
		}
		_ = i
		_ = j
		// TRACK-PARALLEL candidate: the corner the steel actually makes.
		// Find a ridden track running parallel to both arms, take its
		// curve through the corner, and offset it laterally by the drawn
		// line's distance from it — the drawn corner then matches the
		// real track curve exactly.
		if tp := trackParallelCorner(a, b, pts[apex]); tp != nil {
			cand := append(append([]geo.Pt{a}, tp...), b)
			if sc := maxTurn12(geo.NewLine(cand)); sc < bestScore {
				bestScore = sc
				best = tp
			}
		}
		if best != nil {
			if os.Getenv("PORTOLAN_DBGF") != "" {
				span := pts[hi+1].Dist(pts[lo-1])
				if pts[apex].Dist(dbg3Pt) < 400 {
					fmt.Printf("FWIN apex=%d span=%.0fm win=[%d,%d]/%d score=%.1f arcpts=%d theta-arms a=(%.1f,%.1f) apex=(%.1f,%.1f) b=(%.1f,%.1f)\n",
						apex, span, lo, hi, n, bestScore, len(best),
						pts[lo-1].X-pts[apex].X, pts[lo-1].Y-pts[apex].Y,
						pts[apex].X, pts[apex].Y,
						pts[hi+1].X-pts[apex].X, pts[hi+1].Y-pts[apex].Y)
					for z := lo - 1; z <= hi+1 && z < n; z++ {
						fmt.Printf("   pts[%d]=(%.1f,%.1f) turn=%.0f\n", z, pts[z].X-pts[apex].X, pts[z].Y-pts[apex].Y, turn[z])
					}
					for z, q := range best {
						if z%4 == 0 {
							fmt.Printf("   arc[%d]=(%.1f,%.1f)\n", z, q.X-pts[apex].X, q.Y-pts[apex].Y)
						}
					}
				}
			}
			wins = append(wins, window{lo, hi, bendSign(apex), best})
		}
	}
	if len(wins) == 0 {
		return pts
	}
	// consecutive windows that ended up ADJACENT with opposite bend are
	// an S-curve after expansion — their tangent arcs meet at a kink.
	// Drop both replacements and let corner cutting handle the S.
	for x := 0; x+1 < len(wins); x++ {
		gap := 0.0
		for k := wins[x].hi + 1; k <= wins[x+1].lo && k < n; k++ {
			if k > 0 {
				gap += pts[k].Dist(pts[k-1])
			}
		}
		if gap < 20 && wins[x].sign*wins[x+1].sign < 0 {
			wins[x].repl = nil
			wins[x+1].repl = nil
		}
	}
	{
		kept := wins[:0]
		for _, w := range wins {
			if w.repl != nil {
				kept = append(kept, w)
			}
		}
		wins = kept
		if len(wins) == 0 {
			return pts
		}
	}
	var out []geo.Pt
	k := 0
	for _, w := range wins {
		for ; k < w.lo; k++ {
			out = append(out, pts[k])
		}
		// stitch-time fold check: if joining this window's replacement
		// reverses direction against either neighbor, keep the original
		// span instead — overlapping replacements must not retrace
		ok := len(w.repl) >= 2
		if ok && w.lo >= 1 {
			inDir := w.repl[0].Sub(pts[w.lo-1])
			if inDir.Dot(w.repl[1].Sub(w.repl[0])) <= 0 {
				ok = false
			}
		}
		if ok && w.hi+1 < n {
			last := w.repl[len(w.repl)-1]
			outDir := pts[w.hi+1].Sub(last)
			if outDir.Dot(last.Sub(w.repl[len(w.repl)-2])) <= 0 {
				ok = false
			}
		}
		if ok {
			out = append(out, w.repl...)
		} else {
			for z := w.lo; z <= w.hi; z++ {
				out = append(out, pts[z])
			}
		}
		k = w.hi + 1
	}
	for ; k < n; k++ {
		out = append(out, pts[k])
	}
	return out
}

func segDistPt(p, a, b geo.Pt) float64 {
	ab := b.Sub(a)
	t := p.Sub(a).Dot(ab) / math.Max(1e-12, ab.Dot(ab))
	t = math.Max(0, math.Min(1, t))
	return p.Dist(a.Add(ab.Scale(t)))
}

func sharedRoutes(a, b []string) []string {
	var out []string
	for _, x := range a {
		for _, y := range b {
			if x == y {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

func shareRoute(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func boolIdx(atTo bool) int {
	if atTo {
		return 1
	}
	return 0
}

// approachTurn measures the total heading change over the last cut meters
// approaching an end (end=true → the To end). The final 20 m are excluded
// when the cut allows: junction nodes sit metres off the corridor axis
// (weld centroids), and the tail's bend TO the node is weld furniture,
// not real curvature — counting it made the shrink loop cut exactly
// where the droop most needed cutting away (the SE-corner chevron).
func approachTurn(l *geo.Line, toEnd bool, cutLen float64) float64 {
	margin := 0.0
	if cutLen > 28 {
		margin = 20
	}
	var a, b float64
	if toEnd {
		a, b = math.Max(0, l.Len()-cutLen), l.Len()-margin
	} else {
		a, b = margin, math.Min(cutLen, l.Len())
	}
	t1 := l.TangentAtArc(a, 8)
	t2 := l.TangentAtArc(b, 8)
	d := math.Max(-1, math.Min(1, t1.Dot(t2)))
	return math.Acos(d) * 180 / math.Pi
}

// endPiece is the last cut meters of a line oriented TOWARD the node.
func endPiece(l *geo.Line, nodeAtTo bool, cut float64) []geo.Pt {
	if nodeAtTo {
		return bundle.SubLine(l, l.Len()-cut, l.Len()).Pts
	}
	return reversedPts(bundle.SubLine(l, 0, cut).Pts)
}

// startPiece is the first cut meters of a line oriented AWAY from the node.
func startPiece(l *geo.Line, nodeAtFrom bool, cut float64) []geo.Pt {
	if nodeAtFrom {
		return bundle.SubLine(l, 0, cut).Pts
	}
	return reversedPts(bundle.SubLine(l, l.Len()-cut, l.Len()).Pts)
}

func chainPts(a, b []geo.Pt) []geo.Pt {
	out := append([]geo.Pt(nil), a...)
	for _, q := range b {
		if len(out) > 0 && out[len(out)-1].Dist(q) < 1e-9 {
			continue
		}
		out = append(out, q)
	}
	return out
}

// blendFreeArea clears the node's free area — every chain vertex within
// freeR of the junction vertex (index k) — and reconnects the two clean
// sides with a quadratic blend through the honest tangent intersection:
// G1 at both seams, hook-free by construction (the control point is the
// tangent-ray meet, clamped; nearly-parallel tangents degrade to a straight
// ease). This is the Brosi–Bast free-node-area / Transit-blog reconnect,
// and it also swallows the refinement's pinned-end wiggle near the node.
func blendFreeArea(pts []geo.Pt, k int, freeR float64) []geo.Pt {
	if k <= 0 || k >= len(pts)-1 || freeR < 1 {
		return pts
	}
	pre := geo.NewLine(reversedPts(pts[:k+1])) // corner → chain start
	post := geo.NewLine(pts[k:])               // corner → chain end
	d := math.Min(freeR, math.Min(pre.Len(), post.Len())*0.9)
	if d < 2 {
		return pts
	}
	p1 := pre.AtArc(d)
	p2 := post.AtArc(d)
	// tangents pointing INTO the free area from each seam
	t1 := pre.TangentAtArc(d, 8).Scale(-1)
	t2 := post.TangentAtArc(d, 8).Scale(-1)
	// circular-arc reconnection (Transit blog: arcs, never béziers with
	// free arms — a quadratic concentrates its curvature at the apex and
	// spikes the jaggedness gate). The circle-approximating cubic with
	// handles (4/3)·tan(θ/4)·R spreads curvature evenly; the forward
	// ray-meet requirement is the hook guard.
	travel2 := t2.Scale(-1) // direction of travel leaving at p2
	cosA := math.Max(-1, math.Min(1, t1.Dot(travel2)))
	theta := math.Acos(cosA)
	var blend []geo.Pt
	const steps = 14
	if _, ok := rayMeet(p1, t1, p2, t2); !ok || theta < 0.05 {
		// straight-through or opposing arms: plain eased segment
		for i := 0; i <= steps; i++ {
			blend = append(blend, geo.Lerp(p1, p2, float64(i)/steps))
		}
	} else {
		r := d / math.Tan(theta/2)
		h := math.Min(4.0/3.0*math.Tan(theta/4)*r, d)
		c1 := p1.Add(t1.Scale(h))
		c2 := p2.Add(t2.Scale(h))
		for i := 0; i <= steps; i++ {
			t := float64(i) / steps
			a := geo.Lerp(p1, c1, t)
			b := geo.Lerp(c1, c2, t)
			c := geo.Lerp(c2, p2, t)
			blend = append(blend, geo.Lerp(geo.Lerp(a, b, t), geo.Lerp(b, c, t), t))
		}
	}
	keepPre := pre.Len() - d
	out := []geo.Pt{pts[0]}
	acc := 0.0
	for i := 1; i <= k; i++ {
		acc += pts[i].Dist(pts[i-1])
		if acc < keepPre {
			out = append(out, pts[i])
		}
	}
	out = append(out, blend...)
	accPost := 0.0
	for i := k + 1; i < len(pts); i++ {
		accPost += pts[i].Dist(pts[i-1])
		if accPost > d {
			out = append(out, pts[i])
		}
	}
	return out
}

// rayMeet intersects two rays (p + s·d, s ≥ 0); ok only when both rays
// reach the meet going FORWARD — an arm opposing the chord never yields a
// control point (the hook guard).
func rayMeet(p1, d1, p2, d2 geo.Pt) (geo.Pt, bool) {
	den := d1.Cross(d2)
	if math.Abs(den) < 1e-9 {
		return geo.Pt{}, false
	}
	qp := p2.Sub(p1)
	s := qp.Cross(d2) / den
	u := qp.Cross(d1) / den
	if s <= 0 || u <= 0 {
		return geo.Pt{}, false
	}
	return p1.Add(d1.Scale(s)), true
}
