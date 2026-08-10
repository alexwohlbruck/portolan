package stages

import (
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/osm"
)

func loadNYC(t *testing.T) ([]gtfs.Pattern, []bundle.Track, geo.Frame) {
	gtfsPath := localGTFS("5")
	if _, err := os.Stat(gtfsPath); err != nil {
		t.Skip("NYC GTFS not available")
	}
	ways, err := osm.Load("../../testdata/nyc-rail.geojson")
	if err != nil {
		t.Fatal(err)
	}
	frame := frameOf(ways)
	tracks := make([]bundle.Track, len(ways))
	for i, w := range ways {
		pts := make([]geo.Pt, len(w.Coords))
		for j, ll := range w.Coords {
			pts[j] = frame.ToXY(ll)
		}
		tracks[i] = bundle.Track{ID: w.ID, Line: geo.NewLine(pts)}
	}
	feed, err := gtfs.Load(gtfsPath, 0.99)
	if err != nil {
		t.Fatal(err)
	}
	var rail []gtfs.Pattern
	for _, pat := range feed.Patterns {
		tt := pat.Route.Type
		if tt == 0 || tt == 1 || tt == 2 || (tt >= 100 && tt < 200) {
			rail = append(rail, pat)
		}
	}
	return rail, tracks, frame
}

func TestNetworkDiag(t *testing.T) {
	rail, tracks, frame := loadNYC(t)
	paths, err := Match(rail, tracks, frame)
	if err != nil {
		t.Fatal(err)
	}
	net, err := Split(paths, tracks)
	if err != nil {
		t.Fatal(err)
	}

	// components
	parent := make([]int, len(net.Nodes))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	for _, e := range net.Edges {
		parent[find(e.From)] = find(e.To)
	}
	compRoutes := map[int]map[string]bool{}
	compKm := map[int]float64{}
	for _, e := range net.Edges {
		c := find(e.From)
		if compRoutes[c] == nil {
			compRoutes[c] = map[string]bool{}
		}
		for _, r := range e.Routes {
			compRoutes[c][r] = true
		}
		compKm[c] += geo.NewLine(e.Pts).Len() / 1000
	}
	type comp struct {
		km     float64
		routes []string
	}
	var comps []comp
	for c, rs := range compRoutes {
		var ids []string
		for r := range rs {
			ids = append(ids, r)
		}
		sort.Strings(ids)
		comps = append(comps, comp{compKm[c], ids})
	}
	sort.Slice(comps, func(a, b int) bool { return comps[a].km > comps[b].km })
	fmt.Printf("components: %d\n", len(comps))
	for i, c := range comps {
		if i > 25 {
			break
		}
		fmt.Printf("  %6.1f km  %v\n", c.km, c.routes)
	}

	// dangling ends near other geometry = spurious breaks
	lines := make([]*geo.Line, len(net.Edges))
	for i, e := range net.Edges {
		lines[i] = geo.NewLine(e.Pts)
	}
	grid := geo.NewGrid(lines, 64)
	spurious := 0
	for ni, n := range net.Nodes {
		if len(n.Adj) != 1 {
			continue
		}
		own := n.Adj[0]
		best := 1e18
		grid.Near(n.At, 30, func(li int) {
			if li == own {
				return
			}
			if d := lines[li].DistTo(n.At); d < best {
				best = d
			}
		})
		if best < 25 {
			spurious++
			ll := frame.ToLL(n.At)
			if spurious <= 15 {
				e := net.Edges[own]
				fmt.Printf("  dangle %d @ %.5f,%.5f routes=%v dist=%.1fm\n",
					ni, ll.Lat, ll.Lon, e.Routes, best)
			}
		}
	}
	fmt.Printf("deg-1 nodes near other geometry (<25m): %d\n", spurious)
}
