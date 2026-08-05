package stages

import (
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Ghost-route probe: for patterns whose matched paths carry gap bridges in
// the SoHo/LES area (R reroute, M Chrystie), report the committed flanking
// directed edges of every gap and whether the graph walk between them
// succeeds at increasing budgets and in either orientation — separating
// "budget too small", "wrong committed orientation", and "graph truly
// disconnected".
func TestGhostGaps(t *testing.T) {
	rail, tracks, frame := loadNYC(t)
	g := buildTrackGraph(tracks)

	paths, err := Match(rail, tracks, frame)
	if err != nil {
		t.Fatal(err)
	}
	m := &matcher{g: g, p: defaultMatchParams(), walks: map[[2]int]walkRes{},
		usedBy: map[int]map[string]bool{}, usedColor: map[int]map[string]bool{}}

	// undirected components over pieces
	parent := make([]int, len(g.nodes))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for _, e := range g.edges {
		parent[find(e.From)] = find(e.To)
	}
	compSize := map[int]int{}
	for i := range g.nodes {
		compSize[find(i)]++
	}

	ll := func(p geo.Pt) string {
		l := frame.ToLL(p)
		return fmt.Sprintf("%.5f,%.5f", l.Lat, l.Lon)
	}

	for _, path := range paths {
		id := path.Pattern.Route.ID
		sh := path.Pattern.ShapeID
		if !(sh == "R..N78R" || sh == "R..S78R" || sh == "M..N24R" || sh == "M..S24R") {
			continue
		}
		for si, st := range path.Steps {
			if st.Piece != -1 {
				continue
			}
			gl := 0.0
			if st.Gap != nil {
				gl = st.Gap.Len()
			}
			if gl < 40 {
				continue
			}
			fmt.Printf("\n%s %s gap #%d len=%.0fm", id, sh, si, gl)
			if st.Gap != nil {
				fmt.Printf("  from %s to %s", ll(st.Gap.Pts[0]), ll(st.Gap.Pts[len(st.Gap.Pts)-1]))
			}
			fmt.Println()
			if si == 0 || si == len(path.Steps)-1 {
				fmt.Println("  terminal gap — no flanks")
				continue
			}
			a, b := path.Steps[si-1], path.Steps[si+1]
			if a.Piece < 0 || b.Piece < 0 {
				fmt.Println("  flanked by another gap")
				continue
			}
			ea := 2 * a.Piece
			if a.Rev {
				ea++
			}
			eb := 2 * b.Piece
			if b.Rev {
				eb++
			}
			la, lb := g.edges[ea].Line.Len(), g.edges[eb].Line.Len()
			ca := compSize[find(g.edges[ea].To)]
			cb := compSize[find(g.edges[eb].From)]
			fmt.Printf("  flankA piece %d way %s len %.0fm To@%s (comp %d)\n",
				a.Piece, g.edges[ea].Way, la, ll(g.nodes[g.edges[ea].To].At), ca)
			fmt.Printf("  flankB piece %d way %s len %.0fm From@%s (comp %d)\n",
				b.Piece, g.edges[eb].Way, lb, ll(g.nodes[g.edges[eb].From].At), cb)
			for _, budget := range []float64{350, 4*gl + 400, 3000, 10000} {
				w := m.walk(ea, eb, budget)
				status := "FAIL"
				if w.ok {
					status = fmt.Sprintf("ok cost=%.0f via=%d", w.cost, len(w.via))
				}
				fmt.Printf("  walk budget %6.0f: %s\n", budget, status)
			}
			// orientation variants at the generous budget
			for _, v := range [][2]int{{ea ^ 1, eb}, {ea, eb ^ 1}, {ea ^ 1, eb ^ 1}} {
				w := m.walk(v[0], v[1], 4*gl+400)
				if w.ok {
					fmt.Printf("  walk variant u^%d v^%d: ok cost=%.0f via=%d\n",
						v[0]&1, v[1]&1, w.cost, len(w.via))
				}
			}
		}
	}

	// largest components for context
	var sizes []int
	for _, s := range compSize {
		sizes = append(sizes, s)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	if len(sizes) > 6 {
		sizes = sizes[:6]
	}
	fmt.Printf("\ncomponent sizes (top): %v of %d nodes\n", sizes, len(g.nodes))
	_ = math.Inf
}
