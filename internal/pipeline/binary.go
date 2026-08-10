package pipeline

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// PLNB — portolan line binary.
//
// The GeoJSON emit is a parse cost for any client that rebuilds
// interactively: an NYC band is around 11 MB, and a browser pays for it
// twice, once to parse the text and again to allocate one JavaScript
// object per feature before a single vertex reaches the GPU. This
// format removes both. Everything is a flat typed array, so a client
// slices the buffer, uploads positions straight to a vertex buffer, and
// reads per-feature values by index — no per-feature allocation at all.
//
// LAYOUT — little-endian throughout, every section 4-byte aligned.
//
//	header      32 bytes
//	  magic       u32   "PLNB" as bytes 0x50 0x4C 0x4E 0x42
//	  version     u16   1
//	  flags       u16   reserved, 0
//	  nFeatures   u32
//	  nPositions  u32   total vertices across all features
//	  band        i32   the zoom band this file holds, or -1 for the union
//	  strTabOff   u32   byte offset of the string table
//	  reserved    u32   0
//	  reserved    u32   0
//
//	positions     nPositions × 2 × i32
//	starts        (nFeatures+1) × u32
//	<property columns, each nFeatures long>
//	strings
//
// POSITIONS are lon then lat, in units of 1e-7 degrees — signed 32-bit
// fixed point. Not f32: a float32 holds about seven significant digits
// and a longitude like -73.9876543 needs nine, so f32 would quantise
// vertices to roughly two metres and visibly kink a ribbon. i32 at 1e-7
// is exact to about a centimetre and the same width.
//
// STARTS is the standard offset array: feature i owns positions
// starts[i] up to starts[i+1], and starts[nFeatures] == nPositions, so
// a client never special-cases the last feature.
//
// PROPERTY COLUMNS are parallel arrays — column-major, not one record
// per feature — because a renderer wants all the offsets or all the
// colours at once, and a struct-of-arrays layout hands each column over
// as a contiguous slice.
//
//	kind        u8    0 steady, 1 transition, 2 bridge, 255 unknown
//	(pad to 4-byte alignment)
//	color       u32   0x00RRGGBB
//	offsetPx    f32
//	offFromPx   f32
//	offToPx     f32
//	slot        i16
//	nslots      i16
//	bandMin     i16
//	bandMax     i16
//	routeType   i16
//	(pad to 4-byte alignment)
//	label       u32   string-table index
//	mode        u32   string-table index
//	routes      u32   string-table index — the CSV the GeoJSON emits
//	acts        u32   string-table index — ";"-joined, "" when unknown
//
// STRINGS is u32 count, then each string as u32 byte length followed by
// its UTF-8 bytes. Values are interned, so the thousands of segments
// sharing a mode or a route set cost one copy.
const (
	binMagic   uint32 = 0x424E4C50 // "PLNB" little-endian
	binVersion uint16 = 1
	binHeader         = 32
	// binCoordScale: 1e-7 degrees per unit.
	binCoordScale = 1e7
)

// BandUnion is the band value meaning "every band, as the GeoJSON emit
// has always written".
const BandUnion = -1

func kindCode(k string) uint8 {
	switch k {
	case "steady":
		return 0
	case "transition":
		return 1
	case "bridge":
		return 2
	}
	return 255
}

// strTab interns strings and renders the table.
type strTab struct {
	idx  map[string]uint32
	list []string
}

func newStrTab() *strTab {
	// index 0 is always the empty string, so a client can treat 0 as
	// "nothing here" without a lookup
	return &strTab{idx: map[string]uint32{"": 0}, list: []string{""}}
}

func (t *strTab) put(s string) uint32 {
	if i, ok := t.idx[s]; ok {
		return i
	}
	i := uint32(len(t.list))
	t.idx[s] = i
	t.list = append(t.list, s)
	return i
}

func (t *strTab) render(w *bytes.Buffer) {
	binary.Write(w, binary.LittleEndian, uint32(len(t.list)))
	for _, s := range t.list {
		binary.Write(w, binary.LittleEndian, uint32(len(s)))
		w.WriteString(s)
	}
}

// WriteSegmentsBinary emits the PLNB form of the same segments
// WriteSegmentsGeoJSON writes. band is recorded in the header for the
// client's benefit; filtering is the caller's job (see FilterBand).
func WriteSegmentsBinary(path string, segs []stages.Segment, frame geo.Frame, band int) error {
	var pos, starts, kinds, colors, f32s, i16s, u32s bytes.Buffer
	tab := newStrTab()

	nPos := uint32(0)
	for i := range segs {
		s := &segs[i]
		binary.Write(&starts, binary.LittleEndian, nPos)
		for _, p := range s.Line.Pts {
			ll := frame.ToLL(p)
			binary.Write(&pos, binary.LittleEndian, int32(math.Round(ll.Lon*binCoordScale)))
			binary.Write(&pos, binary.LittleEndian, int32(math.Round(ll.Lat*binCoordScale)))
			nPos++
		}
		kinds.WriteByte(kindCode(s.Kind))
		binary.Write(&colors, binary.LittleEndian, hexToRGB(s.Color))
		binary.Write(&f32s, binary.LittleEndian, float32(s.OffsetPx))
		binary.Write(&f32s, binary.LittleEndian, float32(s.OffFromPx))
		binary.Write(&f32s, binary.LittleEndian, float32(s.OffToPx))
		binary.Write(&i16s, binary.LittleEndian, int16(s.Slot))
		binary.Write(&i16s, binary.LittleEndian, int16(s.NSlots))
		binary.Write(&i16s, binary.LittleEndian, int16(s.BandMin))
		binary.Write(&i16s, binary.LittleEndian, int16(s.BandMax))
		binary.Write(&i16s, binary.LittleEndian, int16(s.RouteType))
		binary.Write(&u32s, binary.LittleEndian, tab.put(s.Label))
		binary.Write(&u32s, binary.LittleEndian, tab.put(s.Mode))
		binary.Write(&u32s, binary.LittleEndian, tab.put(strings.Join(s.Routes, ",")))
		binary.Write(&u32s, binary.LittleEndian, tab.put(strings.Join(s.Acts, ";")))
	}
	binary.Write(&starts, binary.LittleEndian, nPos) // the sentinel

	pad := func(b *bytes.Buffer) {
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
	}
	pad(&kinds)
	pad(&i16s)

	body := &bytes.Buffer{}
	body.Write(pos.Bytes())
	body.Write(starts.Bytes())
	body.Write(kinds.Bytes())
	body.Write(colors.Bytes())
	body.Write(f32s.Bytes())
	body.Write(i16s.Bytes())
	body.Write(u32s.Bytes())

	out := &bytes.Buffer{}
	binary.Write(out, binary.LittleEndian, binMagic)
	binary.Write(out, binary.LittleEndian, binVersion)
	binary.Write(out, binary.LittleEndian, uint16(0))
	binary.Write(out, binary.LittleEndian, uint32(len(segs)))
	binary.Write(out, binary.LittleEndian, nPos)
	binary.Write(out, binary.LittleEndian, int32(band))
	binary.Write(out, binary.LittleEndian, uint32(binHeader+body.Len()))
	binary.Write(out, binary.LittleEndian, uint32(0))
	binary.Write(out, binary.LittleEndian, uint32(0))
	out.Write(body.Bytes())
	tab.render(out)

	return os.WriteFile(path, out.Bytes(), 0o644)
}

// hexToRGB packs a "RRGGBB" style hex into 0x00RRGGBB. An unparseable
// or empty colour becomes black, which is what the GeoJSON path already
// shows for a route the feed gave no colour.
func hexToRGB(hex string) uint32 {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) != 6 {
		return 0
	}
	var v uint32
	for i := 0; i < 6; i++ {
		c := h[i]
		var d uint32
		switch {
		case c >= '0' && c <= '9':
			d = uint32(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint32(c-'A') + 10
		default:
			return 0
		}
		v = v<<4 | d
	}
	return v
}

// FilterBand keeps the segments visible in one zoom band.
//
// FAIR emits a full copy of the network per band (15/14/13/0) and
// exactly one of them is ever visible, so a client that knows its zoom
// is downloading three copies it will not draw. BandUnion keeps
// everything, which is what every existing caller gets.
func FilterBand(segs []stages.Segment, band int) []stages.Segment {
	if band == BandUnion {
		return segs
	}
	out := segs[:0:0]
	for _, s := range segs {
		if s.BandMin <= band && band <= s.BandMax {
			out = append(out, s)
		}
	}
	return out
}

// ParseBand reads a --band value: "" or "union" for everything,
// otherwise one of FAIR's band numbers.
func ParseBand(s string) (int, error) {
	switch strings.TrimSpace(s) {
	case "", "union", "all":
		return BandUnion, nil
	case "15":
		return 15, nil
	case "14":
		return 14, nil
	case "13":
		return 13, nil
	case "0":
		return 0, nil
	}
	return 0, fmt.Errorf("band %q: want 15, 14, 13, 0 or union", s)
}
