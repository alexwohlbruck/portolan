package stages

import (
	"slices"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

func TestAttributeWays(t *testing.T) {
	line := func(pts ...geo.Pt) *geo.Line { return geo.NewLine(pts) }
	// 100 → 200 in sequence, 300 stacked 4 m beside both carrying another route
	tracks := []bundle.Track{
		{ID: "100", Line: line(geo.Pt{X: 0}, geo.Pt{X: 300})},
		{ID: "way/200", Line: line(geo.Pt{X: 300}, geo.Pt{X: 600})},
		{ID: "300", Line: line(geo.Pt{X: 0, Y: 4}, geo.Pt{X: 600, Y: 4})},
	}
	g := buildTrackGraphCached(tracks)
	pieceOf := func(way string) int {
		for p := range g.pieces {
			if g.edges[2*p].Way == way {
				return p
			}
		}
		t.Fatalf("no piece for way %s", way)
		return -1
	}
	p100, p200, p300 := pieceOf("100"), pieceOf("way/200"), pieceOf("300")
	path := func(route string, pieces ...int) Path {
		pa := Path{Pattern: gtfs.Pattern{Route: gtfs.Route{ID: route, Type: 1}}}
		for _, p := range pieces {
			pa.Steps = append(pa.Steps, PathStep{Piece: p})
		}
		return pa
	}
	paths := []Path{path("A", p100, p200), path("B", p300)}
	all := unionPieces(nil, []int{p100, p200, p300})
	seg := func(kind string, x0, x1 float64) Segment {
		return Segment{Kind: kind, Routes: []string{"A"}, RouteType: 1,
			Line: line(geo.Pt{X: x0, Y: 2}, geo.Pt{X: x1, Y: 2}), Pieces: all}
	}
	segs := []Segment{
		seg("steady", 0, 600),
		seg("steady", 350, 600),
		seg("transition", 280, 320),
		seg("bridge", 0, 600),
		seg("steady", 600, 0),
	}
	AttributeWays(segs, paths, tracks)
	want := [][]string{{"100", "200"}, {"200"}, {"100", "200"}, nil, {"200", "100"}}
	for i, s := range segs {
		if !slices.Equal(s.Ways, want[i]) {
			t.Errorf("seg %d: ways %v, want %v", i, s.Ways, want[i])
		}
		if s.Pieces != nil {
			t.Errorf("seg %d: pieces not released", i)
		}
	}
}

func TestUnionPieces(t *testing.T) {
	a := []int{1, 3}
	got := unionPieces(a, []int{3, 2, 2})
	if !slices.Equal(got, []int{1, 2, 3}) || !slices.Equal(a, []int{1, 3}) {
		t.Fatalf("union %v, a %v", got, a)
	}
}
