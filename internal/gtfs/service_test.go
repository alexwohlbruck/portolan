package gtfs

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeFeed builds a minimal GTFS zip from name→contents.
func writeFeed(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, body := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const shapesTwo = `shape_id,shape_pt_lat,shape_pt_lon,shape_pt_sequence
s1,40.70,-74.00,1
s1,40.71,-74.00,2
s1,40.72,-74.00,3
`

// A feed with no calendar.txt at all — the MTA railroad shape, a
// service_id per date. The weekly mask has to come off the dates, or the
// whole feed vanishes from every scenario (it used to error out).
func TestDatedCalendarDerivesWeeklyMask(t *testing.T) {
	// 2026-08-03, -10, -17 are Mondays; 2026-08-08 is one Saturday.
	path := writeFeed(t, "dated.zip", map[string]string{
		"calendar_dates.txt": `service_id,date,exception_type
mon,20260803,1
mon,20260810,1
mon,20260817,1
sat,20260808,1
`,
		"routes.txt": `route_id,route_type,route_color
R1,2,FF0000
`,
		"trips.txt": `trip_id,route_id,service_id,shape_id
t1,R1,mon,s1
t2,R1,sat,s1
`,
		"stop_times.txt": `trip_id,stop_sequence,arrival_time,departure_time
t1,1,09:00:00,09:00:00
t1,2,09:30:00,09:30:00
t2,1,09:00:00,09:00:00
t2,2,09:30:00,09:30:00
`,
		"shapes.txt": shapesTwo,
	})
	si, err := LoadServiceFeeds(path, ServiceOpts{})
	if err != nil {
		t.Fatalf("dated-calendar feed rejected: %v", err)
	}
	act := si.Activity[PatKey{"R1", "s1"}]
	if act == nil {
		t.Fatal("no activity for the only pattern")
	}
	// Monday (day 0) recurs three times, Saturday (day 5) once — the
	// weighting is what lets a per-route floor kill holiday one-offs.
	if act[0][9] != 3 {
		t.Errorf("Monday 09h weight = %d, want 3", act[0][9])
	}
	if act[5][9] != 1 {
		t.Errorf("Saturday 09h weight = %d, want 1", act[5][9])
	}
	if act[1][9] != 0 {
		t.Errorf("Tuesday has service it never ran: %d", act[1][9])
	}
}

// frequencies.txt owns the span for headway-based feeds. Read literally,
// the 00:00:00 template put a 24-hour service in the late-night hour only.
func TestFrequenciesSetTheSpan(t *testing.T) {
	path := writeFeed(t, "freq.zip", map[string]string{
		"calendar.txt": `service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday
all,1,1,1,1,1,1,1
`,
		"routes.txt": `route_id,route_type,route_color
A,2,00FF00
`,
		"trips.txt": `trip_id,route_id,service_id,shape_id
t1,A,all,s1
`,
		"stop_times.txt": `trip_id,stop_sequence,arrival_time,departure_time
t1,1,00:00:00,00:00:00
t1,2,00:10:00,00:10:00
`,
		"frequencies.txt": `trip_id,start_time,end_time,headway_secs
t1,05:00:00,23:00:00,600
`,
		"shapes.txt": shapesTwo,
	})
	si, err := LoadServiceFeeds(path, ServiceOpts{})
	if err != nil {
		t.Fatal(err)
	}
	act := si.Activity[PatKey{"A", "s1"}]
	if act == nil {
		t.Fatal("no activity")
	}
	if act[0][12] == 0 {
		t.Error("midday has no service, but the feed runs 05:00–23:00")
	}
	if act[0][3] != 0 {
		t.Error("03:00 has service the feed does not run")
	}
}

// Overlay feeds must carry the same f<i>: route prefix the pipeline's
// loader applies, or a scenario names keys the build can never match —
// this is exactly what made NYC's Saturday map subway-only.
func TestOverlayFeedsArePrefixed(t *testing.T) {
	base := map[string]string{
		"calendar.txt": `service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday
all,1,1,1,1,1,1,1
`,
		"routes.txt": `route_id,route_type,route_color
7,1,0000FF
`,
		"trips.txt": `trip_id,route_id,service_id,shape_id
t1,7,all,s1
`,
		"stop_times.txt": `trip_id,stop_sequence,arrival_time,departure_time
t1,1,09:00:00,09:00:00
t1,2,09:30:00,09:30:00
`,
		"shapes.txt": shapesTwo,
	}
	p1 := writeFeed(t, "primary.zip", base)
	p2 := writeFeed(t, "overlay.zip", base)

	si, err := LoadServiceFeeds(p1+","+p2, ServiceOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := si.Activity[PatKey{"7", "s1"}]; !ok {
		t.Error("primary feed route lost its bare id")
	}
	if _, ok := si.Activity[PatKey{"f1:7", "s1"}]; !ok {
		t.Error("overlay route is not prefixed f1: — scenario keys will never match the build")
	}
	// Same shape id in both feeds must not collide into one geometry.
	if si.shapeOf[PatKey{"7", "s1"}] == si.shapeOf[PatKey{"f1:7", "s1"}] {
		t.Error("shape ids collided across feeds; they are only unique within one")
	}
}

// Derivation is the primary feed's rail; selection is everything drawable.
func TestSelectSpansAllFeedsAndClasses(t *testing.T) {
	rail := map[string]string{
		"calendar.txt": `service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday
all,1,1,1,1,1,1,1
`,
		"routes.txt": `route_id,route_type,route_color
M,1,0000FF
`,
		"trips.txt": `trip_id,route_id,service_id,shape_id
t1,M,all,s1
`,
		"stop_times.txt": `trip_id,stop_sequence,arrival_time,departure_time
t1,1,09:00:00,09:00:00
t1,2,09:30:00,09:30:00
`,
		"shapes.txt": shapesTwo,
	}
	bus := map[string]string{
		"calendar.txt": `service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday
wk,1,1,1,1,1,0,0
`,
		"routes.txt": `route_id,route_type,route_color
B12,3,888888
`,
		"trips.txt": `trip_id,route_id,service_id,shape_id
b1,B12,wk,s1
`,
		"stop_times.txt": `trip_id,stop_sequence,arrival_time,departure_time
b1,1,09:00:00,09:00:00
b1,2,09:30:00,09:30:00
`,
		"shapes.txt": shapesTwo,
	}
	si, err := LoadServiceFeeds(
		writeFeed(t, "rail.zip", rail)+","+writeFeed(t, "bus.zip", bus),
		ServiceOpts{
			Drawable: func(int) bool { return true },
			Derive:   func(t int) bool { return t != 3 },
		})
	if err != nil {
		t.Fatal(err)
	}
	if si.derive[PatKey{"f1:B12", "s1"}] {
		t.Error("bus entered the derivation set")
	}
	if !si.derive[PatKey{"M", "s1"}] {
		t.Error("primary rail missing from the derivation set")
	}

	var weekday, weekend [7][24]bool
	weekday[0][9] = true // Monday 09h
	weekend[5][9] = true // Saturday 09h
	if got := si.Select(weekday, 0.99); !got[PatKey{"f1:B12", "s1"}] {
		t.Error("weekday selection dropped the bus — selection must span all feeds and classes")
	}
	if got := si.Select(weekend, 0.99); got[PatKey{"f1:B12", "s1"}] {
		t.Error("weekend selection kept a weekday-only bus")
	}
	if got := si.Select(weekend, 0.99); !got[PatKey{"M", "s1"}] {
		t.Error("weekend selection dropped the seven-day metro")
	}
}
