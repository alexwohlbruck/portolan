// Package order is stage 5: assign each corridor's COLOR GROUPS to slots so
// that line crossings at nodes are minimized (LOOM's problem, solved
// LOOM-lite: deterministic initial order + adjacent-swap descent against a
// node-crossing cost).
//
// Slots are per COLOR, not per route: same-colored routes riding a corridor
// share ONE ribbon (B·D·F·M is one orange trunk, not four arcs) — the
// bundle-vs-route rule that attempt two never shipped. Order lives in each
// corridor's own travel frame (storage A→B); positions at a node flip when
// the corridor arrives at its NodeA end (LESSONS #12).
package order

import (
	"sort"

	"github.com/alexwohlbruck/portolan/internal/berth"
	"github.com/alexwohlbruck/portolan/internal/bundle"
)

// Slots[c] = ordered COLORS for corridor c (index = slot, storage frame).
type Slots map[int][]string

func Assign(g *bundle.Graph, br *berth.Result, sweeps int) Slots {
	slots := Slots{}
	for ci, bs := range br.Berths {
		seen := map[string]bool{}
		var colors []string
		for _, b := range bs { // berths are (color, id)-sorted → deterministic
			if !seen[b.Color] {
				seen[b.Color] = true
				colors = append(colors, b.Color)
			}
		}
		slots[ci] = colors
	}
	// route→color per corridor, to lift route moves to color moves
	colorOf := map[int]map[string]string{}
	for ci, bs := range br.Berths {
		m := map[string]string{}
		for _, b := range bs {
			m[b.RouteID] = b.Color
		}
		colorOf[ci] = m
	}
	type move struct {
		a, b   int
		colors []string
	}
	var moves []move
	for k, rs := range br.Moves {
		set := map[string]bool{}
		for r := range rs {
			if c, ok := colorOf[k[0]][r]; ok {
				set[c] = true
			}
		}
		var cs []string
		for c := range set {
			cs = append(cs, c)
		}
		sort.Strings(cs)
		if len(cs) > 0 {
			moves = append(moves, move{k[0], k[1], cs})
		}
	}
	sort.Slice(moves, func(i, j int) bool {
		if moves[i].a != moves[j].a {
			return moves[i].a < moves[j].a
		}
		return moves[i].b < moves[j].b
	})

	pos := func(ci int, color string, atNodeA bool) int {
		s := slots[ci]
		for i, c := range s {
			if c == color {
				if atNodeA {
					return len(s) - 1 - i
				}
				return i
			}
		}
		return -1
	}
	sharedFrames := func(a, b int) (bool, bool) {
		ca, cb := g.Corridors[a], g.Corridors[b]
		switch {
		case ca.NodeB == cb.NodeA:
			return false, true
		case ca.NodeB == cb.NodeB:
			return false, false
		case ca.NodeA == cb.NodeA:
			return true, true
		default:
			return true, false
		}
	}
	crossings := func() int {
		total := 0
		for _, m := range moves {
			aA, bA := sharedFrames(m.a, m.b)
			for i := 0; i < len(m.colors); i++ {
				for j := i + 1; j < len(m.colors); j++ {
					pa1, pa2 := pos(m.a, m.colors[i], aA), pos(m.a, m.colors[j], aA)
					pb1, pb2 := pos(m.b, m.colors[i], bA), pos(m.b, m.colors[j], bA)
					if pa1 < 0 || pa2 < 0 || pb1 < 0 || pb2 < 0 {
						continue
					}
					// continuity through a node preserves left-to-right order,
					// which is REVERSED between arriving and leaving frames
					if (pa1-pa2)*(pb1-pb2) > 0 {
						total++
					}
				}
			}
		}
		return total
	}

	best := crossings()
	var cids []int
	for ci := range slots {
		cids = append(cids, ci)
	}
	sort.Ints(cids)
	for s := 0; s < sweeps; s++ {
		improved := false
		for _, ci := range cids {
			ids := slots[ci]
			for i := 0; i+1 < len(ids); i++ {
				ids[i], ids[i+1] = ids[i+1], ids[i]
				if c := crossings(); c < best {
					best = c
					improved = true
				} else {
					ids[i], ids[i+1] = ids[i+1], ids[i]
				}
			}
		}
		if !improved {
			break
		}
	}
	return slots
}
