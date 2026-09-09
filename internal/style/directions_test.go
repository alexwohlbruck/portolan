package style

import "testing"

// A rider at Grand Central reads a sign that says UPTOWN, not one that says
// Woodlawn. GTFS core carries direction_id — an opaque 0/1 — and nothing
// that names it, so the name is curation.
func TestDirectionNamesEachDirectionID(t *testing.T) {
	s := New(Doc{Routes: map[string]Entity{
		"4": {Directions: map[string]string{"0": "Uptown", "1": "Downtown"}},
	}}.Config())

	if got, ok := s.Direction("0", []string{"4"}, nil); !ok || got != "Uptown" {
		t.Errorf(`Direction("0") = %q,%v — want "Uptown"`, got, ok)
	}
	if got, ok := s.Direction("1", []string{"4"}, nil); !ok || got != "Downtown" {
		t.Errorf(`Direction("1") = %q,%v — want "Downtown"`, got, ok)
	}
	// A direction nobody named is reported missing rather than guessed: a
	// board showing the wrong compass point is worse than one showing none.
	if got, ok := s.Direction("2", []string{"4"}, nil); ok {
		t.Errorf(`Direction("2") = %q — want no answer`, got)
	}
	if _, ok := s.Direction("0", []string{"L"}, nil); ok {
		t.Error("an unnamed route borrowed another route's directions")
	}
}

// An operator whose whole network runs Inbound/Outbound says it once; a
// route that needs something truer overrides it.
func TestDirectionRouteBeatsAgency(t *testing.T) {
	s := New(Doc{
		Agencies: map[string]Entity{
			"MTA NYCT": {Directions: map[string]string{"0": "Uptown", "1": "Downtown"}},
		},
		Routes: map[string]Entity{
			"L": {Directions: map[string]string{"0": "8 Av", "1": "Canarsie"}},
		},
	}.Config())

	// the L says what the L's signs say
	if got, _ := s.Direction("0", []string{"L"}, []string{"MTA NYCT"}); got != "8 Av" {
		t.Errorf("L direction 0 = %q, want 8 Av", got)
	}
	// every other route falls through to the agency
	if got, _ := s.Direction("0", []string{"4"}, []string{"MTA NYCT"}); got != "Uptown" {
		t.Errorf("4 direction 0 = %q, want Uptown", got)
	}
	// and a route that names only one direction still inherits the other
	if got, _ := s.Direction("1", []string{"L"}, []string{"MTA NYCT"}); got != "Canarsie" {
		t.Errorf("L direction 1 = %q, want Canarsie", got)
	}
}

// Documents layer global-then-city, and a city naming one direction must not
// wipe what the global document said about the other.
func TestDirectionLayersPerDirectionID(t *testing.T) {
	global := Doc{Agencies: map[string]Entity{
		"MTA NYCT": {Directions: map[string]string{"0": "Northbound", "1": "Southbound"}},
	}}.Config()
	city := Doc{Agencies: map[string]Entity{
		"MTA NYCT": {Directions: map[string]string{"0": "Uptown"}},
	}}.Config()

	s := New(global, city)
	if got, _ := s.Direction("0", nil, []string{"MTA NYCT"}); got != "Uptown" {
		t.Errorf("direction 0 = %q, want the city's Uptown", got)
	}
	if got, _ := s.Direction("1", nil, []string{"MTA NYCT"}); got != "Southbound" {
		t.Errorf("direction 1 = %q, want the global Southbound to survive", got)
	}
}

// Flattening must not alias the caller's document — the resolver writes into
// its own merged copy.
func TestDirectionConfigDoesNotAliasTheDocument(t *testing.T) {
	doc := Doc{Routes: map[string]Entity{
		"4": {Directions: map[string]string{"0": "Uptown"}},
	}}
	cfg := doc.Config()
	New(cfg, Doc{Routes: map[string]Entity{
		"4": {Directions: map[string]string{"0": "Something else"}},
	}}.Config())

	if got := doc.Routes["4"].Directions["0"]; got != "Uptown" {
		t.Errorf("the source document was mutated: %q", got)
	}
}
