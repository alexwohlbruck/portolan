// Package sketch holds the hand-drawn ground truth — the owner's network
// and yard drawings — and the scorers that grade a build against them
// (docs/TOOLS.md). The drawing IS the definition of correct.
//
// The file on disk is precious hand work: read it, never regenerate it,
// and write it only through Save. Two properties that file must keep:
//
//  1. It is COMMITTED beside the code it grades, so a geometry change and
//     the ground truth it was tuned against land in one diff. That is why
//     Save is deterministic and indents per anchor — a sketch whose every
//     save rewrites one 300 kB line is a sketch nobody can review.
//  2. Go is the schema authority. The editor POSTs a document and the
//     server re-encodes it through these types, so a field the model does
//     not carry cannot reach disk. Adding a field to the editor means
//     adding it here first.
//
// Geometry is raw lon/lat. At yard and city zooms the projection error is
// far under a drawn line's width, and projecting would change the saved
// coordinates of every existing sketch.
package sketch

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// LL is one drawn [lon, lat]. It marshals compact and rounded to 7
// decimals — ~1 cm, an order finer than the pen can place — so a saved
// sketch is a readable diff rather than a wall of float64 noise.
type LL [2]float64

func (p LL) MarshalJSON() ([]byte, error) {
	return []byte("[" + num(p[0]) + "," + num(p[1]) + "]"), nil
}

func num(v float64) string {
	return strconv.FormatFloat(math.Round(v*1e7)/1e7, 'f', -1, 64)
}

// Path is a baked polyline. It marshals on ONE line: it is machine output
// derived from the anchors, so per-vertex diffs would bury the hand work.
type Path []LL

func (c Path) MarshalJSON() ([]byte, error) {
	if len(c) == 0 {
		return []byte("[]"), nil
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, p := range c {
		if i > 0 {
			b.WriteByte(',')
		}
		raw, _ := p.MarshalJSON()
		b.Write(raw)
	}
	b.WriteByte(']')
	return []byte(b.String()), nil
}

// Anchor is one bezier control point. Handle state is THREE-valued, not
// two: null means "auto-smooth, derive a Catmull-Rom tangent at bake
// time", a handle equal to the point means "hard corner", anything else
// is user-placed. Collapsing null into an explicit tangent would freeze
// every curve the first time a neighbour moved.
type Anchor struct {
	P    LL  `json:"p"`
	HIn  *LL `json:"hin"`
	HOut *LL `json:"hout"`
}

// Curve is one drawn stroke: the anchors the human edits plus the baked
// polyline every consumer reads. Coords IS the contract — the scorers
// never look at anchors.
//
// A closed curve (a yard boundary) bakes its wrap-around span and repeats
// its first vertex last, so Coords can be read as a ring with no
// re-closing and no special case.
type Curve struct {
	ID      string   `json:"id"`
	Label   string   `json:"label,omitempty"`
	Closed  bool     `json:"closed,omitempty"`
	Anchors []Anchor `json:"anchors"`
	Coords  Path     `json:"coords"`
}

// Ring returns a closed curve's vertices with the repeated closing vertex
// dropped — the form every ring routine here wants.
func (c Curve) Ring() Path {
	n := len(c.Coords)
	if n > 1 && c.Coords[0] == c.Coords[n-1] {
		return c.Coords[:n-1]
	}
	return c.Coords
}

// Line projects a curve into the metric frame for measurement.
func (c Curve) Line(frame geo.Frame) *geo.Line {
	if len(c.Coords) < 2 {
		return nil
	}
	return geo.NewLine(pts(c.Coords, frame))
}

func pts(c Path, frame geo.Frame) []geo.Pt {
	out := make([]geo.Pt, len(c))
	for i, p := range c {
		out[i] = frame.ToXY(geo.LL{Lon: p[0], Lat: p[1]})
	}
	return out
}

// Yard is one drawn yard: a closed boundary and the centerlines running
// through it. Entrances are NOT drawn — they are computed where a
// centerline crosses the boundary (Entrances), because an entrance the
// hand placed separately from the lines that use it is an entrance that
// drifts out of agreement with them.
type Yard struct {
	ID          string  `json:"id"`
	Label       string  `json:"label,omitempty"`
	Boundary    Curve   `json:"boundary"`
	Centerlines []Curve `json:"centerlines"`
}

// Network is one feed's whole drawing.
type Network struct {
	Feed    string  `json:"feed,omitempty"`
	Updated string  `json:"updated,omitempty"`
	Lines   []Curve `json:"lines"`
	Yards   []Yard  `json:"yards"`
}

func LoadNetwork(path string) (*Network, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var n Network
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// Save writes the document atomically, with one anchor per line and each
// baked path on a single line — the split between hand work and machine
// output (see the package comment).
//
// The layout is hand-rolled rather than json.MarshalIndent because
// MarshalIndent re-indents what a custom MarshalJSON returns: every
// coordinate pair would come back out as four lines, and a 17-line sketch
// would land as 29,000 lines of float.
func Save(path string, n *Network) error {
	var b strings.Builder
	b.WriteString("{\n")
	b.WriteString(`  "feed": ` + jstr(n.Feed) + ",\n")
	b.WriteString(`  "updated": ` + jstr(n.Updated) + ",\n")
	b.WriteString(`  "lines": `)
	writeCurves(&b, n.Lines, "  ")
	b.WriteString(",\n")
	b.WriteString(`  "yards": `)
	if len(n.Yards) == 0 {
		b.WriteString("[]")
	} else {
		b.WriteString("[\n")
		for i, y := range n.Yards {
			b.WriteString(`    {` + "\n")
			b.WriteString(`      "id": ` + jstr(y.ID) + ",\n")
			b.WriteString(`      "label": ` + jstr(y.Label) + ",\n")
			b.WriteString(`      "boundary": `)
			bnd := y.Boundary
			bnd.Closed = true // a boundary is a ring by definition, not by flag
			writeCurve(&b, bnd, "      ")
			b.WriteString(",\n")
			b.WriteString(`      "centerlines": `)
			writeCurves(&b, y.Centerlines, "      ")
			b.WriteString("\n    }")
			if i < len(n.Yards)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("  ]")
	}
	b.WriteString("\n}\n")

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeCurves(b *strings.Builder, cs []Curve, indent string) {
	if len(cs) == 0 {
		b.WriteString("[]")
		return
	}
	b.WriteString("[\n")
	for i, c := range cs {
		b.WriteString(indent + "  ")
		writeCurve(b, c, indent+"  ")
		if i < len(cs)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(indent + "]")
}

func writeCurve(b *strings.Builder, c Curve, indent string) {
	b.WriteString("{\n")
	b.WriteString(indent + `  "id": ` + jstr(c.ID) + ",\n")
	b.WriteString(indent + `  "label": ` + jstr(c.Label) + ",\n")
	if c.Closed {
		b.WriteString(indent + `  "closed": true,` + "\n")
	}
	b.WriteString(indent + `  "anchors": `)
	if len(c.Anchors) == 0 {
		b.WriteString("[],\n")
	} else {
		b.WriteString("[\n")
		for i, a := range c.Anchors {
			b.WriteString(indent + "    " + anchorJSON(a))
			if i < len(c.Anchors)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent + "  ],\n")
	}
	raw, _ := c.Coords.MarshalJSON()
	b.WriteString(indent + `  "coords": ` + string(raw) + "\n")
	b.WriteString(indent + "}")
}

func anchorJSON(a Anchor) string {
	h := func(p *LL) string {
		if p == nil {
			return "null"
		}
		raw, _ := p.MarshalJSON()
		return string(raw)
	}
	raw, _ := a.P.MarshalJSON()
	return `{"p": ` + string(raw) + `, "hin": ` + h(a.HIn) + `, "hout": ` + h(a.HOut) + `}`
}

func jstr(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// Frame picks the metric frame for a drawing: the first vertex of the
// first thing drawn. Every measurement in a document must share one
// frame or two distances are not comparable.
func (n *Network) Frame() (geo.Frame, bool) {
	for _, l := range n.Lines {
		if len(l.Coords) > 0 {
			return frameAt(l.Coords[0]), true
		}
	}
	for _, y := range n.Yards {
		if len(y.Boundary.Coords) > 0 {
			return frameAt(y.Boundary.Coords[0]), true
		}
	}
	return geo.Frame{}, false
}

func frameAt(p LL) geo.Frame {
	return geo.NewFrame(geo.LL{Lon: p[0], Lat: p[1]})
}

func (c Curve) String() string {
	if c.Label != "" {
		return c.Label
	}
	return c.ID
}

// label trims a drawn label to a report column width.
func label(s string, n int) string {
	if s == "" {
		s = "?"
	}
	if len(s) > n {
		s = s[:n]
	}
	return s
}
