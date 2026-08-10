// portolan — automatic transit line maps from GTFS feeds.
//
//	portolan chart --gtfs feed.zip --rail rail.geojson --out build.geojson
//	portolan chart --gtfs feed.zip --corridors corridors.geojson --out build.geojson
//	portolan sound --network sketches/nyc.json --build build.geojson
//	portolan atlas [--config portolan.json] [--addr 127.0.0.1:8765]
//	portolan serve [--addr 127.0.0.1:0]   (prints the bound port on stdout)
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/atlas"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/pipeline"
	"github.com/alexwohlbruck/portolan/internal/serve"
	"github.com/alexwohlbruck/portolan/internal/style"
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
	case "scenarios":
		scenarios(os.Args[2:])
	case "atlas":
		atlasCmd(os.Args[2:])
	case "serve":
		serveCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: portolan chart|sound|scenarios|atlas|serve [flags] (see README.md)")
	os.Exit(2)
}

// scenarios lists a feed's derived service scenarios — the ids `chart
// --scenario` takes. Derivation is the pipeline's, so the ids match.
func scenarios(args []string) {
	fs := flag.NewFlagSet("scenarios", flag.ExitOnError)
	gtfsPath := fs.String("gtfs", "", "GTFS zip (comma list: primary,overlay,…)")
	routes := fs.Bool("routes", false, "list each scenario's route short names")
	fs.Parse(args)
	if *gtfsPath == "" {
		fs.Usage()
		os.Exit(2)
	}
	si, err := pipeline.LoadServiceInfo(*gtfsPath)
	if err != nil {
		die(err)
	}
	cover := pipeline.DefaultDials().Cover
	for _, sc := range gtfs.BuildScenarios(si, cover) {
		fmt.Printf("%s  %-44s %4d patterns\n", sc.ID, sc.Label, sc.Patterns)
		if *routes {
			names := map[string]bool{}
			for k := range si.Select(sc.Cells, cover) {
				names[k.Route] = true
			}
			list := make([]string, 0, len(names))
			for n := range names {
				list = append(list, n)
			}
			sort.Strings(list)
			fmt.Printf("    %s\n", strings.Join(list, " "))
		}
	}
}

func chart(args []string) {
	fs := flag.NewFlagSet("chart", flag.ExitOnError)
	gtfsPath := fs.String("gtfs", "", "GTFS zip (comma list: primary,overlay,…)")
	railPath := fs.String("rail", "", "OSM rail extract (GeoJSON) — portolan infers the corridors")
	corridors := fs.String("corridors", "",
		"authored corridor graph (GeoJSON, '-' for stdin) — corridors given, not inferred")
	corridorNodes := fs.String("corridor-nodes", "",
		"nodes half of the corridor graph, when it is split across two files")
	anchor := fs.String("anchor", "", "lat,lon — pin the projection origin (default: derived)")
	streets := fs.String("streets", "", "OSM street extract (GeoJSON) — enables bus routes")
	stops := fs.String("stops", "", "OSM transit-stop extract (GeoJSON) — station name/id matching")
	bboxStr := fs.String("bbox", "", "w,s,e,n — clip pattern shapes to the window")
	lineAg := fs.String("line-agencies", "", "comma list: regional agencies keeping per-line colors")
	out := fs.String("out", "build.geojson", "output GeoJSON")
	format := fs.String("format", "geojson", "geojson | bin (flat typed arrays, see docs/CORRIDORS.md)")
	band := fs.String("band", "", "emit one zoom band only: 15 | 14 | 13 | 0 (default: the union)")
	cover := fs.Float64("cover", 0.99, "pattern trip-coverage fraction")
	scenario := fs.String("scenario", "", "service scenario id (see `portolan scenarios`)")
	styleDir := fs.String("style-dir", style.DefaultDir, "curation documents: <dir>/_default.json + <dir>/<city>.json")
	city := fs.String("city", "", "city id — selects <style-dir>/<city>.json")
	stylePath := fs.String("style", "", "single pre-merged style document (overrides --style-dir)")
	fs.Parse(args)
	// exactly one geometry source: either portolan works the corridor
	// graph out from track, or the caller states it
	switch {
	case *railPath == "" && *corridors == "":
		fmt.Fprintln(os.Stderr, "portolan chart: give --rail (infer corridors) or --corridors (corridors given)")
		fs.Usage()
		os.Exit(2)
	case *railPath != "" && *corridors != "":
		die(fmt.Errorf("--rail and --corridors are alternatives, not a pair"))
	}
	var anchorLL *geo.LL
	if *anchor != "" {
		var lat, lon float64
		if _, err := fmt.Sscanf(*anchor, "%f,%f", &lat, &lon); err != nil {
			die(fmt.Errorf("bad --anchor %q: want lat,lon", *anchor))
		}
		anchorLL = &geo.LL{Lat: lat, Lon: lon}
	}
	var bandPtr *int
	if *band != "" {
		b, err := pipeline.ParseBand(*band)
		die(err)
		bandPtr = &b
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
	// Curation resolves through the SAME loader the atlas uses, so a CLI
	// build and a dashboard build of one city cannot disagree.
	var sty *style.Set
	if *stylePath != "" {
		d, _, err := style.ReadDoc(*stylePath)
		die(err)
		sty = style.New(d.Config())
		if len(d.LineAgencies()) > 0 && len(las) == 0 {
			las = d.LineAgencies()
		}
	} else {
		set, dirLas, err := style.LoadDir(*styleDir, *city)
		die(err)
		sty = set
		if len(las) == 0 {
			las = dirLas
		}
	}
	d := pipeline.DefaultDials()
	d.Cover = *cover
	err := pipeline.Chart(pipeline.ChartOpts{
		GTFS: *gtfsPath, Rail: *railPath, Streets: *streets, Stops: *stops, BBox: bbox,
		Corridors: *corridors, CorridorNodes: *corridorNodes, Anchor: anchorLL,
		LineAgencies: las, Scenario: *scenario, Style: sty,
		Out: *out, Format: *format, Band: bandPtr, Dials: &d,
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

func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:0", "listen address (:0 picks a free port)")
	styleDir := fs.String("style-dir", style.DefaultDir, "curation documents")
	fs.Parse(args)
	die(serve.New(*styleDir).ListenAndServe(*addr))
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
