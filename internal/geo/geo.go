// Package geo is portolan's geometry core: exact vector primitives in a
// local metric frame. Every rule here is load-bearing — see docs/LESSONS.md.
package geo

import (
	"math"
	"runtime"
	"sync"
)

// Pt is a point in the local metric frame (meters).
type Pt struct{ X, Y float64 }

func (a Pt) Sub(b Pt) Pt        { return Pt{a.X - b.X, a.Y - b.Y} }
func (a Pt) Add(b Pt) Pt        { return Pt{a.X + b.X, a.Y + b.Y} }
func (a Pt) Scale(s float64) Pt { return Pt{a.X * s, a.Y * s} }
func (a Pt) Dot(b Pt) float64   { return a.X*b.X + a.Y*b.Y }
func (a Pt) Cross(b Pt) float64 { return a.X*b.Y - a.Y*b.X }
func (a Pt) Dist(b Pt) float64  { return math.Hypot(a.X-b.X, a.Y-b.Y) }
func (a Pt) Norm() float64      { return math.Hypot(a.X, a.Y) }
func (a Pt) Unit() Pt {
	n := a.Norm()
	if n < 1e-12 {
		return Pt{1, 0}
	}
	return Pt{a.X / n, a.Y / n}
}
func (a Pt) Perp() Pt            { return Pt{-a.Y, a.X} } // left normal
func Lerp(a, b Pt, t float64) Pt { return Pt{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t} }

// LL is a WGS84 coordinate.
type LL struct{ Lon, Lat float64 }

// Frame is a local azimuthal-equidistant projection. All pipeline math runs
// in one Frame; lat/lon never leaks past CHART (unit bugs killed us before).
type Frame struct{ lon0, lat0, coslat float64 }

const mPerDegLat = 111320.0

func NewFrame(center LL) Frame {
	return Frame{center.Lon, center.Lat, math.Cos(center.Lat * math.Pi / 180)}
}
func (f Frame) ToXY(p LL) Pt {
	return Pt{(p.Lon - f.lon0) * mPerDegLat * f.coslat, (p.Lat - f.lat0) * mPerDegLat}
}
func (f Frame) ToLL(p Pt) LL {
	return LL{f.lon0 + p.X/(mPerDegLat*f.coslat), f.lat0 + p.Y/mPerDegLat}
}

// Line is a polyline in the metric frame with a cached cumulative-arc table
// and a lazy segment index for capped distance queries (lineindex.go).
// Lines are immutable after NewLine — nothing may assign Pts or arc later.
type Line struct {
	Pts     []Pt
	arc     []float64 // cumulative arc length; arc[0]=0
	idx     *lineIndex
	idxOnce sync.Once
}

func NewLine(pts []Pt) *Line {
	l := &Line{Pts: pts}
	l.rebuildArc()
	return l
}

func (l *Line) rebuildArc() {
	l.arc = make([]float64, len(l.Pts))
	for i := 1; i < len(l.Pts); i++ {
		l.arc[i] = l.arc[i-1] + l.Pts[i].Dist(l.Pts[i-1])
	}
}

// ArcTable exposes the cached cumulative-arc table (read-only; arc[0]=0,
// arc[i] = arc[i-1] + Pts[i].Dist(Pts[i-1]) — the exact accumulation every
// caller would otherwise rebuild).
func (l *Line) ArcTable() []float64 { return l.arc }

func (l *Line) Len() float64 {
	if len(l.arc) == 0 {
		return 0
	}
	return l.arc[len(l.arc)-1]
}

// AtArc returns the point at arc position s (clamped). This is THE walk
// primitive: probes ahead/behind a sample walk the arc, never the straight
// tangent — tangent extrapolation leaves the corridor on every bend
// (docs/LESSONS.md #3).
func (l *Line) AtArc(s float64) Pt {
	n := len(l.Pts)
	if n == 0 {
		return Pt{}
	}
	if s <= 0 {
		return l.Pts[0]
	}
	if s >= l.Len() {
		return l.Pts[n-1]
	}
	lo, hi := 0, n-1
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if l.arc[mid] <= s {
			lo = mid
		} else {
			hi = mid
		}
	}
	span := l.arc[hi] - l.arc[lo]
	if span < 1e-12 {
		return l.Pts[lo]
	}
	return Lerp(l.Pts[lo], l.Pts[hi], (s-l.arc[lo])/span)
}

// TangentAtArc is the unit direction at arc s, from a symmetric window so a
// vertex kink doesn't own the tangent.
func (l *Line) TangentAtArc(s, win float64) Pt {
	a := l.AtArc(s - win)
	b := l.AtArc(s + win)
	return b.Sub(a).Unit()
}

// Resample returns the line resampled at uniform ~step spacing (endpoints
// kept, original vertices NOT kept — jaggedness at map scale is measured on
// uniform samples; sub-2m micro-spans must not fake 45° turns).
func (l *Line) Resample(step float64) []Pt {
	total := l.Len()
	if total < 1e-9 || len(l.Pts) < 2 {
		return append([]Pt(nil), l.Pts...)
	}
	n := int(math.Max(1, math.Round(total/step)))
	out := make([]Pt, 0, n+1)
	for k := 0; k <= n; k++ {
		out = append(out, l.AtArc(total*float64(k)/float64(n)))
	}
	return out
}

// Densify inserts points so no span exceeds step (original vertices kept —
// use when real geometry must be preserved).
func (l *Line) Densify(step float64) []Pt {
	if len(l.Pts) < 2 {
		return append([]Pt(nil), l.Pts...)
	}
	out := []Pt{l.Pts[0]}
	for i := 1; i < len(l.Pts); i++ {
		a, b := l.Pts[i-1], l.Pts[i]
		d := a.Dist(b)
		n := int(math.Max(1, math.Ceil(d/step)))
		for k := 1; k <= n; k++ {
			out = append(out, Lerp(a, b, float64(k)/float64(n)))
		}
	}
	return out
}

// DistTo returns the minimum distance from p to the polyline.
// The scan compares squared distances (segDist's clamped projection without
// the final Hypot) and computes the true distance once, on the winner —
// the same rounded value segDist would return for that segment.
func (l *Line) DistTo(p Pt) float64 {
	best2 := math.Inf(1)
	var bdx, bdy float64
	for i := 1; i < len(l.Pts); i++ {
		d2, dx, dy := segDist2(p, l.Pts[i-1], l.Pts[i])
		if d2 < best2 {
			best2, bdx, bdy = d2, dx, dy
		}
	}
	if math.IsInf(best2, 1) {
		return best2
	}
	return math.Hypot(bdx, bdy)
}

func segDist(p, a, b Pt) float64 {
	ab := b.Sub(a)
	n2 := ab.Dot(ab)
	if n2 < 1e-18 {
		return p.Dist(a)
	}
	t := math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/n2))
	return p.Dist(a.Add(ab.Scale(t)))
}

// segDist2 is segDist without the final square root: identical clamped
// projection (same t, same q, same dx/dy operands), returning the squared
// distance and the components. math.Hypot(dx, dy) on the returned pair is
// bit-identical to segDist's result for the same segment.
func segDist2(p, a, b Pt) (d2, dx, dy float64) {
	ab := b.Sub(a)
	n2 := ab.Dot(ab)
	if n2 < 1e-18 {
		dx, dy = p.X-a.X, p.Y-a.Y
		return dx*dx + dy*dy, dx, dy
	}
	t := math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/n2))
	q := a.Add(ab.Scale(t))
	dx, dy = p.X-q.X, p.Y-q.Y
	return dx*dx + dy*dy, dx, dy
}

// SegIntersect returns the intersection of segments a1-a2 and b1-b2 and true
// if they properly intersect (touching endpoints count).
func SegIntersect(a1, a2, b1, b2 Pt) (Pt, bool) {
	r := a2.Sub(a1)
	s := b2.Sub(b1)
	den := r.Cross(s)
	if math.Abs(den) < 1e-15 {
		return Pt{}, false
	}
	qp := b1.Sub(a1)
	t := qp.Cross(s) / den
	u := qp.Cross(r) / den
	if t < -1e-12 || t > 1+1e-12 || u < -1e-12 || u > 1+1e-12 {
		return Pt{}, false
	}
	return a1.Add(r.Scale(t)), true
}

// CrossSection intersects the perpendicular segment through p (direction
// normal, half-length reach) with the polyline, returning the signed offsets
// of every crossing and the local heading agreement |cos| per crossing.
// Perpendicular INTERSECTIONS, never nearest-point projection
// (docs/LESSONS.md #2).
func (l *Line) CrossSection(p, tangent Pt, reach float64) []Crossing {
	nrm := tangent.Perp()
	r1 := p.Sub(nrm.Scale(reach))
	r2 := p.Add(nrm.Scale(reach))
	var out []Crossing
	for i := 1; i < len(l.Pts); i++ {
		q, ok := SegIntersect(r1, r2, l.Pts[i-1], l.Pts[i])
		if ok {
			dir := l.Pts[i].Sub(l.Pts[i-1]).Unit()
			out = append(out, Crossing{
				Offset:   q.Sub(p).Dot(nrm),
				Parallel: math.Abs(dir.Dot(tangent)),
				At:       q,
				// l.arc holds the identical cumulative sum this loop used
				// to re-accumulate (one Hypot per segment per call)
				Arc: l.arc[i-1] + l.Pts[i-1].Dist(q),
			})
		}
	}
	return out
}

// Crossing is one track crossing a cross-section ray.
type Crossing struct {
	Offset   float64 // signed lateral offset from the section origin
	Parallel float64 // |cos| of heading agreement with the section tangent
	At       Pt
	Arc      float64 // arc position of the crossing along the crossed line
}

// MaxTurnDeg returns the maximum per-vertex turn angle in degrees.
func MaxTurnDeg(pts []Pt) float64 {
	worst := 0.0
	for i := 1; i < len(pts)-1; i++ {
		t := TurnDeg(pts[i-1], pts[i], pts[i+1])
		if t > worst {
			worst = t
		}
	}
	return worst
}

// TurnDeg is the turn angle at b in degrees.
func TurnDeg(a, b, c Pt) float64 {
	v1 := b.Sub(a)
	v2 := c.Sub(b)
	n1, n2 := v1.Norm(), v2.Norm()
	if n1 < 1e-12 || n2 < 1e-12 {
		return 0
	}
	d := v1.Dot(v2) / (n1 * n2)
	d = math.Max(-1, math.Min(1, d))
	return math.Acos(d) * 180 / math.Pi
}

// Unfold removes fold knots: interior vertices whose turn exceeds
// maxDeg. At the sampling steps this codebase draws at (metres, not
// tens of metres), no physical railway turns 90° at a single vertex —
// the tightest steel on earth (a ~15 m tram curve) reads ~25° per
// vertex at a 6 m step. A sharper turn is always an artifact: a
// refinement offset that outran its base line's curvature, a weld
// seam pointing at a stale node, a lens midline crossing its pair.
// Deleting the vertex collapses the knot onto its chord, which is the
// same answer the vote-blanking machinery gives an unstable pocket —
// bridge it straight — applied in position space. Endpoints are never
// touched. Runs to a fixpoint: a multi-vertex curl exposes a new
// reversal each time its tip is cut, so one pass is not enough; the
// pass cap only bounds the pathological all-knot line.
func Unfold(pts []Pt, maxDeg float64) []Pt {
	for pass := 0; pass < 16; pass++ {
		if len(pts) < 3 {
			return pts
		}
		out := pts[:0:0]
		out = append(out, pts[0])
		changed := false
		for i := 1; i < len(pts)-1; i++ {
			if TurnDeg(out[len(out)-1], pts[i], pts[i+1]) > maxDeg {
				changed = true
				continue
			}
			out = append(out, pts[i])
		}
		out = append(out, pts[len(pts)-1])
		pts = out
		if !changed {
			return pts
		}
	}
	return pts
}

// SmoothTurning low-passes a polyline in the TURNING-ANGLE domain: it
// smooths the sequence of segment headings over arc length and rebuilds
// the line from the smoothed headings, keeping every segment's length.
//
// This is the shape-preserving counterpart to GaussianArc. Smoothing
// POSITIONS is curvature-biased — it pulls every apex toward its chord
// at ~sigma²/2R per pass, so an R17 street corner visibly loses its
// radius while a straight line is untouched. Smoothing HEADINGS has no
// such bias: a straight run is a constant heading and a circular arc is
// a LINEAR heading ramp, and a Gaussian reproduces both exactly (it is
// normalized and symmetric). What it does remove is what it should — a
// kink is a step in the heading series, and a step is what a low-pass
// rounds off.
//
// Both endpoints are pinned: the reintegrated line is closed back onto
// the original end by distributing the residual along arc, the standard
// traverse-closure adjustment. The correction is tiny (it is only the
// second-order effect of the heading changes) and spread over the whole
// line rather than concentrated at a vertex.
func SmoothTurning(pts []Pt, sigma float64) []Pt {
	n := len(pts)
	if n < 4 || sigma <= 0 {
		return pts
	}
	m := n - 1 // segments
	seg := make([]float64, m)
	th := make([]float64, m)
	for i := 0; i < m; i++ {
		d := pts[i+1].Sub(pts[i])
		seg[i] = d.Norm()
		th[i] = math.Atan2(d.Y, d.X)
	}
	// unwrap so the series is continuous across the ±π branch cut
	for i := 1; i < m; i++ {
		for th[i]-th[i-1] > math.Pi {
			th[i] -= 2 * math.Pi
		}
		for th[i]-th[i-1] < -math.Pi {
			th[i] += 2 * math.Pi
		}
	}
	// arc position of each segment's midpoint
	mid := make([]float64, m)
	run := 0.0
	for i := 0; i < m; i++ {
		mid[i] = run + seg[i]/2
		run += seg[i]
	}
	total := run
	if total <= 0 {
		return pts
	}
	// Per-sample sigma, capped by the local feature scale. Heading
	// smoothing is unbiased for a LONG arc (a linear heading ramp is
	// reproduced exactly), but a short one — an R17 street corner is only
	// ~27 m of arc — is a ramp shorter than the window, so a fixed sigma
	// spreads its turn over the neighbouring straights and walks the apex
	// inward. That erosion is what deadlocked refinement: the votes push
	// the median out toward the track pair by ~1.9 m and the finish pass
	// pulled it back the same 1.9 m, iteration after iteration, so the
	// line simply sat on the inner rail. Cap sigma at a quarter of the
	// arc the local turn is spread over (turn rate -> radius -> arc for a
	// quarter circle), leaving straight track — where welds live and the
	// radius is huge — at the full sigma.
	sig := make([]float64, m)
	for i := 0; i < m; i++ {
		sig[i] = sigma
		// local turn rate over a +-3 segment window
		lo, hi := i-3, i+3
		if lo < 0 {
			lo = 0
		}
		if hi > m-1 {
			hi = m - 1
		}
		if hi <= lo {
			continue
		}
		dth := math.Abs(th[hi] - th[lo])
		ds := mid[hi] - mid[lo]
		if dth > 1e-9 && ds > 1e-9 {
			r := ds / dth                                    // local radius (m per radian)
			if lim := 0.25 * r * math.Pi / 2; lim < sig[i] { // quarter-circle arc
				sig[i] = lim
			}
		}
	}
	sm := make([]float64, m)
	for i := 0; i < m; i++ {
		s := sig[i]
		if s <= 0 {
			sm[i] = th[i]
			continue
		}
		win := 3 * s
		var sw, sv float64
		for j := i; j >= 0 && mid[i]-mid[j] <= win; j-- {
			w := math.Exp(-sq(mid[i]-mid[j]) / (2 * s * s))
			sv += th[j] * w
			sw += w
		}
		for j := i + 1; j < m && mid[j]-mid[i] <= win; j++ {
			w := math.Exp(-sq(mid[j]-mid[i]) / (2 * s * s))
			sv += th[j] * w
			sw += w
		}
		if sw > 0 {
			sm[i] = sv / sw
		} else {
			sm[i] = th[i]
		}
	}
	out := make([]Pt, n)
	out[0] = pts[0]
	for i := 0; i < m; i++ {
		out[i+1] = Pt{
			X: out[i].X + seg[i]*math.Cos(sm[i]),
			Y: out[i].Y + seg[i]*math.Sin(sm[i]),
		}
	}
	// Closure: reintegrated headings do not land exactly on the original
	// end (up to ~0.15% of length here), and that residual must NOT be
	// spread along arc as a shear — a shear moves mid-line points
	// laterally by up to half the residual, which on a 4 km edge is ~3 m
	// and dragged refined centerlines back off the track pair between
	// iterations (they never converged). Close with a SIMILARITY
	// transform about the start instead — rotate and uniformly scale so
	// the end lands exactly — which pins both ends while preserving
	// shape: circles stay circles, and the correction is ~0.1 deg and
	// ~1.001x here.
	u := out[n-1].Sub(out[0])
	v := pts[n-1].Sub(out[0])
	un, vn := u.Norm(), v.Norm()
	if un > 1e-9 && vn > 1e-9 {
		c := (u.X*v.X + u.Y*v.Y) / (un * un)  // scale * cos(rot)
		sn := (u.X*v.Y - u.Y*v.X) / (un * un) // scale * sin(rot)
		for i := 1; i < n; i++ {
			d := out[i].Sub(out[0])
			out[i] = Pt{
				X: out[0].X + c*d.X - sn*d.Y,
				Y: out[0].Y + sn*d.X + c*d.Y,
			}
		}
	}
	return out
}

// GaussianArc low-passes a polyline along its arc (endpoints pinned). Light
// finishing only — heavy blur smears real geometry (LESSONS: σ22 hid defects
// AND detail; the refined centerline needs only σ≈8).
// Each output point is a pure function of (pts, arc, i) written by index,
// so long lines fan the loop out across workers with identical results.
func GaussianArc(pts []Pt, sigma float64) []Pt {
	n := len(pts)
	if n < 5 || sigma <= 0 {
		return pts
	}
	arc := make([]float64, n)
	for i := 1; i < n; i++ {
		arc[i] = arc[i-1] + pts[i].Dist(pts[i-1])
	}
	win := 3 * sigma
	out := make([]Pt, n)
	out[0], out[n-1] = pts[0], pts[n-1]
	one := func(i int) {
		var sx, sy, sw float64
		for j := i; j >= 0 && arc[i]-arc[j] <= win; j-- {
			w := math.Exp(-sq(arc[i]-arc[j]) / (2 * sigma * sigma))
			sx += pts[j].X * w
			sy += pts[j].Y * w
			sw += w
		}
		for j := i + 1; j < n && arc[j]-arc[i] <= win; j++ {
			w := math.Exp(-sq(arc[j]-arc[i]) / (2 * sigma * sigma))
			sx += pts[j].X * w
			sy += pts[j].Y * w
			sw += w
		}
		out[i] = Pt{sx / sw, sy / sw}
	}
	ParFor(1, n-1, one)
	return out
}

// ParFor runs fn(i) for i in [lo, hi) — serially below a spawn threshold,
// otherwise strided across NumCPU workers. fn must write only per-i state;
// results are then identical to the serial loop.
func ParFor(lo, hi int, fn func(i int)) {
	n := hi - lo
	if n < 256 {
		for i := lo; i < hi; i++ {
			fn(i)
		}
		return
	}
	nw := runtime.NumCPU()
	if nw > n {
		nw = n
	}
	var wg sync.WaitGroup
	for wk := 0; wk < nw; wk++ {
		wg.Add(1)
		go func(wk int) {
			defer wg.Done()
			for i := lo + wk; i < hi; i += nw {
				fn(i)
			}
		}(wk)
	}
	wg.Wait()
}

func sq(x float64) float64 { return x * x }

// ProjectArc returns the arc position of the closest point on the line to p
// (and the distance). Used for lateral correspondence between parallel
// strands — never for centerline geometry (LESSONS #2).
func (l *Line) ProjectArc(p Pt) (float64, float64) {
	best2 := math.Inf(1)
	bestI := -1
	var bestQ Pt
	var bdx, bdy float64
	for i := 1; i < len(l.Pts); i++ {
		a, b := l.Pts[i-1], l.Pts[i]
		ab := b.Sub(a)
		n2 := ab.Dot(ab)
		t := 0.0
		if n2 > 1e-18 {
			t = math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/n2))
		}
		q := a.Add(ab.Scale(t))
		dx, dy := p.X-q.X, p.Y-q.Y
		if d2 := dx*dx + dy*dy; d2 < best2 {
			best2, bestI, bestQ = d2, i, q
			bdx, bdy = dx, dy
		}
	}
	if bestI < 0 {
		return 0, math.Inf(1)
	}
	return l.arc[bestI-1] + l.Pts[bestI-1].Dist(bestQ), math.Hypot(bdx, bdy)
}
