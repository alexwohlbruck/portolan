package style

// TRANSFERS: which stations are actually connected, and which only look it.
//
// A router has to decide whether a rider can walk from one station to
// another and carry on. GTFS answers that in transfers.txt — and almost
// every router ALSO invents transfers from geometry, because most feeds'
// transfers.txt is incomplete and a network with no declared transfers at
// all would otherwise route nobody.
//
// For a fare-gated railway that inference is wrong in a way that costs
// riders money. Borough Hall and Jay St-MetroTech in Brooklyn are 240 m
// apart and the MTA's transfers.txt deliberately does not connect them:
// you leave the paid area and pay again. A router that links stations by
// proximity cannot see the gates, so it offers the walk as a free change
// and hides the one-fare alternative, which is often no slower.
//
// GTFS has the answer already. transfer_type=3 means "Transfers forbidden
// between routes at these stops" — a prohibition, published in the feed,
// which is exactly what a gate is. It is under-used because writing one
// row per unconnected pair by hand is absurd, so portolan derives them:
// for a gated feed, every pair of stations within walking distance that
// the agency's own transfers.txt does NOT connect is, by the agency's own
// account, not a transfer. That is mechanical, needs no curator, and holds
// in any city with a gated network.
//
// Caveat worth knowing: transfer_type=3 is a DENYLIST. GTFS has no way to
// say "my transfers.txt is complete, infer nothing" — which is what a
// gated network actually means — so the prohibitions have to be spelled
// out pair by pair. Deriving them is the price of the standard not having
// the flag.

import "fmt"

// Gated route types are the modes where boarding happens inside a paid
// area, so leaving the station means paying again: metro, rail, funicular
// and monorail. Bus, tram, ferry and cable transfers are settled by the
// tariff's transfer window instead — you tap again and it is free — so an
// undeclared walk between two of those is not a second fare and must not
// be forbidden.
var gatedRouteTypes = map[int]bool{
	1:  true, // metro / subway
	2:  true, // rail
	7:  true, // funicular
	12: true, // monorail
}

// IsGatedRouteType reports whether boarding this mode happens inside fare
// control.
func IsGatedRouteType(t int) bool { return gatedRouteTypes[t] }

// Transfers is the transfer block of a curation document.
//
//	"transfers": {
//	  "forbid_undeclared": true,
//	  "walk_radius_m": 400,
//	  "allow":  [ { "from": "A41", "to": "R29", "minutes": 2 } ],
//	  "forbid": [ { "from": "232", "to": "A41" } ]
//	}
type Transfers struct {
	// ForbidUndeclared derives transfer_type=3 rows: every pair of GATED
	// stations within WalkRadiusM that the feed's transfers.txt does not
	// already connect. Off by default — it is only safe for a feed whose
	// transfers.txt is complete, and asserting that is a curator's call.
	ForbidUndeclared bool `json:"forbid_undeclared,omitempty"`
	// WalkRadiusM is how far apart two stations can be and still be
	// somewhere a router would invent a transfer between. Zero takes
	// DefaultWalkRadiusM.
	//
	// It wants to be generous rather than tight: a prohibition on a pair
	// no router would have linked anyway costs one unused row, while a
	// radius that falls short of what the router reaches leaves exactly
	// the phantom transfer this is here to stop.
	WalkRadiusM float64 `json:"walk_radius_m,omitempty"`
	// Allow adds transfers the agency omitted — a bus stop outside a
	// station's entrance, a cross-platform change the feed never filed.
	// Written as transfer_type=2 with a minimum time.
	Allow []TransferRule `json:"allow,omitempty"`
	// Forbid states a prohibition by hand, for a pair that derivation
	// misses or that a curator knows is out of system. Written as
	// transfer_type=3, and beats Allow.
	Forbid []TransferRule `json:"forbid,omitempty"`
}

// DefaultWalkRadiusM is the derivation radius when none is curated. It
// covers MOTIS's 300 m cross-feed stop linking with room to spare, and the
// transitive chains that linking creates reach further than any single hop.
const DefaultWalkRadiusM = 400

// TransferRule names a station pair. Ids are GTFS stop ids; a pair given
// as parent stations covers every platform under them.
type TransferRule struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Minutes is the walking time for an allowed transfer. Ignored on a
	// prohibition. Zero on an allowed transfer means the router may use
	// its own estimate.
	Minutes int `json:"minutes,omitempty"`
	// Bidirectional writes the reverse row too. Nil is true: a curator
	// listing a connection means the connection, and a station you can
	// walk out of you can usually walk back into. Set false for a
	// one-way passage — an exit-only corridor is a real thing.
	Bidirectional *bool `json:"bidirectional,omitempty"`
}

// Both reports whether the rule applies in both directions.
func (r TransferRule) Both() bool { return r.Bidirectional == nil || *r.Bidirectional }

// Radius is the derivation radius, defaulted.
func (t *Transfers) Radius() float64 {
	if t == nil || t.WalkRadiusM <= 0 {
		return DefaultWalkRadiusM
	}
	return t.WalkRadiusM
}

// Validate reports what is wrong with a transfer block. Same reasoning as
// fares: curation that parses but does nothing is the worst outcome.
func (t *Transfers) Validate() error {
	if t == nil {
		return nil
	}
	if t.WalkRadiusM < 0 {
		return fmt.Errorf("transfers: walk_radius_m %.0f is negative", t.WalkRadiusM)
	}
	for _, set := range []struct {
		name  string
		rules []TransferRule
	}{{"allow", t.Allow}, {"forbid", t.Forbid}} {
		for i, r := range set.rules {
			if r.From == "" || r.To == "" {
				return fmt.Errorf("transfers: %s[%d] needs both from and to", set.name, i)
			}
			if r.From == r.To {
				return fmt.Errorf("transfers: %s[%d] connects %q to itself", set.name, i, r.From)
			}
			if r.Minutes < 0 {
				return fmt.Errorf("transfers: %s[%d] has a negative time", set.name, i)
			}
		}
	}
	return nil
}
