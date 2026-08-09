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
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

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

	out := make([]Segment, 0, len(segs))
	for si := range segs {
		s := &segs[si]
		if (s.Kind != "steady" && s.Kind != "bridge") || len(s.Acts) == 0 || s.Line == nil {
			out = append(out, *s)
			continue
		}
		L := s.Line.Len()
		// coverage per route, cut candidates across routes. A route whose
		// riding paths can't all be masked keeps its ORIGINAL act — this
		// pass may only refine what it can fully reconstruct.
		covers := make(map[int][]pathCover, len(s.Routes))
		trusted := make([]bool, len(s.Routes))
		var cuts []float64
		changed := false
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
				cvs = append(cvs, cv)
				if terminal >= 0 && terminal > tcMarginM && terminal < L-tcMarginM {
					cuts = append(cuts, terminal)
				}
				if cv.a > 1 || cv.b < L-1 {
					changed = true // partial coverage — acts may differ per piece
				}
			}
			if ok && len(cvs) > 0 {
				covers[ri] = cvs
				trusted[ri] = true
			}
		}
		if !changed && len(cuts) == 0 {
			out = append(out, *s)
			continue
		}
		sort.Float64s(cuts)
		var arcs []float64
		for _, c := range cuts {
			if len(arcs) == 0 || c-arcs[len(arcs)-1] > tcDedupeM {
				arcs = append(arcs, c)
			}
		}
		// piece boundaries: [0, cuts..., L]
		bounds := append([]float64{0}, arcs...)
		bounds = append(bounds, L)
		orig := s.Acts
		for bi := 0; bi < len(bounds)-1; bi++ {
			lo, hi := bounds[bi], bounds[bi+1]
			mid := (lo + hi) / 2
			acts := make([]string, len(s.Routes))
			differs := false
			for ri := range s.Routes {
				if !trusted[ri] {
					if ri < len(orig) {
						acts[ri] = orig[ri]
					}
					continue
				}
				var m gtfs.Mask168
				for _, cv := range covers[ri] {
					if cv.a <= mid && mid <= cv.b {
						m = m.Or(cv.mask)
					}
				}
				acts[ri] = m.Hex()
				if ri < len(orig) && acts[ri] != orig[ri] {
					differs = true
				}
			}
			if len(bounds) == 2 && !differs {
				out = append(out, *s) // recompute matched the original — no-op
				break
			}
			ns := *s
			ns.Acts = acts
			ns.Line = subLine(s.Line, lo, hi)
			out = append(out, ns)
		}
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
