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
	ka, kb := g.key(a), g.key(b)
	x0, x1 := min(ka[0], kb[0]), max(ka[0], kb[0])
	y0, y1 := min(ka[1], kb[1]), max(ka[1], kb[1])
	for x := x0; x <= x1; x++ {
		for y := y0; y <= y1; y++ {
			fn([2]int{x, y})
		}
	}
}

// Near visits every distinct line with at least one segment within reach of
// p, passing the line index. Visits are deduplicated.
func (g *Grid) Near(p Pt, reach float64, fn func(line int)) {
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
				if segDist(p, l.Pts[ref.seg-1], l.Pts[ref.seg]) <= reach {
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
	best := math.Inf(1)
	r := int(math.Ceil(maxReach/g.cell)) + 1
	k := g.key(p)
	for dx := -r; dx <= r; dx++ {
		for dy := -r; dy <= r; dy++ {
			for _, ref := range g.cells[[2]int{k[0] + dx, k[1] + dy}] {
				l := g.lines[ref.line]
				d := segDist(p, l.Pts[ref.seg-1], l.Pts[ref.seg])
				if d < best {
					best = d
				}
			}
		}
	}
	return best
}
