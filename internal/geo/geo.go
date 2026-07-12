// Package geo is portolan's geometry core: exact vector primitives in a
// local metric frame. Every rule here is load-bearing — see docs/LESSONS.md.
package geo

import "math"

// Pt is a point in the local metric frame (meters).
type Pt struct{ X, Y float64 }

func (a Pt) Sub(b Pt) Pt          { return Pt{a.X - b.X, a.Y - b.Y} }
func (a Pt) Add(b Pt) Pt          { return Pt{a.X + b.X, a.Y + b.Y} }
func (a Pt) Scale(s float64) Pt   { return Pt{a.X * s, a.Y * s} }
func (a Pt) Dot(b Pt) float64     { return a.X*b.X + a.Y*b.Y }
func (a Pt) Cross(b Pt) float64   { return a.X*b.Y - a.Y*b.X }
func (a Pt) Dist(b Pt) float64    { return math.Hypot(a.X-b.X, a.Y-b.Y) }
func (a Pt) Norm() float64        { return math.Hypot(a.X, a.Y) }
func (a Pt) Unit() Pt             { n := a.Norm(); if n < 1e-12 { return Pt{1, 0} }; return Pt{a.X / n, a.Y / n} }
func (a Pt) Perp() Pt             { return Pt{-a.Y, a.X} } // left normal
func Lerp(a, b Pt, t float64) Pt  { return Pt{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t} }

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

// Line is a polyline in the metric frame with a cached cumulative-arc table.
type Line struct {
	Pts []Pt
	arc []float64 // cumulative arc length; arc[0]=0
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
func (l *Line) DistTo(p Pt) float64 {
	best := math.Inf(1)
	for i := 1; i < len(l.Pts); i++ {
		d := segDist(p, l.Pts[i-1], l.Pts[i])
		if d < best {
			best = d
		}
	}
	return best
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
		if !ok {
			continue
		}
		dir := l.Pts[i].Sub(l.Pts[i-1]).Unit()
		out = append(out, Crossing{
			Offset:   q.Sub(p).Dot(nrm),
			Parallel: math.Abs(dir.Dot(tangent)),
			At:       q,
		})
	}
	return out
}

// Crossing is one track crossing a cross-section ray.
type Crossing struct {
	Offset   float64 // signed lateral offset from the section origin
	Parallel float64 // |cos| of heading agreement with the section tangent
	At       Pt
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

// GaussianArc low-passes a polyline along its arc (endpoints pinned). Light
// finishing only — heavy blur smears real geometry (LESSONS: σ22 hid defects
// AND detail; the refined centerline needs only σ≈8).
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
	for i := 1; i < n-1; i++ {
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
	return out
}

func sq(x float64) float64 { return x * x }

// ProjectArc returns the arc position of the closest point on the line to p
// (and the distance). Used for lateral correspondence between parallel
// strands — never for centerline geometry (LESSONS #2).
func (l *Line) ProjectArc(p Pt) (float64, float64) {
	bestD := math.Inf(1)
	bestArc := 0.0
	for i := 1; i < len(l.Pts); i++ {
		a, b := l.Pts[i-1], l.Pts[i]
		ab := b.Sub(a)
		n2 := ab.Dot(ab)
		t := 0.0
		if n2 > 1e-18 {
			t = math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/n2))
		}
		q := a.Add(ab.Scale(t))
		d := p.Dist(q)
		if d < bestD {
			bestD = d
			bestArc = l.arc[i-1] + a.Dist(q)
		}
	}
	return bestArc, bestD
}
