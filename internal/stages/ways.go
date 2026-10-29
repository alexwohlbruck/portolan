package stages

import (
	"math"
	"sort"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/mode"
)

const (
	waySampleStep = 5.0
	// railWayReach stays under the line index cell so projections are local.
	railWayReach   = 30.0
	streetWayReach = 3.0 // direct segments draw the street geometry itself
	minWayCover    = 10.0
)

// AttributeWays resolves each segment's candidate Pieces into Ways: the
// numeric OSM way ids, ridden by one of the segment's routes, whose track
// runs under the segment's line, ordered along it. Candidates come from
// the edges the segment was drawn from, so a structure stacked above or
// below that carries other edges' service can never be picked up. paths
// must be every path whose pieces the segments reference.
func AttributeWays(segs []Segment, paths []Path, tracks []bundle.Track) {
	if len(tracks) == 0 {
		for i := range segs {
			segs[i].Pieces = nil
		}
		return
	}
	g := buildTrackGraphCached(tracks)

	type pieceRoute struct {
		piece int
		route string
	}
	rode := map[pieceRoute]bool{}
	samples := map[int][]geo.Pt{}
	for _, pa := range paths {
		for _, st := range pa.Steps {
			if st.Piece < 0 {
				continue
			}
			rode[pieceRoute{st.Piece, pa.Pattern.Route.ID}] = true
			if _, ok := samples[st.Piece]; !ok {
				samples[st.Piece] = g.pieces[st.Piece].Resample(waySampleStep)
			}
		}
	}

	geo.ParFor(0, len(segs), func(i int) {
		s := &segs[i]
		cands := s.Pieces
		s.Pieces = nil
		if s.Kind == "bridge" || len(cands) == 0 || s.Line == nil || len(s.Line.Pts) < 2 {
			return
		}
		reach := railWayReach
		if !mode.Of(s.RouteType).Trunked() {
			reach = streetWayReach
		}
		lo, hi := padBBox(s.Line.Pts, reach)
		L := s.Line.Len()
		type hit struct {
			way string
			arc float64
		}
		var hits []hit
		for _, pc := range cands {
			ridden := false
			for _, r := range s.Routes {
				if rode[pieceRoute{pc, r}] {
					ridden = true
					break
				}
			}
			if !ridden {
				continue
			}
			plo, phi := padBBox(g.pieces[pc].Pts, 0)
			if phi.X < lo.X || plo.X > hi.X || phi.Y < lo.Y || plo.Y > hi.Y {
				continue
			}
			sp := samples[pc]
			n, sum := 0, 0.0
			for _, q := range sp {
				arc, d, ok := s.Line.ProjectArcCapped(q, reach)
				// a sample past either end projects onto the endpoint
				if ok && (d < 1 || (arc > 0.5 && arc < L-0.5)) {
					n++
					sum += arc
				}
			}
			if n == 0 {
				continue
			}
			pl := g.pieces[pc].Len()
			if float64(n)/float64(len(sp))*pl < math.Min(minWayCover, pl/2) {
				continue
			}
			if w, ok := osmWayID(g.edges[2*pc].Way); ok {
				hits = append(hits, hit{w, sum / float64(n)})
			}
		}
		sort.SliceStable(hits, func(a, b int) bool { return hits[a].arc < hits[b].arc })
		seen := map[string]bool{}
		for _, h := range hits {
			if !seen[h.way] {
				seen[h.way] = true
				s.Ways = append(s.Ways, h.way)
			}
		}
	})
}

// osmWayID normalizes a track id to a bare numeric way id; synthetic ids
// (unnumbered features, service steel) have none.
func osmWayID(id string) (string, bool) {
	id = strings.TrimPrefix(id, "way/")
	if id == "" {
		return "", false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return id, true
}

func padBBox(pts []geo.Pt, pad float64) (lo, hi geo.Pt) {
	lo = geo.Pt{X: math.Inf(1), Y: math.Inf(1)}
	hi = geo.Pt{X: math.Inf(-1), Y: math.Inf(-1)}
	for _, p := range pts {
		lo.X, lo.Y = math.Min(lo.X, p.X), math.Min(lo.Y, p.Y)
		hi.X, hi.Y = math.Max(hi.X, p.X), math.Max(hi.Y, p.Y)
	}
	return geo.Pt{X: lo.X - pad, Y: lo.Y - pad}, geo.Pt{X: hi.X + pad, Y: hi.Y + pad}
}
