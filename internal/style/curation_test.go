package style

import "testing"

// The curation document addresses a subject once and every knob follows
// the same key, so a curator learns one scheme. These check that fonts,
// borders and per-route trunk policy split like colours and shapes do,
// including the agency-then-route precedence.

func docSet(t *testing.T) *Set {
	t.Helper()
	yes, no := true, false
	return New(Doc{
		Agencies: map[string]Entity{
			"MTA": {Font: "mono", Bordered: &yes},
		},
		Routes: map[string]Entity{
			"L":         {Font: "italic"},
			"Shuttle":   {Bordered: &no},
			"Heritage":  {Trunk: TrunkRoute},
			"Cablecar":  {Shape: "diamond", Font: "bolder", Bordered: &yes},
			"Riverline": {Trunk: TrunkAgency},
		},
	}.Config())
}

func TestRouteFontFallsBackToAgency(t *testing.T) {
	s := docSet(t)
	if got, ok := s.RouteFont([]string{"L"}, []string{"MTA"}); !ok || got != "italic" {
		t.Errorf("route font: got %q/%v, want italic — the route beats its agency", got, ok)
	}
	if got, ok := s.RouteFont([]string{"7"}, []string{"MTA"}); !ok || got != "mono" {
		t.Errorf("agency font: got %q/%v, want mono", got, ok)
	}
	if _, ok := s.RouteFont([]string{"7"}, []string{"PATH"}); ok {
		t.Error("an unaddressed route must report no override, not a default")
	}
}

func TestBorderedDistinguishesFalseFromUnset(t *testing.T) {
	s := docSet(t)
	// an explicit false has to beat the agency's true, which means the
	// second return value is load-bearing — a plain bool could not say
	// "the curator turned this off here"
	if v, ok := s.RouteBordered([]string{"Shuttle"}, []string{"MTA"}); !ok || v {
		t.Errorf("explicit false: got %v/%v, want false/true", v, ok)
	}
	if v, ok := s.RouteBordered([]string{"7"}, []string{"MTA"}); !ok || !v {
		t.Errorf("inherited true: got %v/%v, want true/true", v, ok)
	}
	if v, ok := s.RouteBordered([]string{"7"}, []string{"PATH"}); ok || v {
		t.Errorf("unset: got %v/%v, want false/false", v, ok)
	}
}

func TestPerRouteTrunkOverride(t *testing.T) {
	s := docSet(t)
	if !s.AnyTrunk() {
		t.Fatal("AnyTrunk must see the overrides, or the lookup is skipped entirely")
	}
	if got, ok := s.RouteTrunk([]string{"Heritage"}, nil); !ok || got != TrunkRoute {
		t.Errorf("got %q/%v, want %q", got, ok, TrunkRoute)
	}
	if got, ok := s.RouteTrunk([]string{"Riverline"}, nil); !ok || got != TrunkAgency {
		t.Errorf("got %q/%v, want %q", got, ok, TrunkAgency)
	}
	if _, ok := s.RouteTrunk([]string{"L"}, []string{"MTA"}); ok {
		t.Error("a route with no trunk override must not inherit one from nowhere")
	}
}

func TestEmptySetHasNoOverrides(t *testing.T) {
	s := New()
	if s.AnyTrunk() {
		t.Error("a bare set claims trunk overrides it does not have")
	}
	if _, ok := s.RouteFont([]string{"L"}, []string{"MTA"}); ok {
		t.Error("a bare set claims a font override")
	}
	if _, ok := s.RouteBordered([]string{"L"}, []string{"MTA"}); ok {
		t.Error("a bare set claims a border override")
	}
}
