package pipeline

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// Caterpillars: inline route bullets riding the drawn ribbons (Apple's
// chain of bullets along a trunk). Each caterpillar is N point features
// at ONE geographic anchor on the bundle's centerline; every bullet
// carries a map-aligned PIXEL vector (fork `symbol-anchor-offset`) that
// places it on its own ribbon's slot offset, staggered along the travel
// direction — the group is pixel-space geometry, so it never stretches
// with zoom, rotates with the camera, and the glyphs stay upright.
//
// Placement law: straight-ish sections, clear of junction cut-backs and
// station markers, spaced so a corridor reads its roster without the
// chain wallpapering the line. Bands 14/15 only — below z14 the drawn
// slot offsets shrink (zoomScaledOffset) and constant-px bullets would
// detach from their ribbons.

// CatBullet is one bullet of one caterpillar chain.
type CatBullet struct {
	LL    geo.LL
	Route string
	Label string
	Hex   string
	Acts  string  // hex Mask168 for this route on this segment ("" unknown)
	Mode  string  // class name, for class toggles
	Vec   [2]float64 // map-aligned px: +x east, +y south
	Band  int     // BandMin of the bundle (client zoom gate)
	Group int     // chain id — bullets of one anchor share it
}

const (
	catPitchAlongPx = 17.0 // bullet diameter 14 + 3 gap
	catStraightDeg  = 7.0  // max heading change across the chain window
	catEndClearM    = 180.0
	catStationClearM = 160.0
)

// BuildCaterpillars picks anchors and emits bullet points. segs are the
// final drawn segments (post terminal cuts); sts the snapped stations.
func BuildCaterpillars(segs []stages.Segment, sts []Station, routes map[string]gtfs.Route, frame geo.Frame) []CatBullet {
	// station markers in frame coords — chains keep clear of them
	var stPts []geo.Pt
	for i := range sts {
		for _, m := range sts[i].Markers {
			stPts = append(stPts, frame.ToXY(m.LL))
		}
	}
	clearOfStations := func(p geo.Pt) bool {
		for _, q := range stPts {
			dx, dy := p.X-q.X, p.Y-q.Y
			if dx*dx+dy*dy < catStationClearM*catStationClearM {
				return false
			}
		}
		return true
	}

	// sibling ribbons share EXACT geometry (terminal cuts synchronize
	// them) — group by direction-independent signature per band
	sig := func(l *geo.Line) uint64 {
		a, b := l.Pts[0], l.Pts[len(l.Pts)-1]
		if b.X < a.X || (b.X == a.X && b.Y < a.Y) {
			a, b = b, a
		}
		h := fnv.New64a()
		w := func(v float64) {
			u := uint64(int64(math.Round(v * 10)))
			var buf [8]byte
			for i := 0; i < 8; i++ {
				buf[i] = byte(u >> (8 * i))
			}
			h.Write(buf[:])
		}
		w(a.X)
		w(a.Y)
		w(b.X)
		w(b.Y)
		w(l.Len())
		w(float64(len(l.Pts)))
		return h.Sum64()
	}
	type gkey struct {
		band int
		sig  uint64
	}
	groups := map[gkey][]int{}
	for i := range segs {
		s := &segs[i]
		if s.Kind != "steady" || s.Line == nil || len(s.Line.Pts) < 2 {
			continue
		}
		if s.BandMin != 14 && s.BandMin != 15 {
			continue
		}
		switch s.Mode {
		case "bus", "regional", "ferry":
			continue // a commuter branch's identity is its agency, not a word pill
		}
		groups[gkey{s.BandMin, sig(s.Line)}] = append(groups[gkey{s.BandMin, sig(s.Line)}], i)
	}

	// deterministic iteration: sort group keys
	keys := make([]gkey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].band != keys[j].band {
			return keys[i].band < keys[j].band
		}
		return keys[i].sig < keys[j].sig
	})

	var props []catProposal
	for _, k := range keys {
		members := groups[k]
		ref := segs[members[0]].Line
		refLen := ref.Len()
		if refLen < 2*catEndClearM+100 {
			continue
		}
		// roster: one bullet per route, lateral = its ribbon's offset in
		// the REFERENCE line's travel frame (reversed siblings flip sign)
		type bullet struct {
			route, label, hex, acts, mode string
			lat                           float64
		}
		var roster []bullet
		labelSet := map[string]bool{}
		for _, mi := range members {
			s := &segs[mi]
			rev := s.Line.Pts[0].Dist(ref.Pts[0]) > 1.0
			lat := s.OffsetPx
			if rev {
				lat = -lat
			}
			for ri, rid := range s.Routes {
				rt, ok := routes[rid]
				if !ok {
					continue
				}
				label := displayLabel(rt)
				if label == "" || len(label) > 8 {
					continue
				}
				acts := ""
				if ri < len(s.Acts) {
					acts = s.Acts[ri]
				}
				// a zero-hours route never rides this piece (the E kept on
				// the union past its WTC terminal for geometry bookkeeping)
				// — its bullet here would advertise a phantom. Unknown
				// masks keep the benefit of the doubt.
				if m, ok := gtfs.ParseMask168(acts); ok && m.Empty() {
					continue
				}
				roster = append(roster, bullet{route: rid, label: label, hex: routeHex(rt), acts: acts, mode: s.Mode, lat: lat})
				labelSet[label] = true
			}
		}
		// fold express variants the set already shows (FX beside F)
		kept := roster[:0]
		for _, b := range roster {
			if strings.HasSuffix(b.label, "X") && labelSet[strings.TrimSuffix(b.label, "X")] {
				continue
			}
			kept = append(kept, b)
		}
		roster = kept
		if len(roster) == 0 || len(roster) > 10 {
			continue
		}
		sort.SliceStable(roster, func(i, j int) bool { return roster[i].lat < roster[j].lat })

		// chain window in meters at this band's native zoom — the whole
		// chain must fit inside the straightness window
		mPerPx := 156543.03 * math.Cos(frame.ToLL(ref.Pts[0]).Lat*math.Pi/180) / math.Pow(2, float64(k.band)+8)
		chainM := float64(len(roster)) * catPitchAlongPx * mPerPx
		win := math.Max(70, chainM*0.7+30)
		spacing := 700.0
		if k.band == 14 {
			spacing = 1400.0
		}

		// anchors CENTER between the stops they sit between: boundaries
		// are the segment ends plus every station marker projected onto
		// this bundle; each long-enough gap contributes its midpoint.
		// Riding mid-gap keeps chains as far from stop labels as the
		// corridor allows.
		bounds := []float64{0, refLen}
		for _, q := range stPts {
			if arc, d, ok := ref.ProjectArcCapped(q, 40); ok && d <= 40 {
				bounds = append(bounds, arc)
			}
		}
		sort.Float64s(bounds)

		straightAt := func(arc float64) bool {
			p := ref.AtArc(arc)
			t1 := ref.AtArc(arc - win).Sub(p).Unit()
			t2 := ref.AtArc(arc + win).Sub(p).Unit()
			return t1.Dot(t2) < -math.Cos(catStraightDeg*math.Pi/180)
		}

		lastAt := math.Inf(-1)
		for gi := 1; gi < len(bounds); gi++ {
			lo, hi := bounds[gi-1], bounds[gi]
			// the gap must hold the chain plus clearances on both sides
			lo = math.Max(lo+catStationClearM, catEndClearM)
			hi = math.Min(hi-catStationClearM, refLen-catEndClearM)
			if hi-lo < chainM+40 {
				continue
			}
			mid := (lo + hi) / 2
			if mid-lastAt < spacing {
				continue
			}
			// midpoint first; if the corridor bends there, walk outward
			// for the nearest straight spot still inside the gap
			arc := math.NaN()
			for d := 0.0; d <= (hi-lo)/2; d += 40 {
				if mid+d <= hi && straightAt(mid+d) && clearOfStations(ref.AtArc(mid+d)) {
					arc = mid + d
					break
				}
				if d > 0 && mid-d >= lo && straightAt(mid-d) && clearOfStations(ref.AtArc(mid-d)) {
					arc = mid - d
					break
				}
			}
			if math.IsNaN(arc) {
				continue
			}
			// tangent in map-aligned px frame (+x east, +y south); frame
			// XY is +x east, +y NORTH, so y flips
			tf := ref.AtArc(arc + 6).Sub(ref.AtArc(arc - 6)).Unit()
			tm := geo.Pt{X: tf.X, Y: -tf.Y}
			props = append(props, catProposal{
				band: k.band, pt: ref.AtArc(arc), tm: tm,
				roster: append([]catEntry(nil), func() []catEntry {
					es := make([]catEntry, len(roster))
					for i, b := range roster {
						es[i] = catEntry{route: b.route, label: b.label, hex: b.hex, acts: b.acts, mode: b.mode, lat: b.lat}
					}
					return es
				}()...),
			})
			lastAt = arc
		}
	}

	// MERGE co-located proposals: sibling ribbons whose segment extents
	// differ (the corridor-extent mismatch — the M's cuts land elsewhere
	// than the J/Z's) form separate groups, and their independently
	// centered chains land on top of each other as a clump. Proposals of
	// one band within a chain-length of each other fuse into ONE chain:
	// laterals transplant into the host's travel frame (opposite-heading
	// donors flip sign), the roster re-sorts, and the stagger re-centers.
	sort.SliceStable(props, func(i, j int) bool {
		if props[i].band != props[j].band {
			return props[i].band < props[j].band
		}
		if props[i].pt.X != props[j].pt.X {
			return props[i].pt.X < props[j].pt.X
		}
		return props[i].pt.Y < props[j].pt.Y
	})
	used := make([]bool, len(props))
	var out []CatBullet
	group := 0
	for i := range props {
		if used[i] {
			continue
		}
		host := props[i]
		mergeR := 60.0 + float64(len(host.roster))*20
		for j := i + 1; j < len(props); j++ {
			if used[j] || props[j].band != host.band {
				continue
			}
			if props[j].pt.Dist(host.pt) > mergeR {
				continue
			}
			flip := host.tm.Dot(props[j].tm) < 0
			for _, e := range props[j].roster {
				if flip {
					e.lat = -e.lat
				}
				dup := false
				for _, h := range host.roster {
					if h.route == e.route {
						dup = true
						break
					}
				}
				if !dup {
					host.roster = append(host.roster, e)
				}
			}
			used[j] = true
		}
		sort.SliceStable(host.roster, func(a, b int) bool { return host.roster[a].lat < host.roster[b].lat })

		rt := geo.Pt{X: -host.tm.Y, Y: host.tm.X}
		ll := frame.ToLL(host.pt)
		for bi, b := range host.roster {
			along := (float64(bi) - float64(len(host.roster)-1)/2) * catPitchAlongPx
			out = append(out, CatBullet{
				LL:    ll,
				Route: b.route,
				Label: b.label,
				Hex:   b.hex,
				Acts:  b.acts,
				Mode:  b.mode,
				Vec:   [2]float64{round1(host.tm.X*along + rt.X*b.lat), round1(host.tm.Y*along + rt.Y*b.lat)},
				Band:  host.band,
				Group: group,
			})
		}
		group++
	}
	return out
}

type catEntry struct {
	route, label, hex, acts, mode string
	lat                           float64
}

type catProposal struct {
	band   int
	pt     geo.Pt
	tm     geo.Pt
	roster []catEntry
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
