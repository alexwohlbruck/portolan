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
	for it := 0; it < p.Iters; it++ {
		pts := geo.NewLine(cur.Densify(p.Step)).Pts
		line := geo.NewLine(pts)
		n := len(pts)
		arcOf := make([]float64, n)
		for i := 1; i < n; i++ {
			arcOf[i] = arcOf[i-1] + pts[i].Dist(pts[i-1])
		}
		offStar := make([]float64, n)
		has := make([]bool, n)
		for i := 1; i < n-1; i++ {
			tan := pts[i+1].Sub(pts[i-1]).Unit()
			// curve-following persistence probes (LESSONS #3)
			pa := line.AtArc(arcOf[i] - p.SpanProbe)
			pb := line.AtArc(arcOf[i] + p.SpanProbe)
			var offs []float64
			for _, m := range members {
				if m.DistTo(pts[i]) >= p.Reach {
					continue
				}
				if m.DistTo(pa) >= p.Reach || m.DistTo(pb) >= p.Reach {
					continue // kiss guard: not persistent alongside
				}
				for _, c := range m.CrossSection(pts[i], tan, p.Reach) {
					if c.Parallel >= p.MinParallel {
						offs = append(offs, c.Offset)
					}
				}
			}
			if len(offs) == 0 {
				continue
			}
			o := MedianStrand(Strands(offs, p.StrandGap))
			offStar[i] = math.Max(-p.Reach, math.Min(p.Reach, o))
			has[i] = true
		}
		filt := medianFilter(offStar, has, 2)
		filt, has = gaussianSeries(filt, has, p.OffsetSigma)
		out := make([]geo.Pt, n)
		copy(out, pts)
		moved := 0.0
		for i := 1; i < n-1; i++ {
			if !has[i] {
				continue
			}
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
// centerline's (it drags the median into an S-wiggle otherwise).
func throughMembers(cl *geo.Line, members []*geo.Line, p Params) []*geo.Line {
	base := cl.Resample(12.0)
	if len(base) < 3 {
		return members
	}
	var through []*geo.Line
	for _, m := range members {
		near := 0
		for _, q := range base {
			if m.DistTo(q) < p.Reach {
				near++
			}
		}
		if float64(near) >= p.ThroughFrac*float64(len(base)) {
			through = append(through, m)
		}
	}
	if len(through) == 0 {
		return members
	}
	return through
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
func gaussianSeries(v []float64, has []bool, sigma float64) ([]float64, []bool) {
	n := len(v)
	out := make([]float64, n)
	w := int(2 * sigma)
	for i := range v {
		if !has[i] {
			continue
		}
		var sw, sv float64
		for j := max(0, i-w); j < min(n, i+w+1); j++ {
			if !has[j] {
				continue
			}
			g := math.Exp(-float64((j-i)*(j-i)) / (sigma * sigma))
			sw += g
			sv += v[j] * g
		}
		out[i] = sv / sw
	}
	return out, has
}
