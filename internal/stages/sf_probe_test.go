package stages

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

func TestSouthFerryPathProbe(t *testing.T) {
	rail, tracks, frame := loadNYC(t)
	// crossover set normally comes from the pipeline; mirror it here
	// (loadNYC reads the geojson, so tags are unavailable — mark the known
	// crossover ways by checking SetCrossoverWays wiring instead)
	t.Logf("crossoverWays set: %d entries", len(crossoverWays))
	paths, err := Match(rail, tracks, frame)
	if err != nil {
		t.Fatal(err)
	}
	g := buildTrackGraph(tracks)
	for _, pa := range paths {
		if pa.Pattern.ShapeID != "1..S03R" {
			continue
		}
		for _, st := range pa.Steps {
			if st.Piece < 0 {
				continue
			}
			l := g.pieces[st.Piece]
			mid := l.AtArc(l.Len() / 2)
			ll := frame.ToLL(mid)
			if 40.7000 < ll.Lat && ll.Lat < 40.7090 && -74.0180 < ll.Lon && ll.Lon < -74.0100 {
				t.Logf("  piece %d way %s len %.0f mid %.5f,%.5f rev=%v",
					st.Piece, g.edges[2*st.Piece].Way, l.Len(), ll.Lat, ll.Lon, st.Rev)
			}
		}
		break
	}
	_ = geo.Pt{}
}
