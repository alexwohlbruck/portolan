package gtfs

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// Scenario derivation MUST be deterministic: the atlas lists scenarios from
// one computation and Chart re-derives them in another process.
func TestServiceScenariosDeterministic(t *testing.T) {
	path := os.Getenv("HOME") + "/Documents/code/barrelman/data/gtfs/5.zip"
	if _, err := os.Stat(path); err != nil {
		t.Skip("NYC GTFS not present")
	}
	si, err := LoadService(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := func() string {
		var l []string
		for _, sc := range BuildScenarios(si, 0.99) {
			l = append(l, sc.ID+" "+sc.Label)
		}
		sort.Strings(l)
		return strings.Join(l, "\n")
	}
	first := ids()
	for i := 0; i < 4; i++ {
		if got := ids(); got != first {
			t.Fatalf("run %d differs:\n--- first:\n%s\n--- got:\n%s", i+2, first, got)
		}
	}
}
