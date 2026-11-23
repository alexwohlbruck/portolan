package fairway

import (
	"math"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// grid is a square-cell raster over a metric window. Row j grows north.
type grid struct {
	x0, y0, c float64
	w, h      int
}

func (g grid) cell(p geo.Pt) (int, int, bool) {
	i := int(math.Floor((p.X - g.x0) / g.c))
	j := int(math.Floor((p.Y - g.y0) / g.c))
	return i, j, i >= 0 && j >= 0 && i < g.w && j < g.h
}

func (g grid) center(i, j int) geo.Pt {
	return geo.Pt{X: g.x0 + (float64(i)+0.5)*g.c, Y: g.y0 + (float64(j)+0.5)*g.c}
}

// stroke calls fn for every cell a segment passes through.
func (g grid) stroke(a, b geo.Pt, fn func(idx int)) {
	n := int(math.Ceil(a.Dist(b)/(g.c/3))) + 1
	for k := 0; k <= n; k++ {
		if i, j, ok := g.cell(geo.Lerp(a, b, float64(k)/float64(n))); ok {
			fn(j*g.w + i)
		}
	}
}

// fillEvenOdd marks the cells whose centers fall inside rings under the
// even-odd rule, so a relation's holes stay unmarked.
func (g grid) fillEvenOdd(rings [][]geo.Pt, mark []bool) {
	var xs []float64
	for j := 0; j < g.h; j++ {
		y := g.y0 + (float64(j)+0.5)*g.c
		xs = xs[:0]
		for _, r := range rings {
			for k := 1; k < len(r); k++ {
				a, b := r[k-1], r[k]
				if (a.Y <= y) == (b.Y <= y) {
					continue
				}
				xs = append(xs, a.X+(y-a.Y)/(b.Y-a.Y)*(b.X-a.X))
			}
		}
		if len(xs) < 2 {
			continue
		}
		sortFloats(xs)
		for k := 0; k+1 < len(xs); k += 2 {
			i0 := int(math.Ceil((xs[k]-g.x0)/g.c - 0.5))
			i1 := int(math.Floor((xs[k+1]-g.x0)/g.c - 0.5))
			for i := max(i0, 0); i <= min(i1, g.w-1); i++ {
				mark[j*g.w+i] = true
			}
		}
	}
}

func sortFloats(a []float64) {
	for i := 1; i < len(a); i++ {
		for k := i; k > 0 && a[k] < a[k-1]; k-- {
			a[k], a[k-1] = a[k-1], a[k]
		}
	}
}

// edt returns, for every cell, the distance in metres to the nearest cell
// where feature is true (Felzenszwalb & Huttenlocher's exact transform).
func (g grid) edt(feature []bool) []float64 {
	inf := float64(g.w*g.w + g.h*g.h + 1)
	d := make([]float64, g.w*g.h)
	for k, f := range feature {
		if !f {
			d[k] = inf
		}
	}
	n := max(g.w, g.h)
	f, out := make([]float64, n), make([]float64, n)
	v, z := make([]int, n), make([]float64, n+1)
	for i := 0; i < g.w; i++ {
		for j := 0; j < g.h; j++ {
			f[j] = d[j*g.w+i]
		}
		dt1(f[:g.h], out[:g.h], v, z)
		for j := 0; j < g.h; j++ {
			d[j*g.w+i] = out[j]
		}
	}
	for j := 0; j < g.h; j++ {
		row := d[j*g.w : (j+1)*g.w]
		copy(f, row)
		dt1(f[:g.w], out[:g.w], v, z)
		copy(row, out[:g.w])
	}
	for k := range d {
		d[k] = math.Sqrt(d[k]) * g.c
	}
	return d
}

// dt1: one-dimensional squared distance transform of a sampled function.
func dt1(f, d []float64, v []int, z []float64) {
	n := len(f)
	k := 0
	v[0], z[0], z[1] = 0, math.Inf(-1), math.Inf(1)
	for q := 1; q < n; q++ {
		s := ((f[q] + float64(q*q)) - (f[v[k]] + float64(v[k]*v[k]))) / float64(2*q-2*v[k])
		for s <= z[k] {
			k--
			s = ((f[q] + float64(q*q)) - (f[v[k]] + float64(v[k]*v[k]))) / float64(2*q-2*v[k])
		}
		k++
		v[k], z[k], z[k+1] = q, s, math.Inf(1)
	}
	k = 0
	for q := 0; q < n; q++ {
		for z[k+1] < float64(q) {
			k++
		}
		d[q] = float64((q-v[k])*(q-v[k])) + f[v[k]]
	}
}

// within marks the cells no farther than r from a feature cell.
func (g grid) within(feature []bool, r float64) []bool {
	d := g.edt(feature)
	out := make([]bool, len(d))
	for k, x := range d {
		out[k] = x <= r
	}
	return out
}

func not(a []bool) []bool {
	out := make([]bool, len(a))
	for k, x := range a {
		out[k] = !x
	}
	return out
}

// neighbours of (i,j) in Zhang–Suen order: N, NE, E, SE, S, SW, W, NW.
var ring8 = [8][2]int{{0, 1}, {1, 1}, {1, 0}, {1, -1}, {0, -1}, {-1, -1}, {-1, 0}, {-1, 1}}

func (g grid) nb(b []bool, i, j int) (p [8]bool) {
	for k, o := range ring8 {
		x, y := i+o[0], j+o[1]
		p[k] = x >= 0 && y >= 0 && x < g.w && y < g.h && b[y*g.w+x]
	}
	return
}

func transitions(p [8]bool) (a, b int) {
	for k := 0; k < 8; k++ {
		if p[k] {
			b++
		}
		if !p[k] && p[(k+1)%8] {
			a++
		}
	}
	return
}

// thin reduces b in place to a one-pixel skeleton (Zhang–Suen), visiting
// only the current border so the cost tracks the area, not area × depth.
func (g grid) thin(b []bool) {
	queued := make([]bool, len(b))
	var border []int
	push := func(k int) {
		if b[k] && !queued[k] {
			queued[k] = true
			border = append(border, k)
		}
	}
	for k := range b {
		if !b[k] {
			continue
		}
		i, j := k%g.w, k/g.w
		if p := g.nb(b, i, j); !(p[0] && p[2] && p[4] && p[6]) {
			push(k)
		}
	}
	for changed := true; changed; {
		changed = false
		for step := 0; step < 2; step++ {
			var del, keep []int
			for _, k := range border {
				queued[k] = false
				if !b[k] {
					continue
				}
				p := g.nb(b, k%g.w, k/g.w)
				a, n := transitions(p)
				ok := n >= 2 && n <= 6 && a == 1
				if step == 0 {
					ok = ok && !(p[0] && p[2] && p[4]) && !(p[2] && p[4] && p[6])
				} else {
					ok = ok && !(p[0] && p[2] && p[6]) && !(p[0] && p[4] && p[6])
				}
				if ok {
					del = append(del, k)
				} else {
					keep = append(keep, k)
				}
			}
			border = border[:0]
			for _, k := range del {
				b[k] = false
			}
			for _, k := range keep {
				push(k)
			}
			for _, k := range del {
				i, j := k%g.w, k/g.w
				for _, o := range ring8 {
					x, y := i+o[0], j+o[1]
					if x >= 0 && y >= 0 && x < g.w && y < g.h {
						push(y*g.w + x)
					}
				}
			}
			changed = changed || len(del) > 0
		}
	}
	// thinning leaves knots two pixels thick, where every pixel reads as a
	// junction: strip any pixel whose neighbours stay connected without it
	for changed := true; changed; {
		changed = false
		for k := range b {
			if !b[k] {
				continue
			}
			p := g.nb(b, k%g.w, k/g.w)
			if n := countSet(p); n >= 2 && ringComponents(p) == 1 {
				b[k] = false
				changed = true
			}
		}
	}
}

// components labels the 8-connected runs of set cells, calling fn with each
// one's cells.
func (g grid) components(b []bool, fn func(cells []int)) {
	seen := make([]bool, len(b))
	var stack, cells []int
	for start := range b {
		if !b[start] || seen[start] {
			continue
		}
		seen[start] = true
		stack, cells = append(stack[:0], start), cells[:0]
		for len(stack) > 0 {
			k := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cells = append(cells, k)
			i, j := k%g.w, k/g.w
			for _, o := range ring8 {
				x, y := i+o[0], j+o[1]
				if x < 0 || y < 0 || x >= g.w || y >= g.h {
					continue
				}
				if m := y*g.w + x; b[m] && !seen[m] {
					seen[m] = true
					stack = append(stack, m)
				}
			}
		}
		fn(cells)
	}
}

// fillSmall turns land patches under minCells into water. Patches touching
// the window edge are left alone: they are mainland cut off by the window.
func (g grid) fillSmall(water []bool, minCells int) {
	g.components(not(water), func(cells []int) {
		if len(cells) >= minCells {
			return
		}
		for _, k := range cells {
			if i, j := k%g.w, k/g.w; i == 0 || j == 0 || i == g.w-1 || j == g.h-1 {
				return
			}
		}
		for _, k := range cells {
			water[k] = true
		}
	})
}

// dropShort clears skeleton pieces with fewer than minCells pixels.
func (g grid) dropShort(skel []bool, minCells int) {
	g.components(skel, func(cells []int) {
		if len(cells) < minCells {
			for _, k := range cells {
				skel[k] = false
			}
		}
	})
}

func countSet(p [8]bool) int {
	n := 0
	for _, x := range p {
		if x {
			n++
		}
	}
	return n
}

// ringComponents counts the 8-connected groups among a pixel's neighbours.
// Ring neighbours are adjacent to the next one round, and two edge
// neighbours (N, E, S, W) touch across the corner between them.
func ringComponents(p [8]bool) int {
	parent := [8]int{0, 1, 2, 3, 4, 5, 6, 7}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			x = parent[x]
		}
		return x
	}
	join := func(a, b int) {
		if p[a] && p[b] {
			parent[find(a)] = find(b)
		}
	}
	for k := 0; k < 8; k++ {
		join(k, (k+1)%8)
		if k%2 == 0 {
			join(k, (k+2)%8)
		}
	}
	n := 0
	for k := 0; k < 8; k++ {
		if p[k] && find(k) == k {
			n++
		}
	}
	return n
}
