package geo

import "math"

// Grid is a uniform spatial hash over line segments — the only index the
// pipeline needs (cities fit in memory as vectors; no R-tree, no raster).
type Grid struct {
	cell  float64
	cells map[[2]int][]segRef
	lines []*Line
}

type segRef struct{ line, seg int }

// NewGrid indexes every segment of every line at the given cell size
// (choose ≥ the largest query reach, typically 32–64 m).
func NewGrid(lines []*Line, cell float64) *Grid {
	g := &Grid{cell: cell, cells: map[[2]int][]segRef{}, lines: lines}
	for li, l := range lines {
		for si := 1; si < len(l.Pts); si++ {
			a, b := l.Pts[si-1], l.Pts[si]
			g.eachCell(a, b, func(c [2]int) {
				g.cells[c] = append(g.cells[c], segRef{li, si})
			})
		}
	}
	return g
}

func (g *Grid) key(p Pt) [2]int {
	return [2]int{int(math.Floor(p.X / g.cell)), int(math.Floor(p.Y / g.cell))}
}

func (g *Grid) eachCell(a, b Pt, fn func([2]int)) {
	eachSegCell(a, b, g.cell, fn)
}

// eachSegCell visits the cells a segment is indexed under. Short spans use
// the exact endpoint rectangle — its cell ORDER is load-bearing (see Near)
// and city geometry never exceeds it, so every existing build is
// byte-identical. Long spans walk ALONG the segment instead: the rectangle
// between the endpoints of a km-long diagonal is quadratic in its length
// (a 50 km segment at 64 m cells is 600k cells), and simplified intercity
// shapes produced exactly that — gigabytes of live index for geometry that
// only ever touches a thin band of cells.
func eachSegCell(a, b Pt, cell float64, fn func([2]int)) {
	key := func(p Pt) [2]int {
		return [2]int{int(math.Floor(p.X / cell)), int(math.Floor(p.Y / cell))}
	}
	ka, kb := key(a), key(b)
	x0, x1 := min(ka[0], kb[0]), max(ka[0], kb[0])
	y0, y1 := min(ka[1], kb[1]), max(ka[1], kb[1])
	if (x1-x0+1)*(y1-y0+1) <= 16 {
		for x := x0; x <= x1; x++ {
			for y := y0; y <= y1; y++ {
				fn([2]int{x, y})
			}
		}
		return
	}
	// Half-cell steps emit the cells the segment CROSSES — nothing more.
	// That is sufficient for every consumer: Grid.Near scans a +1-cell
	// margin around the query, and lineIndex dilates its hood at build
	// time, so a corner-clipped cell the stepping skips is always covered
	// by a neighboring crossed cell inside those margins. (A 3×3 stamp
	// here once "helped" — and made the continental index 9× fatter for
	// zero extra correctness.)
	seen := map[[2]int]bool{}
	n := int(math.Ceil(a.Dist(b)/(cell/2))) + 1
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		c := key(Pt{a.X + t*(b.X-a.X), a.Y + t*(b.Y-a.Y)})
		if !seen[c] {
			seen[c] = true
			fn(c)
		}
	}
}

// segWithin reports segDist(p,a,b) <= reach with the identical outcome but
// (almost) never the square root: the squared comparison decides outside a
// ±1e-9 relative band around reach², and the knife edge falls back to the
// exact compare. (fl(dx²+dy²) is within (1±3ε) of the true square and Hypot
// within 1 ulp of the true distance, so decisions outside the band agree.)
func segWithin(p, a, b Pt, reach float64) bool {
	d2, _, _ := segDist2(p, a, b)
	r2 := reach * reach
	if d2 <= r2*(1-1e-9) {
		return true
	}
	if d2 > r2*(1+1e-9) {
		return false
	}
	return segDist(p, a, b) <= reach
}

// segWithinStrict is segWithin for the strict comparison segDist < reach.
func segWithinStrict(p, a, b Pt, reach float64) bool {
	d2, _, _ := segDist2(p, a, b)
	r2 := reach * reach
	if d2 < r2*(1-1e-9) {
		return true
	}
	if d2 >= r2*(1+1e-9) {
		return false
	}
	return segDist(p, a, b) < reach
}

// Near visits every distinct line with at least one segment within reach of
// p, passing the line index. Visits are deduplicated.
func (g *Grid) Near(p Pt, reach float64, fn func(line int)) {
	// NOTE: the visit ORDER (cell scan order, first-witness per line) is
	// load-bearing — callers break score ties by first visit (e.g. FAIR's
	// trackCurveBetween). Changing the scan radius changes that order even
	// though the visited SET is identical; keep +1.
	r := int(math.Ceil(reach/g.cell)) + 1
	k := g.key(p)
	seen := map[int]bool{}
	for dx := -r; dx <= r; dx++ {
		for dy := -r; dy <= r; dy++ {
			for _, ref := range g.cells[[2]int{k[0] + dx, k[1] + dy}] {
				if seen[ref.line] {
					continue
				}
				l := g.lines[ref.line]
				if segWithin(p, l.Pts[ref.seg-1], l.Pts[ref.seg], reach) {
					seen[ref.line] = true
					fn(ref.line)
				}
			}
		}
	}
}

// NearestDist returns the distance from p to the nearest indexed segment
// within maxReach, or +Inf.
func (g *Grid) NearestDist(p Pt, maxReach float64) float64 {
	best2 := math.Inf(1)
	var bdx, bdy float64
	r := int(math.Ceil(maxReach/g.cell)) + 1
	k := g.key(p)
	for dx := -r; dx <= r; dx++ {
		for dy := -r; dy <= r; dy++ {
			for _, ref := range g.cells[[2]int{k[0] + dx, k[1] + dy}] {
				l := g.lines[ref.line]
				d2, ddx, ddy := segDist2(p, l.Pts[ref.seg-1], l.Pts[ref.seg])
				if d2 < best2 {
					best2, bdx, bdy = d2, ddx, ddy
				}
			}
		}
	}
	if math.IsInf(best2, 1) {
		return best2
	}
	return math.Hypot(bdx, bdy)
}
