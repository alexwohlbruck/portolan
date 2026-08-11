package pipeline

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
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
			BandMin: 15, BandMax: 24, Acts: []string{"ff", "0f"},
			Line: line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 100, Y: 0}, geo.Pt{X: 200, Y: 50})},
		{Kind: "transition", Color: "0039A6", Routes: []string{"B"}, Label: "B",
			RouteType: 1, Mode: "metro", Slot: 2, NSlots: 3,
			OffFromPx: -5, OffToPx: 5, BandMin: 14, BandMax: 15,
			Line: line(geo.Pt{X: 200, Y: 50}, geo.Pt{X: 300, Y: 90})},
		{Kind: "bridge", Color: "", Routes: []string{"F"}, Label: "F",
			RouteType: 2, Mode: "regional", BandMin: 13, BandMax: 14,
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

// The bands are HALF-OPEN and ADJACENT — {15,24} {14,15} {13,14} — so
// each band's max is the next band's min. That adjacency is the whole
// test: a fixture whose ranges cannot touch (the old {15,15} {14,14})
// makes an off-by-one on the upper bound invisible, which is exactly
// how a closed comparison shipped and returned two bands per request.
func TestFilterBandKeepsExactlyOneBand(t *testing.T) {
	segs := binFixture()
	if got := len(FilterBand(segs, BandUnion)); got != 3 {
		t.Errorf("union kept %d, want all 3", got)
	}
	for _, tc := range []struct {
		band    int
		wantMin int
		wantMax int
	}{
		{15, 15, 24},
		{14, 14, 15},
		{13, 13, 14},
	} {
		got := FilterBand(segs, tc.band)
		if len(got) != 1 {
			t.Errorf("band %d kept %d segments, want exactly 1: %v",
				tc.band, len(got), bandsOf(got))
			continue
		}
		if got[0].BandMin != tc.wantMin || got[0].BandMax != tc.wantMax {
			t.Errorf("band %d kept (%d,%d), want (%d,%d)",
				tc.band, got[0].BandMin, got[0].BandMax, tc.wantMin, tc.wantMax)
		}
	}
	// a band no segment covers keeps nothing rather than the nearest
	if got := len(FilterBand(segs, 0)); got != 0 {
		t.Errorf("band 0 kept %d, want 0 — this fixture has no z0 copy", got)
	}
}

// bandsOf names the (min,max) pairs a filter let through, so a failure
// says WHICH bands leaked rather than only how many.
func bandsOf(segs []stages.Segment) [][2]int {
	var out [][2]int
	seen := map[[2]int]bool{}
	for _, s := range segs {
		k := [2]int{s.BandMin, s.BandMax}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
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

// TestBinaryStridesMatchTheDocumentedFormulas walks the addressing the
// package comment specifies, for a feature that is NOT the first one.
//
// This is the test that was missing. The layout comment claimed true
// column-major — one contiguous array per property — while the writer
// interleaves by width, and a consumer built a decoder from the comment
// that read plausible-but-wrong values for every property after the
// first in each block. Nothing crashed: each lane is a valid value of
// the right width, so a wrong stride yields a different feature's
// number rather than an error.
//
// Reading feature 0 alone cannot catch that — at i=0 every stride
// multiplies to zero and interleaved and column-major agree. So this
// reads the LAST feature, where they disagree by the whole block.
func TestBinaryStridesMatchTheDocumentedFormulas(t *testing.T) {
	segs := binFixture()
	frame := geo.NewFrame(geo.LL{Lon: -74, Lat: 40.7})
	path := filepath.Join(t.TempDir(), "b.bin")
	if err := WriteSegmentsBinary(path, segs, frame, BandUnion); err != nil {
		t.Fatal(err)
	}
	d, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	nFeat := le.Uint32(d[8:])
	nPos := le.Uint32(d[12:])

	// the formulas exactly as the package comment states them
	posOff := uint32(32)
	startsOff := posOff + nPos*8
	kindsOff := startsOff + (nFeat+1)*4
	colorsOff := kindsOff + align4(nFeat)
	f32Off := colorsOff + nFeat*4
	i16Off := f32Off + nFeat*12
	u32Off := i16Off + align4(nFeat*10)
	if got := le.Uint32(d[20:]); got != u32Off+nFeat*16 {
		t.Errorf("strTabOff %d, but the block formulas give %d", got, u32Off+nFeat*16)
	}

	f32 := func(i, j uint32) float32 { // stride 3
		return math.Float32frombits(le.Uint32(d[f32Off+(i*3+j)*4:]))
	}
	i16 := func(i, j uint32) int16 { // stride 5
		return int16(le.Uint16(d[i16Off+(i*5+j)*2:]))
	}
	u32 := func(i, j uint32) uint32 { // stride 4
		return le.Uint32(d[u32Off+(i*4+j)*4:])
	}
	strs := readStrTab(d, le.Uint32(d[20:]))

	for i, want := range segs {
		i := uint32(i)
		if got := d[kindsOff+i]; got != kindCode(want.Kind) {
			t.Errorf("feature %d kind %d, want %d", i, got, kindCode(want.Kind))
		}
		if got := le.Uint32(d[colorsOff+i*4:]); got != hexToRGB(want.Color) {
			t.Errorf("feature %d color %#x, want %#x", i, got, hexToRGB(want.Color))
		}
		for j, w := range []float64{want.OffsetPx, want.OffFromPx, want.OffToPx} {
			if got := f32(i, uint32(j)); got != float32(w) {
				t.Errorf("feature %d f32[%d] = %v, want %v", i, j, got, w)
			}
		}
		for j, w := range []int{want.Slot, want.NSlots, want.BandMin, want.BandMax, want.RouteType} {
			if got := i16(i, uint32(j)); got != int16(w) {
				t.Errorf("feature %d i16[%d] = %d, want %d", i, j, got, w)
			}
		}
		wantStr := []string{want.Label, want.Mode,
			strings.Join(want.Routes, ","), strings.Join(want.Acts, ";")}
		for j, w := range wantStr {
			if got := strs[u32(i, uint32(j))]; got != w {
				t.Errorf("feature %d u32[%d] = %q, want %q", i, j, got, w)
			}
		}
	}
}

func readStrTab(d []byte, off uint32) []string {
	le := binary.LittleEndian
	n := le.Uint32(d[off:])
	out := make([]string, n)
	at := off + 4
	for i := range out {
		l := le.Uint32(d[at:])
		out[i] = string(d[at+4 : at+4+l])
		at += 4 + l
	}
	return out
}
