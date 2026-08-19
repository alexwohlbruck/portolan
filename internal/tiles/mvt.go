// Package tiles slices a finished build into a z/x/y Mapbox Vector Tile
// pyramid. The pipeline solves a region as ONE graph — no tile seams in
// the geometry — and tiling happens only here, at delivery: the emitted
// ribbons are banded by zoom already (band_min/band_max), so each tile
// zoom carries exactly the band the viewer would have drawn there, and a
// world map stays the sum of its regions.
package tiles

import "math"

// The encoder below is the Vector Tile spec v2 by hand, the same way
// binary.go hand-rolls PLNB: the format is a page of varints and there is
// no reason to take on a protobuf dependency for it.

// mvtValue is one entry of a layer's value table.
type mvtValue struct {
	s    string
	f    float64
	i    int64
	b    bool
	kind byte // 's', 'f', 'i', 'b'
}

// mvtFeature is one feature in tile-local integer coordinates. Lines is a
// multi-line; points use a single one-vertex "line" per point.
type mvtFeature struct {
	id    uint64
	tags  []uint32 // key,value index pairs into the layer tables
	typ   int      // 1 point, 2 linestring
	lines [][][2]int32
}

// mvtLayer accumulates features plus interned key/value tables.
type mvtLayer struct {
	name   string
	feats  []mvtFeature
	keys   []string
	keyIdx map[string]uint32
	vals   []mvtValue
	valIdx map[mvtValue]uint32
	// ext is the layer's declared coordinate resolution. Coordinates are
	// INTEGERS on this grid, so the grid is the geometry's precision
	// floor: at the default 4096 one unit is ~0.3 m at z15, which is
	// 1.4 px once the top zoom is overzoomed to z18 — a smooth arc
	// renders as a visible staircase. The top zoom raises it.
	ext int
}

func newLayer(name string, ext int) *mvtLayer {
	return &mvtLayer{name: name, keyIdx: map[string]uint32{}, valIdx: map[mvtValue]uint32{}, ext: ext}
}

func (l *mvtLayer) key(k string) uint32 {
	if i, ok := l.keyIdx[k]; ok {
		return i
	}
	i := uint32(len(l.keys))
	l.keys = append(l.keys, k)
	l.keyIdx[k] = i
	return i
}

func (l *mvtLayer) val(v mvtValue) uint32 {
	if i, ok := l.valIdx[v]; ok {
		return i
	}
	i := uint32(len(l.vals))
	l.vals = append(l.vals, v)
	l.valIdx[v] = i
	return i
}

// tag appends one property to a feature's tag list. Property values reach
// us as decoded JSON (string/float64/bool); ints ride in as float64 and
// are stored integral when exact, so the client sees the same numbers the
// GeoJSON carried.
func (l *mvtLayer) tag(f *mvtFeature, k string, v any) {
	var mv mvtValue
	switch x := v.(type) {
	case string:
		mv = mvtValue{s: x, kind: 's'}
	case bool:
		mv = mvtValue{b: x, kind: 'b'}
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			mv = mvtValue{i: int64(x), kind: 'i'}
		} else {
			mv = mvtValue{f: x, kind: 'f'}
		}
	case int:
		mv = mvtValue{i: int64(x), kind: 'i'}
	case int64:
		mv = mvtValue{i: x, kind: 'i'}
	default:
		return // lists etc. are JSON-encoded by the caller or dropped
	}
	f.tags = append(f.tags, l.key(k), l.val(mv))
}

// --- protobuf wire helpers -------------------------------------------------

func varint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func zigzag(v int64) uint64 { return uint64((v << 1) ^ (v >> 63)) }

func tagBytes(b []byte, field int, wire int) []byte {
	return varint(b, uint64(field)<<3|uint64(wire))
}

func lenField(b []byte, field int, payload []byte) []byte {
	b = tagBytes(b, field, 2)
	b = varint(b, uint64(len(payload)))
	return append(b, payload...)
}

// --- encoding --------------------------------------------------------------

const extent = 4096

func (v mvtValue) encode() []byte {
	var b []byte
	switch v.kind {
	case 's':
		b = lenField(b, 1, []byte(v.s))
	case 'f':
		b = tagBytes(b, 3, 1)
		bits := math.Float64bits(v.f)
		for i := 0; i < 8; i++ {
			b = append(b, byte(bits>>(8*i)))
		}
	case 'i':
		if v.i >= 0 {
			b = tagBytes(b, 4, 0)
			b = varint(b, uint64(v.i))
		} else {
			b = tagBytes(b, 6, 0)
			b = varint(b, zigzag(v.i))
		}
	case 'b':
		b = tagBytes(b, 7, 0)
		if v.b {
			b = varint(b, 1)
		} else {
			b = varint(b, 0)
		}
	}
	return b
}

// geometry encodes the command stream: one MoveTo+LineTo run per part,
// cursor carried across parts as the spec requires.
func (f *mvtFeature) geometry() []uint32 {
	var g []uint32
	var cx, cy int32
	for _, part := range f.lines {
		if len(part) == 0 {
			continue
		}
		g = append(g, 1<<3|1, // MoveTo, count 1
			uint32(zigzag(int64(part[0][0]-cx))), uint32(zigzag(int64(part[0][1]-cy))))
		cx, cy = part[0][0], part[0][1]
		if f.typ == 2 && len(part) > 1 {
			g = append(g, uint32(len(part)-1)<<3|2) // LineTo, count n-1
			for _, p := range part[1:] {
				g = append(g, uint32(zigzag(int64(p[0]-cx))), uint32(zigzag(int64(p[1]-cy))))
				cx, cy = p[0], p[1]
			}
		}
	}
	return g
}

func (f *mvtFeature) encode() []byte {
	var b []byte
	if f.id != 0 {
		b = tagBytes(b, 1, 0)
		b = varint(b, f.id)
	}
	var tags []byte
	for _, t := range f.tags {
		tags = varint(tags, uint64(t))
	}
	b = lenField(b, 2, tags)
	b = tagBytes(b, 3, 0)
	b = varint(b, uint64(f.typ))
	var geom []byte
	for _, g := range f.geometry() {
		geom = varint(geom, uint64(g))
	}
	return lenField(b, 4, geom)
}

func (l *mvtLayer) encode() []byte {
	var b []byte
	b = tagBytes(b, 15, 0) // version
	b = varint(b, 2)
	b = lenField(b, 1, []byte(l.name))
	for i := range l.feats {
		b = lenField(b, 2, l.feats[i].encode())
	}
	for _, k := range l.keys {
		b = lenField(b, 3, []byte(k))
	}
	for _, v := range l.vals {
		b = lenField(b, 4, v.encode())
	}
	b = tagBytes(b, 5, 0)
	b = varint(b, uint64(l.ext))
	return b
}

// encodeTile wraps the non-empty layers into one tile blob.
func encodeTile(layers []*mvtLayer) []byte {
	var b []byte
	for _, l := range layers {
		if len(l.feats) == 0 {
			continue
		}
		b = lenField(b, 3, l.encode())
	}
	return b
}
