// Package bundle groups parallel tracks and computes their centerlines —
// the crown jewel of the pipeline. The centerline of a bundle is the MEDIAN
// STRAND of its cross-sections (the owner's hand-drawing rules), refined
// iteratively with exact perpendicular intersections and curve-following
// probes. No raster, no nearest-point projection, no heavy blur.
package bundle

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Params are the centerline dials. Defaults are the values that survived
// production tuning in attempt two (see docs/LESSONS.md).
type Params struct {
	Reach       float64 // cross-section half-length (m); membership radius
	StrandGap   float64 // offsets farther apart than this are distinct tracks
	SpanProbe   float64 // membership persistence probe distance along arc (m)
	ThroughFrac float64 // member must be present along this fraction to vote
	Iters       int     // refinement iterations
	Damp        float64 // per-iteration move damping
	Step        float64 // working vertex spacing (m)
	OffsetSigma float64 // gaussian over the OFFSET SERIES, in samples
	FinishSigma float64 // final light low-pass over geometry (m)
	MinParallel float64 // |cos| heading agreement for a crossing to count
	SlopeMax    float64 // max lateral-offset slope (m per m of arc)
}

func DefaultParams() Params {
	return Params{
		Reach:       25.0,
		StrandGap:   4.5,
		SpanProbe:   75.0,
		ThroughFrac: 0.8,
		Iters:       5,
		Damp:        0.8,
		Step:        6.0,
		OffsetSigma: 5.0,
		FinishSigma: 8.0,
		MinParallel: 0.82, // ~35°
		SlopeMax:    0.08, // max lateral-offset slope (m per m of arc)
	}
}

// MedianStrand applies the hand-drawing rules to sorted strand centers:
// 1 → follow, 2 → midpoint, 3 → center, 4 → middle-two midpoint, >4 → drop
// the outermost pair (yard edges) and reapply.
func MedianStrand(centers []float64) float64 {
	k := len(centers)
	for k > 4 {
		centers = centers[1 : k-1]
		k = len(centers)
	}
	if k == 0 {
		return 0
	}
	if k%2 == 1 {
		return centers[k/2]
	}
	return (centers[k/2-1] + centers[k/2]) / 2
}

// Strands clusters sorted signed offsets into distinct tracks (gap > strandGap
// starts a new strand) and returns each strand's center, sorted.
func Strands(offsets []float64, strandGap float64) []float64 {
	if len(offsets) == 0 {
		return nil
	}
	sort.Float64s(offsets)
	var centers []float64
	start, sum, n := 0, 0.0, 0
	flush := func() {
		if n > 0 {
			centers = append(centers, sum/float64(n))
		}
	}
	_ = start
	prev := offsets[0]
	sum, n = offsets[0], 1
	for _, o := range offsets[1:] {
		if o-prev > strandGap {
			flush()
			sum, n = 0, 0
		}
		sum += o
		n++
		prev = o
	}
	flush()
	return centers
}

// Refine runs iterative cross-section refinement of a centerline against its
// member tracks. It is the direct port of the algorithm that fixed the
// Tribeca windows, with every hard-won rule baked in:
//
//   - exact perpendicular intersections (geo.CrossSection), never projection
//   - span-persistence probes walk THE ARC of the current centerline
//   - through-member scoping: a member present along < ThroughFrac of the
//     line is a fork ramp and does not vote
//   - median strand per cross-section (MedianStrand), never a weighted mean
//   - short median filter + gaussian over the OFFSET SERIES so strand-count
//     changes ramp instead of stepping
//   - damped moves, endpoints pinned, light FinishSigma only
func Refine(cl *geo.Line, members []*geo.Line, p Params) *geo.Line {
	if len(cl.Pts) < 3 || len(members) == 0 {
		return cl
	}
	members = throughMembers(cl, members, p)
	cur := cl
	var offs []float64
	for it := 0; it < p.Iters; it++ {
		line := geo.NewLine(cur.Densify(p.Step))
		pts := line.Pts
		n := len(pts)
		arcOf := line.ArcTable() // the identical cumulative sum, already built
		offStar := make([]float64, n)
		has := make([]bool, n)
		var strandCounts []int
		for i := 1; i < n-1; i++ {
			tan := pts[i+1].Sub(pts[i-1]).Unit()
			// curve-following persistence probes (LESSONS #3)
			pa := line.AtArc(arcOf[i] - p.SpanProbe)
			pb := line.AtArc(arcOf[i] + p.SpanProbe)
			offs = offs[:0]
			for _, m := range members {
				if !m.Within(pts[i], p.Reach) {
					continue
				}
				if !m.Within(pa, p.Reach) || !m.Within(pb, p.Reach) {
					continue // kiss guard: not persistent alongside
				}
				// kiss rule PER PASS: a chained strand can cross the
				// section several times (both directional tracks, a loop
				// limb, a terminal return). Each crossing votes only if
				// THAT pass stays alongside the centerline for the probe
				// span — walk the member's own arc from the crossing and
				// require both probes to land near the centerline's probe
				// points. Corridor tracks persist; turnaround limbs curve
				// away and are dropped exactly where they diverge.
				for _, c := range m.CrossSectionNear(pts[i], tan, p.Reach) {
					if c.Parallel < p.MinParallel {
						continue
					}
					qa := m.AtArc(c.Arc - p.SpanProbe)
					qb := m.AtArc(c.Arc + p.SpanProbe)
					near := func(q geo.Pt) bool {
						return q.Dist(pa) < p.Reach*1.5 ||
							q.Dist(pb) < p.Reach*1.5
					}
					if near(qa) && near(qb) {
						offs = append(offs, c.Offset)
					}
				}
			}
			if len(offs) == 0 {
				continue
			}
			st := Strands(offs, p.StrandGap)
			strandCounts = append(strandCounts, len(st))
			o := MedianStrand(st)
			offStar[i] = math.Max(-p.Reach, math.Min(p.Reach, o))
			has[i] = true
		}
		filt := medianFilter(offStar, has, 2)
		// Stiffness scales with corridor width. On a wide interlocking
		// (W 4 St: two stacked 4-track levels flattened to 2D) the strand
		// count flickers as tracks interleave and every flicker steps the
		// median half a track spacing — at the base sigma that survives as
		// a visible S-wobble. The centerline of a wide bundle is
		// structurally straighter than any of its tracks, so smooth the
		// offset SERIES harder the more strands the sections carry; this
		// preserves constant corrections (and therefore real curves) and
		// leaves 1–2-track corridors at the base sigma.
		sigma := p.OffsetSigma
		if k := medianInt(strandCounts); k > 2 {
			sigma *= float64(k) / 2
		}
		filt, has = gaussianSeries(filt, has, sigma)
		// a drawn line eases lateral shifts. Two step sources: the median
		// jumping when strand membership changes at a divergence, and
		// coverage boundaries (has flipping) where a moved sample abuts a
		// pinned one. Fill coverage gaps by interpolation (uncovered ends
		// ramp to zero) and slope-limit the WHOLE series so every lateral
		// correction eases in at a bounded grade.
		filt = fillGaps(filt, has)
		filt = slopeLimit(filt, nil, p.SlopeMax*p.Step)
		out := make([]geo.Pt, n)
		copy(out, pts)
		moved := 0.0
		for i := 1; i < n-1; i++ {
			nrm := pts[i+1].Sub(pts[i-1]).Unit().Perp()
			o := filt[i] * p.Damp
			if math.Abs(o) > moved {
				moved = math.Abs(o)
			}
			out[i] = pts[i].Add(nrm.Scale(o))
		}
		cur = geo.NewLine(geo.GaussianArc(out, p.FinishSigma))
		if moved < 0.3 {
			break
		}
	}
	return cur
}

// throughMembers keeps members present along ≥ ThroughFrac of the line —
// a branch peeling off mid-corridor is the fork's geometry, not this
// centerline's (it drags the median into an S-wiggle otherwise). The
// requirement is CORRIDOR-scale: on a megachain edge (several corridors
// long), demanding presence along 80% of the whole chain would exclude
// every local track group and freeze refinement there (the B/Q Prospect
// Park tunnel stayed unrefined beside a 14.7 km Brighton edge) — so the
// threshold caps at ~1.5 km of arc.
func throughMembers(cl *geo.Line, members []*geo.Line, p Params) []*geo.Line {
	const corridorScaleM = 1500.0
	base := cl.Resample(12.0)
	if len(base) < 3 {
		return members
	}
	need := p.ThroughFrac * math.Min(float64(len(base)), corridorScaleM/12.0)
	var through []*geo.Line
	for _, m := range members {
		near := 0
		for _, q := range base {
			if m.Within(q, p.Reach) {
				near++
			}
		}
		if float64(near) >= need {
			through = append(through, m)
		}
	}
	if len(through) == 0 {
		return members
	}
	return through
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return s[len(s)/2]
}

// medianFilter kills single-sample strand flips (window ±w samples).
func medianFilter(v []float64, has []bool, w int) []float64 {
	n := len(v)
	out := make([]float64, n)
	buf := make([]float64, 0, 2*w+1)
	for i := range v {
		if !has[i] {
			continue
		}
		buf = buf[:0]
		for j := max(0, i-w); j < min(n, i+w+1); j++ {
			if has[j] {
				buf = append(buf, v[j])
			}
		}
		sort.Float64s(buf)
		out[i] = buf[len(buf)/2]
	}
	return out
}

// gaussianSeries low-passes the offset SERIES (σ in samples): a strand-count
// step (express pair peeling) becomes a long ramp; converged sections are ~0
// so it is a no-op there.
// fillGaps produces a fully-defined offset series: interior runs without
// coverage are linearly interpolated between their covered neighbors, and
// uncovered leading/trailing runs are zero (no correction far from any
// evidence — the slope limit then eases the boundary).
func fillGaps(v []float64, has []bool) []float64 {
	n := len(v)
	out := make([]float64, n)
	last := -1
	for i := 0; i < n; i++ {
		if !has[i] {
			continue
		}
		out[i] = v[i]
		if last < 0 {
			// leading uncovered run stays zero
		} else if last < i-1 {
			for j := last + 1; j < i; j++ {
				t := float64(j-last) / float64(i-last)
				out[j] = v[last]*(1-t) + v[i]*t
			}
		}
		last = i
	}
	return out
}

// slopeLimit bounds |dv/di| to maxDelta per sample, easing steps into
// symmetric ramps: forward and backward clamped passes are averaged, so
// regions already within the limit pass through unchanged.
func slopeLimit(v []float64, has []bool, maxDelta float64) []float64 {
	n := len(v)
	fwd := make([]float64, n)
	bwd := make([]float64, n)
	copy(fwd, v)
	copy(bwd, v)
	prev := -1
	for i := 0; i < n; i++ {
		if has != nil && !has[i] {
			continue
		}
		if prev >= 0 {
			lim := maxDelta * float64(i-prev)
			d := fwd[i] - fwd[prev]
			if d > lim {
				fwd[i] = fwd[prev] + lim
			} else if d < -lim {
				fwd[i] = fwd[prev] - lim
			}
		}
		prev = i
	}
	prev = -1
	for i := n - 1; i >= 0; i-- {
		if has != nil && !has[i] {
			continue
		}
		if prev >= 0 {
			lim := maxDelta * float64(prev-i)
			d := bwd[i] - bwd[prev]
			if d > lim {
				bwd[i] = bwd[prev] + lim
			} else if d < -lim {
				bwd[i] = bwd[prev] - lim
			}
		}
		prev = i
	}
	out := make([]float64, n)
	for i := range v {
		out[i] = 0.5 * (fwd[i] + bwd[i])
	}
	return out
}

func gaussianSeries(v []float64, has []bool, sigma float64) ([]float64, []bool) {
	n := len(v)
	out := make([]float64, n)
	w := int(2 * sigma)
	// kernel depends only on |j-i|: tabulate the identical Exp values once
	// ((j-i)*(j-i) == d*d exactly in int arithmetic)
	kern := make([]float64, w+1)
	for d := 0; d <= w; d++ {
		kern[d] = math.Exp(-float64(d*d) / (sigma * sigma))
	}
	for i := range v {
		if !has[i] {
			continue
		}
		var sw, sv float64
		for j := max(0, i-w); j < min(n, i+w+1); j++ {
			if !has[j] {
				continue
			}
			d := j - i
			if d < 0 {
				d = -d
			}
			g := kern[d]
			sw += g
			sv += v[j] * g
		}
		out[i] = sv / sw
	}
	return out, has
}
