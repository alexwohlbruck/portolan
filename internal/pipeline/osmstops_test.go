package pipeline

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Both kinds of OSM object carry the station's name and sit metres apart:
// public_transport=station is the station as a place, stop_position is a
// point on the track where a train halts. Distance alone cannot tell them
// apart, and picking the wrong one sends a rider to a spot on the rails.
func TestOSMStopIsStation(t *testing.T) {
	cases := []struct {
		name  string
		props map[string]any
		want  bool
	}{
		{"public_transport=station", map[string]any{"public_transport": "station", "railway": "station"}, true},
		{"stop_position", map[string]any{"public_transport": "stop_position", "railway": "stop"}, false},
		{"platform", map[string]any{"public_transport": "platform"}, false},
		{"bare railway=station", map[string]any{"railway": "station"}, true},
		{"ferry terminal", map[string]any{"amenity": "ferry_terminal"}, true},
		{"aerialway station", map[string]any{"aerialway": "station"}, true},
		{"nothing useful", map[string]any{"highway": "bus_stop"}, false},
	}
	for _, c := range cases {
		if got := osmStopIsStation(c.props); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

var stopFrame = geo.NewFrame(geo.LL{Lat: 40.69, Lon: -73.987})

// offset returns a point n metres north of the frame's centre.
func metresNorth(n float64) geo.LL {
	c := stopFrame.ToXY(geo.LL{Lat: 40.69, Lon: -73.987})
	return stopFrame.ToLL(geo.Pt{X: c.X, Y: c.Y + n})
}

func subwayStop(id, name string, north float64, station bool) OSMStop {
	return OSMStop{
		ID: id, Name: name, LL: metresNorth(north),
		Classes: map[string]bool{"subway": true},
		Station: station,
		toks:    nameTokens(name),
	}
}

// Jay St–MetroTech has six stop_positions and two station nodes. On score
// alone a stop_position wins whenever the feed's coordinate lands nearer to
// one, which is why some stations came out labelled "Subway Station" and
// others "Subway Stopping Location". The station must win even from further
// away.
func TestStationBeatsANearerStopPosition(t *testing.T) {
	sts := []Station{{
		Name: "Jay St–MetroTech", LL: metresNorth(0), Modes: []string{"subway"},
	}}
	stops := []OSMStop{
		subwayStop("node/1", "Jay St–MetroTech", 10, false), // stop_position, nearer
		subwayStop("node/2", "Jay St–MetroTech", 40, true),  // station, further
	}
	ms := MatchOSMStops(sts, stops, stopFrame)
	if len(ms) != 1 {
		t.Fatalf("expected 1 match, got %d", len(ms))
	}
	if ms[0].OSM != "node/2" {
		t.Errorf("matched %s, want the station node/2", ms[0].OSM)
	}
}

// A fallback, not a filter: most of the world's stations have no station node
// mapped, and a stop_position is a better answer there than none at all.
func TestStopPositionUsedWhenNoStationMapped(t *testing.T) {
	sts := []Station{{
		Name: "Roosevelt Island", LL: metresNorth(0), Modes: []string{"subway"},
	}}
	stops := []OSMStop{subwayStop("node/1", "Roosevelt Island", 12, false)}
	ms := MatchOSMStops(sts, stops, stopFrame)
	if len(ms) != 1 || ms[0].OSM != "node/1" {
		t.Fatalf("expected the stop_position to be matched, got %+v", ms)
	}
}

// The station pass must not strand a feed station on a stop_position that a
// neighbour could have taken: each pass assigns greedily by score, so the
// closer station takes the closer station node.
func TestTwoStationsEachTakeTheirOwnStationNode(t *testing.T) {
	sts := []Station{
		{Name: "Clark St", LL: metresNorth(0), Modes: []string{"subway"}},
		{Name: "Borough Hall", LL: metresNorth(160), Modes: []string{"subway"}},
	}
	stops := []OSMStop{
		subwayStop("node/clark", "Clark St", 8, true),
		subwayStop("node/clark-sp", "Clark St", 2, false),
		subwayStop("node/bh", "Borough Hall", 168, true),
	}
	ms := MatchOSMStops(sts, stops, stopFrame)
	got := map[string]string{}
	for _, m := range ms {
		got[m.FeedName] = m.OSM
	}
	if got["Clark St"] != "node/clark" {
		t.Errorf("Clark St matched %q, want node/clark", got["Clark St"])
	}
	if got["Borough Hall"] != "node/bh" {
		t.Errorf("Borough Hall matched %q, want node/bh", got["Borough Hall"])
	}
}
