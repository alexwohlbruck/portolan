package pipeline

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/osm"
)

func way(id string, lls ...[2]float64) osm.Way {
	w := osm.Way{ID: id}
	for _, ll := range lls {
		w.Coords = append(w.Coords, geo.LL{Lon: ll[0], Lat: ll[1]})
	}
	return w
}

func TestWindowWaysKeepsTrackNearTheWindow(t *testing.T) {
	bbox := []float64{-84.5, 33.6, -84.2, 33.9}
	ways := []osm.Way{
		way("inside", [2]float64{-84.4, 33.7}, [2]float64{-84.3, 33.8}),
		way("margin", [2]float64{-84.17, 33.7}, [2]float64{-84.16, 33.7}),
		way("spans", [2]float64{-86, 33.75}, [2]float64{-82, 33.75}),
		way("far", [2]float64{-77, 38.9}, [2]float64{-76.9, 39}),
		way("past-margin", [2]float64{-84.1, 33.7}, [2]float64{-84.0, 33.7}),
	}
	var got []string
	for _, w := range windowWays(ways, bbox) {
		got = append(got, w.ID)
	}
	want := []string{"inside", "margin", "spans"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kept %v, want %v", got, want)
		}
	}
}
