package stages

import (
	"fmt"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

func TestMergeDiag(t *testing.T) {
	rail, tracks, frame := loadNYC(t)
	paths, err := Match(rail, tracks, frame)
	if err != nil {
		t.Fatal(err)
	}
	mergeLog = func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	}
	defer func() { mergeLog = nil }()
	// wrap positions into lat/lon for readability
	_ = frame
	net, err := Split(paths, tracks)
	if err != nil {
		t.Fatal(err)
	}
	// report edges near Bowling Green loop
	spot := frame.ToXY(geo.LL{Lon: -74.0140, Lat: 40.7025})
	for ei, e := range net.Edges {
		l := geo.NewLine(e.Pts)
		if l.DistTo(spot) < 300 {
			fmt.Printf("post: edge %d routes=%v len=%.0f gap=%v\n", ei, e.Routes, l.Len(), e.Gap)
		}
	}
}
