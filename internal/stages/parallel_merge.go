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
			net.Nodes = append(net.Nodes, Node{At: at})
			return len(net.Nodes) - 1
		}
		n1 := resolve(aStart, bAtTrunkStart, pl.mid[0])
		n2 := resolve(aEnd, bAtTrunkEnd, pl.mid[len(pl.mid)-1])
		if n1 == n2 {
			// the whole pair collapsed to a point — a lens/balloon, not a
			// corridor. Leave it for the loop-aware rules.
			lines[pl.ai] = nil
			continue
		}

		ea, eb := net.Edges[pl.ai], net.Edges[pl.bi]
		var add []Edge
		outer := func(src Edge, l *geo.Line, from, to float64,
			fromNode, toNode int, prepend, appendPt *geo.Pt) {
			if to-from < endSnap {
				return
			}
			sub := bundle.SubLine(l, from, to)
			pts := append([]geo.Pt{}, sub.Pts...)
			if prepend != nil {
				pts = append([]geo.Pt{*prepend}, pts...)
			}
			if appendPt != nil {
				pts = append(pts, *appendPt)
			}
			ne := src
			ne.Pts = pts
			ne.From = fromNode
			ne.To = toNode
			add = append(add, ne)
		}
		p0, p1 := pl.mid[0], pl.mid[len(pl.mid)-1]
		// a's approach [0,lo]: From stays, To becomes n1
		outer(ea, la, 0, pl.lo, ea.From, n1, nil, &p0)
		// a's departure [hi,len]: From becomes n2, To stays
		outer(ea, la, pl.hi, la.Len(), n2, ea.To, &p1, nil)
		// b's approach [0,bLo] runs to the trunk node its arc end maps to
		if pl.rev {
			outer(eb, lb, 0, pl.bLo, eb.From, n2, nil, &p1)
			outer(eb, lb, pl.bHi, lb.Len(), n1, eb.To, &p0, nil)
		} else {
			outer(eb, lb, 0, pl.bLo, eb.From, n1, nil, &p0)
			outer(eb, lb, pl.bHi, lb.Len(), n2, eb.To, &p1, nil)
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
