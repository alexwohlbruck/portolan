package pipeline

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

func TestNumbersAbbreviateOnlyTheirOwnOrdinal(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"4", "4th", true},
		{"42", "42nd", true},
		{"4", "42nd", false},
		{"8", "81st", false},
		{"81", "8", false},
	}
	for _, c := range cases {
		if got := abbreviates(c.a, c.b); got != c.want {
			t.Errorf("abbreviates(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestContractionsAbbreviateButTyposDoNot(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"blvd", "boulevard", true},
		{"pkwy", "parkway", true},
		{"rd", "road", true},
		{"tpke", "turnpike", true},
		{"rockway", "rockaway", false},   // OSM typo, one letter apart
		{"sedgewick", "sedgwick", false}, // the other way round
		{"blvd", "road", false},
		{"jr", "junior", true},
		{"sq", "square", true},
		{"rer", "routiere", false}, // the Paris RER, not a contraction
	}
	for _, c := range cases {
		if got := abbreviates(c.a, c.b); got != c.want {
			t.Errorf("abbreviates(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func idfFlat(names ...string) map[string]float64 {
	var corp [][]string
	for _, n := range names {
		corp = append(corp, nameTokens(n))
	}
	return idfOf(corp)
}

func TestNameCoverSeesASwappedStreet(t *testing.T) {
	idf := idfFlat("147 AV /GUY R BREWER BLVD", "147th Street & Guy R Brewer Boulevard",
		"Rockaway Tpke", "Rockaway Boulevard", "Astoria Blvd", "Astoria Boulevard South",
		"E 34TH AVE & PARK AVE", "East 34th Street & Park Avenue")
	any := func(string, string) bool { return true }
	if _, _, swapped := nameCover(nameTokens("147 AV /GUY R BREWER BLVD"),
		nameTokens("147th Street & Guy R Brewer Boulevard"), idf, any); !swapped {
		t.Error("147 Av is not 147th Street")
	}
	if _, _, swapped := nameCover(nameTokens("ASTORIA BLVD"),
		nameTokens("Astoria Boulevard South"), idf, any); swapped {
		t.Error("OSM adding South is detail, not a different street")
	}
	if _, _, swapped := nameCover(nameTokens("E 34TH AVE & PARK AVE"),
		nameTokens("East 34th Street & Park Avenue"), idf, any); !swapped {
		t.Error("a repeated word hid the street OSM names differently")
	}
}

// Every corner has several poles metres apart, named for different
// streets or blocks: proximity cannot match a bus stop.
func TestBusStopNeedsItsOwnNameNotTheNearestPole(t *testing.T) {
	sts := []Station{testStation("5 AV/E 81 ST", 40.7779, -73.9637, "bus")}
	stops := []OSMStop{
		osmTestStop("node/1", "5th Avenue & 80th Street", 40.7776, -73.9640, false, "bus"),
		osmTestStop("node/2", "5th Avenue & East 82nd Street", 40.7787, -73.9632, false, "bus"),
	}
	if ms := MatchOSMStops(sts, stops, testFrame()); len(ms) != 0 {
		t.Errorf("matched %s to %q", ms[0].OSM, ms[0].OSMName)
	}
	stops = append(stops, osmTestStop("node/3", "5th Avenue & East 81st Street", 40.7781, -73.9636, false, "bus"))
	if ms := MatchOSMStops(sts, stops, testFrame()); len(ms) != 1 || ms[0].OSM != "node/3" {
		t.Errorf("want the 81st St pole, got %+v", ms)
	}
}

// The feed files both directions of a corner under one name; OSM mapped
// one pole. Both directions take its name, only one its id.
func TestBothDirectionsOfAStopRenameTogether(t *testing.T) {
	sts := []Station{
		testStation("QUEENS BLVD/52 ST", 40.742253, -73.912725, "bus"),
		testStation("QUEENS BLVD/52 ST", 40.742761, -73.912941, "bus"),
	}
	stops := []OSMStop{osmTestStop("node/9", "Queens Boulevard & 52nd Street", 40.742290, -73.912700, false, "bus")}
	ms := withSiblings(sts, MatchOSMStops(sts, stops, testFrame()))
	ApplyOSMStopMatches(sts, ms, true)
	for i, s := range sts {
		if s.Name != "Queens Boulevard & 52nd Street" || !s.Renamed {
			t.Errorf("direction %d kept %q", i, s.Name)
		}
	}
	ids := 0
	for _, s := range sts {
		if s.OSMID != "" {
			ids++
		}
	}
	if ids != 1 {
		t.Errorf("%d directions carry the one pole's id", ids)
	}
}

func TestStopClassesReadBusAndFerryTags(t *testing.T) {
	cases := []struct {
		props map[string]any
		want  string
	}{
		{map[string]any{"highway": "bus_stop"}, "bus"},
		{map[string]any{"public_transport": "platform", "bus": "yes"}, "bus"},
		{map[string]any{"public_transport": "stop_position", "ferry": "yes"}, "ferry"},
		{map[string]any{"public_transport": "station", "ferry": "yes"}, "ferry"},
		{map[string]any{"public_transport": "stop_position", "railway": "stop"}, "regional"},
	}
	for _, c := range cases {
		if got := osmStopClasses(c.props); !got[c.want] {
			t.Errorf("%v: classes %v, want %s", c.props, got, c.want)
		}
	}
	if got := osmStopClasses(map[string]any{"public_transport": "stop_position"}); len(got) != 0 {
		t.Errorf("an untagged stop position guessed %v", got)
	}
}

func TestRenameStopRowsRewritesOnlyMatchedNames(t *testing.T) {
	in := "\xef\xbb\xbfstop_id,stop_name,stop_lat,stop_lon\n1,BROADWAY/W 42 ST,40.75,-73.98\n2,\"Kept, as is\",40.76,-73.97\n"
	var out bytes.Buffer
	n, err := renameStopRows(strings.NewReader(in), &out, func(id string) string {
		if id == "1" {
			return "Broadway & West 42nd Street"
		}
		return ""
	})
	if err != nil || n != 1 {
		t.Fatalf("renamed %d, err %v", n, err)
	}
	want := "stop_id,stop_name,stop_lat,stop_lon\n1,Broadway & West 42nd Street,40.75,-73.98\n2,\"Kept, as is\",40.76,-73.97\n"
	if out.String() != want {
		t.Errorf("got\n%s", out.String())
	}
}

func testFrame() geo.Frame { return geo.NewFrame(geo.LL{Lat: 40.75, Lon: -73.95}) }

func TestRareSpellingIsATypoNotAnExpansion(t *testing.T) {
	var osm [][]string
	for i := 0; i < 40; i++ {
		osm = append(osm, nameTokens("Main Street"))
	}
	osm = append(osm, nameTokens("44th Sreet"))
	e := newExpansions(osm)
	if e.plausible("st", "sreet") {
		t.Error("one 'Sreet' against forty 'Street' is a typo")
	}
	if !e.plausible("st", "street") {
		t.Error("street is what st stands for")
	}
}
