package pipeline

// Building the exported feed's transfers.txt.
//
// Three sources, in increasing authority:
//
//	1. the feed's own rows          — the agency's account of its network
//	2. curated `allow` rows         — connections the agency omitted
//	3. prohibitions                 — curated `forbid`, plus every gated
//	                                  station pair the agency left out
//
// A prohibition beats everything: it is the one statement here that a
// router cannot arrive at on its own, and the whole reason the file is
// being rewritten. See style.Transfers for why gates need saying out loud.

import (
	"archive/zip"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/style"
)

// GTFS transfer_type values this writes.
const (
	transferMinimumTime = "2" // a transfer, with min_transfer_time
	transferForbidden   = "3" // "Transfers forbidden between routes at these stops"
)

// transferRow is one row of the rewritten file.
type transferRow struct {
	from, to string
	kind     string // transferMinimumTime | transferForbidden
	seconds  int
}

// pair is an ordered station pair, used as a map key.
type pair struct{ from, to string }

// stopGeo is what the derivation needs to know about a stop.
type stopGeo struct {
	id     string
	lat    float64
	lon    float64
	parent string
	isStop bool // location_type 0 (or blank) — a boardable stop
}

// station resolves a stop to the thing a transfer is declared between: its
// parent station where it has one, itself otherwise. The MTA files
// transfers between parent stations (232, A41) while stop_times calls at
// platforms (232N, 232S), so without this every declared transfer would
// look absent and the derivation would forbid the whole network.
func (s stopGeo) station() string {
	if s.parent != "" {
		return s.parent
	}
	return s.id
}

// buildTransfers rewrites a feed's transfers.txt from its own rows plus
// curation. It returns the file body, or "" when there is nothing to
// change and the source rows should pass through untouched.
func buildTransfers(zipPath string, t *style.Transfers) (string, int, error) {
	if t == nil {
		return "", 0, nil
	}
	if err := t.Validate(); err != nil {
		return "", 0, err
	}
	if !t.ForbidUndeclared && len(t.Allow) == 0 && len(t.Forbid) == 0 {
		return "", 0, nil
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", 0, err
	}
	defer zr.Close()

	stops, err := readStops(&zr.Reader)
	if err != nil {
		return "", 0, err
	}
	declared, existing, err := readDeclaredTransfers(&zr.Reader, stops)
	if err != nil {
		return "", 0, err
	}

	// Start from the agency's own rows: curation adds to the feed, it does
	// not start from a blank file. A rewrite that dropped the MTA's 613
	// in-station transfers would break every legitimate change in the
	// system to fix one phantom.
	rows := append([]transferRow(nil), existing...)

	// Curated additions.
	for _, r := range t.Allow {
		rows = append(rows, transferRow{r.From, r.To, transferMinimumTime, r.Minutes * 60})
		if r.Both() {
			rows = append(rows, transferRow{r.To, r.From, transferMinimumTime, r.Minutes * 60})
		}
	}

	// Prohibitions: curated first, then derived.
	forbidden := map[pair]bool{}
	for _, r := range t.Forbid {
		forbidden[pair{r.From, r.To}] = true
		if r.Both() {
			forbidden[pair{r.To, r.From}] = true
		}
	}
	derived := 0
	if t.ForbidUndeclared {
		gated, err := readGatedStations(&zr.Reader, stops)
		if err != nil {
			return "", 0, err
		}
		for _, p := range undeclaredNearbyPairs(gated, declared, t.Radius()) {
			if !forbidden[p] {
				forbidden[p] = true
				derived++
			}
		}
	}

	// A prohibition beats a transfer, whatever its source — including the
	// agency's own row. That is deliberate: a curator who forbids a pair
	// the feed declares is correcting the feed, which is what this file
	// is for.
	out := rows[:0]
	for _, r := range rows {
		if !forbidden[pair{r.from, r.to}] {
			out = append(out, r)
		}
	}
	rows = out
	for p := range forbidden {
		rows = append(rows, transferRow{p.from, p.to, transferForbidden, 0})
	}

	// Deterministic order so an unchanged feed exports byte-identically
	// and sync can fingerprint-skip it.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].from != rows[j].from {
			return rows[i].from < rows[j].from
		}
		if rows[i].to != rows[j].to {
			return rows[i].to < rows[j].to
		}
		return rows[i].kind < rows[j].kind
	})

	cells := make([][]string, 0, len(rows))
	for _, r := range rows {
		time := ""
		if r.kind == transferMinimumTime && r.seconds > 0 {
			time = strconv.Itoa(r.seconds)
		}
		cells = append(cells, []string{r.from, r.to, r.kind, time})
	}
	return csvTable([]string{"from_stop_id", "to_stop_id", "transfer_type",
		"min_transfer_time"}, cells), derived, nil
}

// undeclaredNearbyPairs is the derivation: gated stations close enough that
// a router would link them, which the agency never connected.
func undeclaredNearbyPairs(gated []stopGeo, declared map[pair]bool, radius float64) []pair {
	// One representative per station — platforms under a parent share its
	// position closely enough, and a prohibition is declared between the
	// stations, not between every platform pairing.
	byStation := map[string]stopGeo{}
	for _, s := range gated {
		if _, seen := byStation[s.station()]; !seen {
			byStation[s.station()] = s
		}
	}
	ids := make([]string, 0, len(byStation))
	for id := range byStation {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// Grid buckets of one radius, so each station only measures against
	// its own cell and the eight around it. A citywide feed has thousands
	// of stations and the all-pairs loop is minutes of nothing.
	cell := func(s stopGeo) (int, int) {
		return int(math.Floor(s.lat / degLat(radius))), int(math.Floor(s.lon / degLon(radius, s.lat)))
	}
	grid := map[[2]int][]string{}
	for _, id := range ids {
		x, y := cell(byStation[id])
		grid[[2]int{x, y}] = append(grid[[2]int{x, y}], id)
	}

	var out []pair
	for _, id := range ids {
		a := byStation[id]
		x, y := cell(a)
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, other := range grid[[2]int{x + dx, y + dy}] {
					if other <= id { // each unordered pair once
						continue
					}
					b := byStation[other]
					if metres(a.lat, a.lon, b.lat, b.lon) > radius {
						continue
					}
					if declared[pair{id, other}] || declared[pair{other, id}] {
						continue
					}
					out = append(out, pair{id, other}, pair{other, id})
				}
			}
		}
	}
	return out
}

// degLat/degLon convert a distance to a coordinate span — enough for grid
// bucketing, where only rough cell size matters.
func degLat(m float64) float64 { return m / 111_320.0 }
func degLon(m, lat float64) float64 {
	c := math.Cos(lat * math.Pi / 180)
	if c < 0.01 {
		c = 0.01
	}
	return m / (111_320.0 * c)
}

// metres is the equirectangular approximation — exact enough at the few
// hundred metres this ever measures.
func metres(lat1, lon1, lat2, lon2 float64) float64 {
	x := (lon2 - lon1) * math.Cos((lat1+lat2)/2*math.Pi/180)
	y := lat2 - lat1
	return math.Sqrt(x*x+y*y) * 111_320.0
}

// ── reading the source feed ─────────────────────────────────────────

func readStops(zr *zip.Reader) (map[string]stopGeo, error) {
	out := map[string]stopGeo{}
	err := eachRow(zr, "stops.txt", func(r map[string]string) error {
		lat, err1 := strconv.ParseFloat(r["stop_lat"], 64)
		lon, err2 := strconv.ParseFloat(r["stop_lon"], 64)
		if err1 != nil || err2 != nil {
			return nil // a stop with no position cannot be measured against
		}
		lt := r["location_type"]
		out[r["stop_id"]] = stopGeo{
			id: r["stop_id"], lat: lat, lon: lon,
			parent: r["parent_station"],
			isStop: lt == "" || lt == "0",
		}
		return nil
	})
	return out, err
}

// readDeclaredTransfers returns the station pairs the feed connects, and
// the rows themselves so they can be carried into the rewrite.
func readDeclaredTransfers(zr *zip.Reader, stops map[string]stopGeo) (map[pair]bool, []transferRow, error) {
	declared := map[pair]bool{}
	var rows []transferRow
	station := func(id string) string {
		if s, ok := stops[id]; ok {
			return s.station()
		}
		return id
	}
	err := eachRow(zr, "transfers.txt", func(r map[string]string) error {
		from, to := r["from_stop_id"], r["to_stop_id"]
		if from == "" || to == "" {
			return nil
		}
		kind := r["transfer_type"]
		if kind == "" {
			kind = "0"
		}
		secs, _ := strconv.Atoi(r["min_transfer_time"])
		rows = append(rows, transferRow{from, to, kind, secs})
		// A same-station row ("232,232") is a dwell time, not a
		// connection, and must not mark the station as connected to
		// anything. Prohibitions in the source are not evidence of a
		// connection either.
		if from != to && kind != transferForbidden {
			declared[pair{station(from), station(to)}] = true
		}
		return nil
	})
	return declared, rows, err
}

// readGatedStations lists the stops served by a mode that boards inside
// fare control. A feed whose routes are ALL gated — a metro feed, which is
// the common case — skips the stop_times pass entirely: every stop
// qualifies, and stop_times is the largest file in the zip.
func readGatedStations(zr *zip.Reader, stops map[string]stopGeo) ([]stopGeo, error) {
	gatedRoutes := map[string]bool{}
	anyUngated := false
	if err := eachRow(zr, "routes.txt", func(r map[string]string) error {
		t, err := strconv.Atoi(r["route_type"])
		if err != nil {
			return nil
		}
		if style.IsGatedRouteType(t) {
			gatedRoutes[r["route_id"]] = true
		} else {
			anyUngated = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(gatedRoutes) == 0 {
		return nil, nil
	}

	all := func() []stopGeo {
		out := make([]stopGeo, 0, len(stops))
		for _, s := range stops {
			if s.isStop {
				out = append(out, s)
			}
		}
		return out
	}
	if !anyUngated {
		return all(), nil
	}

	// Mixed feed: follow routes → trips → stop_times to find which stops a
	// gated service actually calls at.
	gatedTrips := map[string]bool{}
	if err := eachRow(zr, "trips.txt", func(r map[string]string) error {
		if gatedRoutes[r["route_id"]] {
			gatedTrips[r["trip_id"]] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	if err := eachRow(zr, "stop_times.txt", func(r map[string]string) error {
		if gatedTrips[r["trip_id"]] {
			seen[r["stop_id"]] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var out []stopGeo
	for id := range seen {
		if s, ok := stops[id]; ok && s.isStop {
			out = append(out, s)
		}
	}
	return out, nil
}

// eachRow streams a GTFS table from the zip. A missing table is not an
// error — a feed without transfers.txt is simply a feed that declares none.
func eachRow(zr *zip.Reader, table string, fn func(map[string]string) error) error {
	for _, e := range zr.File {
		if filepath.Base(e.Name) != table {
			continue
		}
		r, err := e.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		cr := csv.NewReader(r)
		cr.FieldsPerRecord = -1
		cr.ReuseRecord = true
		head, err := cr.Read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("%s: %w", table, err)
		}
		cols := make([]string, len(head))
		for i, h := range head {
			cols[i] = strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))
		}
		row := map[string]string{}
		for {
			rec, err := cr.Read()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("%s: %w", table, err)
			}
			for k := range row {
				delete(row, k)
			}
			for i, c := range cols {
				if i < len(rec) {
					row[c] = strings.TrimSpace(rec[i])
				}
			}
			if err := fn(row); err != nil {
				return err
			}
		}
	}
	return nil
}
