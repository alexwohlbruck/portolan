// Package corridor reads an AUTHORED corridor graph — the geometry
// portolan's own BUNDLE + MATCH stages exist to infer — straight into a
// stages.Network, so a caller who already knows the graph skips the
// inference entirely.
//
// The format is not a new one. It is portolan's own network dump
// (`<out>.trackcenter.geojson` + `<out>.nodes.geojson`, written by
// pipeline.writeNetwork) read back in, which makes a build's own output
// a valid input and the round trip a free regression test. The contract
// is documented in docs/CORRIDORS.md.
//
// This package is a peer of internal/osm: both turn a GeoJSON file into
// the geometry a chart is drawn from. The difference is that osm hands
// over raw track that still has to be bundled and matched, and corridor
// hands over the finished graph.
package corridor

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// SnapTol is how close an edge endpoint must come to a node for the
// reader to join them when the edge carries no explicit from/to. It is
// deliberately tight: a metre is inside any authoring tool's rounding
// and outside any real distance between two distinct junctions. Callers
// wanting exactness should emit from/to and never rely on this.
const SnapTol = 1.0

// Node is one junction of the authored graph.
type Node struct {
	ID string
	At geo.LL
}

// Edge is one authored corridor: the centerline riders travel, plus the
// set of GTFS route ids that ride it.
type Edge struct {
	// ID is for diagnostics only — it names the edge in error messages.
	ID       string
	From, To string
	Pts      []geo.LL
	Routes   []string
	Tracks   int
	Gap      bool
	OneWay   string
}

// Graph is a corridor graph in lat/lon, before it meets a frame.
type Graph struct {
	Nodes []Node
	Edges []Edge
	// Synthesized reports how many nodes the reader had to invent
	// because the input carried no node features. Callers get told:
	// inventing topology is exactly the guessing this format exists to
	// avoid, and a graph that needed it is one bad rounding away from a
	// different map.
	Synthesized int
}

// Load reads a corridor graph from a file, or from stdin for "-". The
// file must carry the corridors themselves; nodes are optional, and may
// either share the collection or arrive separately (see LoadPair).
func Load(path string) (*Graph, error) {
	g, err := loadPath(path)
	if err != nil {
		return nil, err
	}
	if len(g.Edges) == 0 {
		return nil, fmt.Errorf("corridor: %s: no LineString corridors", name(path))
	}
	return g, nil
}

// LoadPair reads a graph split across the two FeatureCollections
// writeNetwork emits — corridors in one file, nodes in the other.
// nodesPath may be "": a single mixed collection carrying both Points
// and LineStrings is equally valid, and Load alone handles it.
func LoadPair(edgesPath, nodesPath string) (*Graph, error) {
	g, err := Load(edgesPath)
	if err != nil {
		return nil, err
	}
	if nodesPath == "" {
		return g, nil
	}
	// parsed WITHOUT the corridor requirement: a nodes file having no
	// LineStrings is the normal case, not an error
	ng, err := loadPath(nodesPath)
	if err != nil {
		return nil, err
	}
	if len(ng.Edges) > 0 {
		return nil, fmt.Errorf("corridor: %s was given as the nodes file but carries %d corridors",
			name(nodesPath), len(ng.Edges))
	}
	if len(ng.Nodes) == 0 {
		return nil, fmt.Errorf("corridor: %s was given as the nodes file but has no Points",
			name(nodesPath))
	}
	g.Nodes = append(g.Nodes, ng.Nodes...)
	return g, nil
}

func loadPath(path string) (*Graph, error) {
	if path == "-" {
		return parseFC(os.Stdin, "<stdin>")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseFC(f, path)
}

func name(path string) string {
	if path == "-" {
		return "<stdin>"
	}
	return path
}

// geojson mirrors only what the contract reads. Unknown members are
// ignored, so a caller may decorate features freely.
type geojson struct {
	Type     string `json:"type"`
	Features []struct {
		ID       any            `json:"id"`
		Props    map[string]any `json:"properties"`
		Geometry struct {
			Type   string          `json:"type"`
			Coords json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

// LoadReader parses one FeatureCollection that must carry corridors.
func LoadReader(r io.Reader, name string) (*Graph, error) {
	g, err := parseFC(r, name)
	if err != nil {
		return nil, err
	}
	if len(g.Edges) == 0 {
		return nil, fmt.Errorf("corridor: %s: no LineString corridors", name)
	}
	return g, nil
}

// parseFC reads one FeatureCollection. Points become nodes and
// LineStrings become edges, so the two-file and one-mixed-file layouts
// are the same code path — GeoJSON permits a mixed collection and
// portolan's own .stations.geojson already ships one. Completeness is
// the caller's question, not this function's.
func parseFC(r io.Reader, name string) (*Graph, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("corridor: %s: %w", name, err)
	}
	var fc geojson
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("corridor: %s: %w", name, err)
	}
	if len(fc.Features) == 0 {
		return nil, fmt.Errorf("corridor: %s: no features", name)
	}
	g := &Graph{}
	for i, f := range fc.Features {
		switch f.Geometry.Type {
		case "Point":
			var c []float64
			if err := json.Unmarshal(f.Geometry.Coords, &c); err != nil || len(c) < 2 {
				return nil, fmt.Errorf("corridor: %s: feature %d: bad Point coordinates", name, i)
			}
			g.Nodes = append(g.Nodes, Node{
				ID: nodeID(f.Props, f.ID, len(g.Nodes)),
				At: geo.LL{Lon: c[0], Lat: c[1]},
			})
		case "LineString":
			var cs [][]float64
			if err := json.Unmarshal(f.Geometry.Coords, &cs); err != nil {
				return nil, fmt.Errorf("corridor: %s: feature %d: bad LineString coordinates", name, i)
			}
			e := Edge{
				ID:     edgeID(f.Props, f.ID, len(g.Edges)),
				From:   propStr(f.Props, "from"),
				To:     propStr(f.Props, "to"),
				Routes: splitRoutes(f.Props["routes"]),
				Tracks: propInt(f.Props, "tracks"),
				Gap:    propBool(f.Props, "gap"),
				OneWay: strings.ToLower(propStr(f.Props, "oneway")),
			}
			// A LineString with one coordinate is not a corridor. This is
			// the single most common export bug (a degenerate stub left
			// by a caller's own editing), and it must be named, not
			// skipped — a silently dropped edge shows up as a hole in
			// the map hours later.
			if len(cs) < 2 {
				return nil, fmt.Errorf("corridor: %s: edge %s has %d coordinates, needs at least 2",
					name, e.ID, len(cs))
			}
			e.Pts = make([]geo.LL, len(cs))
			for j, c := range cs {
				if len(c) < 2 {
					return nil, fmt.Errorf("corridor: %s: edge %s: coordinate %d is not [lon,lat]",
						name, e.ID, j)
				}
				e.Pts[j] = geo.LL{Lon: c[0], Lat: c[1]}
			}
			switch e.OneWay {
			case "", "forward", "backward":
			default:
				return nil, fmt.Errorf("corridor: %s: edge %s: oneway=%q, want forward|backward",
					name, e.ID, e.OneWay)
			}
			g.Edges = append(g.Edges, e)
		case "":
			return nil, fmt.Errorf("corridor: %s: feature %d has no geometry", name, i)
		default:
			return nil, fmt.Errorf("corridor: %s: feature %d: geometry %s — want Point or LineString",
				name, i, f.Geometry.Type)
		}
	}
	return g, nil
}

func nodeID(props map[string]any, fid any, i int) string {
	for _, k := range []string{"node", "id"} {
		if s := propStr(props, k); s != "" {
			return s
		}
	}
	if s := anyStr(fid); s != "" {
		return s
	}
	return "n" + strconv.Itoa(i)
}

func edgeID(props map[string]any, fid any, i int) string {
	for _, k := range []string{"edge", "id"} {
		if s := propStr(props, k); s != "" {
			return s
		}
	}
	if s := anyStr(fid); s != "" {
		return s
	}
	return "e" + strconv.Itoa(i)
}

// propStr reads a property as a string. Numbers count: writeNetwork
// emits `"edge": 12` and `"node": 3` as JSON numbers, and a caller
// hand-writing `"from": "wall-st"` is just as valid — node ids are
// opaque strings either way.
func propStr(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	return anyStr(props[key])
}

func anyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		return ""
	}
}

func propInt(props map[string]any, key string) int {
	if props == nil {
		return 0
	}
	if f, ok := props[key].(float64); ok {
		return int(f)
	}
	if s, ok := props[key].(string); ok {
		n, _ := strconv.Atoi(s)
		return n
	}
	return 0
}

func propBool(props map[string]any, key string) bool {
	if props == nil {
		return false
	}
	switch t := props[key].(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	}
	return false
}

// splitRoutes reads the `routes` property. writeNetwork emits a CSV
// string and that is the contract, but a caller assembling JSON
// programmatically will reach for an array first, so both are read.
// Ids are taken VERBATIM — no trimming beyond whitespace, no case
// folding, no renumbering. Callers map ribbons back to their own
// identifiers by string equality and a "helpful" normalisation here
// would break exactly that.
func splitRoutes(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case []any:
		for _, e := range t {
			if s := strings.TrimSpace(anyStr(e)); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// Bounds is the graph's lon/lat extent (w, s, e, n).
func (g *Graph) Bounds() (w, s, e, n float64) {
	w, s, e, n = 180, 90, -180, -90
	for _, ed := range g.Edges {
		for _, p := range ed.Pts {
			w, e = math.Min(w, p.Lon), math.Max(e, p.Lon)
			s, n = math.Min(s, p.Lat), math.Max(n, p.Lat)
		}
	}
	return
}
