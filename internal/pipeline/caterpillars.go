package pipeline

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"

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
	Acts  string     // hex Mask168 for this route on this segment ("" unknown)
	Mode  string     // class name, for class toggles
	Vec   [2]float64 // map-aligned px at z14+: +x east, +y south
	// VecLo is the same vector with the LATERAL half scaled to 0.5, which
	// is where zoomScaledOffset puts the ribbons at z11. The viewer
	// interpolates vec between the two across z11–z14 so a bullet tracks
	// its ribbon as the bundle narrows; the along-track stagger is
	// identical in both, because bullet spacing is bullet-sized and must
	// not shrink.
	VecLo [2]float64
	Band  int // BandMin of the bundle (client zoom gate)
	Group int // chain id — bullets of one anchor share it
	// Text marks a WORD label rather than a bullet: rendered as text
	// running along the ribbon, the way a road map labels a highway,
	// because "Orange Line" in a circle is not a bullet, it is a blob.
	Text bool
	// Ang is the label's screen rotation in degrees for Text mode, kept
	// upright (never upside down).
	Ang float64
	// Shape is the bullet outline (circle unless curated otherwise).
	Shape string
}

// bulletLike decides how a route's label is drawn. Systems split cleanly
// into two families and the split is legible in the labels themselves:
// the MTA, WMATA and CDMX put ONE OR TWO characters in a 1:1 bullet,
// while the CTA, Amtrak and most commuter operators use words that only
// read as text along the line.
//
// Length decides, with two refinements. One or two characters is always a
// bullet — that is what "1:1 aspect" means.
//
// A short label CONTAINING A DIGIT is a route CODE, not a word, and codes
// belong in bullets however long they run: Toronto prints its streetcars
// as "501" in a roundel, Barcelona sets "L10N", NYC "M14A". Words of the
// same length do not — "Red" is a word. Digits are the language-agnostic
// tell, and without it Toronto split half its network into running text.
//
// THREE characters with no digit is genuinely ambiguous ("SIR", "Red")
// and is settled by the median label length of the routes around it: in a
// system of single letters it is a bullet, in Chicago's system of colour
// words it is text.
//
// This answers the MIXED systems too, which a per-system rule cannot —
// Boston keeps B, C, D, E as bullets while "Red Line" beside them becomes
// text.
func bulletLike(label string, median int) bool {
	r := []rune(label)
	n := len(r)
	if n <= 2 {
		return true
	}
	digit := false
	for _, c := range r {
		if unicode.IsDigit(c) {
			digit = true
			break
		}
	}
	if digit && n <= 4 {
		return true
	}
	if n >= 4 {
		return false
	}
	return median <= 2
}

// medianLabelLen is the middle display-label length of the routes that
// can carry a caterpillar — the system's own answer to "are we a bullet
// system?".
func medianLabelLen(segs []stages.Segment, routes map[string]gtfs.Route) int {
	seen := map[string]bool{}
	var lens []int
	for i := range segs {
		s := &segs[i]
		switch s.Mode {
		case "bus", "regional", "ferry":
			continue
		}
		for _, rid := range s.Routes {
			rt, ok := routes[rid]
			if !ok || seen[rid] {
				continue
			}
			seen[rid] = true
			if l := displayLabel(rt); l != "" {
				lens = append(lens, len([]rune(l)))
			}
		}
	}
	if len(lens) == 0 {
		return 1
	}
	sort.Ints(lens)
	return lens[len(lens)/2]
}

const (
	catPitchAlongPx  = 17.0 // circular bullet: diameter 14 + 3 gap
	catBulletH       = 14.0
	catBulletGapPx   = 3.0
	catStraightDeg   = 7.0 // max heading change across the chain window
	catEndClearM     = 180.0
	catStationClearM = 160.0
	// how far ACROSS the ribbon two proposals may sit and still be the
	// same bundle. Siblings share exact geometry, so this only has to
	// absorb the metre-scale drift of independently centred chains — it
	// must stay far below the spacing between neighbouring corridors.
	catMergeCrossM = 15.0
)

// bulletWidthPx mirrors the viewer's bulletCanvas: a circle for 1–2
// character labels, a rounded word pill otherwise. The chain staggers by
// the WIDEST bullet in its roster, because a fixed 17 px pitch sized for
// circles overlaps word pills badly — Chicago's "Purple" beside "Red" is
// two 40 px pills 17 px apart. Systems whose labels are all one or two
// characters get exactly the old pitch, so nothing moves in NYC.
func bulletWidthPx(label string) float64 {
	n := len([]rune(label))
	if n <= 2 {
		return catBulletH
	}
	// 9.5px semibold system-ui averages ~5.6px of advance per character;
	// the viewer adds 9px of horizontal padding to the measured text.
	return 5.6*float64(n) + 9
}

// catPitchFor is the along-track stagger for one roster: wide enough that
// the widest bullet clears its neighbour.
func catPitchFor(labels []string) float64 {
	p := catPitchAlongPx
	for _, l := range labels {
		if w := bulletWidthPx(l) + catBulletGapPx; w > p {
			p = w
		}
	}
	return p
}

// BuildCaterpillars picks anchors and emits bullet points. segs are the
// final drawn segments (post terminal cuts); sts the snapped stations.
func BuildCaterpillars(segs []stages.Segment, sts []Station, routes map[string]gtfs.Route, frame geo.Frame) []CatBullet {
	median := medianLabelLen(segs, routes)
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
		switch s.BandMin {
		case 0, 13, 14, 15:
		default:
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
			route, label, hex, acts, mode, shape string
			lat                                  float64
			text                                 bool
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
				if label == "" {
					continue
				}
				asText := !bulletLike(label, median)
				// a bullet has to fit its glyph; a word label has no such
				// limit because it is set as running text
				if !asText && len([]rune(label)) > 8 {
					continue
				}
				if asText && len([]rune(label)) > 24 {
					continue
				}
				shp := shapeOf(rt)
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
				roster = append(roster, bullet{route: rid, label: label, hex: routeHex(rt),
					acts: acts, mode: s.Mode, lat: lat, text: asText, shape: shp})
				labelSet[label] = true
			}
		}
		// fold express variants the set already shows (FX beside F), and
		// fold repeats of one line: a feed that files a route per service
		// variant (Wiener Linien tram 11 is a dozen route_ids) would
		// otherwise stack a dozen identical bullets in one chain.
		kept := roster[:0]
		shown := map[string]bool{}
		for _, b := range roster {
			if strings.HasSuffix(b.label, "X") && labelSet[strings.TrimSuffix(b.label, "X")] {
				continue
			}
			if shown[b.label+"|"+b.hex] {
				continue
			}
			shown[b.label+"|"+b.hex] = true
			kept = append(kept, b)
		}
		roster = kept
		if len(roster) == 0 || len(roster) > 10 {
			continue
		}
		sort.SliceStable(roster, func(i, j int) bool { return roster[i].lat < roster[j].lat })

		// chain window in meters at this band's native zoom — the whole
		// chain must fit inside the straightness window
		// band 0 draws below z13; size its geometry at z12, the lowest
		// zoom the chains are shown at
		bandZoom := float64(k.band)
		if k.band == 0 {
			bandZoom = 12
		}
		// 156543.03 is already the equatorial circumference over 256, so
		// the divisor is 2^zoom — an extra +8 here made every metre-based
		// gate 256x too small, which silently disabled them: a four-bullet
		// chain measured 1 m instead of 246 m, so the "does this chain fit
		// the gap" test was really "is the gap wider than 40 m", and the
		// straightness window collapsed to its 70 m floor.
		mPerPx := 156543.03 * math.Cos(frame.ToLL(ref.Pts[0]).Lat*math.Pi/180) / math.Pow(2, bandZoom)
		// ALONG-TRACK extent of what is actually drawn at one anchor.
		// Bullets chain, so they span the whole roster; word labels CYCLE
		// one per anchor, so they span exactly one label however many
		// lines share the bundle. Counting the full roster for text made
		// a 4-line trunk demand a 690 m gap and 485 m of straightness to
		// place a 162 m label, which is why tightening the spacing could
		// not make them any more frequent — the gates, not the spacing,
		// were binding.
		var bulletLabs, textLabs []string
		for _, b := range roster {
			if b.text {
				textLabs = append(textLabs, b.label)
			} else {
				bulletLabs = append(bulletLabs, b.label)
			}
		}
		extentPx := float64(len(bulletLabs)) * catPitchFor(bulletLabs)
		if len(textLabs) > 0 {
			extentPx = math.Max(extentPx, catPitchFor(textLabs))
		}
		chainM := extentPx * mPerPx
		// STRAIGHTNESS is a bullet constraint, not a label one. A chain of
		// discs strung along a bend reads as a broken necklace — the discs
		// stay upright while the line turns under them — but a word label
		// set at the local tangent sits on a gentle curve perfectly well,
		// which is what every road map does. Requiring it of text cost
		// placements for nothing.
		// ...and it is only a constraint for a MULTI-bullet chain. The
		// along-track stagger is a straight line in map space, so on a
		// bend the far bullets walk off the ribbon; a lone bullet has no
		// stagger (along = 0) and sits on its lateral offset at the local
		// tangent, which is well defined on a curve.
		needStraight := len(bulletLabs) > 1
		win := math.Max(70, chainM*0.7+30)
		// a chain must not wallpaper the line: the further out the band
		// draws, the more ground each chain has to cover
		spacing := 320.0
		switch k.band {
		case 14:
			spacing = 640.0
		case 13:
			spacing = 1408.0
		case 0:
			spacing = 3200
		}
		// A bundle's word labels take one anchor each in turn, so a line
		// on a four-way trunk is only named every fourth anchor. Tighten
		// the spacing by the size of the rotation — up to a point, since
		// consecutive anchors still have to clear each other along the
		// track — so a shared corridor names its lines about as often as
		// a solo one does.
		if nText := func() int {
			n := 0
			for _, b := range roster {
				if b.text {
					n++
				}
			}
			return n
		}(); nText > 1 {
			spacing /= math.Min(float64(nText), 2)
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
		seq := 0
		for gi := 1; gi < len(bounds); gi++ {
			lo, hi := bounds[gi-1], bounds[gi]
			// the gap must hold the chain plus clearances on both sides
			lo = math.Max(lo+catStationClearM, catEndClearM)
			hi = math.Min(hi-catStationClearM, refLen-catEndClearM)
			if hi-lo < chainM+40 {
				continue
			}
			// How many anchors this gap carries. A BULLET chain takes one,
			// centred — a necklace of discs repeated down a single block
			// reads as clutter, and centring keeps it as far from the stop
			// labels at either end as the corridor allows. WORD labels are
			// different: they are how a rider identifies the line they are
			// looking at, so a long gap should name it more than once, the
			// way a road map repeats a highway shield. One chain per gap
			// was the real ceiling on their frequency — the count tracked
			// the number of station gaps and no amount of spacing moved it.
			slots := 1
			if !needStraight {
				if n := int((hi - lo) / spacing); n > slots {
					slots = n
				}
				if slots > 3 {
					slots = 3
				}
			}
			for si := 0; si < slots; si++ {
				// evenly spaced inside the gap, symmetric about its centre
				mid := lo + (hi-lo)*(float64(si)+0.5)/float64(slots)
				if mid-lastAt < spacing {
					continue
				}
				// midpoint first; if the corridor bends there, walk outward
				// for the nearest straight spot still inside the gap
				arc := math.NaN()
				for d := 0.0; d <= (hi-lo)/float64(2*slots); d += 40 {
					if mid+d <= hi && (!needStraight || straightAt(mid+d)) && clearOfStations(ref.AtArc(mid+d)) {
						arc = mid + d
						break
					}
					if d > 0 && mid-d >= lo && (!needStraight || straightAt(mid-d)) && clearOfStations(ref.AtArc(mid-d)) {
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
					band: k.band, pt: ref.AtArc(arc), tm: tm, seq: seq,
					roster: append([]catEntry(nil), func() []catEntry {
						es := make([]catEntry, len(roster))
						for i, b := range roster {
							es[i] = catEntry{route: b.route, label: b.label, hex: b.hex, acts: b.acts,
								mode: b.mode, lat: b.lat, text: b.text, shape: b.shape}
						}
						return es
					}()...),
				})
				lastAt = arc
				seq++
			}
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
		// the corridor-extent mismatch this merge exists for is ALONG the
		// ribbon: two groups of the same bundle centre their chains at
		// different arcs because their segments were cut at different
		// places. ACROSS the ribbon there is no mismatch to fix — sibling
		// ribbons share exact geometry (that is what the signature groups
		// on), so same-bundle proposals sit within metres of each other.
		//
		// A plain radius conflates the two. At Brooklyn Bridge the J/Z
		// runs about 100 m from the 4·5·6, inside the radius, so its chain
		// was absorbed: the J and Z bullets drew stacked on the GREEN
		// ribbon, because a donor's laterals are offsets within its own
		// bundle and carry no memory of the 100 m between the corridors.
		// Split the separation into its components and bar the crossing
		// one.
		tanXY := geo.Pt{X: host.tm.X, Y: -host.tm.Y}
		for j := i + 1; j < len(props); j++ {
			if used[j] || props[j].band != host.band {
				continue
			}
			d := props[j].pt.Sub(host.pt)
			if math.Abs(d.Dot(tanXY)) > mergeR {
				continue
			}
			if math.Abs(d.X*-tanXY.Y+d.Y*tanXY.X) > catMergeCrossM {
				continue
			}
			// a corridor that merely crosses this one is not this bundle
			if math.Abs(host.tm.Dot(props[j].tm)) < 0.85 {
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
		// pitch follows the MERGED roster — the widest bullet sets it
		hl := make([]string, len(host.roster))
		for i, b := range host.roster {
			hl[i] = b.label
		}
		pitch := catPitchFor(hl)
		// Text labels run ALONG the ribbon, so they need no along-track
		// stagger — each sits at its own lateral offset and reads like a
		// road name. Only the bullets form a chain.
		// bullets chain; text takes ONE slot per anchor, cycled by seq so
		// every line of the bundle gets labelled as the corridor runs on
		nb := 0
		var texts []catEntry
		for _, b := range host.roster {
			if b.text {
				texts = append(texts, b)
			} else {
				nb++
			}
		}
		var pick *catEntry
		if len(texts) > 0 {
			pick = &texts[host.seq%len(texts)]
		}
		// screen rotation of the tangent, kept upright: text that reads
		// bottom-up is worse than text that reads against the travel
		// direction, and a rider only needs to know WHICH line this is
		ang := math.Atan2(host.tm.X, -host.tm.Y)*180/math.Pi - 90
		for ang > 90 {
			ang -= 180
		}
		for ang <= -90 {
			ang += 180
		}
		bi := -1
		for _, b := range host.roster {
			if b.text && (pick == nil || b.route != pick.route) {
				continue // another anchor along this corridor carries it
			}
			along := 0.0
			if !b.text {
				bi++
				along = (float64(bi) - float64(nb-1)/2) * pitch
			}
			out = append(out, CatBullet{
				LL:    ll,
				Route: b.route,
				Label: b.label,
				Hex:   b.hex,
				Acts:  b.acts,
				Mode:  b.mode,
				Vec:   [2]float64{round1(host.tm.X*along + rt.X*b.lat), round1(host.tm.Y*along + rt.Y*b.lat)},
				VecLo: [2]float64{round1(host.tm.X*along + rt.X*b.lat*0.5), round1(host.tm.Y*along + rt.Y*b.lat*0.5)},
				Band:  host.band,
				Group: group,
				Text:  b.text,
				Ang:   round1(ang),
				Shape: b.shape,
			})
		}
		group++
	}
	return out
}

type catEntry struct {
	route, label, hex, acts, mode, shape string
	lat                                  float64
	text                                 bool
}

type catProposal struct {
	band int
	pt   geo.Pt
	tm   geo.Pt
	// seq counts this anchor along its corridor. Text labels CYCLE on it:
	// a bundle's word labels cannot all sit at one anchor (they are ~60 px
	// long and only ~6 px apart laterally, so four of them land on top of
	// each other), and they cannot stagger along the track either, because
	// each label already occupies the along-track direction. So each
	// anchor labels ONE line of the bundle and the next anchor labels the
	// next — which is exactly how a road map handles concurrent highways.
	seq    int
	roster []catEntry
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
