package mode

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/style"
)

// Colour trunking is law 5 and stays the default. But it assumes colour
// carries meaning, and an authored network may assign colours
// arbitrarily — two unrelated routes that happen to share a hex would
// merge into one ribbon. TrunkRoute per route is the way out.
func TestSharedColourMergesUntilARouteOptsOut(t *testing.T) {
	defer style.Set_(nil) // the active set is process-wide

	metro := func(id, hex string) gtfs.Route {
		return gtfs.Route{ID: id, ShortName: id, Color: hex, Type: 1, Agency: "AUTH"}
	}
	a, b := metro("A", "FF0000"), metro("B", "FF0000")

	style.Set_(style.New())
	if TrunkKey(a) != TrunkKey(b) {
		t.Fatalf("law 5: same colour must be one ribbon, got %q vs %q", TrunkKey(a), TrunkKey(b))
	}

	style.Set_(style.New(style.Doc{
		Routes: map[string]style.Entity{"B": {Trunk: style.TrunkRoute}},
	}.Config()))
	if TrunkKey(a) == TrunkKey(b) {
		t.Errorf("trunk=route did not separate B from the colour trunk (both %q)", TrunkKey(a))
	}
	if got := TrunkKey(a); got != "FF0000" {
		t.Errorf("the route that did NOT opt out must keep its colour key, got %q", got)
	}
}
