package pipeline

import (
	"reflect"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

// A synthetic two-feed city: the subway's two platforms share a parent,
// the commuter feed's stop shares the station NAME across the street,
// and an unrelated stop sits one block away under a different name.
func testFeed() (*gtfs.Feed, []gtfs.Pattern) {
	feed := &gtfs.Feed{
		Routes:   map[string]gtfs.Route{},
		Agencies: map[string]string{},
		Stops: map[string]gtfs.Stop{
			// parent station record (location_type=1: no parent of its own)
			"GC":  {Name: "Grand Central", LL: geo.LL{Lon: -73.9772, Lat: 40.7527}},
			"GCn": {Name: "Grand Central (north)", LL: geo.LL{Lon: -73.9771, Lat: 40.7529}, Parent: "GC"},
			"GCs": {Name: "Grand Central (south)", LL: geo.LL{Lon: -73.9773, Lat: 40.7525}, Parent: "GC"},
			// overlay feed's stop, same name, ~100 m away, no parent
			"f1:GCT": {Name: "Grand Central", LL: geo.LL{Lon: -73.9762, Lat: 40.7530}},
			// a block away, different name: must stay its own station
			"5AV": {Name: "5 Av", LL: geo.LL{Lon: -73.9814, Lat: 40.7538}},
			// SAME name, same feed, ~260 m apart — NYC's twin "23 St"
			// problem: these are two stations and must not merge
			"23a": {Name: "23 St", LL: geo.LL{Lon: -73.9909, Lat: 40.7429}},
			"23b": {Name: "23 St", LL: geo.LL{Lon: -73.9938, Lat: 40.7441}},
		},
	}
	r4 := gtfs.Route{ID: "4", ShortName: "4", Color: "00933C", Type: 1}
	r10 := gtfs.Route{ID: "10", ShortName: "10", Color: "00933C", Type: 1}
	rB := gtfs.Route{ID: "B", ShortName: "B", Color: "FF6319", Type: 1}
	mnr := gtfs.Route{ID: "f1:hudson", ShortName: "Hudson", Color: "009B3A", Type: 2, Agency: "f1:MNR"}
	bus := gtfs.Route{ID: "M101", ShortName: "M101", Color: "4477AA", Type: 3}
	pats := []gtfs.Pattern{
		{Route: r4, StopIDs: []string{"GCn", "GCs"}},
		{Route: r10, StopIDs: []string{"GCn"}},
		{Route: rB, StopIDs: []string{"5AV", "23a"}},
		{Route: r4, StopIDs: []string{"23b"}},
		{Route: mnr, StopIDs: []string{"f1:GCT"}},
		{Route: bus, StopIDs: []string{"GCn", "5AV"}}, // bus never makes stations
	}
	return feed, pats
}

func TestBuildStationsGroupsAndRanks(t *testing.T) {
	feed, pats := testFeed()
	sts := BuildStations(feed, pats, nil)
	// Grand Central merged (parent + cross-feed name match), 5 Av alone,
	// and the two same-feed "23 St" twins stay two stations
	if len(sts) != 4 {
		t.Fatalf("want 4 stations, got %d: %+v", len(sts), sts)
	}
	n23 := 0
	for i := range sts {
		if sts[i].Name == "23 St" {
			n23++
		}
	}
	if n23 != 2 {
		t.Fatalf("same-feed same-name 260 m apart must NOT merge: %d '23 St' stations", n23)
	}
	var gc, av *Station
	for i := range sts {
		switch sts[i].Name {
		case "Grand Central":
			gc = &sts[i]
		case "5 Av":
			av = &sts[i]
		}
	}
	if gc == nil || av == nil {
		t.Fatalf("missing station: %+v", sts)
	}
	// parent grouping + cross-feed name merge: 4, 10, Hudson all at GC,
	// natural label order (2-digit 10 after 4? no: numeric 4 < 10),
	// letters after numbers
	if !reflect.DeepEqual(gc.Routes, []string{"4", "10", "f1:hudson"}) {
		t.Fatalf("GC routes = %v", gc.Routes)
	}
	// 4 and 10 share a color → one line; MNR agency trunk → second line
	if gc.Lines != 2 {
		t.Fatalf("GC lines = %d, want 2", gc.Lines)
	}
	if len(gc.Routes) != 3 {
		t.Fatalf("GC rank should count routes: %v", gc.Routes)
	}
	if !reflect.DeepEqual(gc.Modes, []string{"metro", "metro", "regional"}) {
		t.Fatalf("GC modes = %v", gc.Modes)
	}
	// the bus never contributes: 5 Av carries only the B
	if !reflect.DeepEqual(av.Routes, []string{"B"}) || av.Lines != 1 {
		t.Fatalf("5 Av = %+v", av)
	}
	if av.LineHex[0] != "FF6319" {
		t.Fatalf("5 Av line color = %v", av.LineHex)
	}
}

func TestBuildStationsDeterministic(t *testing.T) {
	feed, pats := testFeed()
	a := BuildStations(feed, pats, nil)
	for i := 0; i < 5; i++ {
		b := BuildStations(feed, pats, nil)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, a, b)
		}
	}
}

func TestNaturalCmp(t *testing.T) {
	if naturalCmp("2", "10") >= 0 {
		t.Fatal("2 must sort before 10")
	}
	if naturalCmp("10", "B") >= 0 {
		t.Fatal("numbers before letters")
	}
	if naturalCmp("A", "B") >= 0 {
		t.Fatal("A before B")
	}
}

func TestNormName(t *testing.T) {
	if normName("Hoyt–Schermerhorn  Sts") != normName("hoyt schermerhorn sts") {
		t.Fatal("dash and case must fold")
	}
	if normName("Court St") == normName("Court Street") {
		t.Fatal("abbreviations must NOT expand")
	}
}
