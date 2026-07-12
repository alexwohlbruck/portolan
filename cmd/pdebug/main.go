package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/osm"
)

func main() {
	ways, err := osm.Load("testdata/nyc-rail.geojson")
	if err != nil {
		panic(err)
	}
	minLon, minLat, maxLon, maxLat := 180.0, 90.0, -180.0, -90.0
	for _, w := range ways {
		for _, c := range w.Coords {
			if c.Lon < minLon {
				minLon = c.Lon
			}
			if c.Lon > maxLon {
				maxLon = c.Lon
			}
			if c.Lat < minLat {
				minLat = c.Lat
			}
			if c.Lat > maxLat {
				maxLat = c.Lat
			}
		}
	}
	frame := geo.NewFrame(geo.LL{Lon: (minLon + maxLon) / 2, Lat: (minLat + maxLat) / 2})
	tracks := make([]bundle.Track, len(ways))
	for i, w := range ways {
		pts := make([]geo.Pt, len(w.Coords))
		for j, ll := range w.Coords {
			pts[j] = frame.ToXY(ll)
		}
		tracks[i] = bundle.Track{ID: w.ID, Line: geo.NewLine(pts)}
	}
	g := bundle.BuildGraph(tracks, bundle.DefaultGraphParams())

	// hole probe: 40.65872,-73.97602
	probe := frame.ToXY(geo.LL{Lon: -74.00365, Lat: 40.71349})
	fmt.Println("strands within 60m of hole probe:")
	for si, s := range g.Strands {
		if s.Line.DistTo(probe) < 60 {
			fmt.Printf("  strand %d len=%.0fm dist=%.0fm ways[0]=%s\n",
				si, s.Line.Len(), s.Line.DistTo(probe), s.Ways[0])
		}
	}
	fmt.Println("corridors touching those strands:")
	seen := map[int]bool{}
	for _, c := range g.Corridors {
		touch := false
		for _, si := range c.Strands {
			if g.Strands[si].Line.DistTo(probe) < 60 {
				touch = true
			}
		}
		if touch && !seen[c.ID] {
			seen[c.ID] = true
			memKm := 0.0
			for _, m := range c.Members {
				memKm += (m.To - m.From) / 1000
			}
			fmt.Printf("  corridor %d: %d strands, %d members (%.1f km member arc), centerline %.0f m, cl-dist-to-probe %.0f m\n",
				c.ID, len(c.Strands), len(c.Members), memKm, c.Centerline.Len(), c.Centerline.DistTo(probe))
		}
	}
	// strand continuity check: chaining must never teleport
	bad := 0
	for si, s := range g.Strands {
		worst := 0.0
		for i := 1; i < len(s.Line.Pts); i++ {
			d := s.Line.Pts[i].Dist(s.Line.Pts[i-1])
			if d > worst {
				worst = d
			}
		}
		if worst > 60 {
			bad++
			if bad <= 6 {
				fmt.Printf("JUMPY strand %d: max span %.0fm, len %.0fm, %d ways\n",
					si, worst, s.Line.Len(), len(s.Ways))
			}
		}
	}
	fmt.Printf("%d strands with >60m spans\n", bad)
	// the group CONTAINING the probe: members + intervals
	arc202 := -1.0
	for si, s := range g.Strands {
		if s.Line.DistTo(probe) < 10 {
			a, _ := s.Line.ProjectArc(probe)
			fmt.Printf("probe on strand %d at arc %.0f (len %.0f)\n", si, a, s.Line.Len())
			if arc202 < 0 {
				arc202 = a
			}
			for _, c := range g.Corridors {
				for _, m := range c.Members {
					if m.Strand == si && a >= m.From && a <= m.To {
						fmt.Printf("  -> corridor %d (centerline %.0fm, %d members, cl->probe %.0fm, clA=%.0f,%.0f clB=%.0f,%.0f):\n",
							c.ID, c.Centerline.Len(), len(c.Members), c.Centerline.DistTo(probe),
							c.Centerline.Pts[0].X, c.Centerline.Pts[0].Y,
							c.Centerline.Pts[len(c.Centerline.Pts)-1].X, c.Centerline.Pts[len(c.Centerline.Pts)-1].Y)
						for vi := 0; vi < len(c.Centerline.Pts); vi += 8 {
							pp := c.Centerline.Pts[vi]
							fmt.Printf("      cl v%03d  %7.0f,%7.0f\n", vi, pp.X, pp.Y)
						}
						sp275 := bundle.SubLine(g.Strands[275].Line, 9402, 10742)
						fmt.Printf("     spine275 ends: %.0f,%.0f .. %.0f,%.0f len=%.0f\n",
							sp275.Pts[0].X, sp275.Pts[0].Y,
							sp275.Pts[len(sp275.Pts)-1].X, sp275.Pts[len(sp275.Pts)-1].Y, sp275.Len())
						sp2 := bundle.SubLine(g.Strands[202].Line, 7667, 8714)
						fmt.Printf("     member-line ends: %.0f,%.0f .. %.0f,%.0f  probe=%.0f,%.0f\n",
							sp2.Pts[0].X, sp2.Pts[0].Y, sp2.Pts[len(sp2.Pts)-1].X, sp2.Pts[len(sp2.Pts)-1].Y, probe.X, probe.Y)
						for _, mm := range c.Members {
							fmt.Printf("     strand %4d [%6.0f..%6.0f] (%5.0fm)\n",
								mm.Strand, mm.From, mm.To, mm.To-mm.From)
						}
					}
				}
			}
		}
	}
	// group size distribution
	type row struct{ memKm, clKm float64; id int }
	var rows []row
	for _, c := range g.Corridors {
		memKm := 0.0
		for _, m := range c.Members {
			memKm += (m.To - m.From) / 1000
		}
		rows = append(rows, row{memKm, c.Centerline.Len() / 1000, c.ID})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].memKm > rows[j].memKm })
	fmt.Println("top-8 groups by member-km (member-km vs centerline-km):")
	for i, r := range rows {
		if i >= 8 {
			break
		}
		fmt.Fprintf(os.Stdout, "  corridor %d: members %.1f km, centerline %.1f km\n", r.id, r.memKm, r.clKm)
	}
}
