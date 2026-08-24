package yards

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

const (
	outlinePadM = 15.0 // the outline hugs the outer tracks by this much
	msGridM     = 6.0  // marching-squares field pitch — boundary smoothness
	simplifyM   = 1.0  // ring vertex thinning tolerance
)

// hullOutline returns the padded offset contour of the region's hot
// member steel: the iso-line of distance-to-steel at outlinePadM, traced
// by marching squares over a fine field. This is the union-of-buffers
// boundary — it hugs the outer tracks with a tad of padding instead of
// the blocky cell staircase, which is what the region footprint MEANS.
// Largest loop wins (interior courtyards are dropped), CCW, thinned.
func hullOutline(steel []*geo.Line, pad float64) []geo.Pt {
	if len(steel) == 0 {
		return nil
	}
	lo := geo.Pt{X: math.Inf(1), Y: math.Inf(1)}
	hi := geo.Pt{X: math.Inf(-1), Y: math.Inf(-1)}
	for _, l := range steel {
		for _, p := range l.Pts {
			lo.X, lo.Y = math.Min(lo.X, p.X), math.Min(lo.Y, p.Y)
			hi.X, hi.Y = math.Max(hi.X, p.X), math.Max(hi.Y, p.Y)
		}
	}
	margin := pad + 2*msGridM
	x0, y0 := lo.X-margin, lo.Y-margin
	nx := int(math.Ceil((hi.X-lo.X+2*margin)/msGridM)) + 1
	ny := int(math.Ceil((hi.Y-lo.Y+2*margin)/msGridM)) + 1
	if nx < 2 || ny < 2 {
		return nil
	}
	// Signed field: distance-to-steel minus pad; negative = inside.
	// STAMPED, not queried: walking the steel at 3 m and writing min
	// distance into the surrounding grid window is union-of-buffers
	// exactly (disc scalloping at 3 m spacing is <8 cm), and it never
	// scans a dense ladder's segment lists — the per-point NearestDist
	// version took minutes on the Kearny complex alone.
	reach := pad + 2*msGridM
	f := make([]float64, nx*ny)
	for i := range f {
		f[i] = 1e9
	}
	win := int(math.Ceil(reach / msGridM))
	const stampStep = 3.0
	for _, l := range steel {
		total := l.Len()
		for s := 0.0; ; s += stampStep {
			if s > total {
				s = total
			}
			p := l.AtArc(s)
			ci := int(math.Round((p.X - x0) / msGridM))
			cj := int(math.Round((p.Y - y0) / msGridM))
			for j := max(0, cj-win); j <= min(ny-1, cj+win); j++ {
				for i := max(0, ci-win); i <= min(nx-1, ci+win); i++ {
					g := geo.Pt{X: x0 + float64(i)*msGridM, Y: y0 + float64(j)*msGridM}
					if d := g.Dist(p); d < f[j*nx+i] {
						f[j*nx+i] = d
					}
				}
			}
			if s >= total {
				break
			}
		}
	}
	for i := range f {
		v := f[i] - pad
		if v == 0 {
			v = 1e-9 // a corner exactly on the contour breaks interp
		}
		f[i] = v
	}

	// Marching squares, inside (v<0) kept on the LEFT so outer loops come
	// out CCW. Corners per cell: A=bl B=br C=tr D=tl.
	type seg struct{ a, b geo.Pt }
	var segs []seg
	at := func(i, j int) geo.Pt {
		return geo.Pt{X: x0 + float64(i)*msGridM, Y: y0 + float64(j)*msGridM}
	}
	interp := func(pa, pb geo.Pt, fa, fb float64) geo.Pt {
		t := fa / (fa - fb)
		return geo.Lerp(pa, pb, t)
	}
	for j := 0; j < ny-1; j++ {
		for i := 0; i < nx-1; i++ {
			fa, fb := f[j*nx+i], f[j*nx+i+1]
			fc, fd := f[(j+1)*nx+i+1], f[(j+1)*nx+i]
			code := 0
			if fa < 0 {
				code |= 1
			}
			if fb < 0 {
				code |= 2
			}
			if fc < 0 {
				code |= 4
			}
			if fd < 0 {
				code |= 8
			}
			if code == 0 || code == 15 {
				continue
			}
			A, B, C, D := at(i, j), at(i+1, j), at(i+1, j+1), at(i, j+1)
			pS := func() geo.Pt { return interp(A, B, fa, fb) }
			pE := func() geo.Pt { return interp(B, C, fb, fc) }
			pN := func() geo.Pt { return interp(D, C, fd, fc) }
			pW := func() geo.Pt { return interp(A, D, fa, fd) }
			emit := func(a, b geo.Pt) { segs = append(segs, seg{a, b}) }
			switch code {
			case 1: // A
				emit(pS(), pW())
			case 2: // B
				emit(pE(), pS())
			case 4: // C
				emit(pN(), pE())
			case 8: // D
				emit(pW(), pN())
			case 3: // A+B
				emit(pE(), pW())
			case 6: // B+C
				emit(pN(), pS())
			case 12: // C+D
				emit(pW(), pE())
			case 9: // D+A
				emit(pS(), pN())
			case 14: // all but A
				emit(pW(), pS())
			case 13: // all but B
				emit(pS(), pE())
			case 11: // all but C
				emit(pE(), pN())
			case 7: // all but D
				emit(pN(), pW())
			case 5: // A+C — ambiguous, resolve by center
				if fa+fb+fc+fd < 0 {
					emit(pS(), pE())
					emit(pN(), pW())
				} else {
					emit(pS(), pW())
					emit(pN(), pE())
				}
			case 10: // B+D
				if fa+fb+fc+fd < 0 {
					emit(pW(), pS())
					emit(pE(), pN())
				} else {
					emit(pE(), pS())
					emit(pW(), pN())
				}
			}
		}
	}
	if len(segs) == 0 {
		return nil
	}

	// Chain segments into loops via mm-quantized endpoints (shared cell
	// edges compute identical floats, so keys match exactly).
	qk := func(p geo.Pt) [2]int64 {
		return [2]int64{int64(math.Round(p.X * 1000)), int64(math.Round(p.Y * 1000))}
	}
	next := map[[2]int64]int{}
	used := make([]bool, len(segs))
	for si, s := range segs {
		if _, dup := next[qk(s.a)]; !dup {
			next[qk(s.a)] = si
		}
	}
	var best []geo.Pt
	bestArea := 0.0
	for si := range segs {
		if used[si] {
			continue
		}
		var loop []geo.Pt
		cur := si
		for !used[cur] {
			used[cur] = true
			loop = append(loop, segs[cur].a)
			nx, ok := next[qk(segs[cur].b)]
			if !ok {
				break
			}
			cur = nx
		}
		if len(loop) < 3 {
			continue
		}
		area := 0.0
		for i, p := range loop {
			q := loop[(i+1)%len(loop)]
			area += p.X*q.Y - q.X*p.Y
		}
		area /= 2
		if area > bestArea {
			bestArea, best = area, loop
		}
	}
	return thinRing(best, simplifyM)
}

// thinRing drops vertices lying within tol of the chord of their kept
// neighbours — marching squares emits one vertex per 6 m cell and a
// straight yard edge doesn't need 80 of them.
func thinRing(ring []geo.Pt, tol float64) []geo.Pt {
	if len(ring) < 5 {
		return ring
	}
	out := make([]geo.Pt, 0, len(ring)/2)
	for _, p := range ring {
		for len(out) >= 2 {
			a, b := out[len(out)-2], out[len(out)-1]
			ab := p.Sub(a)
			n2 := ab.Dot(ab)
			d := 0.0
			if n2 > 1e-18 {
				t := math.Max(0, math.Min(1, b.Sub(a).Dot(ab)/n2))
				d = b.Sub(a.Add(ab.Scale(t))).Norm()
			}
			if d < tol {
				out = out[:len(out)-1]
			} else {
				break
			}
		}
		out = append(out, p)
	}
	return out
}

// pointInRing is even-odd ray casting (last→first edge implied).
func pointInRing(ring []geo.Pt, p geo.Pt) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		a, b := ring[j], ring[i]
		if (a.Y > p.Y) != (b.Y > p.Y) {
			if a.X+(p.Y-a.Y)*(b.X-a.X)/(b.Y-a.Y) > p.X {
				in = !in
			}
		}
	}
	return in
}

// classifyCells splits the plane under a ring into interior cells (every
// point inside — one map hit answers InYard) and boundary cells (the ring
// passes through — queries pay an exact point-in-ring test). Boundary
// cells come from walking the ring; the interior floods 4-connected from
// the seed cells, never crossing a boundary cell — and a flood can only
// leak outside by crossing the ring, whose cells are all marked.
func classifyCells(ring []geo.Pt, seeds [][2]int) (interior, boundary map[[2]int]bool) {
	boundary = map[[2]int]bool{}
	n := len(ring)
	clo, chi := [2]int{1 << 30, 1 << 30}, [2]int{-(1 << 30), -(1 << 30)}
	for i := 0; i < n; i++ {
		a, b := ring[i], ring[(i+1)%n]
		steps := int(b.Sub(a).Norm()/(cellM/4)) + 1
		for k := 0; k <= steps; k++ {
			c := cellKey(geo.Lerp(a, b, float64(k)/float64(steps)))
			boundary[c] = true
			clo[0], clo[1] = min(clo[0], c[0]), min(clo[1], c[1])
			chi[0], chi[1] = max(chi[0], c[0]), max(chi[1], c[1])
		}
	}
	interior = map[[2]int]bool{}
	var stack [][2]int
	for _, c := range seeds {
		// A region whose steel splits into detached contour blobs keeps
		// only the largest loop — seeds from the other blobs sit OUTSIDE
		// the ring, and an unvalidated seed once flooded the plane.
		if boundary[c] || interior[c] {
			continue
		}
		center := geo.Pt{X: (float64(c[0]) + 0.5) * cellM, Y: (float64(c[1]) + 0.5) * cellM}
		if !pointInRing(ring, center) {
			continue
		}
		interior[c] = true
		stack = append(stack, c)
	}
	dirs := [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range dirs {
			nc := [2]int{c[0] + d[0], c[1] + d[1]}
			// the bbox bound makes a boundary gap a local misread, never
			// a plane flood
			if nc[0] < clo[0] || nc[0] > chi[0] || nc[1] < clo[1] || nc[1] > chi[1] {
				continue
			}
			if !boundary[nc] && !interior[nc] {
				interior[nc] = true
				stack = append(stack, nc)
			}
		}
	}
	return interior, boundary
}

// hullSeeds: cells under the steel itself — every steel point is inside
// the pad contour by construction, so these are safe flood seeds.
func hullSeeds(steel []*geo.Line) [][2]int {
	set := map[[2]int]bool{}
	for _, l := range steel {
		total := l.Len()
		for s := 0.0; ; s += cellM / 2 {
			if s > total {
				s = total
			}
			set[cellKey(l.AtArc(s))] = true
			if s >= total {
				break
			}
		}
	}
	out := make([][2]int, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
