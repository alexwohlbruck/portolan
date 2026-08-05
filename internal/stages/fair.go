package stages

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
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
		GapPx:       dial("fair_gap_px", 6),
		MaxTurn:     dial("fair_max_turn", 30),
		FilletR:     dial("fair_fillet_r", 30),
		MinShortCut: 12,
	}
}

// bands: emit one full copy per zoom band; the free area and fillet double
// per zoom-out so on-screen junction size stays constant (LESSONS #17).
var fairBands = []struct {
	min, max int
	scale    float64
}{
	{15, 24, 1},
	{14, 15, 2},
	{13, 14, 4},
	{0, 13, 8},
}

// Fair draws the junction connections with smooth curvature between slot
// positions (circular arcs preserve parallelism) and cuts zoom bands.
func Fair(n *Network, slots map[int][]string, routes map[string]gtfs.Route) ([]Segment, error) {
	p := defaultFairParams()

	colorOf := func(rid string) string {
		c := routes[rid].Color
		if c == "" {
			c = "888888"
		}
		return c
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
	// storage-frame px offset of a color on an edge
	offsetPx := func(ei int, color string) float64 {
		s, ns := slotOf(ei, color)
		return (float64(s) - float64(ns-1)/2) * p.GapPx
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
		out := ""
		for _, r := range colorRoutes[ei][color] {
			sn := routes[r].ShortName
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

	// through-pairs per node/color: two incident edges sharing a route id of
	// that color really carry it through (walks are continuous — Law 1)
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
					if !shareRoute(ras, rbs) {
						continue
					}
					throughs[ni][c] = append(throughs[ni][c], pair{a, b})
				}
			}
		}
	}

	var segs []Segment
	for _, band := range fairBands {
		cut := make([][2]float64, len(n.Edges)) // per edge: cut at From, To
		lines := make([]*geo.Line, len(n.Edges))
		for ei, e := range n.Edges {
			lines[ei] = geo.NewLine(e.Pts)
			base := p.CutBase * band.scale
			for end := 0; end < 2; end++ {
				c := math.Min(base, lines[ei].Len()/3)
				// a curvy approach is real geometry — shrink the cut
				// rather than lay a synthetic connector across it
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
			a, cur           int
			aAtTo, bAtFrom   bool
			color            string
			mid              []geo.Pt
			mids             [][2]int
		}
		var cands []cand
		for ni := range n.Nodes {
			for color, prs := range throughs[ni] {
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
					cands = append(cands, cand{a, cur, aAtTo, bAtFrom, color, mid, mids})
				}
			}
		}
		// longest chains first: a chained connection claims the fragment
		// span before the fragment's own direct pairs are considered
		sort.SliceStable(cands, func(i, j int) bool {
			return len(cands[i].mids) > len(cands[j].mids)
		})
		for _, c := range cands {
			a, cur := c.a, c.cur
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
				continue
			}
			tail := endPiece(lines[a], c.aAtTo, cut[a][boolIdx(c.aAtTo)])
			head := startPiece(lines[cur], c.bAtFrom, cut[cur][boolIdx(!c.bAtFrom)])
			chain := chainPts(tail, c.mid)
			chain = chainPts(chain, head)
			if len(chain) < 2 {
				continue
			}
			// opposing end tangents are a topology decision, not a
			// smoothing problem (the Grand Central lesson): leave
			// both sides as steady stubs, never a hairpin connector
			tl, hl := geo.NewLine(tail), geo.NewLine(head)
			if tl.Len() > 2 && hl.Len() > 2 &&
				tl.TangentAtArc(tl.Len(), 10).Dot(hl.TangentAtArc(0, 10)) < -0.5 {
				continue
			}
			freeR := math.Min(p.FilletR*band.scale,
				0.8*math.Min(tl.Len(), hl.Len()))
			chain = blendFreeArea(chain, len(tail)-1, freeR)
			segs = append(segs, Segment{
				Kind:      "transition",
				Color:     c.color,
				Routes:    colorRoutes[a][c.color],
				Label:     label(a, c.color),
				RouteType: routeType(a, c.color),
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
			for color := range colorRoutes[ei] {
				ci := colorIdx(ei, color)
				if absorbed[[2]int{ei, ci}] {
					continue // fully covered by a chained transition
				}
				from := cut[ei][0]
				if !served[[3]int{ei, 0, ci}] {
					from = 0
				}
				to := cut[ei][1]
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
					Color:     color,
					Routes:    colorRoutes[ei][color],
					Label:     label(ei, color),
					RouteType: routeType(ei, color),
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
	for i := range segs {
		segs[i].Line = smoothPolyline(segs[i].Line)
	}
	return segs, nil
}

// smoothPolyline: collinear-run simplification followed by two rounds of
// endpoint-pinned Chaikin corner cutting.
func smoothPolyline(l *geo.Line) *geo.Line {
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
	for round := 0; round < 2; round++ {
		if len(simp) < 3 {
			break
		}
		out := make([]geo.Pt, 0, 2*len(simp))
		out = append(out, simp[0])
		for i := 0; i+1 < len(simp); i++ {
			a, b := simp[i], simp[i+1]
			q := geo.Lerp(a, b, 0.25)
			r := geo.Lerp(a, b, 0.75)
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

func segDistPt(p, a, b geo.Pt) float64 {
	ab := b.Sub(a)
	t := p.Sub(a).Dot(ab) / math.Max(1e-12, ab.Dot(ab))
	t = math.Max(0, math.Min(1, t))
	return p.Dist(a.Add(ab.Scale(t)))
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
// approaching an end (end=true → the To end).
func approachTurn(l *geo.Line, toEnd bool, cutLen float64) float64 {
	var a, b float64
	if toEnd {
		a, b = math.Max(0, l.Len()-cutLen), l.Len()
	} else {
		a, b = 0, math.Min(cutLen, l.Len())
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
