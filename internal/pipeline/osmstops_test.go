package pipeline

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

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

// ── abbreviation ──────────────────────────────────────────────────────

func osmTestStop(id, name string, lat, lon float64, station bool, classes ...string) OSMStop {
	cl := map[string]bool{}
	for _, c := range classes {
		cl[c] = true
	}
	return OSMStop{
		ID: id, Name: name, LL: geo.LL{Lat: lat, Lon: lon},
		Classes: cl, Station: station, toks: nameTokens(name),
	}
}

func testStation(name string, lat, lon float64, modes ...string) Station {
	return Station{Name: name, LL: geo.LL{Lat: lat, Lon: lon}, Modes: modes}
}

// Enough of the MTA's and OSM's vocabulary that IDF means something:
// "st"/"street" are everywhere and must weigh nearly nothing, while
// "wash"/"washington" are rare and must carry the comparison.
func nycCorpus() ([]Station, []OSMStop) {
	sts := []Station{
		testStation("W 4 St-Wash Sq", 40.732338, -74.000495, "metro"),
		testStation("Christopher St-Stonewall", 40.733422, -74.002903, "metro"),
		testStation("14 St", 40.737826, -73.996362, "metro"),
		testStation("Annadale", 40.540460, -74.178217, "regional"),
		testStation("Canal St", 40.718803, -74.000193, "metro"),
		testStation("Houston St", 40.728251, -74.005367, "metro"),
	}
	stops := []OSMStop{
		osmTestStop("node/597928309", "West 4th Street–Washington Square", 40.731643, -74.000994, true, "metro"),
		osmTestStop("node/5106257816", "Christopher Street–Stonewall Station", 40.734216, -74.002390, true, "metro"),
		osmTestStop("node/1", "14th Street", 40.737700, -73.996500, true, "metro"),
		osmTestStop("node/42969951", "Annadale", 40.540534, -74.177904, true, "metro"),
		osmTestStop("node/2", "Canal Street", 40.718900, -74.000300, true, "metro"),
		osmTestStop("node/3", "Houston Street", 40.728300, -74.005400, true, "metro"),
	}
	return sts, stops
}

func matchedID(ms []StopMatch, station int) string {
	for _, m := range ms {
		if m.Station == station {
			return m.OSM
		}
	}
	return ""
}

// The MTA abbreviates every word of a name and OSM spells every one out,
// so "W 4 St-Wash Sq" and "West 4th Street–Washington Square" share not a
// single token. Exact-token similarity scored that 0.000 against a
// needSim of 0.126 at the 88 m between them, and the busiest interchange
// in the system — eight lines — went unmatched, along with 14 St, 3 Av,
// 46 St, 65 St, 67 Av and both 5 Av complexes.
func TestAbbreviatedNameMatchesTheSpelledOutOSMName(t *testing.T) {
	sts, stops := nycCorpus()
	ms := MatchOSMStops(sts, stops, geo.NewFrame(geo.LL{Lat: 40.73, Lon: -74.0}))

	for i, want := range map[int]string{
		0: "node/597928309", // W 4 St-Wash Sq, 88 m
		2: "node/1",         // 14 St, spelled out as 14th Street
	} {
		if got := matchedID(ms, i); got != want {
			t.Errorf("%q: got %q, want %q", sts[i].Name, got, want)
		}
	}
}

// The stations that already matched must keep matching. Christopher St
// reaches its node at 99 m — further than W 4 St ever was — on the
// strength of two proper nouns, and nothing here may cost it that.
func TestProperNounMatchesAreUnchanged(t *testing.T) {
	sts, stops := nycCorpus()
	ms := MatchOSMStops(sts, stops, geo.NewFrame(geo.LL{Lat: 40.73, Lon: -74.0}))
	if got := matchedID(ms, 1); got != "node/5106257816" {
		t.Errorf("Christopher St-Stonewall: got %q, want node/5106257816", got)
	}
}

// A stub must not consume the token its own full form is waiting for.
// Against "Christopher Street–Stonewall", the feed's "st" prefixes
// "stonewall" as readily as "street"; if it takes "stonewall" then
// "stonewall" pairs with nothing and the name half disagrees with itself.
func TestAbbreviationDoesNotStealAnExactMatch(t *testing.T) {
	feed := nameTokens("Christopher St-Stonewall")
	osm := nameTokens("Christopher Street–Stonewall Station")
	idf := idfOf([][]string{feed}, [][]string{osm})

	withStub := nameSim(feed, osm, idf)
	// the same name with the stub already spelled out: pairing is then
	// entirely exact, and the stub version must not beat it
	spelled := nameSim(nameTokens("Christopher Street-Stonewall"), osm, idf)
	if withStub > spelled {
		t.Errorf("abbreviated form scored %.3f, above the spelled-out %.3f", withStub, spelled)
	}
	if withStub < 0.5 {
		t.Errorf("two proper nouns in common should still read as agreement, got %.3f", withStub)
	}
}

// An abbreviation is worth the lesser of the two words' weights, so a
// promiscuous one-letter stub stays cheap without a rule about length:
// "w" is on a tenth of the MTA's names and must not inherit the weight
// of the rare "washington" it reaches for.
func TestAbbreviationCreditIsCappedByTheStubsWeight(t *testing.T) {
	if !abbreviates("w", "west") || !abbreviates("wash", "washington") ||
		!abbreviates("4", "4th") || !abbreviates("sq", "square") {
		t.Error("prefix stubs should read as abbreviations")
	}
	if abbreviates("street", "street") {
		t.Error("a word is not an abbreviation of itself")
	}
	if abbreviates("west", "east") {
		t.Error("unrelated words are not abbreviations")
	}
}

// ── class disagreement ────────────────────────────────────────────────

// The MTA files the Staten Island Railway as route_type 2 (regional) and
// OSM maps it station=subway (metro) — legally a railway, operationally a
// subway, and neither source is wrong. Requiring exact agreement dropped
// all 21 of its stations while their nodes sat 19-28 m away under
// identical names.
func TestRailClassesMayDisagree(t *testing.T) {
	sts, stops := nycCorpus()
	ms := MatchOSMStops(sts, stops, geo.NewFrame(geo.LL{Lat: 40.73, Lon: -74.0}))
	if got := matchedID(ms, 3); got != "node/42969951" {
		t.Errorf("Annadale (regional vs metro, 28 m, identical names): got %q", got)
	}
}

// ...but only rail forgives rail. A tram stop still may not claim the bus
// pole beside it, however close, and a ferry terminal is not a station.
func TestNonRailClassesStayGated(t *testing.T) {
	cases := []struct {
		name           string
		station, stop  []string
		wantOK, wantEx bool
	}{
		{"same class", []string{"metro"}, []string{"metro"}, true, true},
		{"regional vs metro", []string{"regional"}, []string{"metro"}, true, false},
		{"tram vs metro", []string{"tram"}, []string{"metro"}, true, false},
		{"metro vs bus", []string{"metro"}, []string{"bus"}, false, false},
		{"tram vs bus", []string{"tram"}, []string{"bus"}, false, false},
		{"regional vs ferry", []string{"regional"}, []string{"ferry"}, false, false},
		{"bus vs bus", []string{"bus"}, []string{"bus"}, true, true},
		{"unknown class station", nil, []string{"metro"}, false, false},
		{"multi-mode picks exact", []string{"bus", "metro"}, []string{"metro"}, true, true},
	}
	for _, c := range cases {
		sc, oc := map[string]bool{}, map[string]bool{}
		for _, x := range c.station {
			sc[x] = true
		}
		for _, x := range c.stop {
			oc[x] = true
		}
		ok, exact := classAffinity(sc, oc)
		if ok != c.wantOK || exact != c.wantEx {
			t.Errorf("%s: got (ok=%v exact=%v), want (ok=%v exact=%v)",
				c.name, ok, exact, c.wantOK, c.wantEx)
		}
	}
}

// A stop of the station's own class wins whenever both are in reach, so
// the forgiveness never costs a match that exact agreement would have
// made. The penalty must not outweigh real distance, though.
func TestExactClassOutranksRailFamilyAtTheSameDistance(t *testing.T) {
	same := 0.65*(1-50.0/osmMatchRadiusM) + 0.35*1.0
	cross := 0.65*(1-50.0/osmMatchRadiusM) + 0.35*1.0 - crossClassPenalty
	if cross >= same {
		t.Error("a cross-class stop must not outrank an exact-class one at equal distance")
	}
	nearCross := 0.65*(1-10.0/osmMatchRadiusM) + 0.35*1.0 - crossClassPenalty
	farSame := 0.65*(1-240.0/osmMatchRadiusM) + 0.35*1.0
	if nearCross <= farSame {
		t.Error("the penalty should not cost a stop that is 230 m nearer")
	}
}
