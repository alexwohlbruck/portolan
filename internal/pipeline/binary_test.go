package pipeline

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// The binary emit is a public wire format — a client parses it by
// offset arithmetic against the documented layout, with no library in
// between. So these read it back the way that client will: by offsets,
// not through a Go decoder that could share a bug with the writer.

func binFixture() []stages.Segment {
	line := func(pts ...geo.Pt) *geo.Line { return geo.NewLine(pts) }
	return []stages.Segment{
		{Kind: "steady", Color: "EE352E", Routes: []string{"1", "2"}, Label: "1",
			RouteType: 1, Mode: "metro", Slot: -1, NSlots: 3, OffsetPx: -5,
			BandMin: 15, BandMax: 15, Acts: []string{"ff", "0f"},
			Line: line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 100, Y: 0}, geo.Pt{X: 200, Y: 50})},
		{Kind: "transition", Color: "0039A6", Routes: []string{"B"}, Label: "B",
			RouteType: 1, Mode: "metro", Slot: 2, NSlots: 3,
			OffFromPx: -5, OffToPx: 5, BandMin: 14, BandMax: 14,
			Line: line(geo.Pt{X: 200, Y: 50}, geo.Pt{X: 300, Y: 90})},
		{Kind: "bridge", Color: "", Routes: []string{"F"}, Label: "F",
			RouteType: 2, Mode: "regional", BandMin: 0, BandMax: 13,
			Line: line(geo.Pt{X: 300, Y: 90}, geo.Pt{X: 400, Y: 90})},
	}
}

func TestBinaryLayoutReadsBackByOffset(t *testing.T) {
	segs := binFixture()
	frame := geo.NewFrame(geo.LL{Lon: -74, Lat: 40.7})
	path := filepath.Join(t.TempDir(), "b.bin")
	if err := WriteSegmentsBinary(path, segs, frame, 15); err != nil {
		t.Fatal(err)
	}
	d, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian

	if got := le.Uint32(d[0:]); got != binMagic {
		t.Fatalf("magic %#x, want %#x", got, binMagic)
	}
	if got := le.Uint16(d[4:]); got != binVersion {
		t.Fatalf("version %d, want %d", got, binVersion)
	}
	nFeat := le.Uint32(d[8:])
	nPos := le.Uint32(d[12:])
	band := int32(le.Uint32(d[16:]))
	strOff := le.Uint32(d[20:])
	if int(nFeat) != len(segs) {
		t.Fatalf("nFeatures %d, want %d", nFeat, len(segs))
	}
	wantPos := 0
	for _, s := range segs {
		wantPos += len(s.Line.Pts)
	}
	if int(nPos) != wantPos {
		t.Fatalf("nPositions %d, want %d", nPos, wantPos)
	}
	if band != 15 {
		t.Errorf("band %d, want 15", band)
	}

	posOff := uint32(binHeader)
	startsOff := posOff + nPos*8
	starts := make([]uint32, nFeat+1)
	for i := range starts {
		starts[i] = le.Uint32(d[startsOff+uint32(i)*4:])
	}
	// the sentinel is what lets a client avoid special-casing the last
	// feature; if it is wrong, every consumer's last ribbon is wrong
	if starts[nFeat] != nPos {
		t.Errorf("sentinel %d, want %d", starts[nFeat], nPos)
	}
	for i := range segs {
		if int(starts[i+1]-starts[i]) != len(segs[i].Line.Pts) {
			t.Errorf("feature %d spans %d positions, want %d",
				i, starts[i+1]-starts[i], len(segs[i].Line.Pts))
		}
	}
	// positions must survive the fixed-point round trip to well under a
	// metre — this is the whole reason they are i32 and not f32
	for i := range segs {
		for j, p := range segs[i].Line.Pts {
			k := starts[i] + uint32(j)
			lon := float64(int32(le.Uint32(d[posOff+k*8:]))) / binCoordScale
			lat := float64(int32(le.Uint32(d[posOff+k*8+4:]))) / binCoordScale
			back := frame.ToXY(geo.LL{Lon: lon, Lat: lat})
			if dist := back.Dist(p); dist > 0.05 {
				t.Errorf("feature %d vertex %d moved %.3f m through the encoding", i, j, dist)
			}
		}
	}

	// property columns, in declared order after the starts array
	kindsOff := startsOff + (nFeat+1)*4
	colorsOff := kindsOff + align4(nFeat)
	f32Off := colorsOff + nFeat*4
	i16Off := f32Off + nFeat*12
	u32Off := i16Off + align4(nFeat*10)

	wantKinds := []uint8{0, 1, 2}
	for i := range segs {
		if got := d[kindsOff+uint32(i)]; got != wantKinds[i] {
			t.Errorf("feature %d kind %d, want %d", i, got, wantKinds[i])
		}
	}
	if got := le.Uint32(d[colorsOff:]); got != 0xEE352E {
		t.Errorf("colour %#x, want 0xEE352E", got)
	}
	if got := le.Uint32(d[colorsOff+8:]); got != 0 {
		t.Errorf("a segment with no colour must encode 0, got %#x", got)
	}
	if got := math.Float32frombits(le.Uint32(d[f32Off:])); got != -5 {
		t.Errorf("offsetPx %v, want -5", got)
	}
	if got := int16(le.Uint16(d[i16Off:])); got != -1 {
		t.Errorf("slot %d, want -1 — a signed column must stay signed", got)
	}
	if got := int16(le.Uint16(d[i16Off+2:])); got != 3 {
		t.Errorf("nslots %d, want 3", got)
	}

	// string table: index 0 is reserved for "", so a client can treat 0
	// as "nothing here" without a lookup
	count := le.Uint32(d[strOff:])
	strs := make([]string, count)
	at := strOff + 4
	for i := range strs {
		n := le.Uint32(d[at:])
		strs[i] = string(d[at+4 : at+4+n])
		at += 4 + n
	}
	if strs[0] != "" {
		t.Errorf("string 0 is %q, want the empty string", strs[0])
	}
	routesIdx := le.Uint32(d[u32Off+8:]) // label, mode, routes, acts
	if strs[routesIdx] != "1,2" {
		t.Errorf("routes %q, want \"1,2\"", strs[routesIdx])
	}
	actsIdx := le.Uint32(d[u32Off+12:])
	if strs[actsIdx] != "ff;0f" {
		t.Errorf("acts %q, want \"ff;0f\"", strs[actsIdx])
	}
	if int(at) != len(d) {
		t.Errorf("string table ends at %d but the file is %d bytes", at, len(d))
	}
}

func align4(n uint32) uint32 { return (n + 3) &^ 3 }

func TestFilterBandKeepsOnlyTheVisibleCopies(t *testing.T) {
	segs := binFixture()
	if got := len(FilterBand(segs, BandUnion)); got != 3 {
		t.Errorf("union kept %d, want all 3", got)
	}
	if got := len(FilterBand(segs, 15)); got != 1 {
		t.Errorf("band 15 kept %d, want 1", got)
	}
	if got := len(FilterBand(segs, 13)); got != 1 {
		t.Errorf("band 13 kept %d, want 1 (the 0..13 bridge)", got)
	}
	// band 0 is a real band, and the reason ChartOpts.Band is a pointer:
	// an int zero value here would mean "band 0" to every caller that
	// never set it
	if got := len(FilterBand(segs, 0)); got != 1 {
		t.Errorf("band 0 kept %d, want 1", got)
	}
	if len(FilterBand(segs, 15)) == len(segs) {
		t.Error("filtering did nothing")
	}
}

func TestParseBandRejectsNonsense(t *testing.T) {
	for _, s := range []string{"", "union", "all"} {
		if b, err := ParseBand(s); err != nil || b != BandUnion {
			t.Errorf("ParseBand(%q) = %d, %v; want the union", s, b, err)
		}
	}
	for _, s := range []string{"12", "16", "z15", "-1"} {
		if _, err := ParseBand(s); err == nil {
			t.Errorf("ParseBand(%q) should have failed", s)
		}
	}
}
