package fairway

// branch is a run of skeleton pixels between two nodes (pixels whose
// neighbour count is not 2), both ends included.
type branch struct {
	px   []int
	a, b int // end pixels
}

func (g grid) degree(b []bool, k int) int {
	n := 0
	for _, x := range g.nb(b, k%g.w, k/g.w) {
		if x {
			n++
		}
	}
	return n
}

func (g grid) neighbours(b []bool, k int, fn func(int)) {
	i, j := k%g.w, k/g.w
	// 4-neighbours first, so a walk never cuts a corner past a pixel it
	// would otherwise have to come back for
	for _, o := range [8][2]int{{0, 1}, {1, 0}, {0, -1}, {-1, 0}, {1, 1}, {1, -1}, {-1, -1}, {-1, 1}} {
		x, y := i+o[0], j+o[1]
		if x >= 0 && y >= 0 && x < g.w && y < g.h && b[y*g.w+x] {
			fn(y*g.w + x)
		}
	}
}

// branches splits a skeleton into node-to-node runs. A closed loop with no
// node (water round an island) comes back as one run from a pixel of it to
// itself.
func (g grid) branches(b []bool) []branch {
	isNode := func(k int) bool { return g.degree(b, k) != 2 }
	seen := map[[2]int]bool{} // directed first step out of a node
	used := map[int]bool{}    // interior pixels already traced
	var out []branch
	walk := func(start, first int) {
		if seen[[2]int{start, first}] {
			return
		}
		px := []int{start, first}
		prev, cur := start, first
		for !isNode(cur) && cur != start {
			used[cur] = true
			next := -1
			g.neighbours(b, cur, func(n int) {
				if next < 0 && n != prev && !(len(px) > 2 && n == px[len(px)-3]) {
					next = n
				}
			})
			if next < 0 {
				break
			}
			prev, cur = cur, next
			px = append(px, cur)
		}
		seen[[2]int{start, first}] = true
		if len(px) >= 2 {
			seen[[2]int{px[len(px)-1], px[len(px)-2]}] = true
		}
		out = append(out, branch{px: px, a: start, b: px[len(px)-1]})
	}
	for k := range b {
		if b[k] && isNode(k) {
			g.neighbours(b, k, func(n int) { walk(k, n) })
		}
	}
	for k := range b {
		if b[k] && !used[k] && !isNode(k) {
			var first = -1
			g.neighbours(b, k, func(n int) {
				if first < 0 {
					first = n
				}
			})
			if first >= 0 {
				walk(k, first)
			}
		}
	}
	return out
}

func (g grid) length(px []int) float64 {
	l := 0.0
	for k := 1; k < len(px); k++ {
		l += g.center(px[k-1]%g.w, px[k-1]/g.w).Dist(g.center(px[k]%g.w, px[k]/g.w))
	}
	return l
}

// prune removes dead-end branches shorter than keep(junction pixel): the
// spurs a shoreline bump or pier throws off the centerline. Repeats until
// stable, because removing one spur can turn its junction into a bend.
func (g grid) prune(b []bool, keep func(junction int) float64) {
	for round := 0; round < 50; round++ {
		removed := false
		for _, br := range g.branches(b) {
			da, db := g.degree(b, br.a), g.degree(b, br.b)
			var junction int
			var tail []int
			switch {
			case da == 1 && db >= 3:
				junction, tail = br.b, br.px[:len(br.px)-1]
			case db == 1 && da >= 3:
				junction, tail = br.a, br.px[1:]
			default:
				continue
			}
			if g.length(br.px) >= keep(junction) {
				continue
			}
			for _, k := range tail {
				b[k] = false
			}
			removed = true
		}
		if !removed {
			return
		}
	}
}
