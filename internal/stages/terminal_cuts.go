package stages

// Terminal cuts — per-segment service at STOP granularity where it
// matters (docs/DYNAMIC-SERVICE.md, the mid-edge short-turn residual).
//
// SPLIT stores activity per edge between junctions, OR'd over every
// pattern that rides ANY of the edge: a short-turn terminal that sits
// mid-edge keeps the whole edge lit whenever the short pattern runs (the
// M's weekend Essex St terminal lit 5.5 km of the Jamaica line to the
// Chrystie fork), and a pattern whose path merely TIP-TOUCHES an edge's
// last metres donates its hours to all of it (the overnight Myrtle
// shuttle lighting the same edge at 3am).
//
// This pass runs AFTER FAIR, on the emitted segments — no ORDER or FAIR
// decision changes, no slots move, junction drawing is untouched. Each
// steady/bridge segment is checked against the matched paths of its own
// routes: where a pattern terminates in the segment's interior, the
// geometry is cut there, and every resulting piece recomputes each
// route's activity from the patterns that actually COVER it. Two pieces
// share an endpoint at the same offset, so the seam is invisible under
// round caps; the only visible change is ink going dark at the right
// hours in the right places.

import (
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

var tcDebug = os.Getenv("PORTOLAN_TCDBG") != ""

const (
	tcEndDistM = 40.0  // a path endpoint this close to the segment is ON it
	tcRideM    = 40.0  // a sample this close to a path counts as ridden
	tcMarginM  = 120.0 // interior cuts keep clear of the segment ends
	tcDedupeM  = 60.0  // cuts closer than this collapse into one
)

// pathCover is one pattern's coverage of one segment: the arc interval
// it rides, and its activity mask.
type pathCover struct {
	a, b float64
	mask gtfs.Mask168
}

// CutSegmentsAtTerminals cuts steady/bridge segments at mid-segment
// pattern terminals and rebuilds per-piece activity from pattern
// coverage. Segments without acts (no service info) pass through.
//
// terms, when given, is aligned with paths: each pattern's first/last
// STOP locations in frame XY. Shapes overrun their terminal by tail
// trackage (plus the trim margin), so the honest cut is at the stop —
// the weekend M's drawn tail ends AT Essex St, not 300 m past it in
// the junction throat. Zero points mean unknown; the path tip stands in.
func CutSegmentsAtTerminals(segs []Segment, paths []Path, terms [][2]geo.Pt) []Segment {
	if patternActs == nil {
		return segs
	}
	if len(terms) != len(paths) {
		terms = nil
	}
	byRoute := map[string][]int{}
	for i := range paths {
		rid := paths[i].Pattern.Route.ID
		byRoute[rid] = append(byRoute[rid], i)
	}

	// Parallel ribbons of one corridor share exact geometry, and the
	// client bundles them BY that geometry (the _g hash) for dynamic
	// re-centering. Cutting only the short-turning ribbon would desync
	// the pair — the J stays whole while the M splits — so cut arcs are
	// pooled per GEOMETRY GROUP and applied to every member: pieces stay
	// identical across ribbons and the bundle survives the cut.
	sig := func(l *geo.Line) uint64 {
		h := uint64(1469598103934665603) // fnv64 offset basis
		mix := func(v float64) {
			// via int64: uint64(negative float) is undefined, and frame
			// coords are signed — a direct cast collapses every negative
			// coordinate to garbage and collides unrelated segments
			u := uint64(int64(math.Round(v * 100)))
			for i := 0; i < 8; i++ {
				h ^= u & 0xff
				h *= 1099511628211
				u >>= 8
			}
		}
		mix(float64(len(l.Pts)))
		for _, p := range l.Pts {
			mix(p.X)
			mix(p.Y)
		}
		return h
	}
	groupCuts := map[uint64][]float64{}
	segSig := make([]uint64, len(segs))
	for si := range segs {
		s := &segs[si]
		if (s.Kind != "steady" && s.Kind != "bridge") || s.Line == nil {
			continue
		}
		segSig[si] = sig(s.Line)
	}

	out := make([]Segment, 0, len(segs))
	type prepared struct {
		si      int
		covers  map[int][]pathCover
		trusted []bool
	}
	var prep []prepared
	for si := range segs {
		s := &segs[si]
		if (s.Kind != "steady" && s.Kind != "bridge") || len(s.Acts) == 0 || s.Line == nil {
			continue
		}
		L := s.Line.Len()
		// coverage per route, cut candidates across routes. A route whose
		// riding paths can't all be masked keeps its ORIGINAL act — this
		// pass may only refine what it can fully reconstruct.
		covers := make(map[int][]pathCover, len(s.Routes))
		trusted := make([]bool, len(s.Routes))
		var cuts []float64
		for ri, rid := range s.Routes {
			ok := true
			var cvs []pathCover
			for _, pi := range byRoute[rid] {
				pa := &paths[pi]
				cv, terminal, okc := coverOn(s.Line, L, pa.Line)
				if !okc {
					continue
				}
				mask, has := patternActs[pathActKey(*pa)]
				if !has {
					ok = false
					break
				}
				cv.mask = mask
				// pull the terminal back from the overshot path tip to
				// the pattern's terminal STOP where we know it
				if terminal >= 0 && terms != nil {
					tipPt := s.Line.AtArc(terminal)
					stop := terms[pi][0]
					if b := terms[pi][1]; b != (geo.Pt{}) &&
						(stop == (geo.Pt{}) || b.Dist(tipPt) < stop.Dist(tipPt)) {
						stop = b
					}
					if stop != (geo.Pt{}) {
						if sa, sd := s.Line.ProjectArc(stop); sd < 80 && math.Abs(sa-terminal) < 600 {
							if math.Abs(cv.a-terminal) < 1 {
								cv.a = sa
							} else if math.Abs(cv.b-terminal) < 1 {
								cv.b = sa
							}
							terminal = sa
						}
					}
				}
				// the THROUGH-TAIL case: the path runs the whole segment
				// because MATCH appends terminal pieces whole, but the
				// pattern's last STOP sits mid-segment — the H's shape
				// ends AT Rockaway Blvd while its matched path runs the
				// full piece to the 80 St crossover. Coverage ends at
				// the stop; the relay tail beyond gets nothing.
				if terminal < 0 && terms != nil && cv.a <= 1 && cv.b >= L-1 {
					for _, stop := range terms[pi] {
						if stop == (geo.Pt{}) {
							continue
						}
						sa, sd := s.Line.ProjectArc(stop)
						if sd > 80 || sa <= 1 || sa >= L-1 {
							continue
						}
						// which side is the tail? The side holding the
						// path's nearer TIP, close to the segment.
						// Coverage shrinks to the stop UNCONDITIONALLY
						// (a terminal's platforms spread — patterns
						// ending at nearer platforms must not keep the
						// tail lit); only the CUT placement respects
						// the sliver margin.
						pp := pa.Line.Pts
						for _, tip := range []geo.Pt{pp[0], pp[len(pp)-1]} {
							ta, td := s.Line.ProjectArc(tip)
							if td > tcEndDistM {
								continue
							}
							if ta > sa && L-ta < tcMarginM {
								cv.b = sa // tail on the high side
							} else if ta < sa && ta < tcMarginM {
								cv.a = sa // tail on the low side
							} else {
								continue
							}
							if sa > tcMarginM && sa < L-tcMarginM {
								terminal = sa
							}
						}
					}
				}
				cvs = append(cvs, cv)
				if terminal >= 0 && terminal > tcMarginM && terminal < L-tcMarginM {
					cuts = append(cuts, terminal)
				}
			}
			if ok && len(cvs) > 0 {
				covers[ri] = cvs
				trusted[ri] = true
			}
		}
		groupCuts[segSig[si]] = append(groupCuts[segSig[si]], cuts...)
		prep = append(prep, prepared{si: si, covers: covers, trusted: trusted})
	}

	// deduped cut arcs per geometry group
	groupArcs := make(map[uint64][]float64, len(groupCuts))
	for g, cuts := range groupCuts {
		sort.Float64s(cuts)
		var arcs []float64
		for _, c := range cuts {
			if len(arcs) == 0 || c-arcs[len(arcs)-1] > tcDedupeM {
				arcs = append(arcs, c)
			}
		}
		groupArcs[g] = arcs
	}
	prepBySeg := make(map[int]*prepared, len(prep))
	for i := range prep {
		prepBySeg[prep[i].si] = &prep[i]
	}

	for si := range segs {
		s := &segs[si]
		if (s.Kind != "steady" && s.Kind != "bridge") || s.Line == nil {
			out = append(out, *s)
			continue
		}
		L := s.Line.Len()
		var arcs []float64
		for _, a := range groupArcs[segSig[si]] {
			if a > tcMarginM && a < L-tcMarginM {
				arcs = append(arcs, a)
			}
		}
		pr := prepBySeg[si]
		if len(arcs) == 0 && pr == nil {
			out = append(out, *s)
			continue
		}
		bounds := append([]float64{0}, arcs...)
		bounds = append(bounds, L)
		orig := s.Acts
		// A RING has no free end, so it has no terminal overshoot: its
		// coverage wraps rather than stopping, and a piece whose midpoint
		// falls outside a cover interval is a gap in the INTERVALS, not
		// track beyond the last stop. Zeroing it punches a permanent hole
		// in a closed loop — the Atlanta streetcar lost 435 m at Luckie
		// St and read as broken at Centennial under every timestamp.
		// Rings keep the segment's own hours.
		ring := len(s.Line.Pts) > 2 &&
			s.Line.Pts[0].Dist(s.Line.Pts[len(s.Line.Pts)-1]) < 25
		anyDiffers := false
		var pieces []Segment
		for bi := 0; bi < len(bounds)-1; bi++ {
			lo, hi := bounds[bi], bounds[bi+1]
			mid := (lo + hi) / 2
			acts := append([]string(nil), orig...)
			if pr != nil && !ring {
				for ri := range s.Routes {
					if !pr.trusted[ri] {
						continue
					}
					var m gtfs.Mask168
					for _, cv := range pr.covers[ri] {
						if cv.a <= mid && mid <= cv.b {
							m = m.Or(cv.mask)
						}
					}
					for len(acts) <= ri {
						acts = append(acts, "")
					}
					if acts[ri] != m.Hex() {
						acts[ri] = m.Hex()
						anyDiffers = true
					}
				}
			}
			// tail pieces with all-zero hours are KEPT (dark under any
			// timestamp, visible on the union): at terminals like
			// Atlantic the "overshoot" is the platforms themselves —
			// the GTFS stop point sits at one end of them — and the
			// terminus clamp walks the chain to cap the true tip.
			ns := *s
			ns.Acts = acts
			ns.Line = subLine(s.Line, lo, hi)
			pieces = append(pieces, ns)
		}
		if len(pieces) == 1 && !anyDiffers {
			out = append(out, *s) // recompute matched the original — no-op
			continue
		}
		if tcDebug {
			L := s.Line.Len()
			for _, a := range arcs {
				if a < tcMarginM || a > L-tcMarginM {
					fmt.Printf("TCDBG bad arc %.0f on L=%.0f routes=%v band=%d\n", a, L, s.Routes, s.BandMin)
				}
			}
			for pi2, ns := range pieces {
				if ns.Line.Len() < 1 {
					fmt.Printf("TCDBG zero piece %d/%d routes=%v L=%.0f arcs=%v band=%d\n", pi2, len(pieces), s.Routes, L, arcs, s.BandMin)
				}
				for ri, a := range ns.Acts {
					if a == "000000000000000000000000000000000000000000" && ri < len(s.Acts) && s.Acts[ri] != a {
						fmt.Printf("TCDBG zeroed act r=%s piece %d/%d L=%.0f arcs=%v band=%d prTrusted=%v\n",
							s.Routes[ri], pi2, len(pieces), L, arcs, s.BandMin, pr != nil && pr.trusted[ri])
					}
				}
			}
		}
		out = append(out, pieces...)
	}
	return out
}

// coverOn reports how much of segment line `sl` (length L) the path
// rides: the covered arc interval, plus the arc of a path TERMINAL that
// lands in the segment (or -1). A path either covers the whole segment
// (segments end at junctions; paths only join and leave there) or a
// prefix/suffix ending at one of its own endpoints.
func coverOn(sl *geo.Line, L float64, path *geo.Line) (pathCover, float64, bool) {
	// where do the path's endpoints sit relative to the segment?
	pts := path.Pts
	e0, e1 := pts[0], pts[len(pts)-1]
	a0, d0 := sl.ProjectArc(e0)
	a1, d1 := sl.ProjectArc(e1)
	in0 := d0 < tcEndDistM && a0 > 1 && a0 < L-1
	in1 := d1 < tcEndDistM && a1 > 1 && a1 < L-1
	ride := func(arc float64) bool { return path.DistTo(sl.AtArc(arc)) < tcRideM }
	switch {
	case in0 && in1:
		// both path endpoints inside one junction-bounded segment: the
		// path lives entirely within it (a stub pattern)
		lo, hi := math.Min(a0, a1), math.Max(a0, a1)
		if !ride((lo + hi) / 2) {
			return pathCover{}, -1, false
		}
		// two interior terminals: report the one further from an end —
		// the caller's margin filters both anyway
		return pathCover{a: lo, b: hi}, lo, true
	case in0 || in1:
		t := a0
		if in1 {
			t = a1
		}
		// which side of t does the path ride? The probe must step past
		// the ride tolerance or both sides always read as ridden.
		const pr = tcRideM + 20
		loSide := t > pr && ride(t-pr)
		hiSide := t < L-pr && ride(t+pr)
		if loSide == hiSide {
			return pathCover{}, -1, false // ambiguous or crossing — skip
		}
		if loSide {
			return pathCover{a: 0, b: t}, t, true
		}
		return pathCover{a: t, b: L}, t, true
	default:
		// no endpoint inside: full coverage or none
		if ride(L*0.5) && ride(L*0.25) && ride(L*0.75) {
			return pathCover{a: 0, b: L}, -1, true
		}
		return pathCover{}, -1, false
	}
}

// subLine extracts [lo,hi] of a line by arc, keeping original vertices
// between the interpolated ends (transition vertex density elsewhere is
// load-bearing; steady pieces just must not lose their shape).
func subLine(l *geo.Line, lo, hi float64) *geo.Line {
	arc := l.ArcTable()
	var pts []geo.Pt
	pts = append(pts, l.AtArc(lo))
	for i, a := range arc {
		if a > lo && a < hi {
			pts = append(pts, l.Pts[i])
		}
	}
	pts = append(pts, l.AtArc(hi))
	if len(pts) < 2 {
		return l
	}
	return geo.NewLine(pts)
}
