// Package berth is stage 4: map-match every route pattern onto the bundle
// graph. A route claims a berth (slot demand) on each corridor it rides;
// stretches with no corridor in reach become BRIDGE edges carrying the shape
// geometry (track data gaps — the Nostrand rule: shapes define geometry only
// across gaps; where tracks exist, tracks win).
package berth

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

type Params struct {
	Reach      float64 // shape sample → corridor centerline assignment radius
	SampleStep float64 // shape sampling (m)
	MinRun     float64 // an assignment run shorter than this is noise (m)
	MinGap     float64 // an unassigned run longer than this becomes a bridge
}

func DefaultParams() Params {
	return Params{Reach: 30, SampleStep: 15, MinRun: 45, MinGap: 60}
}

// Leg is one stretch of a matched pattern: a corridor traversal (with
// direction) or a bridge (gap geometry).
type Leg struct {
	Corridor int  // -1 for bridges
	Forward  bool // traversal in centerline A→B direction
	// for corridor legs: entry/exit arc positions on the centerline
	FromArc, ToArc float64
	Bridge         *geo.Line // for gap legs
}

// Match is one pattern's path through the graph.
type Match struct {
	Pattern gtfs.Pattern
	Legs    []Leg
}

// Route berthing summary per corridor.
type Berth struct {
	RouteID string
	Color   string
	Label   string
	Type    int // GTFS route_type (drives widths client-side)
}

// Result of stage 4.
type Result struct {
	Matches []Match
	// Berths[c] = the distinct routes riding corridor c (deterministic order)
	Berths map[int][]Berth
	// Moves records observed node movements: routes continuing corridor→
	// corridor (junction pairing evidence for FAIR/ORDER)
	Moves map[[2]int]map[string]bool
}

// MatchAll assigns every pattern to the graph.
func MatchAll(g *bundle.Graph, patterns []gtfs.Pattern, frame geo.Frame, p Params) *Result {
	lines := make([]*geo.Line, len(g.Corridors))
	for i, c := range g.Corridors {
		lines[i] = c.Centerline
	}
	grid := geo.NewGrid(lines, 64)

	res := &Result{Berths: map[int][]Berth{}, Moves: map[[2]int]map[string]bool{}}
	seen := map[int]map[string]bool{}

	for _, pat := range patterns {
		pts := make([]geo.Pt, len(pat.Shape))
		for i, ll := range pat.Shape {
			pts[i] = frame.ToXY(ll)
		}
		shape := geo.NewLine(pts)
		if shape.Len() < p.MinRun {
			continue
		}
		samples := shape.Resample(p.SampleStep)
		// nearest corridor per sample
		assign := make([]int, len(samples))
		for i, q := range samples {
			best, bestI := p.Reach, -1
			grid.Near(q, p.Reach, func(ci int) {
				if d := lines[ci].DistTo(q); d < best {
					best, bestI = d, ci
				}
			})
			assign[i] = bestI
		}
		// smooth: kill runs shorter than MinRun (flicker at corridor seams)
		minRun := int(math.Max(1, p.MinRun/p.SampleStep))
		assign = smoothRuns(assign, minRun)
		// build legs
		var legs []Leg
		i := 0
		for i < len(samples) {
			j := i
			for j+1 < len(samples) && assign[j+1] == assign[i] {
				j++
			}
			ci := assign[i]
			if ci >= 0 {
				cl := lines[ci]
				a0, _ := cl.ProjectArc(samples[i])
				a1, _ := cl.ProjectArc(samples[j])
				legs = append(legs, Leg{
					Corridor: ci, Forward: a1 >= a0, FromArc: a0, ToArc: a1,
				})
			} else if float64(j-i+1)*p.SampleStep >= p.MinGap {
				sub := geo.NewLine(samples[i : j+1])
				legs = append(legs, Leg{Corridor: -1, Bridge: sub})
			}
			i = j + 1
		}
		if len(legs) == 0 {
			continue
		}
		res.Matches = append(res.Matches, Match{Pattern: pat, Legs: legs})
		// berths + moves
		for li, leg := range legs {
			if leg.Corridor < 0 {
				continue
			}
			if seen[leg.Corridor] == nil {
				seen[leg.Corridor] = map[string]bool{}
			}
			if !seen[leg.Corridor][pat.Route.ID] {
				seen[leg.Corridor][pat.Route.ID] = true
				res.Berths[leg.Corridor] = append(res.Berths[leg.Corridor], Berth{
					RouteID: pat.Route.ID,
					Color:   routeColor(pat.Route),
					Label:   pat.Route.ShortName,
					Type:    pat.Route.Type,
				})
			}
			if li > 0 && legs[li-1].Corridor >= 0 {
				k := [2]int{legs[li-1].Corridor, leg.Corridor}
				if res.Moves[k] == nil {
					res.Moves[k] = map[string]bool{}
				}
				res.Moves[k][pat.Route.ID] = true
			}
		}
	}
	for ci := range res.Berths {
		sort.Slice(res.Berths[ci], func(a, b int) bool {
			x, y := res.Berths[ci][a], res.Berths[ci][b]
			if x.Color != y.Color {
				return x.Color < y.Color
			}
			return x.RouteID < y.RouteID
		})
	}
	return res
}

func smoothRuns(assign []int, minRun int) []int {
	n := len(assign)
	out := append([]int(nil), assign...)
	i := 0
	for i < n {
		j := i
		for j+1 < n && out[j+1] == out[i] {
			j++
		}
		if j-i+1 < minRun && i > 0 && j+1 < n && out[i-1] == out[j+1] {
			for k := i; k <= j; k++ {
				out[k] = out[i-1] // absorb flicker between identical neighbors
			}
		}
		i = j + 1
	}
	return out
}

var typeColor = map[int]string{
	0: "999933", 1: "333399", 2: "663300", 3: "336699", 4: "006666",
}

func routeColor(r gtfs.Route) string {
	if r.Color != "" {
		return r.Color
	}
	if c, ok := typeColor[r.Type]; ok {
		return c
	}
	return "555555"
}
