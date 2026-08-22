package pipeline

import (
	"fmt"
	"sort"
	"testing"
)

func TestDumpLMasks(t *testing.T) {
	group := "/home/alex/Documents/code/portolan/data/gtfs/hartford-line.zip,/home/alex/Documents/code/portolan/data/gtfs/mta-lirr.zip,/home/alex/Documents/code/portolan/data/gtfs/mta-metro-north.zip,/home/alex/Documents/code/portolan/data/gtfs/mta-subway.zip,/home/alex/Documents/code/portolan/data/gtfs/nj-transit-rail.zip,/home/alex/Documents/code/portolan/data/gtfs/patco.zip,/home/alex/Documents/code/portolan/data/gtfs/path.zip,/home/alex/Documents/code/portolan/data/gtfs/rioc-nyc.zip,/home/alex/Documents/code/portolan/data/gtfs/septa-rail.zip,/home/alex/Documents/code/portolan/data/gtfs/amtrak.zip,/home/alex/Documents/code/portolan/data/gtfs/viarail.zip"
	for _, tc := range []struct{ name, paths, route string }{
		{"member", "/home/alex/Documents/code/portolan/data/gtfs/mta-subway.zip", "L"},
		{"group", group, "f3:L"},
	} {
		si, err := LoadServiceInfo(tc.paths)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		pm := si.PatternMasks()
		var lines []string
		for k, m := range pm {
			if k.Route != tc.route {
				continue
			}
			lines = append(lines, fmt.Sprintf("   shape=%-24s mask=%s", k.Shape, m.Hex()))
		}
		sort.Strings(lines)
		fmt.Printf("%s: %d L patterns\n", tc.name, len(lines))
		for _, l := range lines {
			fmt.Println(l)
		}
	}
}
