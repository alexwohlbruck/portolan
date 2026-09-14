package pipeline

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/style"
)

// A slice of downtown Brooklyn, which is where this whole mechanism came
// from. Borough Hall (232/423) and Jay St-MetroTech (A41/R29) are ~240 m
// apart and the MTA connects neither pair: leaving one and entering the
// other is a second fare. Court St (R28) IS connected to Borough Hall.
func brooklynFeed() map[string]string {
	return map[string]string{
		"routes.txt": "route_id,route_type\n" +
			"2,1\n4,1\nA,1\nF,1\nR,1\n",
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\n" +
			"232,Borough Hall,40.693219,-73.989998,1,\n" +
			"232N,Borough Hall,40.693219,-73.989998,0,232\n" +
			"423,Borough Hall,40.692404,-73.990151,1,\n" +
			"423N,Borough Hall,40.692404,-73.990151,0,423\n" +
			"R28,Court St,40.694100,-73.991777,1,\n" +
			"R28N,Court St,40.694100,-73.991777,0,R28\n" +
			"A41,Jay St-MetroTech,40.692338,-73.987342,1,\n" +
			"A41N,Jay St-MetroTech,40.692338,-73.987342,0,A41\n" +
			"R29,Jay St-MetroTech,40.692180,-73.985942,1,\n" +
			"R29N,Jay St-MetroTech,40.692180,-73.985942,0,R29\n",
		"transfers.txt": "from_stop_id,to_stop_id,transfer_type,min_transfer_time\n" +
			"232,232,2,180\n" + // dwell, not a connection
			"232,423,2,300\n" +
			"423,232,2,300\n" +
			"232,R28,2,180\n" +
			"R28,232,2,180\n" +
			"A41,R29,2,90\n" +
			"R29,A41,2,90\n",
	}
}

// parse the rewritten file back into a lookup of pair → transfer_type.
func transferKinds(t *testing.T, body string) map[pair]string {
	t.Helper()
	out := map[pair]string{}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for _, l := range lines[1:] {
		c := strings.Split(l, ",")
		if len(c) < 3 {
			t.Fatalf("malformed row %q", l)
		}
		out[pair{c[0], c[1]}] = c[2]
	}
	return out
}

// The case that started this: the gated pair the agency never connected
// must come out forbidden, in both directions.
func TestForbidUndeclaredBlocksOutOfSystemPair(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.zip")
	writeTestZip(t, src, brooklynFeed())

	body, derived, err := buildTransfers(src, &style.Transfers{ForbidUndeclared: true})
	if err != nil {
		t.Fatalf("buildTransfers: %v", err)
	}
	if derived == 0 {
		t.Fatal("derived no prohibitions at all")
	}
	kinds := transferKinds(t, body)

	for _, p := range []pair{{"423", "A41"}, {"A41", "423"}, {"232", "A41"}, {"A41", "232"}} {
		if kinds[p] != transferForbidden {
			t.Errorf("%s→%s: transfer_type %q, want %q (out of system)",
				p.from, p.to, kinds[p], transferForbidden)
		}
	}
}

// …while every transfer the agency DOES declare survives untouched. A fix
// that forbade the network wholesale would be worse than the bug.
func TestForbidUndeclaredKeepsDeclaredTransfers(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.zip")
	writeTestZip(t, src, brooklynFeed())

	body, _, err := buildTransfers(src, &style.Transfers{ForbidUndeclared: true})
	if err != nil {
		t.Fatalf("buildTransfers: %v", err)
	}
	kinds := transferKinds(t, body)

	for _, p := range []pair{
		{"232", "423"}, {"423", "232"}, // the two Borough Hall halves
		{"232", "R28"}, {"R28", "232"}, // Borough Hall ↔ Court St
		{"A41", "R29"}, {"R29", "A41"}, // the two Jay St halves
	} {
		if kinds[p] == transferForbidden {
			t.Errorf("%s→%s was forbidden, but the agency declares it", p.from, p.to)
		}
		if kinds[p] == "" {
			t.Errorf("%s→%s vanished from the rewritten file", p.from, p.to)
		}
	}
	// The same-station dwell row is not a connection and must not be read
	// as one — but it is still a row the feed shipped, so it survives.
	if kinds[pair{"232", "232"}] == transferForbidden {
		t.Error("a same-station dwell row was forbidden")
	}
}

// Only GATED modes get prohibitions. A bus network's undeclared walks are
// settled by the tariff's transfer window, not by a gate.
func TestForbidUndeclaredSkipsUngatedModes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "bus.zip")
	writeTestZip(t, src, map[string]string{
		"routes.txt": "route_id,route_type\nB1,3\nB2,3\n",
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\n" +
			"s1,Stop One,40.6930,-73.9900,0,\n" +
			"s2,Stop Two,40.6924,-73.9873,0,\n",
	})
	body, derived, err := buildTransfers(src, &style.Transfers{ForbidUndeclared: true})
	if err != nil {
		t.Fatalf("buildTransfers: %v", err)
	}
	if derived != 0 {
		t.Errorf("derived %d prohibitions for a bus-only feed, want 0: %q", derived, body)
	}
}

// Stations further apart than the radius are left alone — nothing would
// have linked them, and a prohibition there is noise.
func TestForbidUndeclaredRespectsRadius(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.zip")
	writeTestZip(t, src, brooklynFeed())

	// 100 m: Borough Hall (423) to Jay St (A41) is ~240 m, so out of reach.
	body, _, err := buildTransfers(src, &style.Transfers{ForbidUndeclared: true, WalkRadiusM: 100})
	if err != nil {
		t.Fatalf("buildTransfers: %v", err)
	}
	if transferKinds(t, body)[pair{"423", "A41"}] == transferForbidden {
		t.Error("a pair beyond the radius was forbidden")
	}
}

// Curated allow rows add connections the agency omitted, both ways by
// default, and are written with the time in seconds.
func TestCuratedAllowAddsTransfer(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.zip")
	writeTestZip(t, src, brooklynFeed())

	body, _, err := buildTransfers(src, &style.Transfers{
		Allow: []style.TransferRule{{From: "R28", To: "A41", Minutes: 5}},
	})
	if err != nil {
		t.Fatalf("buildTransfers: %v", err)
	}
	if k := transferKinds(t, body)[pair{"R28", "A41"}]; k != transferMinimumTime {
		t.Errorf("curated allow: transfer_type %q, want %q", k, transferMinimumTime)
	}
	if k := transferKinds(t, body)[pair{"A41", "R28"}]; k != transferMinimumTime {
		t.Error("curated allow was not made bidirectional by default")
	}
	if !strings.Contains(body, "R28,A41,2,300") {
		t.Errorf("5 minutes should be written as 300 seconds: %q", body)
	}
}

// A curated prohibition beats the agency's own row — that is the point of
// being able to write one.
func TestCuratedForbidBeatsDeclaredTransfer(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.zip")
	writeTestZip(t, src, brooklynFeed())

	body, _, err := buildTransfers(src, &style.Transfers{
		Forbid: []style.TransferRule{{From: "232", To: "R28"}},
	})
	if err != nil {
		t.Fatalf("buildTransfers: %v", err)
	}
	kinds := transferKinds(t, body)
	if kinds[pair{"232", "R28"}] != transferForbidden {
		t.Errorf("curated forbid did not override the feed: %q", kinds[pair{"232", "R28"}])
	}
	// and the old permissive row must be gone, not merely accompanied
	if strings.Contains(body, "232,R28,2,") {
		t.Error("the feed's permissive row survived alongside the prohibition")
	}
}

// No curation at all leaves the feed's transfers.txt to pass through.
func TestNoTransferCurationLeavesFeedAlone(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.zip")
	writeTestZip(t, src, brooklynFeed())

	for _, tc := range []struct {
		name string
		in   *style.Transfers
	}{
		{"nil", nil},
		{"empty block", &style.Transfers{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _, err := buildTransfers(src, tc.in)
			if err != nil {
				t.Fatalf("buildTransfers: %v", err)
			}
			if body != "" {
				t.Errorf("want passthrough, got a rewrite: %q", body)
			}
		})
	}
}

// The rewrite has to be stable: sync fingerprints the exported zip, and a
// file whose row order wandered would rebuild the world every run.
func TestTransfersRewriteIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.zip")
	writeTestZip(t, src, brooklynFeed())

	first, _, err := buildTransfers(src, &style.Transfers{ForbidUndeclared: true})
	if err != nil {
		t.Fatalf("buildTransfers: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, _, err := buildTransfers(src, &style.Transfers{ForbidUndeclared: true})
		if err != nil {
			t.Fatalf("buildTransfers: %v", err)
		}
		if again != first {
			t.Fatal("two runs over the same feed produced different files")
		}
	}
}

// A feed with no transfers.txt at all still gains the curated rows.
func TestTransfersWrittenIntoFeedWithNone(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.zip")
	feed := brooklynFeed()
	delete(feed, "transfers.txt")
	writeTestZip(t, src, feed)

	body, _, err := buildTransfers(src, &style.Transfers{
		Allow: []style.TransferRule{{From: "232", To: "423", Minutes: 5}},
	})
	if err != nil {
		t.Fatalf("buildTransfers: %v", err)
	}
	dst := filepath.Join(dir, "out.zip")
	if err := rewriteZip(src, dst, nil, nil, nil, body); err != nil {
		t.Fatalf("rewriteZip: %v", err)
	}
	got := readTestZip(t, dst)["transfers.txt"]
	if !strings.Contains(got, "232,423,2,300") {
		t.Errorf("curated transfer missing from a feed that shipped no file: %q", got)
	}
}

func TestTransfersValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *style.Transfers
	}{
		{"self pair", &style.Transfers{Allow: []style.TransferRule{{From: "A", To: "A"}}}},
		{"missing to", &style.Transfers{Allow: []style.TransferRule{{From: "A"}}}},
		{"negative radius", &style.Transfers{WalkRadiusM: -1, ForbidUndeclared: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.in.Validate(); err == nil {
				t.Error("want a validation error, got none")
			}
		})
	}
}
