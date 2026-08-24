package osm

import (
	"os"
	"reflect"
	"testing"
)

const nycRail = "../../testdata/nyc-rail.geojson"

// LoadWithService must split the extract without disturbing the regular
// pool: same ways, same order, same ids as Load has always returned. The
// service pool holds exactly the yard/siding/spur ways the old gate
// dropped — LESSONS: >4 strands are yards, but the steel is real and the
// yard detector needs it.
func TestLoadWithServiceSplitsExactly(t *testing.T) {
	if _, err := os.Stat(nycRail); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	regular, service, err := LoadWithService(nycRail)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(nycRail)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(regular, loaded) {
		t.Fatalf("regular pool diverges from Load: %d vs %d ways", len(regular), len(loaded))
	}
	// Fixture census (14,102 features): 8,253 untagged + 1,033 crossover
	// rail LineStrings stay regular; 4,079 yard + 519 spur + 218 siding
	// go to the service pool.
	if len(regular) != 9286 {
		t.Errorf("regular pool = %d ways, want 9286", len(regular))
	}
	if len(service) != 4816 {
		t.Errorf("service pool = %d ways, want 4816", len(service))
	}
	seen := map[string]bool{}
	for _, w := range regular {
		if s := w.Tags["service"]; s != "" && s != "crossover" {
			t.Fatalf("regular pool holds service=%s way %s", s, w.ID)
		}
		seen[w.ID] = true
	}
	svc := map[string]int{}
	for _, w := range service {
		s := w.Tags["service"]
		if s == "" || s == "crossover" {
			t.Fatalf("service pool holds service=%q way %s", s, w.ID)
		}
		if seen[w.ID] {
			t.Fatalf("way %s in both pools", w.ID)
		}
		svc[s]++
	}
	if svc["yard"] != 4079 || svc["spur"] != 519 || svc["siding"] != 218 {
		t.Errorf("service census yard=%d spur=%d siding=%d, want 4079/519/218",
			svc["yard"], svc["spur"], svc["siding"])
	}
}
