package pipeline

import "testing"

// Both kinds of OSM object carry the station's name and sit metres apart:
// public_transport=station is the station as a place, stop_position is a
// point on the track where a train halts. Distance alone cannot tell them
// apart, and picking the wrong one sends a rider to a spot on the rails —
// Jay St–MetroTech has six stop_positions and two station nodes, and matched
// a stop_position while Clark St matched its station.
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

// The bonus has to beat a stop_position that is nearer, without reaching
// past a station that is genuinely closer.
func TestStationBonusOutranksACloserStopPosition(t *testing.T) {
	// A stop_position 10 m away vs a station node 25 m away.
	near := 0.65*(1-10.0/osmMatchRadiusM) + 0.35*1.0
	far := 0.65*(1-25.0/osmMatchRadiusM) + 0.35*1.0 + osmStationBonus
	if far <= near {
		t.Errorf("station at 25m (%.4f) should outrank stop_position at 10m (%.4f)", far, near)
	}

	// But a station 10 m away still beats one 200 m away.
	close := 0.65*(1-10.0/osmMatchRadiusM) + 0.35*1.0 + osmStationBonus
	distant := 0.65*(1-200.0/osmMatchRadiusM) + 0.35*1.0 + osmStationBonus
	if distant >= close {
		t.Errorf("a distant station must not outrank a near one")
	}
}
