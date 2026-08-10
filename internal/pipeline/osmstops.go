package pipeline

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// OSM station matching: GTFS stop names are operator bookkeeping — CATS
// files "Mint St CityLYNX", MBTA files "Boylston - Outbound", and half the
// world's feeds SHOUT. OSM carries what is on the sign. This stage pairs
// each drawn station with the OSM stop that is really the same place, so
// the map can show the sign's name and so a downstream consumer
// (parchment) can look the station up by its OSM id.
//
// PROXIMITY IS THE PRIMARY TELL. Two transit stops of a compatible class
// a few metres apart are the same stop; no name evidence is needed and
// none is demanded. Charlotte's feed calls one "Charlotte Transportation
// Center" and OSM calls it "CTC/Arena" — no token in common, 2 m apart,
// obviously the same place. Name similarity is the SECONDARY signal: it
// buys range (the further apart two candidates sit, the more the names
// have to agree) and it breaks ties when several stops crowd one corner.
//
// NOTHING HERE IS LANGUAGE-SPECIFIC. There is no stopword list, no
// "Station"/"Estación"/"Bahnhof" table, no street-type table — an earlier
// draft had all three and they were quietly English-and-Spanish-shaped,
// which is the city-specific code this repo does not write. Instead the
// weight of a word is IDF measured over THIS city's own stop names: a
// token most names carry cannot be what distinguishes one from another,
// whatever language it is in and whether it is "Station", "CityLYNX",
// "Straße" or "駅". Rare proper nouns dominate the comparison for free.
//
// Class still gates: a light-rail station may not match a bus
// stop_position sharing its corner, however close. Matching is one-to-one
// and greedy by score, so two stops crowding a corner are claimed by the
// two stations that fit them best. A station with no candidate keeps its
// feed name and gets no id — the common case for stops OSM has not mapped.

// OSMStop is one named transit stop from the OSM extract.
type OSMStop struct {
	ID      string // "node/12345" — the id parchment looks up
	Name    string
	LL      geo.LL
	Classes map[string]bool // portolan class names this stop can serve
	toks    []string
}

// osmStopClasses reads the class of an OSM stop from its tags. A stop can
// serve several (an interchange tagged subway=yes + train=yes).
func osmStopClasses(p map[string]any) map[string]bool {
	s := func(k string) string {
		v, _ := p[k].(string)
		return strings.ToLower(strings.TrimSpace(v))
	}
	yes := func(k string) bool { v := s(k); return v == "yes" || v == "true" }
	cl := map[string]bool{}
	switch s("railway") {
	case "tram_stop":
		cl["tram"] = true
	case "halt":
		cl["regional"] = true
	}
	switch s("station") {
	case "subway":
		cl["metro"] = true
	case "light_rail":
		cl["tram"] = true
		cl["metro"] = true // light rail is drawn either way across feeds
	case "train":
		cl["regional"] = true
	case "monorail":
		cl["monorail"] = true
	case "funicular":
		cl["funicular"] = true
	}
	if yes("subway") {
		cl["metro"] = true
	}
	if yes("light_rail") {
		cl["tram"] = true
		cl["metro"] = true
	}
	if yes("train") {
		cl["regional"] = true
	}
	if yes("tram") {
		cl["tram"] = true
	}
	if yes("monorail") {
		cl["monorail"] = true
	}
	if yes("funicular") {
		cl["funicular"] = true
	}
	if s("aerialway") == "station" {
		cl["aerial"] = true
		cl["cable"] = true
	}
	if s("amenity") == "ferry_terminal" {
		cl["ferry"] = true
	}
	// A bare railway=station with no sub-tag is still rail of some kind;
	// let it serve the rail classes rather than dropping a real station.
	if len(cl) == 0 && s("railway") == "station" {
		cl["metro"], cl["regional"], cl["tram"], cl["monorail"] = true, true, true, true
	}
	// public_transport=station with nothing else is usually a bus station.
	if len(cl) == 0 {
		cl["bus"] = true
	}
	return cl
}

// LoadOSMStops reads the stops extract. A missing file is not an error —
// the extract is opt-in per city, and its absence simply means no
// matching happens.
func LoadOSMStops(path string) ([]OSMStop, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var fc struct {
		Features []struct {
			ID       string         `json:"id"`
			Props    map[string]any `json:"properties"`
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, err
	}
	out := make([]OSMStop, 0, len(fc.Features))
	for _, f := range fc.Features {
		name, _ := f.Props["name"].(string)
		if strings.TrimSpace(name) == "" || len(f.Geometry.Coordinates) < 2 {
			continue
		}
		out = append(out, OSMStop{
			ID: f.ID, Name: name,
			LL:      geo.LL{Lon: f.Geometry.Coordinates[0], Lat: f.Geometry.Coordinates[1]},
			Classes: osmStopClasses(f.Props),
			toks:    nameTokens(name),
		})
	}
	return out, nil
}

// nameTokens splits a stop name into comparable words: case folded,
// diacritics reduced to their base letter, and every non-alphanumeric
// rune treated as a separator. No words are removed — which ones matter
// is decided by IDF, per city, below.
//
// Scripts without spaces (CJK) come through as one token per run, which
// still compares exactly; that is the honest behaviour for a matcher with
// no word segmenter, and proximity carries those cities regardless.
func nameTokens(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(foldAccent(r))
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Fields(b.String())
}

// accentFold reduces the diacritics portolan's cities actually use to a
// base letter, so "Estació" and "Estacio" compare equal. This is Unicode
// normalisation, not a word list — it says nothing about which words
// matter. An explicit map, not two parallel strings: the parallel version
// was off by one and silently folded ž→s and ğ→l, which is the kind of
// wrong that only shows up as a station renamed to the wrong thing.
var accentFold = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ä': 'a', 'ã': 'a', 'å': 'a', 'ā': 'a', 'ă': 'a', 'ą': 'a',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e', 'ĕ': 'e', 'ė': 'e', 'ę': 'e', 'ě': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i', 'ĩ': 'i', 'ī': 'i', 'ĭ': 'i', 'į': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o', 'ø': 'o', 'ō': 'o', 'ŏ': 'o', 'ő': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u', 'ũ': 'u', 'ū': 'u', 'ŭ': 'u', 'ů': 'u', 'ű': 'u',
	'ý': 'y', 'ÿ': 'y',
	'ç': 'c', 'ć': 'c', 'ĉ': 'c', 'ċ': 'c', 'č': 'c',
	'ñ': 'n', 'ń': 'n', 'ņ': 'n', 'ň': 'n',
	'š': 's', 'ś': 's', 'ş': 's', 'ș': 's',
	'ž': 'z', 'ź': 'z', 'ż': 'z',
	'ď': 'd', 'đ': 'd', 'ł': 'l', 'ľ': 'l', 'ğ': 'g',
	'ť': 't', 'ț': 't', 'ř': 'r', 'ŕ': 'r',
	'ß': 's', 'æ': 'a', 'œ': 'o', 'þ': 't', 'ð': 'd',
}

func foldAccent(r rune) rune {
	if f, ok := accentFold[r]; ok {
		return f
	}
	return r
}

// idfOf measures how much each token discriminates, from the names this
// city actually uses. log(1 + N/df): a token on every name scores near
// zero, a token on one name scores high. This is what replaces every
// hand-written stopword list, and it is why the matcher needs no idea
// what language it is reading.
//
// Each corpus is measured SEPARATELY and a token takes its LOWEST score
// across them, because a word only has to be uninformative on one side to
// be uninformative. Pooling hid exactly that: "CityLYNX" is on 10 of
// Charlotte's 43 feed names and none of OSM's 209, so pooled it looked
// like a rare word (1 name in 25) instead of the operator brand it is
// (1 in 4). A token missing from a corpus is scored as if it appeared
// half a time there, which is high, so the minimum comes from the corpus
// that actually uses it.
func idfOf(corpora ...[][]string) map[string]float64 {
	seenAll := map[string]bool{}
	type stat struct {
		n  int
		df map[string]int
	}
	stats := make([]stat, 0, len(corpora))
	for _, c := range corpora {
		st := stat{df: map[string]int{}}
		for _, toks := range c {
			st.n++
			seen := map[string]bool{}
			for _, t := range toks {
				if !seen[t] {
					seen[t] = true
					st.df[t]++
					seenAll[t] = true
				}
			}
		}
		stats = append(stats, st)
	}
	idf := make(map[string]float64, len(seenAll))
	for t := range seenAll {
		best := math.Inf(1)
		for _, st := range stats {
			if st.n == 0 {
				continue
			}
			d := float64(st.df[t])
			if d == 0 {
				d = 0.5 // absent here: expensive, so the min comes elsewhere
			}
			if v := math.Log(1 + float64(st.n)/d); v < best {
				best = v
			}
		}
		if math.IsInf(best, 1) {
			best = 0
		}
		idf[t] = best
	}
	return idf
}

// nameSim is IDF-weighted containment: the shared weight over the lighter
// side's total. Containment rather than Jaccard because abbreviation is
// the norm — "Mint" against "Mint Street" should read as agreement, not
// as half a disagreement — and the weighting means the words the city
// repeats everywhere ("Street", "CityLYNX") contribute almost nothing to
// either side of that ratio.
func nameSim(a, b []string, idf map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	wa, wb := 0.0, 0.0
	setA := map[string]bool{}
	for _, t := range a {
		if !setA[t] {
			setA[t] = true
			wa += idf[t]
		}
	}
	setB := map[string]bool{}
	shared := 0.0
	for _, t := range b {
		if !setB[t] {
			setB[t] = true
			wb += idf[t]
			if setA[t] {
				shared += idf[t]
			}
		}
	}
	lighter := math.Min(wa, wb)
	if lighter <= 0 {
		return 0
	}
	return math.Min(1, shared/lighter)
}

// StopMatch is one accepted pairing, for logging and for the report.
type StopMatch struct {
	Station  int // index into the stations slice
	OSM      string
	FeedName string
	OSMName  string
	Dist     float64
	Sim      float64
	// LostInfo: when the OSM name is a strict token-subset of the feed's,
	// the share of the feed name's IDF weight that adopting OSM would
	// throw away. Near zero when OSM merely drops words the whole city
	// repeats ("Station", "Southbound Platform"); high when it drops a
	// rare proper noun the feed was carrying ("…y Poder Judicial CDMX").
	LostInfo float64
}

// lostInfo measures what adopting the OSM name would discard. Zero unless
// OSM's tokens are contained in the feed's — a name that ADDS words is
// not losing any.
//
// A dropped word only counts if OSM's vocabulary for this city USES that
// word somewhere. That one test separates the two cases that otherwise
// look identical to IDF: "CityLYNX", "Av" and "Ln" appear in none of
// Charlotte's 209 OSM names, so they are feed-side bookkeeping — a brand
// and an abbreviation style — and dropping them costs nothing. "Poder",
// "Judicial" and "Santa María" do appear in Mexico City's OSM names, so
// they are real vocabulary and dropping them is real loss. No list, no
// language: just whether the other source ever says the word.
func lostInfo(feed, osm []string, idf map[string]float64, osmVocab map[string]bool) float64 {
	fset := map[string]bool{}
	for _, t := range feed {
		fset[t] = true
	}
	for _, t := range osm {
		if !fset[t] {
			return 0 // OSM says something the feed did not
		}
	}
	oset := map[string]bool{}
	for _, t := range osm {
		oset[t] = true
	}
	total, dropped := 0.0, 0.0
	for t := range fset {
		total += idf[t]
		if !oset[t] && osmVocab[t] {
			dropped += idf[t]
		}
	}
	if total <= 0 {
		return 0
	}
	return dropped / total
}

const (
	// osmMatchRadiusM is the outer limit: past this, two stops are not
	// the same stop no matter how alike the names.
	osmMatchRadiusM = 250.0
	// osmSureM is close enough that proximity alone settles it. A
	// compatible-class transit stop this near IS the stop.
	osmSureM = 45.0
	// osmSimAtRange is the name agreement demanded at the outer limit;
	// the requirement ramps from zero at osmSureM to this at the radius.
	osmSimAtRange = 0.6
)

// needSim is the secondary signal's price: nothing while proximity is
// decisive, rising to osmSimAtRange as the candidate drifts out to the
// radius. This is the whole "proximity primary, name secondary" rule.
func needSim(d float64) float64 {
	if d <= osmSureM {
		return 0
	}
	t := (d - osmSureM) / (osmMatchRadiusM - osmSureM)
	return t * osmSimAtRange
}

// MatchOSMStops pairs stations with OSM stops and returns the accepted
// matches, best-first. It does not mutate the stations — the caller
// decides whether a match renames anything (ApplyOSMStopMatches).
func MatchOSMStops(sts []Station, stops []OSMStop, frame geo.Frame) []StopMatch {
	if len(sts) == 0 || len(stops) == 0 {
		return nil
	}
	// project once; build the city's own IDF from both name corpora
	oxy := make([]geo.Pt, len(stops))
	ocorp := make([][]string, len(stops))
	for i := range stops {
		oxy[i] = frame.ToXY(stops[i].LL)
		ocorp[i] = stops[i].toks
	}
	stoks := make([][]string, len(sts))
	sxy := make([]geo.Pt, len(sts))
	for i := range sts {
		stoks[i] = nameTokens(sts[i].Name)
		sxy[i] = frame.ToXY(sts[i].LL)
	}
	idf := idfOf(stoks, ocorp)
	// every word OSM uses anywhere in this city
	osmVocab := map[string]bool{}
	for _, toks := range ocorp {
		for _, t := range toks {
			osmVocab[t] = true
		}
	}

	type cand struct {
		si, oi int
		score  float64
		dist   float64
		sim    float64
	}
	var cands []cand
	for si := range sts {
		classes := map[string]bool{}
		for _, m := range sts[si].Modes {
			classes[m] = true
		}
		for oi := range stops {
			d := sxy[si].Dist(oxy[oi])
			if d > osmMatchRadiusM {
				continue
			}
			// CLASS gates regardless of how close: a tram stop is not the
			// bus pole beside it. A station of unknown class is left alone.
			shared := false
			for c := range classes {
				if stops[oi].Classes[c] {
					shared = true
					break
				}
			}
			if !shared {
				continue
			}
			sim := nameSim(stoks[si], stops[oi].toks, idf)
			if sim < needSim(d) {
				continue
			}
			// proximity dominates the ranking too; the name only orders
			// candidates that are similarly close
			score := 0.65*(1-d/osmMatchRadiusM) + 0.35*sim
			cands = append(cands, cand{si, oi, score, d, sim})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].si != cands[j].si {
			return cands[i].si < cands[j].si
		}
		return cands[i].oi < cands[j].oi
	})
	usedS := make([]bool, len(sts))
	usedO := make([]bool, len(stops))
	var out []StopMatch
	for _, c := range cands {
		if usedS[c.si] || usedO[c.oi] {
			continue
		}
		usedS[c.si], usedO[c.oi] = true, true
		out = append(out, StopMatch{
			Station: c.si, OSM: stops[c.oi].ID,
			FeedName: sts[c.si].Name, OSMName: stops[c.oi].Name,
			Dist: c.dist, Sim: c.sim,
			LostInfo: lostInfo(stoks[c.si], stops[c.oi].toks, idf, osmVocab),
		})
	}
	return out
}

// sameNameFolded reports two spellings of one name — equal once case,
// punctuation and diacritics are folded away.
func sameNameFolded(a, b string) bool {
	return strings.Join(nameTokens(a), " ") == strings.Join(nameTokens(b), " ")
}

// shouty reports a name set entirely in capitals. Judged on the letters
// only, so "CTC/Arena" or "Line 1" are not caught by their punctuation
// and digits, and a name with no cased letters at all (CJK) never is.
func shouty(s string) bool {
	letters, upper := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	return letters > 1 && upper == letters
}

// accents counts the diacritics a spelling carries — the tiebreak when
// two spellings are otherwise identical.
func accents(s string) int {
	n := 0
	for _, r := range strings.ToLower(s) {
		if _, ok := accentFold[r]; ok {
			n++
		}
	}
	return n
}

// maxLostInfo is how much of a feed name's weight OSM may drop before the
// rename counts as losing information rather than tidying it. Set from
// evidence at both ends: Charlotte's "Hawthorne Ln & 8th St CityLYNX" →
// "Hawthorne & 8th" drops 0.42 of the weight and must pass, while Mexico
// City's "Niños Héroes y Poder Judicial CDMX" → "Niños Héroes" drops 0.71
// and must not.
const maxLostInfo = 0.45

// betterName decides whether to adopt the OSM spelling. A match means the
// two are the same place and OSM's name is the one on the sign, so the
// default is YES — that is the point of the stage. Two exceptions, both
// measured rather than listed:
//
//   - Typographic: where the two are the SAME name differing only in case
//     or diacritics, the better-set spelling wins. Richer diacritics
//     first — Mexico City keeps its feed's "Tláhuac" over OSM's "Tlahuac"
//     and takes OSM's "Peñón Viejo" over "Penón Viejo". Then CASE: a feed
//     that SHOUTS loses to a properly cased name, because all-caps is
//     worse typography for a map label and half the world's feeds shout.
//     Atlanta files "DOBBS PLAZA" where OSM has "Dobbs Plaza". A dead
//     heat keeps the feed's, so nothing churns for free.
//   - Informational: where OSM is a strict subset of the feed's words,
//     adopt it only if what it drops is weightless in this city. Toronto's
//     "King Station - Southbound Platform" → "King" drops words nearly
//     every Toronto stop carries, so it is tidying; Mexico City's "Niños
//     Héroes y Poder Judicial CDMX" → "Niños Héroes" drops a rare proper
//     noun, so it is loss. IDF tells the two apart in any language.
func betterName(feedName, osmName string, lost float64) bool {
	if osmName == "" || osmName == feedName {
		return false
	}
	if sameNameFolded(feedName, osmName) {
		if af, ao := accents(feedName), accents(osmName); af != ao {
			return ao > af
		}
		return shouty(feedName) && !shouty(osmName)
	}
	return lost <= maxLostInfo
}

// colocatedM is how far apart two stations can be and still be one
// place — a street-running pair on opposite kerbs, not two stations.
const colocatedM = 60.0

// colocated reports that every station already holding `name` stands
// essentially on top of one of the cohort's stations.
func colocated(sts []Station, cohort []int, name string) bool {
	for i := range sts {
		if sts[i].Name != name {
			continue
		}
		near := false
		for _, si := range cohort {
			if metersBetween(sts[i].LL, sts[si].LL) <= colocatedM {
				near = true
				break
			}
		}
		if !near {
			return false
		}
	}
	return true
}

// metersBetween is a local equirectangular distance — this stage has no
// projection frame at hand and the distances involved are tens of metres.
func metersBetween(a, b geo.LL) float64 {
	const mPerDeg = 111320.0
	dx := (a.Lon - b.Lon) * mPerDeg * math.Cos(a.Lat*math.Pi/180)
	dy := (a.Lat - b.Lat) * mPerDeg
	return math.Hypot(dx, dy)
}

// ApplyOSMStopMatches attaches the OSM id to every matched station and,
// when rename is set, adopts the OSM name. The id is attached either way:
// knowing WHICH OSM object a station is is useful even when the feed's
// own name is the one to draw.
//
// One law governs the renaming, and it is about collisions rather than
// about spelling: A RENAME MAY NOT MERGE TWO DISTINCT STATION NAMES INTO
// ONE, AND MUST APPLY UNIFORMLY TO EVERY STATION THAT SHARED THE OLD NAME.
// Both halves earn their keep on NYC. Without the first, OSM renames
// "Whitehall St-South Ferry" to "South Ferry" — which is a different
// station on the 1 — and swaps "Grand Central-42 St" with the Metro-North
// "Grand Central". Without the second, the three genuinely distinct "Fort
// Hamilton Pkwy" stations rename one at a time and the map shows one
// "Parkway" beside two "Pkwy".
func ApplyOSMStopMatches(sts []Station, ms []StopMatch, rename bool) (renamed int) {
	for _, m := range ms {
		sts[m.Station].OSMID = m.OSM
	}
	if !rename {
		return 0
	}
	// group the proposals by the OLD name; a group renames all-or-nothing
	type grp struct {
		idx    []int
		target string
		ok     bool
	}
	groups := map[string]*grp{}
	order := []string{}
	for _, m := range ms {
		if !betterName(sts[m.Station].Name, m.OSMName, m.LostInfo) {
			continue
		}
		old := sts[m.Station].Name
		g := groups[old]
		if g == nil {
			g = &grp{target: m.OSMName, ok: true}
			groups[old] = g
			order = append(order, old)
		}
		if g.target != m.OSMName {
			g.ok = false // the shared name disagrees about its new name
		}
		g.idx = append(g.idx, m.Station)
	}
	// every name currently on the map, and how many stations hold it
	held := map[string]int{}
	for i := range sts {
		held[sts[i].Name]++
	}
	for _, old := range order {
		g := groups[old]
		if !g.ok || g.target == old {
			continue
		}
		// the whole old-name cohort must be moving, or the ones left
		// behind would disagree with the ones that moved
		if len(g.idx) != held[old] {
			continue
		}
		// and the target must not already belong to a different PLACE.
		// Distance is what makes a collision a problem: NYC's "South
		// Ferry" is 200 m of separate station from "Whitehall St-South
		// Ferry", while Charlotte's two "Elizabeth & Hawthorne" stops are
		// the two directions of one corner, 4 m apart, which OSM itself
		// names twice. Sharing a name with something you are standing on
		// is not a collision.
		if held[g.target] > 0 && !colocated(sts, g.idx, g.target) {
			continue
		}
		for _, si := range g.idx {
			sts[si].Name = g.target
			renamed++
		}
		delete(held, old)
		held[g.target] = len(g.idx)
	}
	return renamed
}
