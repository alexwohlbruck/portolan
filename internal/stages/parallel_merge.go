package stages

// Multitrack corridor merge — the partial-overlap rule.
//
// The weld in trunkweld.go collapses a same-trunk strand only when the
// WHOLE of it shadows another edge. Right for a siding beside a spine,
// useless for a corridor: the Montauk Branch runs beside the LIRR main
// for 500 m through Jamaica and then leaves for Long Island, so 13 km
// of honest divergence vetoes the merge the yard plainly wants. Real
// multitrack corridors are all like this — lines share steel for a
// stretch and part company, and the stretch is what should draw as one
// trunk.
//
// So this pass works on INTERVALS. For each same-trunk edge pair it
// finds the longest sustained run within gauge, splits both edges at
// the run's boundaries, and replaces the two middle pieces with one
// median line. The outer pieces keep their own geometry:
//
//	  ═╗                 ╔═     separate approaches
//	   ╠═════════════════╣      one trunk over the shared stretch
//	  ═╝                 ╚═     separate departures
//
// NODE IDENTITY is the part that failed the first time and is now the
// heart of the pass: the first attempt minted fresh nodes for the trunk
// ends unconditionally, so wherever the overlap reached a parent's own
// end the parent's real node — with everything else attached to it —
// was left behind, and the corridor severed exactly there (Jamaica came
// out as floating stubs). The rule now:
//
//   - overlap ends mid-edge            → mint a node there;
//   - overlap reaches ONE parent's end → the trunk inherits that node;
//   - overlap reaches BOTH parents'
//     ends                             → their two nodes weld into one,
//     every referencing edge rewired.
//
// A weld is what actually fuses a terminal ladder: the tails end on
// nearby-but-distinct nodes, and inheriting only one of them would
// leave the other dangling again.
//
// Guards: same sole trunk only (E/Z/J can never join the LIRR however
// close they run); the run must be sustained (split_corridor_run); the
// two intervals must be comparable so a degenerate projection is
// rejected; and the median must stay within gauge of both parents,
// which fails exactly when the "parallel" run is a lens around
// something real.

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
)

// bboxNear: cheap reject before any sampling.
func bboxNear(a, b *geo.Line, pad float64) bool {
	ax0, ay0, ax1, ay1 := lineBox(a)
	bx0, by0, bx1, by1 := lineBox(b)
	return ax0-pad <= bx1 && bx0-pad <= ax1 && ay0-pad <= by1 && by0-pad <= ay1
}

func lineBox(l *geo.Line) (x0, y0, x1, y1 float64) {
	x0, y0 = math.Inf(1), math.Inf(1)
	x1, y1 = math.Inf(-1), math.Inf(-1)
	for _, p := range l.Pts {
		x0, y0 = math.Min(x0, p.X), math.Min(y0, p.Y)
		x1, y1 = math.Max(x1, p.X), math.Max(y1, p.Y)
	}
	return
}

// taperTo bends one end of a polyline onto a target point with a
// smoothstep lateral blend over `span` metres. This is what makes every
// merge seam a Y: the first version bare-appended the median endpoint
// to each approach piece, a hard lateral jog of up to half the gauge
// drawn as an elbow — Jamaica's joins read as lollipops and hooks, and
// every trunk seam carried a jog that showed as corridor wobble.
func taperTo(pts []geo.Pt, target geo.Pt, atStart bool, span float64) []geo.Pt {
	out := append([]geo.Pt{}, pts...)
	if len(out) < 2 {
		return out
	}
	endIdx := len(out) - 1
	if atStart {
		endIdx = 0
	}
	delta := target.Sub(out[endIdx])
	if delta.Norm() < 0.5 {
		out[endIdx] = target
		return out
	}
	// arc distances from the tapered end
	arc := make([]float64, len(out))
	if atStart {
		for i := 1; i < len(out); i++ {
			arc[i] = arc[i-1] + out[i].Dist(out[i-1])
		}
	} else {
		for i := len(out) - 2; i >= 0; i-- {
			arc[i] = arc[i+1] + out[i].Dist(out[i+1])
		}
	}
	total := arc[0]
	if !atStart {
		total = arc[0]
	}
	_ = total
	for i := range out {
		if arc[i] >= span {
			continue
		}
		t := 1 - arc[i]/span
		w := t * t * (3 - 2*t) // smoothstep: tangent-continuous at both ends
		out[i] = out[i].Add(delta.Scale(w))
	}
	return out
}

// overlapRun: the longest interval of `a` staying within gauge of `b`.
// Single out-of-gauge samples don't end a run — a corridor widens around
// a platform and closes again, and splitting there is the confetti this
// pass exists to remove.
func overlapRun(a, b *geo.Line, gauge, step, minRun float64) (lo, hi float64, ok bool) {
	n := int(a.Len()/step) + 1
	inside := make([]bool, n+1)
	for i := 0; i <= n; i++ {
		inside[i] = b.DistTo(a.AtArc(math.Min(float64(i)*step, a.Len()))) <= gauge
	}
	for i := 1; i < n; i++ {
		if !inside[i] && inside[i-1] && inside[i+1] {
			inside[i] = true
		}
	}
	bestLo, bestHi := 0.0, 0.0
	for i := 0; i <= n; {
		if !inside[i] {
			i++
			continue
		}
		j := i
		for j+1 <= n && inside[j+1] {
			j++
		}
		l := math.Min(float64(i)*step, a.Len())
		h := math.Min(float64(j)*step, a.Len())
		if h-l > bestHi-bestLo {
			bestLo, bestHi = l, h
		}
		i = j + 1
	}
	if bestHi-bestLo < minRun {
		return 0, 0, false
	}
	// trim the ends back to genuinely PARALLEL separation. The gauge asks
	// "could these merge here"; the ends of a run answer a different
	// question — a junction's divergence fan stays under the gauge for
	// its first 150-200 m while separating monotonically, and merging
	// that stretch drags the trunk past the real junction (the 149 St
	// green loop pinched into a buttonhook: the 4·5 trunk grew 140 m
	// into the fan and the 5's transition had to hairpin back). A
	// corridor hovers well inside the gauge; a fan only ever brushes it.
	for bestHi-bestLo > 0 && b.DistTo(a.AtArc(bestHi)) > gauge*0.6 {
		bestHi -= step
	}
	for bestHi-bestLo > 0 && b.DistTo(a.AtArc(bestLo)) > gauge*0.6 {
		bestLo += step
	}
	if bestHi-bestLo < minRun {
		return 0, 0, false
	}
	return bestLo, bestHi, true
}

// MergeParallelCorridors runs to fixpoint. Returns merges performed.
func MergeParallelCorridors(net *Network, routes map[string]gtfs.Route) int {
	gauge := dial("split_corridor_merge", 90)
	minRun := dial("split_corridor_run", 180)
	const step = 20.0
	const endSnap = 30.0 // an overlap this close to a parent's end IS the end

	trunkOf := func(rid string) string {
		r, ok := routes[rid]
		if !ok || !mode.Of(r.Type).Trunked() {
			return ""
		}
		return mode.TrunkKey(r)
	}
	soleTrunk := func(e *Edge) string {
		t := ""
		for _, rid := range e.Routes {
			k := trunkOf(rid)
			if k == "" || (t != "" && k != t) {
				return ""
			}
			t = k
		}
		return t
	}
	unionRoutes := func(a, b *Edge) []string {
		seen := map[string]bool{}
		var out []string
		for _, r := range append(append([]string{}, a.Routes...), b.Routes...) {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		sort.Strings(out)
		return out
	}
	unionActs := func(a, b *Edge) map[string]gtfs.Mask168 {
		if a.Acts == nil && b.Acts == nil {
			return nil
		}
		out := map[string]gtfs.Mask168{}
		for r, m := range a.Acts {
			out[r] = out[r].Or(m)
		}
		for r, m := range b.Acts {
			out[r] = out[r].Or(m)
		}
		return out
	}

	merges := 0
	for pass := 0; pass < 400; pass++ {
		rebuildAdj(net)
		lines := make([]*geo.Line, len(net.Edges))
		for i := range net.Edges {
			if !net.Edges[i].Gap && len(net.Edges[i].Pts) >= 2 && net.Edges[i].From != net.Edges[i].To {
				lines[i] = geo.NewLine(net.Edges[i].Pts)
			}
		}
		type plan struct {
			ai, bi          int
			lo, hi          float64
			bLo, bHi        float64
			rev             bool
			mid             []geo.Pt
		}
		var pl *plan
		for ai := 0; ai < len(net.Edges) && pl == nil; ai++ {
			la := lines[ai]
			if la == nil || la.Len() < minRun {
				continue
			}
			ta := soleTrunk(&net.Edges[ai])
			if ta == "" {
				continue
			}
			for bi := ai + 1; bi < len(net.Edges); bi++ {
				lb := lines[bi]
				if lb == nil || lb.Len() < minRun || soleTrunk(&net.Edges[bi]) != ta {
					continue
				}
				// a LENS — two edges between one node pair — is structure,
				// not a corridor: a balloon loop's legs are exactly this
				// shape and merging them pinched the 149 St green loop
				// into a buttonhook. Same-route lenses belong to
				// mergeDirectionLenses with its significance bar; the
				// rest are real (opposing one-way sides, terminal loops).
				ef, et := net.Edges[ai].From, net.Edges[ai].To
				bf, bt := net.Edges[bi].From, net.Edges[bi].To
				if (ef == bf && et == bt) || (ef == bt && et == bf) {
					continue
				}
				if !bboxNear(la, lb, gauge) {
					continue
				}
				lo, hi, ok := overlapRun(la, lb, gauge, step, minRun)
				if !ok {
					continue
				}
				bLoArc, _ := lb.ProjectArc(la.AtArc(lo))
				bHiArc, _ := lb.ProjectArc(la.AtArc(hi))
				rev := bHiArc < bLoArc
				bLo, bHi := bLoArc, bHiArc
				if rev {
					bLo, bHi = bHiArc, bLoArc
				}
				if bHi-bLo < minRun*0.5 {
					continue
				}
				if r := (hi - lo) / math.Max(1, bHi-bLo); r < 0.5 || r > 2 {
					continue
				}
				npts := int((hi-lo)/10) + 2
				mid := make([]geo.Pt, 0, npts)
				for k := 0; k < npts; k++ {
					f := float64(k) / float64(npts-1)
					pa := la.AtArc(lo + f*(hi-lo))
					var pb geo.Pt
					if rev {
						pb = lb.AtArc(bHi - f*(bHi-bLo))
					} else {
						pb = lb.AtArc(bLo + f*(bHi-bLo))
					}
					mid = append(mid, geo.Lerp(pa, pb, 0.5))
				}
				ml := geo.NewLine(mid)
				if ml.Len() < minRun*0.5 {
					continue
				}
				bad := false
				for _, q := range ml.Resample(20) {
					if la.DistTo(q) > gauge || lb.DistTo(q) > gauge {
						bad = true
						break
					}
				}
				if bad {
					continue
				}
				pl = &plan{ai, bi, lo, hi, bLo, bHi, rev, mid}
				break
			}
		}
		if pl == nil {
			break
		}

		la, lb := lines[pl.ai], lines[pl.bi]
		// weld node nb into na: every edge referencing nb moves to na.
		weld := func(na, nb int) int {
			if na == nb {
				return na
			}
			for k := range net.Edges {
				if net.Edges[k].From == nb {
					net.Edges[k].From = na
				}
				if net.Edges[k].To == nb {
					net.Edges[k].To = na
				}
			}
			return na
		}
		// parent end nodes reached by the overlap, -1 when it ends mid-edge
		aStart, aEnd := -1, -1
		if pl.lo < endSnap {
			aStart = net.Edges[pl.ai].From
		}
		if la.Len()-pl.hi < endSnap {
			aEnd = net.Edges[pl.ai].To
		}
		bStart, bEnd := -1, -1
		if pl.bLo < endSnap {
			bStart = net.Edges[pl.bi].From
		}
		if lb.Len()-pl.bHi < endSnap {
			bEnd = net.Edges[pl.bi].To
		}
		// map b's arc ends onto the trunk's travel ends
		bAtTrunkStart, bAtTrunkEnd := bStart, bEnd
		if pl.rev {
			bAtTrunkStart, bAtTrunkEnd = bEnd, bStart
		}
		resolve := func(nA, nB int, at geo.Pt) int {
			switch {
			case nA >= 0 && nB >= 0:
				return weld(nA, nB)
			case nA >= 0:
				return nA
			case nB >= 0:
				return nB
			}
			// NO node-snap here. It was tried (inherit any same-trunk node
			// within 45 m instead of minting) to close the Jamaica whisker
			// gap — but PinEdgeTips is what actually closed it, and the
			// snap PINCHED things that are genuinely two nodes ~40 m
			// apart: the 149 St green loop's mouth folded into a
			// buttonhook and the Mott Haven wye funnelled three strands
			// into one point, drawing a braid. Distinct nearby nodes are
			// often real (a balloon mouth IS two nodes); fusing belongs
			// to the weld rules that check geometry, never to proximity.
			net.Nodes = append(net.Nodes, Node{At: at})
			return len(net.Nodes) - 1
		}
		n1 := resolve(aStart, bAtTrunkStart, pl.mid[0])
		n2 := resolve(aEnd, bAtTrunkEnd, pl.mid[len(pl.mid)-1])
		// the trunk must END at its nodes: an inherited or welded node
		// keeps the parent's real position, which can sit half a gauge
		// from the median's lerp endpoint — every ribbon at that node
		// then jogged sideways to reach it. Bend the median onto the node
		// positions instead.
		// the lerp median inherits every crossover jiggle of its parents;
		// smooth it BEFORE pinning the ends onto the nodes
		pl.mid = smoothPolyline(geo.NewLine(pl.mid)).Pts
		t1, t2 := net.Nodes[n1].At, net.Nodes[n2].At
		span := math.Min(150, geo.NewLine(pl.mid).Len()*0.4)
		pl.mid = taperTo(pl.mid, t1, true, span)
		pl.mid = taperTo(pl.mid, t2, false, span)
		if n1 == n2 {
			// the whole pair collapsed to a point — a lens/balloon, not a
			// corridor. Leave it for the loop-aware rules.
			lines[pl.ai] = nil
			continue
		}

		ea, eb := net.Edges[pl.ai], net.Edges[pl.bi]
		var add []Edge
		// approach and departure pieces bend ONTO the trunk node with the
		// same smoothstep taper the median uses — a Y arm, never an elbow
		outer := func(src Edge, l *geo.Line, from, to float64,
			fromNode, toNode int, nodeAtStart bool, node geo.Pt) {
			if to-from < endSnap {
				return
			}
			sub := bundle.SubLine(l, from, to)
			sp := math.Min(150, sub.Len()*0.6)
			pts := taperTo(sub.Pts, node, nodeAtStart, sp)
			ne := src
			ne.Pts = pts
			ne.From = fromNode
			ne.To = toNode
			add = append(add, ne)
		}
		// a's approach [0,lo]: From stays, To becomes n1
		outer(ea, la, 0, pl.lo, ea.From, n1, false, t1)
		// a's departure [hi,len]: From becomes n2, To stays
		outer(ea, la, pl.hi, la.Len(), n2, ea.To, true, t2)
		// b's approach [0,bLo] runs to the trunk node its arc end maps to
		if pl.rev {
			outer(eb, lb, 0, pl.bLo, eb.From, n2, false, t2)
			outer(eb, lb, pl.bHi, lb.Len(), n1, eb.To, true, t1)
		} else {
			outer(eb, lb, 0, pl.bLo, eb.From, n1, false, t1)
			outer(eb, lb, pl.bHi, lb.Len(), n2, eb.To, true, t2)
		}
		add = append(add, Edge{
			From: n1, To: n2, Pts: pl.mid,
			Routes: unionRoutes(&ea, &eb),
			Acts:   unionActs(&ea, &eb),
			Tracks: ea.Tracks + eb.Tracks,
		})

		var next []Edge
		for k := range net.Edges {
			if k == pl.ai || k == pl.bi {
				continue
			}
			next = append(next, net.Edges[k])
		}
		net.Edges = append(next, add...)
		merges++
	}
	if merges > 0 {
		rebuildAdj(net)
		trimEdgeEnds(net)
		contractChains(net)
		compactNodes(net)
		rebuildAdj(net)
	}
	return merges
}

// DropInterlockingStubs removes short dangling same-trunk strands that
// terminate nowhere. A drawn dead-end must be a TERMINUS — a place some
// pattern actually ends — and every pattern terminal is known, so a
// dangling tip near none of them, whose trunk continues through its base
// node without it, is interlocking litter: half a crossover movement
// left over from merging (the Van Wyck hook at Jamaica curved into the
// yard and just stopped). Real branch tails (the Lower Montauk to Long
// Island City) end at terminals and keep drawing.
func DropInterlockingStubs(net *Network, terms []geo.Pt) int {
	dropped := 0
	for {
		rebuildAdj(net)
		victim := -1
		for ei := range net.Edges {
			e := &net.Edges[ei]
			if e.Gap || len(e.Pts) < 2 || e.From == e.To {
				continue
			}
			l := geo.NewLine(e.Pts)
			if l.Len() > 600 {
				continue
			}
			t := ""
			for _, rid := range e.Routes {
				// reuse the weld's trunk notion: mixed or untrunked edges
				// are never litter
				k := func() string {
					r, ok := stubRoutes[rid]
					if !ok || !mode.Of(r.Type).Trunked() {
						return ""
					}
					return mode.TrunkKey(r)
				}()
				if k == "" || (t != "" && k != t) {
					t = ""
					break
				}
				t = k
			}
			if t == "" {
				continue
			}
			degF := len(net.Nodes[e.From].Adj)
			degT := len(net.Nodes[e.To].Adj)
			// an ISOLATED fragment — dangling at both ends — is litter
			// whenever it shadows the trunk that replaced it; there is no
			// connectivity to lose in dropping it
			if degF == 1 && degT == 1 {
				shadowed := false
				for oi := range net.Edges {
					if oi == ei {
						continue
					}
					o := &net.Edges[oi]
					if o.Gap || len(o.Pts) < 2 {
						continue
					}
					same := false
					for _, rid := range o.Routes {
						if r, ok := stubRoutes[rid]; ok && mode.TrunkKey(r) == t {
							same = true
							break
						}
					}
					if !same {
						continue
					}
					ol := geo.NewLine(o.Pts)
					all := true
					for _, q := range l.Resample(20) {
						if ol.DistTo(q) > 120 {
							all = false
							break
						}
					}
					if all {
						shadowed = true
						break
					}
				}
				if shadowed {
					victim = ei
					break
				}
				continue
			}
			tipNode, baseNode := -1, -1
			var tip geo.Pt
			if degF == 1 {
				tipNode, baseNode, tip = e.From, e.To, e.Pts[0]
			} else if degT == 1 {
				tipNode, baseNode, tip = e.To, e.From, e.Pts[len(e.Pts)-1]
			}
			if tipNode < 0 {
				continue
			}
			// the trunk must continue through the base without this stub
			cont := 0
			for _, oi := range net.Nodes[baseNode].Adj {
				if oi == ei {
					continue
				}
				for _, rid := range net.Edges[oi].Routes {
					if r, ok := stubRoutes[rid]; ok && mode.TrunkKey(r) == t {
						cont++
						break
					}
				}
			}
			if cont < 2 {
				continue
			}
			// terminal protection with a shadow exception: a terminating
			// tail that runs beside the through trunk for its whole length
			// is platform detail (Jamaica's whiskers), not a route — the
			// unshadowed terminus tail (the Lower Montauk into LIC) is the
			// one the protection exists for.
			near := false
			for _, tm := range terms {
				if tm.Dist(tip) < 150 {
					near = true
					break
				}
			}
			if near {
				shadowed := false
				for oi := range net.Edges {
					if oi == ei {
						continue
					}
					o := &net.Edges[oi]
					if o.Gap || len(o.Pts) < 2 {
						continue
					}
					sameTrunk := false
					for _, rid := range o.Routes {
						if r, ok := stubRoutes[rid]; ok && mode.TrunkKey(r) == t {
							sameTrunk = true
							break
						}
					}
					if !sameTrunk {
						continue
					}
					ol := geo.NewLine(o.Pts)
					ok2 := true
					for _, q := range l.Resample(20) {
						if ol.DistTo(q) > 90 {
							ok2 = false
							break
						}
					}
					if ok2 {
						shadowed = true
						break
					}
				}
				if !shadowed {
					continue
				}
			}
			victim = ei
			break
		}
		if victim < 0 {
			break
		}
		net.Edges = append(net.Edges[:victim], net.Edges[victim+1:]...)
		dropped++
	}
	if dropped > 0 {
		compactNodes(net)
		rebuildAdj(net)
	}
	return dropped
}

// stubRoutes: the route table for DropInterlockingStubs, set by the
// pipeline alongside the call (plumbing a param through the closure
// tangle above was noisier than a package registry).
var stubRoutes map[string]gtfs.Route

func SetStubRoutes(m map[string]gtfs.Route) { stubRoutes = m }

// SmoothTrunkCorridors low-passes sole-trunk REGIONAL edges. Mainline
// rail has minimum curve radii; 30 m-wavelength wiggle on a trunk is a
// construction artifact (a station-throat median snaking between the
// platform tracks it averaged), never track. Trams and metros keep
// their geometry — street corners and tight subway curves are identity.
// Node ends stay pinned exactly so seams cannot open.
func SmoothTrunkCorridors(net *Network, routes map[string]gtfs.Route) int {
	n := 0
	for ei := range net.Edges {
		e := &net.Edges[ei]
		if e.Gap || len(e.Pts) < 3 {
			continue
		}
		regional, sole := true, ""
		for _, rid := range e.Routes {
			r, ok := routes[rid]
			if !ok || mode.Of(r.Type) != mode.Regional {
				regional = false
				break
			}
			k := mode.TrunkKey(r)
			if sole != "" && k != sole {
				regional = false
				break
			}
			sole = k
		}
		if !regional || sole == "" {
			continue
		}
		l := geo.NewLine(e.Pts)
		if l.Len() < 300 {
			continue
		}
		p0, p1 := e.Pts[0], e.Pts[len(e.Pts)-1]
		// Chaikin rounds corners but cannot flatten a 30 m snake; a real
		// low-pass can. Two passes of a small Gaussian over 25 m samples
		// kills sub-150 m wavelength artifact wiggle. The stray guard is
		// the safety: a genuine tight curve (junction wye) would be
		// dragged more than the bar allows and keeps its geometry.
		pts := l.Resample(25)
		for pass := 0; pass < 2; pass++ {
			nx := append([]geo.Pt{}, pts...)
			for i := 2; i < len(pts)-2; i++ {
				nx[i] = geo.Pt{
					X: (pts[i-2].X + 2*pts[i-1].X + 3*pts[i].X + 2*pts[i+1].X + pts[i+2].X) / 9,
					Y: (pts[i-2].Y + 2*pts[i-1].Y + 3*pts[i].Y + 2*pts[i+1].Y + pts[i+2].Y) / 9,
				}
			}
			pts = nx
		}
		pts[0], pts[len(pts)-1] = p0, p1
		worst := 0.0
		for _, q := range pts {
			if d := l.DistTo(q); d > worst {
				worst = d
			}
		}
		if worst > 35 {
			continue // a real curve, not artifact wiggle — keep it
		}
		e.Pts = pts
		n++
	}
	return n
}

// PinEdgeTips reconciles geometry with topology after node surgery: a
// weld rewires an edge's node ID but nobody moved its polyline, so the
// drawn line could end tens of metres from the junction it belongs to —
// at Jamaica the trunk seam drew a 33 m break that read as a floating
// whisker. Every tip further than 8 m from its node bends onto it with
// the standard taper.
func PinEdgeTips(net *Network) int {
	n := 0
	for ei := range net.Edges {
		e := &net.Edges[ei]
		if len(e.Pts) < 2 {
			continue
		}
		l := geo.NewLine(e.Pts)
		// short span: the tip must REACH its node, not sweep toward it —
		// a 120 m taper fanned the Van Wyck wye's movements into a braid
		// of crossing arcs; 45 m reads as a switch, which is what it is
		span := math.Min(45, l.Len()*0.5)
		if from := net.Nodes[e.From].At; e.Pts[0].Dist(from) > 8 {
			e.Pts = taperTo(e.Pts, from, true, span)
			n++
		}
		if to := net.Nodes[e.To].At; e.Pts[len(e.Pts)-1].Dist(to) > 8 {
			e.Pts = taperTo(e.Pts, to, false, span)
			n++
		}
	}
	return n
}
