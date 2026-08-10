package stages

// Trunk throat weld — the terminal-interlocking rule. A big terminal
// (Jamaica: ten LIRR services over an eight-track ladder) hands MATCH a
// different platform track per pattern, all faithfully real, and SPLIT
// then draws one trunk as four or five parallel weaving strands. Every
// strand is the same color and the same label, so the fidelity is pure
// noise — a road map draws the corridor, not the interlocking.
//
// The rule is topological, not city-specific: an edge whose riders all
// belong to ONE trunk may be welded away when the SAME trunk already
// connects the edge's endpoints by a heavier parallel path close by.
// Riders move onto that path and the minor strand vanishes. Iterated,
// this chews a ladder inward one crossover box at a time until the trunk
// rides a single spine wherever the strands stay within welding gauge.
//
// Two guards keep real geography intact:
//   - weight: riders only ever move onto a path at least as heavy in
//     trunk service-hours — a spine absorbs a siding, never the reverse;
//   - gauge: every sample of the minor strand must lie within
//     split_trunk_weld of the surviving path AND vice versa. Genuine
//     branch splits (Main Line vs Atlantic at the Jamaica fork) fail the
//     length cap immediately — their "alternative" is kilometres around.
//
// The same pass kills same-trunk GAP CHORDS: a shape bridge that leaves
// its corridor sideways (the once-a-day Montauk pattern cutting straight
// across six tracks west of Jamaica) is fabricated ink, not track. A gap
// whose riders are one trunk is dropped outright when the trunk stays
// connected without it and the chord exits its endpoints against the
// corridor grain — a real corridor bridge (Fresh Pond) leaves along the
// grain and keeps drawing.

import (
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
)

// maxKinkDeg: the sharpest direction change along a line, measured over a
// ~24 m window so vertex noise doesn't register as a bend.
func maxKinkDeg(l *geo.Line) float64 {
	pts := l.Resample(12)
	worst := 0.0
	for i := 2; i < len(pts); i++ {
		a := pts[i-1].Sub(pts[i-2]).Unit()
		b := pts[i].Sub(pts[i-1]).Unit()
		if d := math.Acos(math.Max(-1, math.Min(1, a.Dot(b)))) * 180 / math.Pi; d > worst {
			worst = d
		}
	}
	return worst
}

// maskHours counts lit hours — the service weight of one rider on one edge.
func maskHours(m gtfs.Mask168) int {
	n := 0
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			if m.Active(d, h) {
				n++
			}
		}
	}
	return n
}

// WeldTrunkThroats runs the weld to fixpoint. Returns strands welded and
// gap chords dropped, for the pipeline log.
func WeldTrunkThroats(net *Network, routes map[string]gtfs.Route) (welds, chords int) {
	gauge := dial("split_trunk_weld", 90)

	trunkOf := func(rid string) string {
		r, ok := routes[rid]
		if !ok {
			return ""
		}
		if !mode.Of(r.Type).Trunked() {
			return ""
		}
		return mode.TrunkKey(r)
	}
	// the whole edge must belong to one trunk to be a weld candidate —
	// shared steel (Amtrak beside the LIRR) is never deleted from under
	// another operator's ribbon
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
	// carriesTrunk: the edge can host trunk T riders (≥1 member aboard)
	carriesTrunk := func(e *Edge, t string) bool {
		for _, rid := range e.Routes {
			if trunkOf(rid) == t {
				return true
			}
		}
		return false
	}
	// trunk service weight of an edge: lit hours summed over members, or
	// member count when the build ran without a calendar
	weight := func(e *Edge, t string) int {
		w, hours := 0, false
		for _, rid := range e.Routes {
			if trunkOf(rid) != t {
				continue
			}
			if e.Acts != nil {
				if h := maskHours(e.Acts[rid]); h > 0 {
					w += h
					hours = true
				}
			}
		}
		if hours {
			return w
		}
		n := 0
		for _, rid := range e.Routes {
			if trunkOf(rid) == t {
				n++
			}
		}
		return n
	}

	// shortest alternative path From→To for trunk t avoiding edge ex:
	// non-gap trunk-carrying edges only, every hop at least minW heavy,
	// total length within cap. Dijkstra over so small a neighbourhood that
	// a plain heap-free scan is fine.
	altPath := func(ex, from, to int, t string, minW int, capLen float64, maxHops int) []int {
		type st struct {
			node, hops int
			dist       float64
			via        []int
		}
		best := map[int]float64{from: 0}
		q := []st{{from, 0, 0, nil}}
		for len(q) > 0 {
			// pop nearest
			bi := 0
			for i := range q {
				if q[i].dist < q[bi].dist {
					bi = i
				}
			}
			cur := q[bi]
			q = append(q[:bi], q[bi+1:]...)
			if cur.node == to {
				return cur.via
			}
			if cur.hops >= maxHops {
				continue
			}
			for _, ei := range net.Nodes[cur.node].Adj {
				if ei == ex {
					continue
				}
				e := &net.Edges[ei]
				if e.Gap || len(e.Pts) < 2 || !carriesTrunk(e, t) {
					continue
				}
				if e.From != cur.node && e.To != cur.node {
					continue
				}
				// the weight bar keeps a spine from welding onto a siding,
				// but the short LINK edges at a ladder step carry only
				// what crosses them — always lighter than the strand they
				// serve. Only sustained runs have to out-weigh the minor.
				if minW >= 0 && geo.NewLine(e.Pts).Len() > 120 && weight(e, t) < minW {
					continue
				}
				nn := e.From + e.To - cur.node
				nd := cur.dist + geo.NewLine(e.Pts).Len()
				if nd > capLen {
					continue
				}
				if b, ok := best[nn]; ok && b <= nd {
					continue
				}
				best[nn] = nd
				via := append(append([]int(nil), cur.via...), ei)
				q = append(q, st{nn, cur.hops + 1, nd, via})
			}
		}
		return nil
	}

	// symmetric gauge check: minor and path shadow each other end to end
	within := func(a *geo.Line, bs []*geo.Line, bar float64) bool {
		for _, q := range a.Resample(20) {
			d := math.Inf(1)
			for _, b := range bs {
				if v := b.DistTo(q); v < d {
					d = v
				}
			}
			if d > bar {
				return false
			}
		}
		return true
	}

	// corridor grain at a node, excluding the candidate edge: the exit
	// bearing of each sibling trunk edge
	grainMismatch := func(ex int, node int, t string, dir geo.Pt) bool {
		worst := true
		for _, ei := range net.Nodes[node].Adj {
			if ei == ex {
				continue
			}
			e := &net.Edges[ei]
			if len(e.Pts) < 2 || !carriesTrunk(e, t) {
				continue
			}
			ax := outwardTangent(geo.NewLine(e.Pts), e.From == node)
			// along the corridor axis in either direction is "with the
			// grain"; cos 50° ≈ 0.64
			if math.Abs(ax.Dot(dir)) > 0.64 {
				worst = false
			}
		}
		return worst
	}

	// contractsToRing: with edge ex gone, does the chain through `start`
	// close back on itself? A terminal BALLOON loop is exactly two
	// parallel strands within welding gauge, so the weld rule reaches for
	// it — but deleting one side leaves a bare ring, and a ring has no
	// end for the emitter to start a ribbon at (the JFK AirTrain's Howard
	// Beach loop lost its join and drew a 50 m gap). Loops keep both
	// sides; they are a shape, not an interlocking.
	// chainEnd follows the degree-2 chain leaving `start` along `first`
	// (with edge ex deleted) and returns the junction it terminates at,
	// or -1 if it closes on itself.
	chainEnd := func(ex, start, first int) int {
		prev := first
		e0 := &net.Edges[prev]
		if e0.From == e0.To {
			return -1
		}
		cur := e0.From + e0.To - start
		for hops := 0; hops < 4000; hops++ {
			if cur == start {
				return -1
			}
			var nexts []int
			for _, ei := range net.Nodes[cur].Adj {
				if ei != ex && ei != prev {
					nexts = append(nexts, ei)
				}
			}
			if len(nexts) != 1 {
				return cur // a junction: the contracted chain ends here
			}
			e := &net.Edges[nexts[0]]
			if e.From == e.To {
				return -1
			}
			prev, cur = nexts[0], e.From+e.To-cur
		}
		return -1
	}
	contractsToRing := func(ex, start int) bool {
		// `start` only contracts away if exactly two edges survive the
		// removal. When it does, the chain through it becomes ONE edge —
		// and if both of its ends land on the same junction, that edge is
		// a bare ring with no place for a ribbon to begin.
		var out []int
		for _, ei := range net.Nodes[start].Adj {
			if ei != ex {
				out = append(out, ei)
			}
		}
		if len(out) != 2 {
			return false // still a junction: nothing contracts here
		}
		a := chainEnd(ex, start, out[0])
		b := chainEnd(ex, start, out[1])
		return a < 0 || b < 0 || a == b
	}

	dbg := os.Getenv("PORTOLAN_DBGW") != ""
	changed := true
	for pass := 0; changed && pass < 600; pass++ {
		changed = false
		rebuildAdj(net)
		// lightest candidates first: sidings weld into spines before the
		// spine order can matter
		order := make([]int, len(net.Edges))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			ea, eb := &net.Edges[order[a]], &net.Edges[order[b]]
			ta, tb := soleTrunk(ea), soleTrunk(eb)
			wa, wb := math.MaxInt, math.MaxInt
			if ta != "" {
				wa = weight(ea, ta)
			}
			if tb != "" {
				wb = weight(eb, tb)
			}
			if wa != wb {
				return wa < wb
			}
			return order[a] < order[b]
		})
		for _, ei := range order {
			e := &net.Edges[ei]
			if len(e.Pts) < 2 || e.From == e.To || len(e.Routes) == 0 {
				continue
			}
			t := soleTrunk(e)
			if t == "" {
				continue
			}
			el := geo.NewLine(e.Pts)

			if e.Gap {
				// gap chord: fabricated ink sideways out of a served
				// corridor, with the trunk connected fine without it.
				// "Sideways" shows either at the ends (exit against the
				// corridor grain) or in the middle — the once-a-day
				// Montauk chord runs WITH the Main Line for 300 m and
				// then elbows 90° across six tracks, so the ends alone
				// look innocent. A real corridor bridge (Fresh Pond) is
				// smooth end to end and keeps drawing.
				out0 := outwardTangent(el, true)
				out1 := outwardTangent(el, false)
				sideways := grainMismatch(ei, e.From, t, out0) ||
					grainMismatch(ei, e.To, t, out1) ||
					maxKinkDeg(el) > 55
				if dbg {
					fmt.Printf("weldDBG gap=%d t=%s len=%.0f kink=%.0f grain=%v/%v alt=%v\n",
						ei, t, el.Len(), maxKinkDeg(el),
						grainMismatch(ei, e.From, t, out0), grainMismatch(ei, e.To, t, out1),
						altPath(ei, e.From, e.To, t, -1, math.Inf(1), 250) != nil)
				}
				if !sideways {
					continue
				}
				if altPath(ei, e.From, e.To, t, -1, math.Inf(1), 250) == nil {
					continue
				}
				net.Edges = append(net.Edges[:ei], net.Edges[ei+1:]...)
				chords++
				changed = true
				break
			}

			w := weight(e, t)
			// the survivor must be the SAME corridor, not a detour: its
			// total length is capped just above the minor's. This, not a
			// loose gauge, is what keeps a 95 m strand from welding onto a
			// 360 m swing around the other side of a junction (the JFK
			// AirTrain broke its drawn ribbon exactly that way).
			capLen := el.Len()*1.6 + 60
			via := altPath(ei, e.From, e.To, t, w, capLen, 12)
			if via == nil {
				if dbg {
					fmt.Printf("weldDBG edge=%d t=%s w=%d len=%.0f: no alt path\n", ei, t, w, el.Len())
				}
				continue
			}
			lines := make([]*geo.Line, len(via))
			for i, vi := range via {
				lines[i] = geo.NewLine(net.Edges[vi].Pts)
			}
			if !within(el, lines, gauge) {
				if dbg {
					fmt.Printf("weldDBG edge=%d t=%s w=%d len=%.0f: gauge fail e→P\n", ei, t, w, el.Len())
				}
				continue
			}
			ok := true
			for _, l := range lines {
				if !within(l, []*geo.Line{el}, gauge) {
					ok = false
					break
				}
			}
			if !ok {
				if dbg {
					fmt.Printf("weldDBG edge=%d t=%s w=%d len=%.0f: gauge fail P→e\n", ei, t, w, el.Len())
				}
				continue
			}
			if contractsToRing(ei, e.From) || contractsToRing(ei, e.To) {
				if dbg {
					fmt.Printf("weldDBG edge=%d t=%s w=%d len=%.0f: would leave a ring\n", ei, t, w, el.Len())
				}
				continue
			}
			// move the riders, drop the strand
			for _, vi := range via {
				v := &net.Edges[vi]
				have := map[string]bool{}
				for _, r := range v.Routes {
					have[r] = true
				}
				for _, r := range e.Routes {
					if !have[r] {
						v.Routes = append(v.Routes, r)
					}
				}
				sort.Strings(v.Routes)
				if e.Acts != nil {
					if v.Acts == nil {
						v.Acts = map[string]gtfs.Mask168{}
					}
					for r, m := range e.Acts {
						v.Acts[r] = v.Acts[r].Or(m)
					}
				}
			}
			if dbg {
				fmt.Printf("weldDBG WELD edge=%d t=%s w=%d len=%.0f routes=%v via=%v\n",
					ei, t, w, el.Len(), e.Routes, via)
			}
			net.Edges = append(net.Edges[:ei], net.Edges[ei+1:]...)
			welds++
			changed = true
			break
		}
	}
	if welds+chords > 0 {
		dropShadowStubs(net)
		contractChains(net)
		compactNodes(net)
		rebuildAdj(net)
	}
	smoothGapBridges(net)
	return welds, chords
}

// smoothGapBridges redraws kinked gap chords as tangent-matched curves.
// A gap bridge is fabricated geometry by definition — OSM has no track
// there — so when the shape chord it inherited elbows across a corridor
// (the LIC–Montauk train's Lower Montauk connection into the Jamaica
// fork: 300 m with the Main Line grain, then a hard turn across six
// tracks), the map is drawing the fabrication's noise, not information.
// Fabricate gracefully instead: a cubic Bézier from end to end, leaving
// each anchor along the corridor it connects to. Smooth corridor bridges
// (Fresh Pond) sit under the kink gate and keep their shape geometry,
// which tracks the real alignment closely.
func smoothGapBridges(net *Network) {
	rebuildAdj(net)
	for ei := range net.Edges {
		e := &net.Edges[ei]
		if !e.Gap || len(e.Pts) < 2 {
			continue
		}
		l := geo.NewLine(e.Pts)
		if l.Len() > 3000 || maxKinkDeg(l) < 40 {
			continue
		}
		// direction of travel INTO the gap at each end: the outward
		// tangent of the best-connected sibling edge, reversed. Sibling
		// choice = most riders shared with the gap (its continuation).
		dirAt := func(node int, fallback geo.Pt) geo.Pt {
			bestShared, dir := -1, fallback
			for _, oi := range net.Nodes[node].Adj {
				if oi == ei || len(net.Edges[oi].Pts) < 2 {
					continue
				}
				o := &net.Edges[oi]
				shared := 0
				for _, r := range o.Routes {
					for _, s := range e.Routes {
						if r == s {
							shared++
						}
					}
				}
				if shared > bestShared {
					bestShared = shared
					dir = outwardTangent(geo.NewLine(o.Pts), o.From == node).Scale(-1)
				}
			}
			return dir
		}
		p0, p3 := e.Pts[0], e.Pts[len(e.Pts)-1]
		d0 := dirAt(e.From, outwardTangent(l, true))
		d3 := dirAt(e.To, outwardTangent(l, false)).Scale(-1)
		// control arms at a third of the chord — the standard gentle ease
		arm := p0.Dist(p3) / 3
		c1 := p0.Add(d0.Scale(arm))
		c2 := p3.Sub(d3.Scale(arm))
		n := int(l.Len()/15) + 4
		pts := make([]geo.Pt, 0, n+1)
		for i := 0; i <= n; i++ {
			t := float64(i) / float64(n)
			u := 1 - t
			pt := p0.Scale(u * u * u).
				Add(c1.Scale(3 * u * u * t)).
				Add(c2.Scale(3 * u * t * t)).
				Add(p3.Scale(t * t * t))
			pts = append(pts, pt)
		}
		e.Pts = pts
	}
}
