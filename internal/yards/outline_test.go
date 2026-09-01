package yards

import (
	"math"
	"math/rand"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// inRing is even-odd ray casting against a closed ring (last→first edge
// implied).
func inRing(ring []geo.Pt, p geo.Pt) bool {
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

func ringDist(ring []geo.Pt, p geo.Pt) float64 {
	best := math.Inf(1)
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		a, b := ring[j], ring[i]
		ab := b.Sub(a)
		t := 0.0
		if n2 := ab.Dot(ab); n2 > 1e-18 {
			t = math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/n2))
		}
		if d := p.Dist(a.Add(ab.Scale(t))); d < best {
			best = d
		}
	}
	return best
}

func ringArea(ring []geo.Pt) float64 {
	s := 0.0
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		s += ring[j].X*ring[i].Y - ring[i].X*ring[j].Y
	}
	return s / 2
}

func TestOutlineConsistent(t *testing.T) {
	ix := Build(ladder(10, 4, 0, 0, 800, ""), DefaultParams())
	if len(ix.Regions()) != 1 {
		t.Fatal("scene did not form one region")
	}
	ring := ix.Regions()[0].Outline
	if len(ring) < 4 {
		t.Fatalf("outline has %d vertices", len(ring))
	}
	if ringArea(ring) <= 0 {
		t.Fatalf("outline is not CCW (area %.1f)", ringArea(ring))
	}
	n := len(ring)
	for i := 0; i < n; i++ {
		p, q, r := ring[(i+n-1)%n], ring[i], ring[(i+1)%n]
		if (q.X-p.X)*(r.Y-q.Y) == (q.Y-p.Y)*(r.X-q.X) {
			t.Fatalf("collinear run survives at vertex %d", i)
		}
	}
	// The ring and the cell mask must never disagree (single region, no
	// holes here): away from the knife edge, inside-the-ring IS InYard.
	for x := -80.0; x <= 880; x += 7 {
		for y := -100.0; y <= 140; y += 7 {
			p := geo.Pt{X: x, Y: y}
			if ringDist(ring, p) < 2 {
				continue
			}
			if got, want := ix.InYard(p), inRing(ring, p); got != want {
				t.Fatalf("probe %v: InYard %v, ring says %v", p, got, want)
			}
		}
	}
}

// Property: on random connected blobs (with pinches and holes), the
// traced ring must enclose every mask cell, exclude every point outside
// it from the mask, and for hole-free blobs carry exactly the mask area.
// A silent tracing miss doesn't fail on its own — it hands a consumer a
// yard with a bite taken out.
func TestOutlineMatchesMaskBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	dirs := [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	const cell = 24.0
	for trial := 0; trial < 60; trial++ {
		cells := map[[2]int]bool{}
		c := [2]int{rng.Intn(7) - 3, rng.Intn(7) - 3}
		cells[c] = true
		for i := 0; i < 60; i++ {
			d := dirs[rng.Intn(4)]
			c = [2]int{c[0] + d[0], c[1] + d[1]}
			cells[c] = true
		}
		ring := traceOutline(cells, cell)

		// Hole census: flood the complement from outside the bbox;
		// unreached complement cells are holes.
		lo, hi := [2]int{1 << 30, 1 << 30}, [2]int{-(1 << 30), -(1 << 30)}
		for cc := range cells {
			lo[0], lo[1] = min(lo[0], cc[0]), min(lo[1], cc[1])
			hi[0], hi[1] = max(hi[0], cc[0]), max(hi[1], cc[1])
		}
		lo[0], lo[1], hi[0], hi[1] = lo[0]-1, lo[1]-1, hi[0]+1, hi[1]+1
		outside := map[[2]int]bool{lo: true}
		stack := [][2]int{lo}
		for len(stack) > 0 {
			cc := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, d := range dirs {
				nc := [2]int{cc[0] + d[0], cc[1] + d[1]}
				if nc[0] < lo[0] || nc[0] > hi[0] || nc[1] < lo[1] || nc[1] > hi[1] ||
					cells[nc] || outside[nc] {
					continue
				}
				outside[nc] = true
				stack = append(stack, nc)
			}
		}
		holes := 0
		for x := lo[0]; x <= hi[0]; x++ {
			for y := lo[1]; y <= hi[1]; y++ {
				cc := [2]int{x, y}
				if !cells[cc] && !outside[cc] {
					holes++
				}
			}
		}

		if holes == 0 {
			want := float64(len(cells)) * cell * cell
			if got := ringArea(ring); math.Abs(got-want) > 1e-6 {
				t.Fatalf("trial %d: ring area %.1f, mask area %.1f", trial, got, want)
			}
		}
		for cc := range cells {
			center := geo.Pt{X: (float64(cc[0]) + 0.5) * cell, Y: (float64(cc[1]) + 0.5) * cell}
			if !inRing(ring, center) {
				t.Fatalf("trial %d: mask cell %v center outside the ring", trial, cc)
			}
		}
		for i := 0; i < 400; i++ {
			p := geo.Pt{
				X: (float64(lo[0]) + rng.Float64()*float64(hi[0]-lo[0]+1)) * cell,
				Y: (float64(lo[1]) + rng.Float64()*float64(hi[1]-lo[1]+1)) * cell,
			}
			if ringDist(ring, p) < 1e-6 {
				continue
			}
			cc := [2]int{int(math.Floor(p.X / cell)), int(math.Floor(p.Y / cell))}
			if !inRing(ring, p) && cells[cc] {
				t.Fatalf("trial %d: probe %v outside ring but its cell %v is in the mask", trial, p, cc)
			}
		}
	}
}
