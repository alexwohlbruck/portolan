package main

import (
	"fmt"
	"os"

	"github.com/alexwohlbruck/portolan/internal/pipeline"
)

func main() {
	home := os.Getenv("HOME")
	for _, scen := range []string{"7f96b50a", "20f0c0b3", "e4bc58f8"} {
		out := "build/nyc.scen-" + scen + ".geojson"
		err := pipeline.Chart(pipeline.ChartOpts{
			GTFS:     home + "/Documents/code/barrelman/data/gtfs/5.zip",
			Rail:     "testdata/nyc-rail.geojson",
			Out:      out,
			Scenario: scen,
		}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, scen+": "+f+"\n", a...) })
		if err != nil {
			panic(err)
		}
	}
}
