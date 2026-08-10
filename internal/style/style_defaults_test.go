package style

import "testing"

// The shipped defaults must reproduce the behaviour that was hardcoded
// across mode.go, fair.go and map.html before this package existed —
// a config-driven rewrite that silently restyles every city is a bug.
//
// Changed deliberately 2026-08-10: ferry moved from route to AGENCY
// trunking — a harbour is a few routes over the same water, and parallel
// ribbons there assert a geography that does not exist.
//
// Changed deliberately 2026-08-09: aerial, funicular and cable moved from
// band floor 15 to 0. They are fixed infrastructure, as permanent as
// track, and a floor of 15 hid Mexico City's three Cablebús lines at every
// zoom but the closest while their station dots kept drawing.
func TestDefaultsMatchShippedBehaviour(t *testing.T) {
	s := New()
	for _, c := range []struct {
		name           string
		trunk, color   string
		width, opacity float64
		floor          int
	}{
		{"metro", TrunkColor, "", 1, 1, 0},
		{"tram", TrunkColor, "", 0.75, 1, 0},
		{"regional", TrunkAgency, "", 1, 1, 0},
		{"monorail", TrunkColor, "", 0.85, 1, 0},
		{"ferry", TrunkAgency, "4A9EDB", 0.7, 0.65, 13},
		{"bus", TrunkNone, "888888", 0.5, 0.9, 15},
		{"aerial", TrunkRoute, "", 0.6, 0.75, 0},
		{"funicular", TrunkRoute, "", 0.7, 1, 0},
		{"cable", TrunkRoute, "", 0.75, 1, 0},
	} {
		got := s.Class(c.name)
		if got.Trunk != c.trunk || got.Color != c.color ||
			got.Width != c.width || got.Opacity != c.opacity || got.BandFloor != c.floor {
			t.Errorf("%s = %+v, want trunk=%s color=%s w=%v op=%v floor=%d",
				c.name, got, c.trunk, c.color, c.width, c.opacity, c.floor)
		}
	}
}

// A config naming one field must change that field and nothing else.
func TestLayeringIsPerField(t *testing.T) {
	s := New(Config{Modes: map[string]Class{"ferry": {Color: "#FF0000"}}})
	f := s.Class("ferry")
	if f.Color != "FF0000" {
		t.Errorf("color override not applied: %q (the # must be stripped)", f.Color)
	}
	if f.Width != 0.7 || f.BandFloor != 13 || f.Trunk != TrunkAgency {
		t.Errorf("naming color disturbed other fields: %+v", f)
	}
}

// Later layers win — global block, then the city's own.
func TestCityLayerBeatsGlobal(t *testing.T) {
	s := New(
		Config{Modes: map[string]Class{"bus": {Color: "111111"}}},
		Config{Modes: map[string]Class{"bus": {Color: "222222"}}},
	)
	if got := s.Class("bus").Color; got != "222222" {
		t.Errorf("city layer lost: %q", got)
	}
}

// Overrides resolve by id OR by human-facing name, case-insensitively —
// feed ids like "f2:1" are bookkeeping nobody wants to look up.
func TestColorOverrideLookup(t *testing.T) {
	s := New(Config{Colors: map[string]string{
		"agency:Metro-North Railroad": "#00A1DE",
		"route:f1:7":                  "EE0034",
	}})
	if h, ok := s.AgencyColor("f2:1", "Metro-North Railroad"); !ok || h != "00A1DE" {
		t.Errorf("agency by name = %q,%v", h, ok)
	}
	if h, ok := s.RouteColor("f1:7"); !ok || h != "EE0034" {
		t.Errorf("route by id = %q,%v", h, ok)
	}
	if _, ok := s.RouteColor("nope"); ok {
		t.Error("unknown route matched an override")
	}
	if New().Any() {
		t.Error("empty config reports overrides")
	}
}
