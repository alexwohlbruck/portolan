# portolan

**Automatic transit line maps from GTFS feeds.**

A *portolan* is a medieval nautical chart: a web of hand-drawn rhumb lines
connecting harbors, drawn by people who cared that every line was exactly
where a sailor needed it to be. That is the bar for this tool — auto-generated
maps with hand-drawn quality. Interlined routes render as clean parallel
ribbons that stay equidistant at every zoom, split and merge with smooth
curves at junctions, and sit precisely on the centerline of the physical
track bundle they ride.

This is the third attempt at [PAR-12](docs/PAR-12.md) (see
[docs/LESSONS.md](docs/LESSONS.md) for why the first two taught us everything
we needed and why neither survived). It is a standalone tool: GTFS zip in,
map out. No database required to build a city.

## The pitch

```
gtfs.zip + rail.geojson  ──►  portolan chart  ──►  segments.geojson / tiles
                              portolan sound  ──►  score vs hand-drawn ground truth
                              portolan atlas  ──►  dev server: sketch editor, viewer, tuner
```

- **Input**: any GTFS static feed, plus an OSM rail extract for the covered
  area (one Overpass query; documented below).
- **Output**: line-map geometry — per-route ribbons with slot ordering and
  junction transitions, ready for MapLibre with the variable line-offset fork
  (continuous-zoom interlining, the PAR-12 requirement) or for baked-offset
  rendering.
- **Speed**: whole-city rebuilds in seconds, not minutes. The edit-rebuild-look
  loop is the product.

## Why Go

The previous pipeline was Python: a full NYC build took ~4 minutes, which
made tuning agonizing. The requirement is *iteration speed* — both compile
time and run time. Go compiles this whole repo in well under a second, runs
this workload within a small factor of C, needs no runtime setup, ships the
dev server and the build as one static binary, and has zip/CSV/JSON/HTTP/PNG
in the standard library (GTFS is a zip of CSVs; the tools are an HTTP server).
Rust was considered and rejected for cold-compile times and iteration friction
on graph code; Zig for ecosystem thinness (we'd hand-roll zip, CSV, HTTP).
If profiling ever demands it, the geometry core is deliberately simple,
allocation-conscious code that ports to Zig/Rust mechanically — but a city
build in Go should land in low single-digit seconds, so it won't.

## The algorithm

Seven small stages, all exact vector geometry, **no raster step anywhere**,
and no repair passes. Read [docs/ALGORITHM.md](docs/ALGORITHM.md) — it is the
heart of the project. The one-paragraph version:

> Sample cross-sections along every track; two tracks bundle where they stay
> mutually parallel for a sustained span (kisses can't bundle by
> construction). A bundle's centerline is the median strand of its
> cross-sections (the hand-drawing rules: 1 track → follow it, 2 → midpoint,
> 3 → center track, 4 → middle-two midpoint). Nodes appear exactly where
> bundle membership changes persistently — physical forks. Route shapes are
> map-matched onto the bundle graph, ordered per edge to minimize crossings
> (LOOM's problem), junctions are cut per zoom band and refilled with smooth
> fillets, and segments are emitted with travel-frame slots for client-side
> offset rendering.

## Commands

| command | what it does |
|---|---|
| `portolan chart <city.toml>` | build a city: GTFS + rail extract → segments |
| `portolan sound <city.toml>` | score the build against the hand-drawn network (the regression gate) |
| `portolan atlas <city.toml>` | dev server: sketch editor, before/after windows, cross-section probe |

A `city.toml` names the GTFS zip(s), the rail extract, and the output path.
See `testdata/` for the NYC fixture and `docs/TOOLS.md` for the dev loop.

## Getting a rail extract

```
# Overpass: all regular-service rail in a bbox (yards/sidings excluded)
[out:json][timeout:120];
way["railway"~"^(rail|subway|light_rail|tram)$"]["service"!~"."]({{bbox}});
out geom;
```

Convert to GeoJSON (`osmtogeojson`, or `portolan fetch` once implemented).

## Ground truth

`testdata/sketches/nyc.json` is the owner's hand-drawn NYC network (15 lines,
88.6 km) from the `portolan atlas` sketch editor. It is the definition of
correct. Every geometry change must pass `portolan sound` before it merges —
the gates (deviation, coverage, jaggedness at map scale, wobble) are described
in [docs/TOOLS.md](docs/TOOLS.md).

## Status

**The full pipeline is implemented and passes all gates on NYC.**

```
$ portolan chart --gtfs nyc.zip --rail testdata/nyc-rail.geojson --out nyc.geojson
chart:  29 routes, 210 patterns, 8079 rail ways        (0.1s)
bundle: 1017 strands, 1567 corridors, 1413 nodes       (4.4s)
berth:  210 matches, 1739 berths, 1420 moves           (7.1s)
order:  slots on 605 corridors                         (7.9s)
fair:   ~10k segments across 4 zoom bands              (7.5s)

$ portolan sound --network testdata/sketches/nyc.json --build nyc.geojson
jaggedness: max turn 23°, 0 spikes · wobble p90 5.6 m
fwd mean 1.8 m, p90 3.6 m · 0 self-intersections · PASS
```

For scale: the Python predecessor's best-ever score was fwd 2.6 m / p90
4.8 m with an 88° jag spike (FAIL), in ~4 minutes per build. Chicago (CTA +
an Overpass extract, zero city-specific configuration) builds end-to-end in
~7 s with correct Loop topology.

Known rough edges: densest junction interiors (City Hall loop complex) show
residual artifacts; bridge legs (track data gaps) render as raw shape chords;
ordering is LOOM-lite (local descent, not exact); station/stop grouping not
started. The dev loop for all of it: `portolan atlas` + windows + the scorer.
