// portolan — automatic transit line maps from GTFS feeds.
//
//	portolan chart --gtfs feed.zip --rail rail.geojson --out build.geojson
//	portolan sound --network sketches/nyc.json --build build.geojson
//	portolan atlas [--config portolan.json] [--addr 127.0.0.1:8765]
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/atlas"
	"github.com/alexwohlbruck/portolan/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "chart":
		chart(os.Args[2:])
	case "sound":
		sound(os.Args[2:])
	case "atlas":
		atlasCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: portolan chart|sound|atlas [flags] (see README.md)")
	os.Exit(2)
}

func chart(args []string) {
	fs := flag.NewFlagSet("chart", flag.ExitOnError)
	gtfsPath := fs.String("gtfs", "", "GTFS zip (comma list: primary,overlay,…)")
	railPath := fs.String("rail", "", "OSM rail extract (GeoJSON)")
	streets := fs.String("streets", "", "OSM street extract (GeoJSON) — enables bus routes")
	bboxStr := fs.String("bbox", "", "w,s,e,n — clip pattern shapes to the window")
	lineAg := fs.String("line-agencies", "", "comma list: regional agencies keeping per-line colors")
	out := fs.String("out", "build.geojson", "output GeoJSON")
	cover := fs.Float64("cover", 0.99, "pattern trip-coverage fraction")
	fs.Parse(args)
	if *railPath == "" {
		fs.Usage()
		os.Exit(2)
	}
	var bbox []float64
	if *bboxStr != "" {
		for _, p := range strings.Split(*bboxStr, ",") {
			v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				die(fmt.Errorf("bad --bbox: %w", err))
			}
			bbox = append(bbox, v)
		}
		if len(bbox) != 4 {
			die(fmt.Errorf("--bbox wants w,s,e,n"))
		}
	}
	var las []string
	if *lineAg != "" {
		las = strings.Split(*lineAg, ",")
	}
	d := pipeline.DefaultDials()
	d.Cover = *cover
	err := pipeline.Chart(pipeline.ChartOpts{
		GTFS: *gtfsPath, Rail: *railPath, Streets: *streets, BBox: bbox,
		LineAgencies: las,
		Out:          *out, Dials: &d,
	}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	die(err)
}

func sound(args []string) {
	fs := flag.NewFlagSet("sound", flag.ExitOnError)
	netPath := fs.String("network", "", "drawn network JSON (ground truth)")
	buildPath := fs.String("build", "", "build GeoJSON to grade")
	fs.Parse(args)
	if *netPath == "" || *buildPath == "" {
		fs.Usage()
		os.Exit(2)
	}
	res, err := pipeline.Sound(pipeline.SoundOpts{Network: *netPath, Build: *buildPath})
	die(err)
	res.Print()
	if res.Failures > 0 {
		os.Exit(1)
	}
}

func atlasCmd(args []string) {
	fs := flag.NewFlagSet("atlas", flag.ExitOnError)
	cfg := fs.String("config", "portolan.json", "workbench config (feeds, paths)")
	addr := fs.String("addr", "127.0.0.1:8765", "listen address")
	maplibre := fs.String("maplibre", "../maplibre-gl-js/dist",
		"MapLibre fork dist dir (variable line-offset build)")
	fs.Parse(args)
	srv, err := atlas.NewServer(*cfg, *maplibre)
	die(err)
	die(srv.ListenAndServe(*addr))
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "portolan:", err)
		os.Exit(1)
	}
}
