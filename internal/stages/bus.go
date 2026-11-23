package stages

import (
	"math"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/mode"
	"github.com/alexwohlbruck/portolan/internal/style"
)

// DirectSegments: classes whose trunk policy is "none" are PATH MATCHING
// AND NOTHING MORE (owner call, 2026-08-08, for buses). A matched path of
// such a class is already a street centerline walk — the drawn geometry —
// so it goes straight to segments: no junction graph, no strand
// refinement, no corridor merging, no slots, no smoothing. One thin line
// per street, visible from the class's band floor up.
//
// The only processing is overlap dedup: forty routes down Fifth Avenue
// must not stack forty ribbons. Paths share graph-piece geometry
// verbatim, so identical drawn segments dedupe on their quantized
// endpoint pair (first path wins, later paths keep only their unseen
// stretches). The emit stays linear in ridden street km, not route count.
func DirectSegments(paths []Path) []Segment {
	const q = 1.0 // metres — piece geometry is shared, so exact-ish is fine
	type segKey [4]int32
	keyOf := func(a, b geo.Pt) segKey {
		k := segKey{
			int32(math.Round(a.X / q)), int32(math.Round(a.Y / q)),
			int32(math.Round(b.X / q)), int32(math.Round(b.Y / q)),
		}
		if k[0] > k[2] || (k[0] == k[2] && k[1] > k[3]) {
			k[0], k[1], k[2], k[3] = k[2], k[3], k[0], k[1]
		}
		return k
	}
	seen := map[segKey]bool{}
	var out []Segment
	sty := style.Active()
	for _, p := range paths {
		r := p.Pattern.Route
		var acts []string
		if patternActs != nil {
			if m, ok := patternActs[pathActKey(p)]; ok {
				acts = []string{m.Hex()}
			}
		}
		cls := mode.Of(r.Type)
		cs := sty.Class(cls.String())
		hex := cs.Color
		if h, ok := sty.RouteColor(r.ID, r.ShortName, r.LongName); ok {
			hex = h
		} else if h, ok := sty.AgencyColor(r.Agency, mode.AgencyName(r.Agency)); ok {
			hex = h
		} else if hex == "" {
			if hex = r.Color; hex == "" {
				hex = "888888"
			}
		}
		var pieces []int
		for _, st := range p.Steps {
			if st.Piece >= 0 {
				pieces = append(pieces, st.Piece)
			}
		}
		pieces = unionPieces(nil, pieces)
		var run []geo.Pt
		flush := func() {
			if len(run) >= 2 {
				out = append(out, Segment{
					Kind: "steady", Color: hex,
					Routes: []string{r.ID}, Label: r.ShortName,
					RouteType: r.Type, Mode: cls.String(),
					NSlots:  1,
					BandMin: cs.BandFloor, BandMax: 24,
					Acts:   acts,
					Line:   geo.NewLine(append([]geo.Pt{}, run...)),
					Pieces: pieces,
				})
			}
			run = nil
		}
		pts := p.Line.Pts
		for i := 1; i < len(pts); i++ {
			k := keyOf(pts[i-1], pts[i])
			if seen[k] {
				flush()
				continue
			}
			seen[k] = true
			if len(run) == 0 {
				run = append(run, pts[i-1])
			}
			run = append(run, pts[i])
		}
		flush()
	}
	return out
}

const (
	// busTermReach: farthest a terminal stop may sit from the matched line
	// for the line to be cut there.
	busTermReach = 80.0
	// busOvershoot: the most a matched line runs past a terminal. A loop
	// route ends where it starts, so a longer cut would be the wrong pass.
	busOvershoot = 400.0
	// busSpurTip: a spur whose tip the shape comes within this of is a
	// street the bus really runs up and back down, and stays.
	busSpurTip = 30.0
	// busStray: farther than this from the shape, the drawn line has left
	// the street the bus drives. Wider than a divided road, where shape and
	// matched carriageway sit 20–40 m apart on the same road.
	busStray = 40.0
	// busOnShape: within this of the shape the line is back on the street
	// the bus drives; a mend or a spur cut runs out to here, so no stub of
	// the wrong street is left hanging off the route.
	busOnShape = 5.0
)

// TidyBusPaths cleans the drawn line of each untrunked (street-matched) path. MATCH
// rides whole street pieces, so a path starts and ends at a junction that
// can lie past the terminal, and where it bridges a gap it anchors on the
// next piece's far end and rides back along it. Both read on the map as
// the bus going somewhere it does not: the line is cut at the terminal
// stops, and every out-and-back the shape itself does not make is removed.
func TidyBusPaths(paths []Path, frame geo.Frame) {
	for i := range paths {
		p := &paths[i]
		if mode.Of(p.Pattern.Route.Type).Trunked() || p.Line == nil || len(p.Line.Pts) < 2 {
			continue
		}
		shape := make([]geo.Pt, len(p.Pattern.Shape))
		for k, ll := range p.Pattern.Shape {
			shape[k] = frame.ToXY(ll)
		}
		sl := geo.NewLine(shape)
		pts := dropSpurs(p.Line.Pts, sl)
		if sl.Len() > 0 {
			pts = mendExcursions(pts, sl)
		}
		pts = cutToTerminals(pts, sl, frame.ToXY(p.Pattern.TermA), frame.ToXY(p.Pattern.TermB))
		if len(pts) >= 2 {
			p.Line = geo.NewLine(pts)
		}
	}
}

// cutToTerminals trims the line to where it passes its first and last
// stop, searching only the near end for each so a loop route cannot cut
// itself short. Only a tail that has left the shape is an overshoot.
func cutToTerminals(pts []geo.Pt, shape *geo.Line, a, b geo.Pt) []geo.Pt {
	l := geo.NewLine(pts)
	total := l.Len()
	s0, s1 := 0.0, total
	if s, d := projectWithin(l, a, 0, math.Min(busOvershoot, total/2)); d <= busTermReach {
		s0 = s
	}
	if s, d := projectWithin(l, b, math.Max(total-busOvershoot, total/2), total); d <= busTermReach {
		s1 = s
	}
	if s0 > 0 && !offShape(l, shape, 0, s0) {
		s0 = 0
	}
	if s1 < total && !offShape(l, shape, s1, total) {
		s1 = total
	}
	if s1-s0 < 1 {
		return pts
	}
	return subPts(l, s0, s1)
}

// projectWithin: the closest point to p on the line between arcs lo and hi.
func projectWithin(l *geo.Line, p geo.Pt, lo, hi float64) (float64, float64) {
	best, at := math.Inf(1), lo
	as := l.ArcTable()
	for k := 1; k < len(l.Pts); k++ {
		if as[k] < lo || as[k-1] > hi {
			continue
		}
		a, b := l.Pts[k-1], l.Pts[k]
		v := b.Sub(a)
		t := 0.0
		if l2 := v.Dot(v); l2 > 0 {
			t = math.Max(0, math.Min(1, p.Sub(a).Dot(v)/l2))
		}
		s := as[k-1] + t*(as[k]-as[k-1])
		if s < lo || s > hi {
			continue
		}
		if d := p.Dist(geo.Lerp(a, b, t)); d < best {
			best, at = d, s
		}
	}
	return at, best
}

// dropSpurs removes out-and-back runs: the line reverses at a tip and
// retraces itself. Kept when the shape reaches the tip too.
func dropSpurs(pts []geo.Pt, shape *geo.Line) []geo.Pt {
	const (
		step  = 2.0 // m between probes back along each leg
		same  = 4.0 // legs closer than this are one street ridden twice
		limit = 3000.0
	)
	for changed := true; changed; {
		changed = false
		l := geo.NewLine(pts)
		as := l.ArcTable()
		for k := 1; k+1 < len(pts); k++ {
			in := pts[k].Sub(pts[k-1]).Unit()
			out := pts[k+1].Sub(pts[k]).Unit()
			if in.Dot(out) > -0.95 {
				continue
			}
			if shape.DistTo(pts[k]) <= busSpurTip {
				continue
			}
			tip := as[k]
			run := 0.0
			// the spur is the retraced part OFF the shape: a route that
			// really runs out and back along one street (a loop route's
			// two legs) coincides with itself for kilometres, on the shape
			for run+step <= limit && tip-run-step >= 0 && tip+run+step <= l.Len() &&
				l.AtArc(tip-run-step).Dist(l.AtArc(tip+run+step)) <= same &&
				shape.DistTo(l.AtArc(tip-run-step)) > busOnShape {
				run += step
			}
			if run < step {
				continue
			}
			head := subPts(l, 0, tip-run)
			tail := subPts(l, tip+run, l.Len())
			pts = append(head, tail[1:]...)
			changed = true
			break
		}
	}
	return pts
}

// mendExcursions replaces every run of the line that strays more than
// busStray from the shape with the shape itself, between where the line
// leaves it and where it comes back. A bus shape is a survey of the street
// ridden, so where MATCH lost it (a road the extract lacks, a gap anchored
// on the far end of a piece) the shape is the better drawing. Progress
// along the shape is tracked monotonically, so a route that passes the
// same corner twice is mended against the right pass.
func mendExcursions(pts []geo.Pt, shape *geo.Line) []geo.Pt {
	line := geo.NewLine(pts)
	dense := line.Densify(5)
	if len(dense) < 3 {
		return pts
	}
	arcs := make([]float64, len(dense))
	dist := make([]float64, len(dense))
	at, _ := projectWithin(shape, dense[0], 0, shape.Len()*0.2+200)
	since := 0.0 // line travelled since it was last on the shape
	for i, q := range dense {
		if i > 0 {
			since += dense[i].Dist(dense[i-1])
		}
		// the bus cannot have got farther along its shape than it has
		// travelled, give or take a corner cut
		// whether the line has left the shape is plain distance; progress
		// only says which pass of the shape a mend should splice in
		dist[i] = shape.DistTo(q)
		a, d := projectWithin(shape, q, math.Max(0, at-30), math.Min(shape.Len(), at+1.5*since+200))
		arcs[i] = at
		if d > busStray {
			continue
		}
		if a > at {
			at = a
		}
		since = 0
		arcs[i] = at
	}
	keep := make([]bool, len(dense))
	splice := map[int][]geo.Pt{} // shape to draw in place of a removed run, keyed by its first index
	for i := range keep {
		keep[i] = true
	}
	for i := 1; i < len(dense); {
		if dist[i] <= busStray {
			i++
			continue
		}
		j := i
		for j < len(dense) && dist[j] > busStray {
			j++
		}
		if j == len(dense) {
			break // strays to the very end: the terminal cut trims past the last stop
		}
		run, far := 0.0, 0.0
		for k := i; k <= j; k++ {
			run += dense[k].Dist(dense[k-1])
			far = math.Max(far, dist[k])
		}
		// a wrong turn is longer than the shape it stands for; a run that
		// is shorter, or only just off the shape for a long way (the other
		// carriageway of a divided road), is the match being right and the
		// progress estimate being unsure, and stays as drawn
		if s0, s1 := arcs[i-1], arcs[j]; s1 > s0 && s1-s0 <= run+100 && !(far < 60 && run > 500) {
			// widen to where the line left and rejoined the street, so no
			// stub of the wrong street is left on either side
			a, b := i-1, j
			for back := 0.0; a > 0 && dist[a] > busOnShape && back < 150; a-- {
				back += dense[a].Dist(dense[a-1])
			}
			for fwd := 0.0; b < len(dense)-1 && dist[b] > busOnShape && fwd < 150; b++ {
				fwd += dense[b+1].Dist(dense[b])
			}
			if arcs[b] > arcs[a] {
				for k := a + 1; k < b; k++ {
					keep[k] = false
				}
				splice[a+1] = subPts(shape, arcs[a], arcs[b])
			}
		}
		i = j
	}
	var out []geo.Pt
	for k, q := range dense {
		if sp, ok := splice[k]; ok {
			out = append(out, sp...)
		}
		if keep[k] {
			out = append(out, q)
		}
	}
	if len(out) < 2 {
		return pts
	}
	return out
}

// offShape: whether most of the line between arcs lo and hi lies off the
// shape.
func offShape(l, shape *geo.Line, lo, hi float64) bool {
	if shape == nil || shape.Len() == 0 {
		return true
	}
	off, n := 0, 0
	for s := lo; s <= hi; s += 5 {
		n++
		if shape.DistTo(l.AtArc(s)) > busStray/2 {
			off++
		}
	}
	return 2*off > n
}
