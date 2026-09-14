package pipeline

// Emitting a curated tariff as GTFS Fares v2.
//
// style.Fares says what a rider pays in the plainest terms a curator can
// write — one price, and whether a transfer is included. GTFS says the same
// thing across three related tables, because it is built to describe tariffs
// far more elaborate than this one. The translation is mechanical:
//
//	fare_products.txt        the price itself, once
//	fare_leg_rules.txt       which legs are charged at it — here, all of them
//	fare_transfer_rules.txt  what a second leg costs — here, nothing, inside
//	                         a count and a time window
//
// Downstream this is what makes a trip priceable at all. A router that
// computes fares (MOTIS reads v2 and only v2) groups an itinerary's legs
// into FARE TRANSFERS: consecutive legs joined by a matching transfer rule
// become one payment, and legs with no rule between them become two. So the
// transfer rule is not merely a discount — it is the thing that decides how
// many times a rider reaches for their card, which is the difference
// between a $2.90 trip and a $5.80 one.

import (
	"fmt"
	"strconv"

	"github.com/alexwohlbruck/portolan/internal/style"
)

// The ids are fixed and namespaced rather than generated: a curated tariff
// is a single product by construction, and a stable id keeps an exported
// feed byte-identical between runs, which is what the sync fingerprints
// depend on to skip unchanged work.
const (
	fareProductID  = "portolan_base"
	fareLegGroupID = "portolan_all_legs"
)

// fareFiles are the GTFS filenames a curated tariff writes. Anything the
// source feed had under these names is replaced wholesale — a curator who
// states a tariff is overriding what the feed said, and interleaving two
// fare tables would produce a third that nobody wrote.
var fareFiles = []string{
	"fare_products.txt",
	"fare_leg_rules.txt",
	"fare_transfer_rules.txt",
}

// buildFares renders a curated tariff as GTFS Fares v2 tables, keyed by
// filename. A nil or priceless tariff renders nothing, and the feed's own
// fare data — whatever it is — passes through untouched.
func buildFares(f *style.Fares) (map[string]string, error) {
	if f == nil || f.Price <= 0 {
		return nil, nil
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}

	out := map[string]string{}

	// Price. amount is written at the currency's own precision rather than
	// rounded to cents, because not every ISO 4217 currency has two
	// decimal places and a tariff in yen is not 290.00.
	out["fare_products.txt"] = csvTable(
		[]string{"fare_product_id", "fare_product_name", "amount", "currency"},
		[][]string{{fareProductID, f.ProductName(),
			strconv.FormatFloat(f.Price, 'f', -1, 64), f.Currency}},
	)

	// Which legs it covers: all of them. network_id, the area columns and
	// the timeframe columns are all left empty, which in Fares v2 means
	// "matches anything" — a flat fare is precisely the rule with no
	// qualifications on it.
	out["fare_leg_rules.txt"] = csvTable(
		[]string{"leg_group_id", "network_id", "from_area_id", "to_area_id",
			"from_timeframe_group_id", "to_timeframe_group_id",
			"fare_product_id", "rule_priority"},
		[][]string{{fareLegGroupID, "", "", "", "", "", fareProductID, ""}},
	)

	// What a transfer costs: nothing, within the curated allowance. The
	// rule carries no fare_product_id, and fare_transfer_type 0 totals the
	// journey as "first leg's fare, plus the transfer's" — so with no
	// transfer product the second leg rides on the first fare.
	if t := f.Transfer; t != nil && t.Count != 0 {
		duration, durationType := "", ""
		if t.Minutes > 0 {
			duration = strconv.Itoa(t.Minutes * 60)
			// 1 = measured from the first leg's DEPARTURE to the next
			// leg's departure. Departure-to-departure is how transfer
			// windows are advertised ("two hours from when you tap"),
			// and it is what barrelman's v1→v2 conversion already emits,
			// so a feed priced by either route behaves the same way.
			durationType = "1"
		}
		out["fare_transfer_rules.txt"] = csvTable(
			[]string{"from_leg_group_id", "to_leg_group_id", "transfer_count",
				"duration_limit", "duration_limit_type", "fare_transfer_type",
				"fare_product_id"},
			[][]string{{fareLegGroupID, fareLegGroupID, strconv.Itoa(t.Count),
				duration, durationType, "0", ""}},
		)
	}

	return out, nil
}

// csvTable renders a header and rows as RFC-4180 CSV with a trailing
// newline. Written here rather than through encoding/csv because the rows
// are ours: every value is an id, a number or a curated name, and the
// escaping needed is the quote-and-double rule for the one field (a fare
// product's name) that can contain a comma.
func csvTable(header []string, rows [][]string) string {
	var b []byte
	writeRow := func(cells []string) {
		for i, c := range cells {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, csvField(c)...)
		}
		b = append(b, '\n')
	}
	writeRow(header)
	for _, r := range rows {
		writeRow(r)
	}
	return string(b)
}

func csvField(v string) string {
	needsQuote := false
	for i := 0; i < len(v); i++ {
		if c := v[i]; c == ',' || c == '"' || c == '\n' || c == '\r' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return v
	}
	out := make([]byte, 0, len(v)+2)
	out = append(out, '"')
	for i := 0; i < len(v); i++ {
		if v[i] == '"' {
			out = append(out, '"')
		}
		out = append(out, v[i])
	}
	return string(append(out, '"'))
}

// fareSummary is the one-line build log for a curated tariff.
func fareSummary(f *style.Fares) string {
	if f == nil || f.Price <= 0 {
		return ""
	}
	s := fmt.Sprintf("%s %s per leg", strconv.FormatFloat(f.Price, 'f', -1, 64), f.Currency)
	switch t := f.Transfer; {
	case t == nil || t.Count == 0:
		return s + ", no free transfer"
	case t.Count < 0 && t.Minutes > 0:
		return fmt.Sprintf("%s, free transfers for %d min", s, t.Minutes)
	case t.Count < 0:
		return s + ", free transfers"
	case t.Minutes > 0:
		return fmt.Sprintf("%s, %d free transfer(s) within %d min", s, t.Count, t.Minutes)
	default:
		return fmt.Sprintf("%s, %d free transfer(s)", s, t.Count)
	}
}
