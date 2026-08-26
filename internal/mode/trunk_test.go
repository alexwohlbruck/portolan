package mode

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/style"
)

// Colour trunking is law 5 and stays the default. But it assumes colour
// carries meaning, and an authored network may assign colours
// arbitrarily — two unrelated routes that happen to share a hex would
// merge into one ribbon. TrunkRoute per route is the way out.
func TestSharedColourMergesUntilARouteOptsOut(t *testing.T) {
	defer style.Set_(nil) // the active set is process-wide

	metro := func(id, hex string) gtfs.Route {
		return gtfs.Route{ID: id, ShortName: id, Color: hex, Type: 1, Agency: "AUTH"}
	}
	a, b := metro("A", "FF0000"), metro("B", "FF0000")

	style.Set_(style.New())
	if TrunkKey(a) != TrunkKey(b) {
		t.Fatalf("law 5: same colour must be one ribbon, got %q vs %q", TrunkKey(a), TrunkKey(b))
	}

	style.Set_(style.New(style.Doc{
		Routes: map[string]style.Entity{"B": {Trunk: style.TrunkRoute}},
	}.Config()))
	if TrunkKey(a) == TrunkKey(b) {
		t.Errorf("trunk=route did not separate B from the colour trunk (both %q)", TrunkKey(a))
	}
	if got := TrunkKey(a); got != "FF0000" {
		t.Errorf("the route that did NOT opt out must keep its colour key, got %q", got)
	}
}

// A trunk group SCOPES law 5 rather than replacing it. The case it exists
// for: a caller whose classes GTFS cannot express. A game files both its
// heavy and light metro as route_type 1, so the engine sees one class and
// merges two unrelated railways that happen to share a hex.
func TestTrunkGroupScopesColourWithoutReplacingIt(t *testing.T) {
	defer style.Set_(nil) // the active set is process-wide

	metro := func(id, hex string) gtfs.Route {
		return gtfs.Route{ID: id, ShortName: id, Color: hex, Type: 1, Agency: "AUTH"}
	}
	heavyA, heavyB := metro("HA", "FF0000"), metro("HB", "FF0000")
	light := metro("L", "FF0000")

	// Absent, nothing moves: this is every real-world feed, and law 5's
	// key stays the bare colour byte for byte.
	style.Set_(style.New())
	for _, r := range []gtfs.Route{heavyA, heavyB, light} {
		if got := TrunkKey(r); got != "FF0000" {
			t.Fatalf("unscoped key must be the bare colour, got %q for %s", got, r.ID)
		}
	}

	style.Set_(style.New(style.Doc{
		Routes: map[string]style.Entity{
			"HA": {TrunkGroup: "heavy-metro"},
			"HB": {TrunkGroup: "heavy-metro"},
			"L":  {TrunkGroup: "light-metro"},
		},
	}.Config()))

	// Same group and same colour still merge — the whole point is that
	// scoping narrows law 5, it does not switch it off.
	if TrunkKey(heavyA) != TrunkKey(heavyB) {
		t.Errorf("same group + same colour must stay one ribbon, got %q vs %q",
			TrunkKey(heavyA), TrunkKey(heavyB))
	}
	// A different group with the SAME colour must not.
	if TrunkKey(heavyA) == TrunkKey(light) {
		t.Errorf("different groups sharing a colour merged anyway (both %q)", TrunkKey(heavyA))
	}
}

// The scope must reach a route through its agency too, so a caller can
// group a whole railway without naming every route on it.
func TestTrunkGroupAppliesByAgency(t *testing.T) {
	defer style.Set_(nil)

	rapid := gtfs.Route{ID: "P", ShortName: "P", Color: "D43E2D", Type: 1, Agency: "rapid"}
	commuter := gtfs.Route{ID: "N", ShortName: "N", Color: "D43E2D", Type: 1, Agency: "commuter"}

	style.Set_(style.New())
	if TrunkKey(rapid) != TrunkKey(commuter) {
		t.Fatalf("precondition: equal colours merge by default")
	}

	style.Set_(style.New(style.Doc{
		Agencies: map[string]style.Entity{
			"rapid":    {TrunkGroup: "heavy-metro"},
			"commuter": {TrunkGroup: "commuter-rail"},
		},
	}.Config()))
	if TrunkKey(rapid) == TrunkKey(commuter) {
		t.Errorf("agency-level groups did not separate equal colours (both %q)", TrunkKey(rapid))
	}
}

// A per-ROUTE group beats its agency's: the narrower statement wins, the
// same way a per-route colour beats an agency colour.
func TestRouteTrunkGroupBeatsAgency(t *testing.T) {
	defer style.Set_(nil)

	onAgency := gtfs.Route{ID: "A", ShortName: "A", Color: "00FF00", Type: 1, Agency: "op"}
	overridden := gtfs.Route{ID: "B", ShortName: "B", Color: "00FF00", Type: 1, Agency: "op"}

	style.Set_(style.New(style.Doc{
		Agencies: map[string]style.Entity{"op": {TrunkGroup: "heavy-metro"}},
		Routes:   map[string]style.Entity{"B": {TrunkGroup: "light-metro"}},
	}.Config()))
	if TrunkKey(onAgency) == TrunkKey(overridden) {
		t.Errorf("the per-route group did not beat the agency's (both %q)", TrunkKey(onAgency))
	}
}
