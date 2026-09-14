package style

// FARES: what an agency charges, supplied where the feed does not say.
//
// GTFS has carried fares since the beginning — fare_attributes.txt and
// fare_rules.txt in v1, the fare_products/fare_leg_rules/fare_transfer_rules
// family in v2 — and most agencies publish neither. The MTA is the case that
// forced this: not one of its seven feeds ships a single fare row, so a
// router reading them can price a trip across New York at nothing at all.
//
// The reason is not laziness. v1 cannot express a network whose transfers
// are free within a window, whose fares cap over a week, or which charges
// differently by rider category, and v2 — which can — is young enough that
// adoption is thin. An agency in that position publishes nothing rather
// than publish a number that is wrong.
//
// So it is curation, for the same reason a route's colour is: the fact is
// public, stable, and printed on the station wall, and there is no signal
// in the feed to compute it from. A curator writes the fare down once,
// portolan emits it as standard Fares v2, and every downstream consumer —
// MOTIS included — prices trips from the agency's real tariff.
//
// Deliberately small. This models a FLAT fare with an optional free-transfer
// window, which is what a metro or a city bus network charges and what the
// missing-fare feeds overwhelmingly are. Zone, distance and time-of-day
// tariffs are not here: those an agency must publish itself, because they
// are too big to hold correctly by hand and too easy to hold wrongly.

import "fmt"

// Fares is the fare block of a curation document: one flat price per leg,
// plus how transfers between legs are treated.
//
//	"fares": {
//	  "currency": "USD",
//	  "price": 2.90,
//	  "transfer": { "count": 1, "minutes": 120 }
//	}
type Fares struct {
	// Currency is the ISO 4217 code the prices are in. Required when a
	// price is given — a bare number is not a fare.
	Currency string `json:"currency,omitempty"`
	// Price is what one leg costs. A rider boarding any service on this
	// feed pays this, and nothing else unless a transfer rule says so.
	Price float64 `json:"price"`
	// Transfer describes what a rider pays to continue onto a second
	// vehicle. Absent means every leg is charged in full — which is the
	// honest default, because an agency that grants no free transfer is
	// commoner than one that does.
	Transfer *FareTransfer `json:"transfer,omitempty"`
	// Name labels the product in the emitted feed and in any UI that
	// shows it. Empty gets a plain "Base fare".
	Name string `json:"name,omitempty"`
}

// FareTransfer is a free-transfer allowance: how many, and for how long.
type FareTransfer struct {
	// Count is how many free transfers one fare buys. -1 is unlimited
	// within the window; 0 means none, which is the same as omitting
	// Transfer entirely and is accepted so a curator can say it out loud.
	Count int `json:"count"`
	// Minutes is the window, measured from the first leg's departure.
	// Zero leaves it unbounded, which is what an agency that grants a
	// transfer "for the rest of the journey" charges.
	Minutes int `json:"minutes,omitempty"`
}

// Validate reports what is wrong with a fare block, so a curator learns it
// from a failed build rather than from a trip priced at zero. Curation has
// no output of its own, and a fare that silently does nothing is exactly
// the failure this package's loader is strict to avoid.
func (f *Fares) Validate() error {
	if f == nil {
		return nil
	}
	if f.Price < 0 {
		return fmt.Errorf("fares: price %.2f is negative", f.Price)
	}
	if f.Price > 0 && f.Currency == "" {
		return fmt.Errorf("fares: price %.2f given without a currency", f.Price)
	}
	if len(f.Currency) != 0 && len(f.Currency) != 3 {
		return fmt.Errorf("fares: currency %q is not a 3-letter ISO 4217 code", f.Currency)
	}
	if f.Transfer != nil {
		if f.Transfer.Count < -1 {
			return fmt.Errorf("fares: transfer count %d is below -1 (unlimited)", f.Transfer.Count)
		}
		if f.Transfer.Minutes < 0 {
			return fmt.Errorf("fares: transfer window %d minutes is negative", f.Transfer.Minutes)
		}
	}
	return nil
}

// ProductName is the label to file the fare under.
func (f *Fares) ProductName() string {
	if f == nil || f.Name == "" {
		return "Base fare"
	}
	return f.Name
}
