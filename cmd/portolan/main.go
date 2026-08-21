// portolan — automatic transit line maps from GTFS feeds.
//
// The command surface is documented for humans in docs/CLI.md and, in
// the terminal, by `portolan help` and `portolan help <command>`. This
// file's job is to keep those two agreeing: every command is one row of
// the table below, carrying its own summary, usage lines, examples and
// flag grouping, so a command cannot be added without being documented
// or documented without existing.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexwohlbruck/portolan/internal/atlas"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/pipeline"
	"github.com/alexwohlbruck/portolan/internal/registry"
	"github.com/alexwohlbruck/portolan/internal/serve"
	"github.com/alexwohlbruck/portolan/internal/style"
	"github.com/alexwohlbruck/portolan/internal/sync"
	"github.com/alexwohlbruck/portolan/internal/tiles"
)

// command is one verb. The help text is data, not a switch statement, so
// `portolan help` and the dispatcher read the same list.
type command struct {
	name    string
	summary string   // one line, shown in the command list
	usage   []string // usage lines, without the leading "portolan "
	about   string   // a paragraph or two, shown by `help <command>`
	example []string
	// groups orders the flags for help output: {heading, flag names}.
	// Anything not listed is printed last under "other", so a forgotten
	// flag is visible rather than hidden.
	groups []flagGroup
	// flags registers the command's flags; run does the work.
	flags func(*flag.FlagSet) any
	run   func(*flag.FlagSet, any)
	// raw, when set, takes over argument handling entirely — for a
	// command whose first argument is a verb, not a flag (sync
	// check|patch|global). printHelp still reads flags as usual.
	raw func(args []string)
}

type flagGroup struct {
	head  string
	names []string
}

var commands []*command

func init() {
	commands = []*command{chartCmd, soundCmd, scenariosCmd, tilesCmd, syncCmd, atlasCmd, serveCmd}
	// wired here, not in the literal: runSync prints syncCmd's help, and
	// the compiler calls that reference an initialization cycle
	syncCmd.raw = runSync
}

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stdout)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "help", "-h", "--help":
		if len(os.Args) > 2 {
			c := find(os.Args[2])
			if c == nil {
				fmt.Fprintf(os.Stderr, "portolan: no command %q\n", os.Args[2])
				printUsage(os.Stderr)
				os.Exit(2)
			}
			c.printHelp(os.Stdout)
			return
		}
		printUsage(os.Stdout)
		return
	case "version", "-v", "--version":
		fmt.Println(versionString())
		return
	}
	c := find(os.Args[1])
	if c == nil {
		fmt.Fprintf(os.Stderr, "portolan: no command %q\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
	c.exec(os.Args[2:])
}

func find(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return nil
}

// exec parses and runs. Parsing uses ContinueOnError so that an explicit
// -h prints help to STDOUT and exits 0 — it is a successful request for
// help, not a usage error — while a genuine mistake goes to stderr with
// a pointer to the right help page and exits 2.
func (c *command) exec(args []string) {
	if c.raw != nil {
		c.raw(args)
		return
	}
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := c.flags(fs)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			c.printHelp(os.Stdout)
			return
		}
		fmt.Fprintf(os.Stderr, "portolan %s: %v\n", c.name, err)
		fmt.Fprintf(os.Stderr, "try: portolan help %s\n", c.name)
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "portolan %s: unexpected argument %q — flags only\n",
			c.name, fs.Arg(0))
		fmt.Fprintf(os.Stderr, "try: portolan help %s\n", c.name)
		os.Exit(2)
	}
	c.run(fs, cfg)
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, "portolan — automatic transit line maps from GTFS feeds\n\n")
	fmt.Fprint(w, "usage: portolan <command> [flags]\n\ncommands:\n")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(w, "  %-10s %s\n", "version", "print the build version")
	fmt.Fprintf(w, "  %-10s %s\n", "help", "this, or `portolan help <command>` for one command")
	fmt.Fprint(w, `
examples:
  portolan chart --gtfs nyc.zip --rail nyc-rail.geojson --out nyc.geojson
  portolan chart --gtfs feed.zip --corridors corridors.geojson --out build.geojson
  portolan sound --network sketches/network-5.json --build nyc.geojson
  portolan atlas

docs:
  README.md            what portolan is, and how to try it
  docs/CLI.md          every command and flag, in full
  docs/CORRIDORS.md    charting a network whose geometry you already have
  docs/CITIES.md       adding a city
`)
}

func (c *command) printHelp(w io.Writer) {
	fmt.Fprintf(w, "portolan %s — %s\n\nusage:\n", c.name, c.summary)
	for _, u := range c.usage {
		fmt.Fprintf(w, "  portolan %s\n", u)
	}
	if c.about != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(c.about))
	}
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c.flags(fs)

	seen := map[string]bool{}
	printFlag := func(f *flag.Flag) {
		def := f.DefValue
		switch {
		case def == "":
			def = ""
		case def == "false":
			def = ""
		default:
			def = fmt.Sprintf(" (default %s)", def)
		}
		fmt.Fprintf(w, "  --%-16s %s%s\n", f.Name, f.Usage, def)
	}
	for _, g := range c.groups {
		fmt.Fprintf(w, "\n%s:\n", g.head)
		for _, n := range g.names {
			if f := fs.Lookup(n); f != nil {
				printFlag(f)
				seen[n] = true
			}
		}
	}
	var rest []*flag.Flag
	fs.VisitAll(func(f *flag.Flag) {
		if !seen[f.Name] {
			rest = append(rest, f)
		}
	})
	if len(rest) > 0 {
		// "other" only means something next to named groups; a command
		// that never grouped its flags just has flags
		head := "flags"
		if len(c.groups) > 0 {
			head = "other"
		}
		fmt.Fprintf(w, "\n%s:\n", head)
		for _, f := range rest {
			printFlag(f)
		}
	}
	if len(c.example) > 0 {
		fmt.Fprint(w, "\nexamples:\n")
		for _, e := range c.example {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}
}

// version is stamped by the release build:
//
//	go build -ldflags "-X main.version=$(cat VERSION)"
//
// A build without it says "devel" rather than claiming a release number
// it does not have — `portolan version` is how a bug report says which
// binary it came from, so it must never be optimistic.
var version = ""

// releaseVersion is the bare semver a release was stamped with, or ""
// for an unstamped build. /version reports it, and a supervising process
// compares it against the contract its renderer speaks — so it must be
// the number alone, with no "v", no revision and no decoration.
func releaseVersion() string { return version }

// versionString reports the release version, plus the VCS revision the
// binary was built from when the toolchain recorded one.
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		if version != "" {
			return "portolan v" + version
		}
		return "portolan (unknown build)"
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	v := bi.Main.Version
	if version != "" {
		v = "v" + version // the release stamp wins over the module version
	} else if v == "" || v == "(devel)" {
		v = "devel"
	}
	if rev == "" {
		return "portolan " + v
	}
	return fmt.Sprintf("portolan %s (%s%s)", v, rev, dirty)
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "portolan:", err)
		os.Exit(1)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "portolan: "+format+"\n", a...)
	os.Exit(2)
}

// ---------------------------------------------------------------- chart

type chartFlags struct {
	gtfs, rail, corridors, corridorNodes  string
	streets, stops, bbox, anchor          string
	excludeBBox                           string
	out, format, band                     string
	styleDir, city, stylePath, lineAgency string
	scenario                              string
	exportGTFS                            string
	onestop                               string
	set                                   string
	allowUnmatched                        bool
	cover                                 float64
}

var chartCmd = &command{
	name:    "chart",
	summary: "build a city's map geometry from a feed plus track",
	usage: []string{
		"chart --gtfs <feed.zip> --rail <track.geojson> [flags]",
		"chart --gtfs <feed.zip> --corridors <graph.geojson> [flags]",
	},
	about: `Exactly one geometry source is required, and they are alternatives.
--rail hands portolan raw OpenStreetMap track and it works the corridors
out; --corridors takes a corridor graph you already have, skipping the
inference entirely (docs/CORRIDORS.md).

Writes <out>, plus <out>.stations.geojson, <out>.style.json and the
<out>.trackcenter.geojson / <out>.nodes.geojson pair, which is itself a
valid --corridors input.

--gtfs may be omitted with --rail, which dumps the bundled track strands
and stops: a quick check that an OSM extract is usable, with no map.`,
	groups: []flagGroup{
		{"inputs", []string{"gtfs", "rail", "corridors", "corridor-nodes", "streets", "stops"}},
		{"window and projection", []string{"bbox", "exclude-bbox", "anchor"}},
		{"output", []string{"out", "format", "band", "export-gtfs", "onestop", "set", "allow-unmatched"}},
		{"curation", []string{"style-dir", "feed", "style", "line-agencies"}},
		{"service", []string{"scenario", "cover"}},
	},
	example: []string{
		"portolan chart --gtfs nyc.zip --rail nyc-rail.geojson --out nyc.geojson",
		"portolan chart --gtfs mta.zip --rail nyc-rail.geojson --style-dir style --feed mta-subway --out mta-subway.geojson",
		"portolan chart --gtfs feed.zip --corridors build.geojson.trackcenter.geojson \\",
		"    --corridor-nodes build.geojson.nodes.geojson --out rebuilt.geojson",
		"portolan chart --gtfs nyc.zip --rail nyc-rail.geojson --format bin --band 15 --out nyc15.bin",
		"portolan chart --gtfs nyc.zip --rail nyc-rail.geojson --scenario sun-03 --out nyc-night.geojson",
	},
	flags: func(fs *flag.FlagSet) any {
		c := &chartFlags{}
		fs.StringVar(&c.gtfs, "gtfs", "", "GTFS zip or directory; comma list for overlay feeds")
		fs.StringVar(&c.rail, "rail", "", "OSM rail extract (GeoJSON) — portolan infers the corridors")
		fs.StringVar(&c.corridors, "corridors", "", "corridor graph you already have (GeoJSON, - for stdin)")
		fs.StringVar(&c.corridorNodes, "corridor-nodes", "", "nodes half of the corridor graph, if split across two files")
		fs.StringVar(&c.streets, "streets", "", "OSM street extract (GeoJSON) — enables bus routes")
		fs.StringVar(&c.stops, "stops", "", "OSM transit-stop extract (GeoJSON) — station names and ids")
		fs.StringVar(&c.bbox, "bbox", "", "clip to a window: w,s,e,n (LONGITUDE first)")
		fs.StringVar(&c.excludeBBox, "exclude-bbox", "", "cut these windows OUT: w,s,e,n[;w,s,e,n...] — territory a group build draws")
		fs.StringVar(&c.anchor, "anchor", "", "pin the projection origin: lat,lon (LATITUDE first)")
		fs.StringVar(&c.out, "out", "", "output path (default build.geojson, or build.bin with --format bin)")
		fs.StringVar(&c.format, "format", "geojson", "geojson | bin — see docs/CLI.md for the binary layout")
		fs.StringVar(&c.band, "band", "", "emit one zoom band: 15 | 14 | 13 | 0 (default: all four)")
		fs.StringVar(&c.styleDir, "style-dir", style.DefaultDir, "curation directory: <dir>/_default.json + <dir>/<city>.json")
		fs.StringVar(&c.city, "feed", "", "feed key — selects <style-dir>/<feed>.json curation")
		fs.StringVar(&c.stylePath, "style", "", "one pre-merged curation document (overrides --style-dir)")
		fs.StringVar(&c.lineAgency, "line-agencies", "", "comma list: regional agencies keeping per-line colours")
		fs.StringVar(&c.scenario, "scenario", "", "build one service scenario (see `portolan scenarios`)")
		fs.StringVar(&c.exportGTFS, "export-gtfs", "", "directory: write the source feeds back out with matched shapes.txt")
		fs.StringVar(&c.onestop, "onestop", "", "onestop id per source zip, by basename: key=f-…[,key=f-…] — stations carry gtfs_ids")
		fs.StringVar(&c.set, "set", "", "tuning overrides: key=val[,key=val] over the defaults (keys as in the atlas tuning panel, e.g. join_tol=120)")
		fs.BoolVar(&c.allowUnmatched, "allow-unmatched", false, "ship rail patterns that failed to path-match (default: the build fails)")
		fs.Float64Var(&c.cover, "cover", 0.99, "pattern trip-coverage fraction")
		return c
	},
	run: func(fs *flag.FlagSet, cfg any) { runChart(cfg.(*chartFlags)) },
}

func runChart(c *chartFlags) {
	// PORTOLAN_HEAPPROF=<path>: overwrite a heap profile there every 3 s
	// (forcing a GC first, so it shows LIVE bytes). The last write before
	// an OOM kill names the allocation site — how the continental-scale
	// memory blowups were pinned. Debug family, like PORTOLAN_DBG*.
	if hp := os.Getenv("PORTOLAN_HEAPPROF"); hp != "" {
		go func() {
			for {
				time.Sleep(3 * time.Second)
				if f, err := os.Create(hp); err == nil {
					runtime.GC()
					pprof.WriteHeapProfile(f)
					f.Close()
				}
			}
		}()
	}
	// Every input is checked BEFORE the build starts. A typo in --format
	// used to surface at the emit, after a full NYC build had already
	// run: fifteen seconds to be told about a misspelt word.
	switch {
	case c.rail == "" && c.corridors == "":
		fail("chart needs geometry: --rail (infer the corridors) or --corridors (supply them)\n" +
			"try: portolan help chart")
	case c.rail != "" && c.corridors != "":
		fail("--rail and --corridors are alternatives, not a pair")
	}
	if c.corridors != "" && c.gtfs == "" {
		fail("--corridors needs --gtfs: the graph names routes by route_id, " +
			"and routes.txt is what those ids mean")
	}
	switch c.format {
	case "geojson", "bin":
	default:
		fail("--format %q: want geojson or bin", c.format)
	}
	if c.corridorNodes != "" && c.corridors == "" {
		fail("--corridor-nodes is the second half of --corridors, which was not given")
	}

	out := c.out
	if out == "" {
		// the default name follows the format: "build.geojson" holding
		// a binary payload is a lie a caller then has to debug
		out = "build.geojson"
		if c.format == "bin" {
			out = "build.bin"
		}
	}

	var bandPtr *int
	if c.band != "" {
		b, err := pipeline.ParseBand(c.band)
		if err != nil {
			fail("%v", err) // a bad flag is a usage error (2), not a build failure (1)
		}
		bandPtr = &b
	}
	// bbox is w,s,e,n and anchor is lat,lon. The orders genuinely differ:
	// a bbox is GeoJSON's, longitude first, and an anchor is the order
	// every map UI hands you when you copy a location. Both say so in
	// their help text, and both are validated here rather than misread.
	var bbox []float64
	if c.bbox != "" {
		for _, p := range strings.Split(c.bbox, ",") {
			v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				fail("bad --bbox %q: want w,s,e,n as numbers", c.bbox)
			}
			bbox = append(bbox, v)
		}
		if len(bbox) != 4 {
			fail("--bbox wants four numbers, w,s,e,n — got %d", len(bbox))
		}
	}
	var exclude [][]float64
	if c.excludeBBox != "" {
		for _, win := range strings.Split(c.excludeBBox, ";") {
			if strings.TrimSpace(win) == "" {
				continue
			}
			var b []float64
			for _, p := range strings.Split(win, ",") {
				v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
				if err != nil {
					fail("bad --exclude-bbox window %q: want w,s,e,n as numbers", win)
				}
				b = append(b, v)
			}
			if len(b) != 4 {
				fail("--exclude-bbox window wants four numbers, w,s,e,n — got %d in %q", len(b), win)
			}
			exclude = append(exclude, b)
		}
	}
	var anchorLL *geo.LL
	if c.anchor != "" {
		parts := strings.Split(c.anchor, ",")
		if len(parts) != 2 {
			fail("--anchor wants two numbers, lat,lon — got %q", c.anchor)
		}
		lat, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		lon, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if e1 != nil || e2 != nil {
			fail("bad --anchor %q: want lat,lon as numbers", c.anchor)
		}
		anchorLL = &geo.LL{Lat: lat, Lon: lon}
	}

	var las []string
	if c.lineAgency != "" {
		las = strings.Split(c.lineAgency, ",")
	}
	// --onestop: zip basename (sans .zip) → onestop id. Sync fills the
	// same map from the registry; this flag is the by-hand route.
	var onestop map[string]string
	if c.onestop != "" {
		onestop = map[string]string{}
		for _, kv := range strings.Split(c.onestop, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
			if !ok || k == "" || v == "" {
				fail("--onestop wants key=onestop-id[,key=onestop-id]\ntry: portolan help chart")
			}
			onestop[strings.TrimSuffix(k, ".zip")] = v
		}
	}
	// Curation resolves through the SAME loader the atlas uses, so a CLI
	// build and a dashboard build of one city cannot disagree.
	var sty *style.Set
	if c.stylePath != "" {
		d, _, err := style.ReadDoc(c.stylePath)
		die(err)
		sty = style.New(d.Config())
		if len(d.LineAgencies()) > 0 && len(las) == 0 {
			las = d.LineAgencies()
		}
	} else {
		// --feed takes a comma list for group builds: members in gtfs
		// order, the group's own document last
		set, dirLas, err := style.LoadDir(c.styleDir, strings.Split(c.city, ",")...)
		die(err)
		sty = set
		if len(las) == 0 {
			las = dirLas
		}
	}
	d := pipeline.DefaultDials()
	d.Cover = c.cover
	// --set rides through the same flat-json keys the atlas tuning panel
	// uses, so the two surfaces cannot disagree about a dial's name
	if c.set != "" {
		raw, _ := json.Marshal(d)
		m := map[string]float64{}
		die(json.Unmarshal(raw, &m))
		for _, kv := range strings.Split(c.set, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
			if !ok {
				fail("--set wants key=val[,key=val]\ntry: portolan help chart")
			}
			if _, known := m[k]; !known {
				fail("--set: unknown dial " + k + "\ntry: portolan help chart")
			}
			f, err := strconv.ParseFloat(v, 64)
			die(err)
			m[k] = f
		}
		raw, _ = json.Marshal(m)
		die(json.Unmarshal(raw, &d))
	}
	die(pipeline.Chart(pipeline.ChartOpts{
		GTFS: c.gtfs, Rail: c.rail, Streets: c.streets, Stops: c.stops, BBox: bbox, Exclude: exclude,
		Corridors: c.corridors, CorridorNodes: c.corridorNodes, Anchor: anchorLL,
		LineAgencies: las, Scenario: c.scenario, Style: sty, ExportGTFS: c.exportGTFS, Onestop: onestop, AllowUnmatched: c.allowUnmatched,
		Out: out, Format: c.format, Band: bandPtr, Dials: &d,
	}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }))
}

// ---------------------------------------------------------------- sound

type soundFlags struct{ network, build string }

var soundCmd = &command{
	name:    "sound",
	summary: "score a build against a hand-drawn reference network",
	usage:   []string{"sound --network <reference.json> --build <build.geojson>"},
	about: `Compares a build against a network drawn by hand in the atlas sketch
editor: how far the drawn ink sits from the reference, how much of the
reference is covered, whether ribbons duplicate each other, and how
jagged the result is.

Exits 0 when every gate passes and 1 when any gate fails, so it can be
the check in a CI job or a Makefile.`,
	example: []string{
		"portolan sound --network sketches/network-5.json --build build/nyc.geojson",
	},
	flags: func(fs *flag.FlagSet) any {
		s := &soundFlags{}
		fs.StringVar(&s.network, "network", "", "drawn reference network (JSON)")
		fs.StringVar(&s.build, "build", "", "build GeoJSON to grade")
		return s
	},
	run: func(fs *flag.FlagSet, cfg any) {
		s := cfg.(*soundFlags)
		if s.network == "" || s.build == "" {
			fail("sound needs --network and --build\ntry: portolan help sound")
		}
		res, err := pipeline.Sound(pipeline.SoundOpts{Network: s.network, Build: s.build})
		die(err)
		res.Print()
		if res.Failures > 0 {
			os.Exit(1)
		}
	},
}

// ------------------------------------------------------------ scenarios

type scenarioFlags struct {
	gtfs   string
	routes bool
}

var scenariosCmd = &command{
	name:    "scenarios",
	summary: "list a feed's time-of-day service scenarios",
	usage:   []string{"scenarios --gtfs <feed.zip> [--routes]"},
	about: `Prints the scenario ids that chart --scenario takes: the distinct
service pictures a feed contains, such as a weekday peak, a Sunday
daytime and an overnight network. Derivation is the pipeline's own, so
the ids match what chart will accept.`,
	example: []string{
		"portolan scenarios --gtfs nyc.zip",
		"portolan scenarios --gtfs nyc.zip --routes",
	},
	flags: func(fs *flag.FlagSet) any {
		s := &scenarioFlags{}
		fs.StringVar(&s.gtfs, "gtfs", "", "GTFS zip; comma list for overlay feeds")
		fs.BoolVar(&s.routes, "routes", false, "also list each scenario's route short names")
		return s
	},
	run: func(fs *flag.FlagSet, cfg any) {
		s := cfg.(*scenarioFlags)
		if s.gtfs == "" {
			fail("scenarios needs --gtfs\ntry: portolan help scenarios")
		}
		si, err := pipeline.LoadServiceInfo(s.gtfs)
		die(err)
		cover := pipeline.DefaultDials().Cover
		for _, sc := range gtfs.BuildScenarios(si, cover) {
			fmt.Printf("%s  %-44s %4d patterns\n", sc.ID, sc.Label, sc.Patterns)
			if s.routes {
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
	},
}

// ---------------------------------------------------------------- atlas

type atlasFlags struct{ config, addr, maplibre string }

var atlasCmd = &command{
	name:    "atlas",
	summary: "run the workbench: map, rebuild, tuning, sketch editor",
	usage:   []string{"atlas [--config portolan.json] [--addr 127.0.0.1:8765]"},
	about: `Serves the workbench, which is the main way to use portolan: a city
picker, a live map, a rebuild button, a time-of-day slider, a drawing
tool for reference networks and a panel of tuning dials.

Cities come from the config file, one entry each — there is no
city-specific code anywhere in the repo (docs/CITIES.md).

Even spacing across a continuous zoom range needs a MapLibre build with
variable line offsets; point --maplibre at its dist directory. Stock
MapLibre renders the same build with fixed, pre-baked offsets.`,
	example: []string{
		"portolan atlas",
		"portolan atlas --addr 127.0.0.1:9000 --maplibre ../maplibre-gl-js/dist",
	},
	flags: func(fs *flag.FlagSet) any {
		a := &atlasFlags{}
		fs.StringVar(&a.config, "config", "portolan.json", "workbench config (feeds, paths)")
		fs.StringVar(&a.addr, "addr", "127.0.0.1:8765", "listen address")
		fs.StringVar(&a.maplibre, "maplibre", "../maplibre-gl-js/dist",
			"MapLibre fork dist dir (variable line-offset build)")
		return a
	},
	run: func(fs *flag.FlagSet, cfg any) {
		a := cfg.(*atlasFlags)
		srv, err := atlas.NewServer(a.config, a.maplibre)
		die(err)
		die(srv.ListenAndServe(a.addr))
	},
}

// ---------------------------------------------------------------- serve

type serveFlags struct{ addr, styleDir, token string }

var serveCmd = &command{
	name:    "serve",
	summary: "run the build server: HTTP in, streamed progress out",
	usage:   []string{"serve [--addr 127.0.0.1:0]"},
	about: `A long-lived process that charts on request, for a client that
rebuilds interactively. The bound port is printed on stdout as the first
line, so a supervising process can read it back from a :0 request rather
than guessing a free one.

  POST /chart               the same inputs as chart, as JSON -> {"id": …}
  GET  /chart/{id}/progress server-sent events: stage name and 0-100
  POST /chart/{id}/cancel   abandon a build in flight
  GET  /chart/{id}/build    the artifacts, ?artifact=stations|style|…
  GET  /version             version and the formats this binary speaks

--token is worth setting whenever anything else on the machine might
reach the port. A request body names files to read (gtfs, style_dir,
corridors), so an open port is a file-read oracle for every local
process; loopback is not a boundary between processes on one host.
/healthz and /version stay open so a supervisor can tell "not up yet"
from "wrong token".

Builds are serialized: portolan's build configuration is still process
state, so two at once would read each other's colours and dials. See
docs/CLI.md for the request body and the endpoints in full.`,
	example: []string{
		"portolan serve --addr 127.0.0.1:0",
		"portolan serve --addr 127.0.0.1:0 --token $(openssl rand -hex 16)",
		`curl -s localhost:$PORT/chart -d '{"gtfs":"nyc.zip","rail":"nyc-rail.geojson"}'`,
	},
	flags: func(fs *flag.FlagSet) any {
		s := &serveFlags{}
		fs.StringVar(&s.addr, "addr", "127.0.0.1:0", "listen address (:0 picks a free port)")
		fs.StringVar(&s.styleDir, "style-dir", style.DefaultDir, "curation directory for requests that name a feed")
		fs.StringVar(&s.token, "token", "", "require `Authorization: Bearer <token>` on every request")
		return s
	},
	run: func(fs *flag.FlagSet, cfg any) {
		s := cfg.(*serveFlags)
		srv := serve.New(s.styleDir)
		srv.Version = releaseVersion()
		srv.Token = s.token
		die(srv.ListenAndServe(s.addr))
	},
}

// ------------------------------------------------------------ tiles

type tilesFlags struct {
	build   string
	out     string
	name    string
	maxzoom int
}

var tilesCmd = &command{
	name:    "tiles",
	summary: "slice a build into a z/x/y vector-tile pyramid",
	usage:   []string{"tiles --build <build.geojson> --out <dir>"},
	about: `Slices the output fan of a chart run into Mapbox Vector Tiles, the
delivery format for region- and world-scale maps: the viewer streams
only the tiles under its viewport instead of holding the whole document.

The geometry is still solved as one graph — tiling happens after the
fact, so there are no seams. Each zoom level carries exactly the FAIR
band the viewer would draw there (band 0 fills z0-12, then 13, 14, and
15 which overzooms upward). The .stations.geojson sibling is picked up
automatically; stations and bundle markers enter at z11, caterpillar
bullets at z12, matching the viewer's own symbol floors. A tiles.json
(TileJSON 3.0) is written beside the pyramid.`,
	example: []string{
		"portolan tiles --build build/northeast.geojson --out build/tiles/northeast --name northeast",
	},
	flags: func(fs *flag.FlagSet) any {
		t := &tilesFlags{}
		fs.StringVar(&t.build, "build", "", "ribbon GeoJSON from chart (stations sibling auto-detected)")
		fs.StringVar(&t.out, "out", "", "output directory for {z}/{x}/{y}.mvt and tiles.json")
		fs.StringVar(&t.name, "name", "", "tileset name for tiles.json (defaults to the build's basename)")
		fs.IntVar(&t.maxzoom, "maxzoom", 18, "top of the pyramid; the renderer overzooms beyond it")
		return t
	},
	run: func(fs *flag.FlagSet, cfg any) {
		t := cfg.(*tilesFlags)
		if t.build == "" || t.out == "" {
			fail("tiles needs --build and --out\ntry: portolan help tiles")
		}
		if t.name == "" {
			t.name = strings.TrimSuffix(filepath.Base(t.build), ".geojson")
		}
		st, err := tiles.Build(tiles.Opts{Build: t.build, Out: t.out, MaxZoom: t.maxzoom, Name: t.name})
		die(err)
		fmt.Printf("%d tiles written (%.1f MB), %d unchanged, %d pruned → %s\n",
			st.Tiles, float64(st.Bytes)/1e6, st.Unchanged, st.Removed, t.out)
	},
}

// ------------------------------------------------------------ sync

type syncFlags struct {
	config, data, build, tiles, exportGTFS string
	state, styleDir, feeds                 string
	jobs                                   int
	dryRun, jsonOut                        bool
}

var syncCmd = &command{
	name:    "sync",
	summary: "reconcile the feed fleet against upstream (docs/SYNC.md)",
	usage: []string{
		"sync check  --config portolan.json [flags]",
		"sync patch  --config portolan.json --feeds key1,key2 [flags]",
		"sync global --config portolan.json [flags]",
	},
	about: `Keeps a fleet of feeds current. check asks transitland which feeds
moved (by the registry's onestop ids), diffs against the state manifest,
and downloads what changed into --data. patch rebuilds exactly the
builds whose inputs changed; global is the same executor with every
feed in the changed set — the oracle patch must match. patch and global
are not yet implemented; check works today.

TRANSITLAND_API_KEY is read from the environment. Feed entries without
an onestop id are reported and skipped. A new upstream sha over
identical content records the sha and rebuilds nothing — the content
hash is the identity that matters (docs/SYNC.md).`,
	groups: []flagGroup{
		{"inputs", []string{"config", "data", "feeds"}},
		{"outputs", []string{"build", "tiles", "export-gtfs", "state", "style-dir"}},
		{"run", []string{"jobs", "dry-run", "json"}},
	},
	example: []string{
		"portolan sync check --config portolan.json --dry-run",
		"portolan sync check --config portolan.json --json",
		"portolan sync patch --config portolan.json --feeds mta-subway",
	},
	flags: func(fs *flag.FlagSet) any {
		s := &syncFlags{}
		fs.StringVar(&s.config, "config", "portolan.json", "feed registry")
		fs.StringVar(&s.data, "data", "data/gtfs", "where GTFS zips live / are downloaded")
		fs.StringVar(&s.build, "build", "build", "build fan output dir")
		fs.StringVar(&s.tiles, "tiles", "build/tiles", "tile pyramids + index.json")
		fs.StringVar(&s.exportGTFS, "export-gtfs", "build/export", "corrected GTFS zips (empty = skip export)")
		fs.StringVar(&s.state, "state", "", "state manifest (default <build>/sync-state.json)")
		fs.StringVar(&s.styleDir, "style-dir", style.DefaultDir, "curation directory")
		fs.StringVar(&s.feeds, "feeds", "", "comma list of feed keys (patch only)")
		fs.IntVar(&s.jobs, "jobs", runtime.NumCPU(), "parallel feed builds")
		fs.BoolVar(&s.dryRun, "dry-run", false, "plan only: print what would happen, change nothing")
		fs.BoolVar(&s.jsonOut, "json", false, "final stdout line is RESULT {…} for a supervising process")
		return s
	},
	// raw is runSync, assigned in init() to break an initialization cycle
}

func runSync(args []string) {
	if len(args) == 0 {
		fail("sync needs a subcommand: check, patch or global\ntry: portolan help sync")
	}
	sub := args[0]
	switch sub {
	case "help", "-h", "--help":
		syncCmd.printHelp(os.Stdout)
		return
	case "check", "patch", "global":
	default:
		fail("sync %q: want check, patch or global\ntry: portolan help sync", sub)
	}
	fs := flag.NewFlagSet("sync "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sf := syncCmd.flags(fs).(*syncFlags)
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			syncCmd.printHelp(os.Stdout)
			return
		}
		fmt.Fprintf(os.Stderr, "portolan sync %s: %v\n", sub, err)
		fmt.Fprintf(os.Stderr, "try: portolan help sync\n")
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "portolan sync %s: unexpected argument %q — flags only\n", sub, fs.Arg(0))
		fmt.Fprintf(os.Stderr, "try: portolan help sync\n")
		os.Exit(2)
	}
	if sub == "patch" && sf.feeds == "" {
		fail("sync patch needs --feeds: the keys whose zips changed\ntry: portolan help sync")
	}
	if sub != "patch" && sf.feeds != "" {
		fail("--feeds is a patch flag — %s finds the changed set itself", sub)
	}
	if sf.state == "" {
		sf.state = filepath.Join(sf.build, "sync-state.json")
	}
	if sub == "check" {
		runSyncCheck(sf)
		return
	}
	runSyncStub(sub, sf)
}

func runSyncCheck(sf *syncFlags) {
	cfg, err := registry.Load(sf.config)
	die(err)
	res, err := sync.Check(sync.CheckOpts{
		Config:    cfg,
		StatePath: sf.state,
		DataDir:   sf.data,
		Client:    sync.NewClient(os.Getenv("TRANSITLAND_API_KEY")),
		DryRun:    sf.dryRun,
		Log:       func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	})
	die(err)
	fmt.Printf("%d changed, %d skipped, %d errors\n",
		len(res.Changed), len(res.Skipped), len(res.Errors))
	if sf.jsonOut {
		b, _ := json.Marshal(res)
		fmt.Printf("RESULT %s\n", b)
	}
	if len(res.Errors) > 0 {
		os.Exit(1)
	}
}

// runSyncStub: patch and global parse their flags today and land in a
// later phase; --dry-run says what the run would have done, so the
// operator can see the plan before the executor exists.
func runSyncStub(sub string, sf *syncFlags) {
	if sf.dryRun {
		scope := "every registered feed"
		if sub == "patch" {
			scope = "feeds " + sf.feeds
		}
		fmt.Printf("sync %s would: take %s as the changed set, measure the affected\n", sub, scope)
		fmt.Printf("closure (shared steel, groups, overlays — docs/SYNC.md), rebuild those\n")
		fmt.Printf("builds into %s, retile into %s, export corrected GTFS\n", sf.build, sf.tiles)
		fmt.Printf("into %s, and record each stage in %s\n", sf.exportGTFS, sf.state)
	}
	fmt.Fprintf(os.Stderr, "portolan: sync %s is not yet implemented — sync check works today\n", sub)
	os.Exit(1)
}
