package pipeline

import (
	"math"
	"reflect"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/stages"
	"github.com/alexwohlbruck/portolan/internal/style"
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
	for _, r := range []gtfs.Route{r4, r10, rB, mnr, bus} {
		feed.Routes[r.ID] = r
	}
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
	// parent grouping + cross-feed name merge: 4, 10, Hudson all at GC.
	// Default color policy: the Hudson's letter group sorts before the
	// numeric 4/10 group, numeric order within a group (4 before 10)
	if !reflect.DeepEqual(gc.Routes, []string{"f1:hudson", "4", "10"}) {
		t.Fatalf("GC routes = %v", gc.Routes)
	}
	// 4 and 10 share a color → one line; MNR agency trunk → second line
	if gc.Lines != 2 {
		t.Fatalf("GC lines = %d, want 2", gc.Lines)
	}
	if len(gc.Routes) != 3 {
		t.Fatalf("GC rank should count routes: %v", gc.Routes)
	}
	if !reflect.DeepEqual(gc.Modes, []string{"regional", "metro", "metro"}) {
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

func TestSnapStations(t *testing.T) {
	feed, pats := testFeed()
	sts := BuildStations(feed, pats, nil)
	var gc *Station
	for i := range sts {
		if sts[i].Name == "Grand Central" {
			gc = &sts[i]
		}
	}
	if gc == nil {
		t.Fatal("no Grand Central")
	}
	frame := geo.NewFrame(geo.LL{Lon: -73.977, Lat: 40.753})
	// a north-south ribbon 40 m east of the station centroid, carrying
	// the 4 and the 10 in a 2-slot bundle (the 4's ribbon at +3 px)
	mk := func(lls ...geo.LL) *geo.Line {
		pts := make([]geo.Pt, len(lls))
		for i, ll := range lls {
			pts[i] = frame.ToXY(ll)
		}
		return geo.NewLine(pts)
	}
	line := mk(geo.LL{Lon: -73.9767, Lat: 40.7500}, geo.LL{Lon: -73.9767, Lat: 40.7560})
	segs := []stages.Segment{
		{Kind: "steady", Routes: []string{"4", "10"}, NSlots: 2, OffsetPx: 3,
			Color: "00933C", BandMin: 15, Line: line},
		// the commuter ribbon, further east, carries the Hudson line —
		// DRAWN in the agency trunk's purple, not the branch's green
		{Kind: "steady", Routes: []string{"f1:hudson"}, NSlots: 1, OffsetPx: 0,
			Color: "5D2B90", BandMin: 15, Line: mk(geo.LL{Lon: -73.9760, Lat: 40.7500}, geo.LL{Lon: -73.9760, Lat: 40.7560})},
	}
	SnapStations(sts, segs, frame, 6, feed.Routes)

	if len(gc.Markers) != 2 {
		t.Fatalf("GC should have 2 markers (subway bundle + commuter), got %+v", gc.Markers)
	}
	// marker order follows route order: the Hudson's group first
	sub := gc.Markers[1]
	if !reflect.DeepEqual(sub.Routes, []string{"4", "10"}) {
		t.Fatalf("subway marker routes = %v", sub.Routes)
	}
	// snapped ONTO the line: longitude moved from the centroid to the ribbon
	if math.Abs(sub.LL.Lon - -73.9767) > 1e-4 {
		t.Fatalf("marker not snapped: lon %v", sub.LL.Lon)
	}
	// north-south line → bearing ~0 (mod 180)
	b := math.Mod(math.Abs(sub.Bearing), 180)
	if b > 5 && b < 175 {
		t.Fatalf("bearing = %v, want ~north-south", sub.Bearing)
	}
	// the 4/10 occupy ONE ribbon of a 2-slot bundle: partial coverage
	// draws a dot on THEIR ribbon (at its slot offset, in the segment's
	// drawn color), never a pill over the line that passes them by
	if sub.Pill || len(sub.Dots) != 1 || sub.Dots[0].Off != 3 || sub.Dots[0].Hex != "00933C" {
		t.Fatalf("subway marker = %+v", sub)
	}
	// the commuter dot takes the DRAWN trunk color, not the branch's own
	com := gc.Markers[0]
	if com.Pill || len(com.Dots) != 1 || com.Dots[0].Hex != "5D2B90" {
		t.Fatalf("commuter marker should wear the ribbon's color: %+v", com)
	}
	// label anchors at the busiest marker (the 2-route subway bundle)
	if gc.LabelLL != sub.LL {
		t.Fatalf("label at %v, want subway marker %v", gc.LabelLL, sub.LL)
	}
	// a station with no ribbon in reach keeps an unsnapped marker
	var av *Station
	for i := range sts {
		if sts[i].Name == "5 Av" {
			av = &sts[i]
		}
	}
	if len(av.Markers) != 1 || av.Markers[0].SpanPx != 0 || av.Markers[0].LL != av.LL {
		t.Fatalf("unsnapped fallback = %+v", av.Markers)
	}
}

func countName(sts []Station, name string) int {
	n := 0
	for i := range sts {
		if sts[i].Name == name {
			n++
		}
	}
	return n
}

// Same name a block apart is NOT one station unless a rider can walk
// between them without paying again — and transfers.txt is the ground
// truth for that whenever the feed ships one (NYC's Rector St pair).
func TestTransfersControlMerging(t *testing.T) {
	feed, pats := testFeed()
	feed.Stops["RA"] = gtfs.Stop{Name: "Rector St", LL: geo.LL{Lon: -74.0135, Lat: 40.7075}}
	feed.Stops["RB"] = gtfs.Stop{Name: "Rector St", LL: geo.LL{Lon: -74.0122, Lat: 40.7073}}
	pats = append(pats,
		gtfs.Pattern{Route: feed.Routes["4"], StopIDs: []string{"RA"}},
		gtfs.Pattern{Route: feed.Routes["B"], StopIDs: []string{"RB"}})

	// no transfers.txt → the 150 m name fallback merges them
	if n := countName(BuildStations(feed, pats, nil), "Rector St"); n != 1 {
		t.Fatalf("no transfers.txt: proximity should merge, got %d stations", n)
	}
	// the feed ships transfers and does NOT link them → two stations
	feed.Transfers = [][2]string{{"GCn", "GCs"}}
	if n := countName(BuildStations(feed, pats, nil), "Rector St"); n != 2 {
		t.Fatalf("transfers authoritative: want 2 Rector St, got %d", n)
	}
	// a link folds them into one complex again
	feed.Transfers = append(feed.Transfers, [2]string{"RA", "RB"})
	if n := countName(BuildStations(feed, pats, nil), "Rector St"); n != 1 {
		t.Fatalf("linked complex: want 1 Rector St, got %d", n)
	}
	// cross-feed name merging is untouched by same-feed transfers
	if n := countName(BuildStations(feed, pats, nil), "Grand Central"); n != 1 {
		t.Fatalf("cross-feed Grand Central merge broke: %d", n)
	}
}

// The NYC ordering the user pointed at Apple for: color groups over
// alphabetical order — W 4 St reads A·C·E then B·D·F·M, and Columbus
// Circle's letter groups come before the 1 (docs/STOP-LABELS.md).
func TestBulletOrdering(t *testing.T) {
	mk := func(id, color string, ty int) gtfs.Route {
		return gtfs.Route{ID: id, ShortName: id, Color: color, Type: ty}
	}
	byID := map[string]gtfs.Route{}
	for _, r := range []gtfs.Route{
		mk("A", "0062CF", 1), mk("C", "0062CF", 1), mk("E", "0062CF", 1),
		mk("B", "EB6800", 1), mk("D", "EB6800", 1), mk("F", "EB6800", 1), mk("M", "EB6800", 1),
		mk("1", "D82233", 1), mk("2", "D82233", 1),
	} {
		byID[r.ID] = r
	}
	w4 := []string{"A", "B", "C", "D", "E", "F", "M"}
	sortBullets(w4, byID)
	if !reflect.DeepEqual(w4, []string{"A", "C", "E", "B", "D", "F", "M"}) {
		t.Fatalf("W 4 St order = %v", w4)
	}
	cc := []string{"1", "2", "A", "B", "C", "D"}
	sortBullets(cc, byID)
	if !reflect.DeepEqual(cc, []string{"A", "C", "B", "D", "1", "2"}) {
		t.Fatalf("Columbus Circle order = %v", cc)
	}

	// feed policy: route_sort_order wins, absentees fall to the back
	style.Set_(style.New(style.Config{BulletOrder: style.BulletsFeed}))
	defer style.Set_(nil)
	so := map[string]gtfs.Route{
		"X": {ID: "X", ShortName: "X", SortOrder: 20, Type: 1},
		"Y": {ID: "Y", ShortName: "Y", SortOrder: 10, Type: 1},
		"Z": {ID: "Z", ShortName: "Z", SortOrder: -1, Type: 1},
	}
	fp := []string{"X", "Y", "Z"}
	sortBullets(fp, so)
	if !reflect.DeepEqual(fp, []string{"Y", "X", "Z"}) {
		t.Fatalf("feed order = %v", fp)
	}

	// natural policy: numbers before letters, plain and simple
	style.Set_(style.New(style.Config{BulletOrder: style.BulletsNatural}))
	nat := []string{"A", "1", "B", "2"}
	sortBullets(nat, byID)
	if !reflect.DeepEqual(nat, []string{"1", "2", "A", "B"}) {
		t.Fatalf("natural order = %v", nat)
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
