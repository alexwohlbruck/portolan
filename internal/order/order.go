// Package order is stage 5: assign each corridor's berths to slots so that
// line crossings at nodes are minimized (LOOM's core problem, solved
// LOOM-lite: deterministic initial order + adjacent-swap descent against a
// node-crossing cost). Order lives in each corridor's own travel frame
// (storage A→B); positions at a node flip when the corridor arrives at its
// NodeA end (LESSONS #12).
package order

import (
	"sort"

	"github.com/alexwohlbruck/portolan/internal/berth"
	"github.com/alexwohlbruck/portolan/internal/bundle"
)

// Slots[c] = ordered route ids for corridor c (index = slot, storage frame).
type Slots map[int][]string

func Assign(g *bundle.Graph, br *berth.Result, sweeps int) Slots {
	slots := Slots{}
	for ci, bs := range br.Berths {
		ids := make([]string, len(bs))
		for i, b := range bs {
			ids[i] = b.RouteID
		}
		slots[ci] = ids // berths are already (color, id)-sorted → deterministic
	}
	// shared-move route sets per corridor pair
	type move struct {
		a, b   int
		routes []string
	}
	var moves []move
	for k, rs := range br.Moves {
		var ids []string
		for r := range rs {
			ids = append(ids, r)
		}
		sort.Strings(ids)
		moves = append(moves, move{k[0], k[1], ids})
	}
	sort.Slice(moves, func(i, j int) bool {
		if moves[i].a != moves[j].a {
			return moves[i].a < moves[j].a
		}
		return moves[i].b < moves[j].b
	})

	pos := func(ci int, route string, atNodeA bool) int {
		s := slots[ci]
		for i, r := range s {
			if r == route {
				if atNodeA {
					return len(s) - 1 - i
				}
				return i
			}
		}
		return -1
	}
	sharedNode := func(a, b int) (int, bool, bool) {
		ca, cb := g.Corridors[a], g.Corridors[b]
		// returns node, aAtNodeA, bAtNodeA
		switch {
		case ca.NodeB == cb.NodeA:
			return ca.NodeB, false, true
		case ca.NodeB == cb.NodeB:
			return ca.NodeB, false, false
		case ca.NodeA == cb.NodeA:
			return ca.NodeA, true, true
		case ca.NodeA == cb.NodeB:
			return ca.NodeA, true, false
		}
		return -1, false, false
	}
	crossings := func() int {
		total := 0
		for _, m := range moves {
			_, aA, bA := sharedNode(m.a, m.b)
			for i := 0; i < len(m.routes); i++ {
				for j := i + 1; j < len(m.routes); j++ {
					pa1, pa2 := pos(m.a, m.routes[i], aA), pos(m.a, m.routes[j], aA)
					pb1, pb2 := pos(m.b, m.routes[i], bA), pos(m.b, m.routes[j], bA)
					if pa1 < 0 || pa2 < 0 || pb1 < 0 || pb2 < 0 {
						continue
					}
					// crossing: order flips through the node (positions
					// compared in each corridor's frame facing the node —
					// continuity means same left-to-right order, which is
					// REVERSED between "arriving" and "leaving" frames)
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
