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
	// Station: this object is the station as a PLACE
	// (public_transport=station, railway=station, amenity=ferry_terminal),
	// rather than a stop_position — a point on the track where a train
	// halts. Both carry the station's name and sit metres apart, so
	// distance cannot tell them apart, but only one of them is somewhere a
	// rider can be sent.
	Station bool
	toks    []string
}

// osmStopIsStation reports whether an OSM object is the station itself
// rather than a stopping point on the rails inside it.
func osmStopIsStation(p map[string]any) bool {
	s := func(k string) string {
		v, _ := p[k].(string)
		return strings.ToLower(strings.TrimSpace(v))
	}
	switch s("public_transport") {
	case "station":
		return true
	case "stop_position", "platform":
		return false
	}
	return s("railway") == "station" || s("railway") == "halt" ||
		s("amenity") == "ferry_terminal" || s("aerialway") == "station"
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
	if s("amenity") == "ferry_terminal" || yes("ferry") {
		cl["ferry"] = true
	}
	if s("highway") == "bus_stop" || yes("bus") || yes("trolleybus") {
		cl["bus"] = true
	}
	// A bare railway=station (or the stop_position railway=stop) with no
	// sub-tag is still rail of some kind; let it serve the rail classes
	// rather than dropping a real station.
	if len(cl) == 0 && (s("railway") == "station" || s("railway") == "stop") {
		cl["metro"], cl["regional"], cl["tram"], cl["monorail"] = true, true, true, true
	}
	// public_transport=station with nothing else is usually a bus station.
	// A stop_position or platform with no mode tag says nothing about what
	// stops there, and guessing "bus" let a rail stop position claim a bus
	// stop on its corner: it serves no class, so it matches nothing.
	if len(cl) == 0 && s("public_transport") == "station" {
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
			Station: osmStopIsStation(f.Props),
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

// abbreviates reports one token written short for the other: a strict
// prefix of it, or a contraction (see contracts). This is the single structural fact about abbreviation
// that needs no word list and no language — "st" for "street", "wash"
// for "washington", "4" for "4th", "estac" for "estación" — and it is
// what lets a feed that shortens every word still agree with an OSM name
// that spells them all out.
//
// It says nothing about WHICH word was meant: "st" prefixes "stuyvesant"
// as readily as "street". That ambiguity is priced in rather than
// legislated away — see abbrevCredit and the pairing order in nameSim.
//
// A number is the exception: it abbreviates only its own ordinal form, a
// digit run followed by letters ("4" → "4th", "4e", "4ª"), never a longer
// number. As plain prefixes "4" paired with "42nd" and "8" with "81", and
// 5 Av/E 81 St took the name of the pole on 80th.
func abbreviates(a, b string) bool {
	if len(a) == len(b) {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if allDigits(a) {
		if !strings.HasPrefix(b, a) {
			return false
		}
		r := []rune(b[len(a):])
		return len(r) > 0 && !unicode.IsDigit(r[0])
	}
	return strings.HasPrefix(b, a) || contracts(a, b)
}

// contracts reports a written as a contraction of b: same first letter,
// same last letter past two letters, and every letter of a found in b in
// order — "blvd" in "boulevard",
// "pkwy" in "parkway", "rd" in "road". Prefixes alone missed every one of
// them, and NYC's bus feed abbreviates street types exactly this way.
// Letters only: a number is never a contraction of anything.
func contracts(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	// a contraction drops letters ("rd" for "road"); two spellings a
	// letter apart are a misspelling ("Rockway", "Sedgewick"), and taking
	// OSM's would copy its typo onto the stop
	if len(ra) < 2 || len(rb)-len(ra) < 2 || ra[0] != rb[0] || allDigits(a) || allDigits(b) {
		return false
	}
	// past two letters a contraction keeps the word's last letter too
	// ("blvd", "pkwy", "jr"); without it "rer" read as "routière"
	if len(ra) > 2 && ra[len(ra)-1] != rb[len(rb)-1] {
		return false
	}
	j := 1
	for i := 1; i < len(rb) && j < len(ra); i++ {
		if rb[i] == ra[j] {
			j++
		}
	}
	return j == len(ra)
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}

// abbrevCredit is a prefix pairing's worth against the same two words
// spelled alike. An abbreviation is real but weaker evidence — the
// shorter the stub, the more words it could have been — so it counts
// for less, and a station that agrees outright always outranks one that
// only agrees once expanded.
const abbrevCredit = 0.75

// uniqueToks drops repeats, keeping first-seen order so pairing is
// deterministic across runs.
func uniqueToks(t []string) []string {
	seen := make(map[string]bool, len(t))
	out := make([]string, 0, len(t))
	for _, x := range t {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// nameSim is IDF-weighted containment: the shared weight over the lighter
// side's total. Containment rather than Jaccard because abbreviation is
// the norm — "Mint" against "Mint Street" should read as agreement, not
// as half a disagreement — and the weighting means the words the city
// repeats everywhere ("Street", "CityLYNX") contribute almost nothing to
// either side of that ratio.
//
// Words pair one-to-one, exact spellings first and abbreviations after,
// so a stub never consumes the token its own full form was waiting for:
// against "Christopher Street–Stonewall", the feed's "st" must not take
// "stonewall" and leave "christopher" to carry the name alone.
//
// A pairing is credited the LOWER of the two tokens' weights. A stub
// cannot be worth more than the word it stands in for, and it is what
// keeps promiscuous one-letter stubs cheap without a rule about their
// length: "w" is on a tenth of the MTA's names, so it scores near zero
// however rare the "washington" it reaches for.
func nameSim(a, b []string, idf map[string]float64) float64 {
	ta, tb := uniqueToks(a), uniqueToks(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	wa, wb := 0.0, 0.0
	for _, t := range ta {
		wa += idf[t]
	}
	for _, t := range tb {
		wb += idf[t]
	}
	lighter := math.Min(wa, wb)
	if lighter <= 0 {
		return 0
	}

	pairedA := make([]bool, len(ta))
	usedB := make([]bool, len(tb))
	shared := 0.0
	for i, x := range ta {
		for j, y := range tb {
			if usedB[j] || x != y {
				continue
			}
			pairedA[i], usedB[j] = true, true
			shared += idf[x]
			break
		}
	}
	for i, x := range ta {
		if pairedA[i] {
			continue
		}
		best, credit := -1, 0.0
		for j, y := range tb {
			if usedB[j] || !abbreviates(x, y) {
				continue
			}
			c := abbrevCredit * math.Min(idf[x], idf[y])
			// the closest expansion when several fit: "st" is likelier
			// "street" than "stuyvesant"
			if c > credit || (c == credit && best >= 0 && len(y) < len(tb[best])) {
				best, credit = j, c
			}
		}
		if best >= 0 {
			pairedA[i], usedB[best] = true, true
			shared += credit
		}
	}
	return math.Min(1, shared/lighter)
}

// nameCover is nameSim from both sides at once: the share of each name's
// weight that pairs with the other, exactly or as an abbreviation. Two
// names agree only when both are high; nameSim's one-sided containment
// lets "Rockaway Blvd" claim "Farmers Blvd & Rockaway Blvd".
//
// swapped reports a word left over on BOTH sides: one name says something
// in a place where the other says something else. That is a different
// street, however little the words weigh — "147 Av" is not "147th
// Street", and "Rockaway Tpke" is not "Rockaway Boulevard". A word left
// over on one side only is detail ("Astoria Boulevard South").
func nameCover(a, b []string, idf map[string]float64, plausible func(short, long string) bool) (ca, cb float64, swapped bool) {
	// every word counts, repeats included: "E 34th Ave & Park Ave" deduped
	// to one "ave", which paired with "Avenue" and hid the "Street" OSM
	// had where the feed's first "Ave" stood
	ta, tb := a, b
	wa, wb := 0.0, 0.0
	for _, t := range ta {
		wa += idf[t]
	}
	for _, t := range tb {
		wb += idf[t]
	}
	if wa <= 0 || wb <= 0 {
		return 0, 0, false
	}
	pairedA := make([]bool, len(ta))
	usedB := make([]bool, len(tb))
	for i, x := range ta {
		for j, y := range tb {
			if !usedB[j] && x == y {
				pairedA[i], usedB[j] = true, true
				ca += idf[x]
				cb += idf[y]
				break
			}
		}
	}
	for i, x := range ta {
		if pairedA[i] {
			continue
		}
		best := -1
		for j, y := range tb {
			if usedB[j] || !abbreviates(x, y) {
				continue
			}
			short, long := x, y
			if len(short) > len(long) {
				short, long = long, short
			}
			if !plausible(short, long) {
				continue
			}
			if best < 0 || len(y) < len(tb[best]) {
				best = j
			}
		}
		if best >= 0 {
			pairedA[i], usedB[best] = true, true
			ca += idf[x]
			cb += idf[tb[best]]
		}
	}
	leftA, leftB := false, false
	for i := range ta {
		leftA = leftA || !pairedA[i]
	}
	for j := range tb {
		leftB = leftB || !usedB[j]
	}
	return ca / wa, cb / wb, leftA && leftB
}

// expansions measures, for this city, which long words each short form
// stands for and how often: OSM's own vocabulary says "st" is "street"
// hundreds of times and "sreet" once. A pairing that expands a short form
// to a spelling the city almost never uses is a typo being copied, not an
// abbreviation being read.
type expansions struct {
	df    map[string]int // OSM name count per token
	vocab []string
	best  map[string]int // short form → df of its commonest expansion
}

func newExpansions(osm [][]string) *expansions {
	e := &expansions{df: map[string]int{}, best: map[string]int{}}
	for _, toks := range osm {
		seen := map[string]bool{}
		for _, t := range toks {
			if !seen[t] {
				seen[t] = true
				e.df[t]++
			}
		}
	}
	for t := range e.df {
		e.vocab = append(e.vocab, t)
	}
	sort.Strings(e.vocab)
	return e
}

// typoShare: an expansion used less than this share as often as the
// commonest one is taken for a misspelling.
const typoShare = 0.05

func (e *expansions) plausible(short, long string) bool {
	top, ok := e.best[short]
	if !ok {
		for _, t := range e.vocab {
			if len(t) > len(short) && abbreviates(short, t) && e.df[t] > top {
				top = e.df[t]
			}
		}
		e.best[short] = top
	}
	n, inOSM := e.df[long]
	if !inOSM {
		return true // the long form is the feed's: nothing to copy
	}
	return float64(n) >= typoShare*float64(top)
}

// busOnly: every route at this stop is a bus.
func busOnly(s Station) bool {
	for _, m := range s.Modes {
		if m != "bus" {
			return false
		}
	}
	return len(s.Modes) > 0
}

// busAgree is how much of each name a bus stop pairing must cover. A bus
// pole is not a station: every corner has two to eight of them metres
// apart, each named for a different street or block, so proximity says
// nothing and the names must say the same thing on both sides.
const busAgree = 0.85

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

// osmStationBonus outranks roughly 20 m of distance: enough that a station
// node beats a stop_position across the platform, not enough to reach past
// a genuinely nearer station.
const osmStationBonus = 0.13

// railFamily is the set of classes that mean "runs on rails in a city".
// A feed and OSM routinely disagree about WHICH of them a line is, and
// neither is wrong: the Staten Island Railway is filed by the MTA as
// route_type 2 (regional) and mapped in OSM as station=subway (metro),
// because it is legally a railway and operationally a subway. Requiring
// the two to agree exactly threw away all 21 of its stations while their
// OSM nodes sat 19-28 m away under identical names.
//
// Ferry, bus, aerial and the rest stay hard-gated. Those confusions are
// real disagreements about what the thing IS, not two names for one
// answer, and a tram stop must still not claim the bus pole beside it.
var railFamily = map[string]bool{
	"metro": true, "regional": true, "tram": true,
}

// crossClassPenalty prices a pairing that only agrees it is rail. Enough
// that a stop of the station's own class wins whenever both are in reach,
// not enough to lose to a stop that merely sits nearer.
const crossClassPenalty = 0.10

// classAffinity scores a station's classes against an OSM stop's: 1 for
// a class both hold, railFamilyOnly when they merely agree it is rail,
// and 0 for a pairing the gate should drop.
func classAffinity(stationClasses, stopClasses map[string]bool) (ok, exact bool) {
	for c := range stationClasses {
		if stopClasses[c] {
			return true, true
		}
	}
	rail := func(cs map[string]bool) bool {
		for c := range cs {
			if railFamily[c] {
				return true
			}
		}
		return false
	}
	if rail(stationClasses) && rail(stopClasses) {
		return true, false
	}
	return false, false
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
	exp := newExpansions(ocorp)
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
			// bus pole beside it. Two rail classes that disagree with each
			// other still pass, at a price — see railFamily.
			ok, exactClass := classAffinity(classes, stops[oi].Classes)
			if !ok {
				continue
			}
			sim := nameSim(stoks[si], stops[oi].toks, idf)
			if sim < needSim(d) {
				continue
			}
			if busOnly(sts[si]) {
				if ca, cb, swapped := nameCover(stoks[si], stops[oi].toks, idf, exp.plausible); swapped || math.Min(ca, cb) < busAgree {
					continue
				}
			}
			// proximity dominates the ranking too; the name only orders
			// candidates that are similarly close
			score := 0.65*(1-d/osmMatchRadiusM) + 0.35*sim
			if !exactClass {
				score -= crossClassPenalty
			}
			// ...but a station outranks a stopping point that is merely
			// nearer. Both carry the station's name and sit metres apart, so
			// without this the winner is whichever the feed's coordinate
			// happened to land beside: Clark St matched its station node and
			// Jay St–MetroTech matched one of its six stop_positions, and the
			// panel called them different kinds of place. Only the station is
			// somewhere a rider can be sent. A bonus rather than a filter —
			// plenty of stations are mapped with no station node at all, and
			// a stop_position is a better answer there than none.
			if stops[oi].Station {
				score += osmStationBonus
			}
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
		// one-to-one, except that a bus pole may name every stop its name
		// fully agrees with: a feed often files both directions of a
		// corner, OSM one pole, and the agreement test already made the
		// pairing as sure as a name can be
		if usedS[c.si] || (usedO[c.oi] && !busOnly(sts[c.si])) {
			continue
		}
		// the id stays one-to-one even where the name is shared: the
		// second direction is not that node
		id := stops[c.oi].ID
		if usedO[c.oi] {
			id = ""
		}
		usedS[c.si], usedO[c.oi] = true, true
		out = append(out, StopMatch{
			Station: c.si, OSM: id,
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
		if m.OSM != "" {
			sts[m.Station].OSMID = m.OSM
		}
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
			sts[si].Renamed = true
			renamed++
		}
		delete(held, old)
		held[g.target] = len(g.idx)
	}
	return renamed
}
