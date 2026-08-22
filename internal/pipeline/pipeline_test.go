package pipeline

import "testing"

// A filter expression can slice `acts` at a computed offset but cannot
// count commas, so the tile has to say which slot each route owns.
func TestActsIndex(t *testing.T) {
	if got := actsIndex([]string{"A", "C", "E"}, 3); got != "A=00;C=01;E=02" {
		t.Fatalf("actsIndex = %q", got)
	}
	// ids are the feed's own and can be long, or carry an overlay prefix
	if got := actsIndex([]string{"B_CMX0700L1", "f1:237"}, 2); got != "B_CMX0700L1=00;f1:237=01" {
		t.Fatalf("actsIndex long = %q", got)
	}
	// misalignment is not indexable: naming a slot that is not there would
	// have a consumer read another route's hours as this one's
	for _, tc := range []struct {
		routes []string
		masks  int
	}{
		{[]string{"A", "C"}, 3},
		{[]string{"A", "C"}, 1},
		{nil, 0},
	} {
		if got := actsIndex(tc.routes, tc.masks); got != "" {
			t.Errorf("actsIndex(%v, %d) = %q, want empty", tc.routes, tc.masks, got)
		}
	}
	// a delimiter inside an id would break every lookup built on it
	if got := actsIndex([]string{"A;B"}, 1); got != "" {
		t.Errorf("actsIndex with a semicolon = %q, want empty", got)
	}
	if got := actsIndex([]string{"A=B"}, 1); got != "" {
		t.Errorf("actsIndex with an equals = %q, want empty", got)
	}
}
