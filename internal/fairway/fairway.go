// Package fairway derives river centerlines from the shoreline, so ferries
// that share a river draw as one line down its middle instead of a fan of
// OSM ferry lanes (docs/FAIRWAY.md).
//
// Water is rasterised, its shoreline smoothed so piers and slips stop
// mattering, and the result thinned to its medial axis. Spurs thrown off by
// shoreline bumps are pruned, and only river-width reaches are kept: in an
// open harbour the medial axis means nothing, and ferries there keep their
// own lanes.
package fairway

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Shore is the water outline in a metric frame. Coast lines carry OSM's
// orientation: land on the left, water on the right. Each Area is one
// element's rings, filled even-odd.
type Shore struct {
	Coast [][]geo.Pt
	Areas [][][]geo.Pt
}

// Load reads a water extract (tools/feed.sh water) into frame coordinates.
func Load(path string, frame geo.Frame) (*Shore, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc struct {
		Features []struct {
			Geometry struct {
				Type   string          `json:"type"`
				Coords json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("fairway: %s: %w", path, err)
	}
	xy := func(cs [][]float64) []geo.Pt {
		pts := make([]geo.Pt, 0, len(cs))
		for _, c := range cs {
			if len(c) >= 2 {
				pts = append(pts, frame.ToXY(geo.LL{Lon: c[0], Lat: c[1]}))
			}
		}
		return pts
	}
	s := &Shore{}
	for _, f := range fc.Features {
		switch f.Geometry.Type {
		case "LineString":
			var cs [][]float64
			if json.Unmarshal(f.Geometry.Coords, &cs) == nil && len(cs) >= 2 {
				s.Coast = append(s.Coast, xy(cs))
			}
		case "Polygon":
			var rs [][][]float64
			if json.Unmarshal(f.Geometry.Coords, &rs) != nil {
				continue
			}
			var rings [][]geo.Pt
			for _, r := range rs {
				if len(r) >= 4 {
					rings = append(rings, xy(r))
				}
			}
			if len(rings) > 0 {
				s.Areas = append(s.Areas, rings)
			}
		}
	}
	return s, nil
}

// Params tunes the centerline. Lengths are metres.
type Params struct {
	Cell      float64 // raster resolution
	Smooth    float64 // shoreline features narrower than 2× this vanish
	MaxRadius float64 // half-width above which water is open, not a river
	MinRadius float64 // half-width below which no ferry fits
	SpurRatio float64 // a dead end shorter than this × its junction's half-width is a spur
	MinLen    float64 // shortest connected piece of network worth keeping
	MinIslet  float64 // land smaller than this (m²) is a pier, buoy or barge, not an island
	Bend      float64 // smoothing along the centerline: wobbles shorter than this go
}

func DefaultParams() Params {
	return Params{Cell: 25, Smooth: 60, MaxRadius: 1100, MinRadius: 60, SpurRatio: 2.5, MinLen: 1500, MinIslet: 250_000, Bend: 150}
}

// Reach is one stretch of centerline with the channel half-width at each
// vertex.
type Reach struct {
	Line   *geo.Line
	Radius []float64
}

// Net is the fairway network: river centerlines a ferry can ride.
type Net struct {
	Reaches []Reach
	index   *geo.Grid
}

// Build derives the fairway network over the window [lo, hi].
func Build(s *Shore, lo, hi geo.Pt, p Params) *Net {
	c := p.Cell
	// a metro window at 25 m is a few million cells; cap a national one
	for (hi.X-lo.X)/c*(hi.Y-lo.Y)/c > 16e6 {
		c *= 1.5
	}
	g := grid{x0: lo.X, y0: lo.Y, c: c,
		w: int(math.Ceil((hi.X - lo.X) / c)), h: int(math.Ceil((hi.Y - lo.Y) / c))}
	if g.w < 3 || g.h < 3 {
		return &Net{}
	}
	water := g.water(s)
	// open the water so slips between piers fill in, then close it so the
	// piers themselves vanish
	water = g.within(not(g.within(not(water), p.Smooth)), p.Smooth)
	water = not(g.within(not(g.within(water, p.Smooth)), p.Smooth))
	g.fillSmall(water, int(p.MinIslet/(g.c*g.c)))
	radius := g.edt(not(water))

	skel := append([]bool(nil), water...)
	g.thin(skel)
	spur := func(j int) float64 { return p.SpurRatio*radius[j] + 4*g.c }
	g.prune(skel, spur)
	for k, on := range skel {
		if on && (radius[k] > p.MaxRadius || radius[k] < p.MinRadius) {
			skel[k] = false
		}
	}
	g.prune(skel, spur)
	g.dropShort(skel, int(p.MinLen/g.c))

	n := &Net{}
	for _, br := range g.branches(skel) {
		if len(br.px) < 2 {
			continue
		}
		pts := make([]geo.Pt, len(br.px))
		for k, px := range br.px {
			pts[k] = g.center(px%g.w, px/g.w)
		}
		pts = simplify(geo.GaussianArc(geo.NewLine(pts).Resample(20), p.Bend), 2)
		rad := make([]float64, len(pts))
		for k, q := range pts {
			if i, j, ok := g.cell(q); ok {
				rad[k] = math.Max(radius[j*g.w+i], g.c)
			}
		}
		n.Reaches = append(n.Reaches, Reach{Line: geo.NewLine(pts), Radius: rad})
	}
	lines := make([]*geo.Line, len(n.Reaches))
	for k, r := range n.Reaches {
		lines[k] = r.Line
	}
	n.index = geo.NewGrid(lines, 500)
	return n
}

// water rasterises the shore: coastline splits the window into regions,
// each voted sea or land by which side of the coastline it lies on; water
// areas are filled on top.
func (g grid) water(s *Shore) []bool {
	n := g.w * g.h
	wall := make([]bool, n)
	for _, l := range s.Coast {
		for k := 1; k < len(l); k++ {
			g.stroke(l[k-1], l[k], func(i int) { wall[i] = true })
		}
	}
	region := make([]int32, n)
	for i := range region {
		region[i] = -1
	}
	var regions int32
	var stack []int
	for start := range wall {
		if wall[start] || region[start] >= 0 {
			continue
		}
		region[start] = regions
		stack = append(stack[:0], start)
		for len(stack) > 0 {
			k := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			i, j := k%g.w, k/g.w
			for _, o := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				x, y := i+o[0], j+o[1]
				if x < 0 || y < 0 || x >= g.w || y >= g.h {
					continue
				}
				if m := y*g.w + x; !wall[m] && region[m] < 0 {
					region[m] = regions
					stack = append(stack, m)
				}
			}
		}
		regions++
	}
	vote := make([]int, regions)
	for _, l := range s.Coast {
		for k := 1; k < len(l); k++ {
			a, b := l[k-1], l[k]
			d := b.Sub(a)
			if d.Norm() < 1e-9 {
				continue
			}
			right := geo.Pt{X: d.Y, Y: -d.X}.Unit().Scale(1.5 * g.c)
			steps := int(d.Norm()/g.c) + 1
			for t := 0; t < steps; t++ {
				m := geo.Lerp(a, b, (float64(t)+0.5)/float64(steps))
				if i, j, ok := g.cell(m.Add(right)); ok && region[j*g.w+i] >= 0 {
					vote[region[j*g.w+i]]++
				}
				if i, j, ok := g.cell(m.Sub(right)); ok && region[j*g.w+i] >= 0 {
					vote[region[j*g.w+i]]--
				}
			}
		}
	}
	water := make([]bool, n)
	for k, r := range region {
		water[k] = r >= 0 && vote[r] > 0
	}
	for _, rings := range s.Areas {
		g.fillEvenOdd(rings, water)
	}
	// the coastline's own cells take the majority of their neighbours
	for k := range wall {
		if !wall[k] || water[k] {
			continue
		}
		wet := 0
		g.neighbours(water, k, func(int) { wet++ })
		water[k] = wet >= 4
	}
	return water
}

// nearest finds the closest fairway point to q within reach, with the
// channel half-width there and the centerline's direction.
func (n *Net) nearest(q geo.Pt, reach float64) (foot geo.Pt, d, radius float64, tangent geo.Pt, ok bool) {
	if n == nil || n.index == nil {
		return
	}
	d = math.Inf(1)
	n.index.Near(q, reach, func(li int) {
		r := n.Reaches[li]
		arc, dist := r.Line.ProjectArc(q)
		if dist >= d || dist > reach {
			return
		}
		d, ok = dist, true
		foot = r.Line.AtArc(arc)
		tangent = r.Line.TangentAtArc(arc, 50)
		radius = r.radiusAt(arc)
	})
	return
}

func (r Reach) radiusAt(arc float64) float64 {
	as := r.Line.ArcTable()
	for k := 1; k < len(as); k++ {
		if arc <= as[k] {
			span := as[k] - as[k-1]
			if span <= 0 {
				return r.Radius[k]
			}
			t := (arc - as[k-1]) / span
			return r.Radius[k-1]*(1-t) + r.Radius[k]*t
		}
	}
	return r.Radius[len(r.Radius)-1]
}

// InChannel reports whether p lies in the middle of a fairway's channel,
// away from either bank.
func (n *Net) InChannel(p geo.Pt) bool {
	_, d, r, _, ok := n.nearest(p, 2*n.maxRadius())
	return ok && d <= 0.8*r
}

func (n *Net) maxRadius() float64 {
	m := 0.0
	for _, r := range n.Reaches {
		for _, x := range r.Radius {
			m = math.Max(m, x)
		}
	}
	return m
}

// Snap pulls a ferry's path onto the centerline wherever it runs along a
// river, and leaves it alone where it crosses one, docks, or is out in
// open water. The handover eases over a few hundred metres so the line
// leaves the centerline in a curve, not a kink.
func (n *Net) Snap(pts []geo.Pt) []geo.Pt {
	if n == nil || len(n.Reaches) == 0 || len(pts) < 2 {
		return pts
	}
	const step = 20.0
	line := geo.NewLine(pts)
	if line.Len() < 2*step {
		return pts
	}
	s := line.Resample(step)
	reach := 1.2 * n.maxRadius()
	w := make([]float64, len(s))
	foot := make([]geo.Pt, len(s))
	for k, q := range s {
		foot[k] = q
		f, d, r, tan, ok := n.nearest(q, reach)
		if !ok {
			continue
		}
		dir := s[min(k+1, len(s)-1)].Sub(s[max(k-1, 0)]).Unit()
		align := math.Abs(dir.Dot(tan))
		foot[k] = f
		w[k] = ease(1.0, 0.75, d/r) * ease(math.Cos(50*math.Pi/180), math.Cos(25*math.Pi/180), align)
	}
	w = blur(w, 8)
	out := make([]geo.Pt, len(s))
	for k, q := range s {
		out[k] = geo.Lerp(q, foot[k], w[k])
	}
	out[0], out[len(out)-1] = pts[0], pts[len(pts)-1]
	return out
}

// ease is 0 at or beyond lo and 1 at or beyond hi, smooth between.
func ease(lo, hi, x float64) float64 {
	t := math.Max(0, math.Min(1, (x-lo)/(hi-lo)))
	return t * t * (3 - 2*t)
}

// blur is a Gaussian over sample indices, clamped at the ends.
func blur(v []float64, sigma float64) []float64 {
	r := int(3 * sigma)
	ker := make([]float64, 2*r+1)
	for i := range ker {
		x := float64(i - r)
		ker[i] = math.Exp(-x * x / (2 * sigma * sigma))
	}
	out := make([]float64, len(v))
	for k := range v {
		sum, wt := 0.0, 0.0
		for i, kw := range ker {
			j := k + i - r
			if j < 0 || j >= len(v) {
				continue
			}
			sum += kw * v[j]
			wt += kw
		}
		out[k] = sum / wt
	}
	return out
}

// simplify is Douglas–Peucker with the endpoints pinned.
func simplify(pts []geo.Pt, tol float64) []geo.Pt {
	if len(pts) < 3 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	var rec func(a, b int)
	rec = func(a, b int) {
		best, bi := 0.0, -1
		for k := a + 1; k < b; k++ {
			if d := geo.NewLine([]geo.Pt{pts[a], pts[b]}).DistTo(pts[k]); d > best {
				best, bi = d, k
			}
		}
		if bi >= 0 && best > tol {
			keep[bi] = true
			rec(a, bi)
			rec(bi, b)
		}
	}
	rec(0, len(pts)-1)
	var out []geo.Pt
	for k, p := range pts {
		if keep[k] {
			out = append(out, p)
		}
	}
	return out
}
