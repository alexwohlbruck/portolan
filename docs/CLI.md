# The portolan command line

Five commands. `chart` builds a map, `sound` grades one, `scenarios`
lists what a feed contains, `atlas` is the workbench, and `serve` is the
build server.

```
portolan <command> [flags]
```

`portolan help` lists the commands; `portolan help <command>` prints one
command's flags, grouped, with examples. This page is the same material
in full.

## Conventions

**Exit codes.** `0` success. `1` the work failed — a missing file, an
unreadable feed, a build error, or `sound` finding a gate exceeded. `2`
you asked for something impossible — an unknown command, a bad flag, a
missing required input. The split matters in scripts: `2` means fix the
command, `1` means fix the data.

**stdout and stderr.** Results go to stdout; progress and diagnostics go
to stderr. So `portolan scenarios --gtfs feed.zip > list.txt` captures
the list and nothing else, and a `chart` build's stage log never
contaminates a piped artifact. The one command that puts a *value* on
stdout is `serve`, whose first line is the port it bound.

**Coordinate order differs between two flags, deliberately.**

| flag | order | why |
|---|---|---|
| `--bbox` | `w,s,e,n` — longitude first | it is a GeoJSON bounding box, and GeoJSON is `lon,lat` |
| `--anchor` | `lat,lon` — latitude first | it is a place, and every map UI hands you a place latitude first |

Both say so in `--help`. Getting one wrong usually produces an empty or
absurd result rather than a subtle one, but check this first if a build
comes back inexplicably empty.

**Flags only.** No command takes positional arguments; passing one is an
error rather than being silently ignored.

---

## chart

Build a city's map geometry.

```
portolan chart --gtfs <feed.zip> --rail <track.geojson>       [flags]
portolan chart --gtfs <feed.zip> --corridors <graph.geojson>  [flags]
```

Exactly one geometry source is required, and the two are alternatives:

- **`--rail`** hands portolan raw OpenStreetMap track and it works out
  which tracks form which corridor and which route rides which. This is
  the normal path.
- **`--corridors`** takes a corridor graph you already have and skips
  that inference entirely — for a planning tool, a scenario editor, a
  simulator, or a rebuild of a previous portolan build. The contract is
  in [CORRIDORS.md](CORRIDORS.md).

### Inputs

| flag | meaning |
|---|---|
| `--gtfs` | GTFS zip **or a directory of the `.txt` tables**. A comma list merges feeds: `primary.zip,overlay.zip`. Overlay route and stop ids are prefixed `f1:`, `f2:` … so ids from different agencies cannot collide. A zip made from a containing folder (`gtfs/routes.txt`) works too. |
| `--rail` | OSM rail extract (GeoJSON). `tools/feed.sh rail <city>` fetches one. |
| `--corridors` | A corridor graph (GeoJSON). `-` reads stdin. |
| `--corridor-nodes` | The nodes half of that graph, when it arrives as two files rather than one mixed collection. |
| `--streets` | OSM street extract (GeoJSON). Bus routes are drawn only when this is given — without street geometry there is nothing to match them onto. |
| `--stops` | OSM transit-stop extract (GeoJSON). Gives each drawn station its OSM id and, unless curation says otherwise, the name on the sign. |

`--gtfs` may be omitted **with `--rail`**, which dumps the bundled track
strands and stops without drawing a map. It is a quick check that an OSM
extract is usable. With `--corridors` it is required: the graph names
routes by `route_id`, and `routes.txt` is what those ids mean.

### Window and projection

| flag | meaning |
|---|---|
| `--bbox w,s,e,n` | Clip route shapes to a window. Without it a national feed's intercity shape leaves the extract and draws a straight chord across the country. |
| `--anchor lat,lon` | Pin the projection origin. |

Every measurement in the pipeline happens in a local metric frame, so
moving that frame's origin perturbs every float in the build. The origin
is normally derived from the data's own extent — which means it *moves
when the data grows*. Pin it when two builds of different networks have
to be compared coordinate for coordinate, or when a network is being
edited and must not re-round as it grows.

### Output

| flag | meaning |
|---|---|
| `--out` | Output path. Defaults to `build.geojson`, or `build.bin` under `--format bin`. |
| `--format` | `geojson` (default) or `bin`. |
| `--band` | Emit one zoom band — `15`, `14`, `13` or `0` — instead of all four. |

A build writes several files, all derived from `--out`:

| file | what it is |
|---|---|
| `<out>` | the ribbons: one feature per drawn segment |
| `<out>.stations.geojson` | stations, their markers, and the route bullets riding the lines |
| `<out>.style.json` | the resolved style, so the viewer renders widths and colours from the build rather than duplicating the table |
| `<out>.trackcenter.geojson` | the corridor graph portolan used — a valid `--corridors` input |
| `<out>.nodes.geojson` | its junctions |
| `<out>.strands.geojson`, `<out>.paths.geojson` | intermediate geometry for the workbench's debug layers — **`--rail` only**, since `--corridors` never bundles or map-matches and so has neither to show |

**`--band`.** The pipeline emits a full copy of the network per zoom
band, and exactly one is ever visible. The ranges are **half-open** —
`[band_min, band_max)`, and each band's max is the next band's min — so
a filtered build holds exactly one `(band_min, band_max)` pair. Two
would draw every ribbon twice at two different slot pitches. A client that knows its zoom is
otherwise downloading three copies it will not draw. Filtering happens
at the emit, after every stage has seen the whole network — the layout
stages need all four bands to decide slot order and junction drawing, so
dropping them earlier would change the map rather than the download.

**`--format bin`** writes PLNB, a flat typed-array form for clients that
rebuild interactively and cannot afford to parse megabytes of text each
time: positions, a start-index array, and per-feature property blocks,
so a client slices the buffer, uploads positions straight to a vertex
buffer, and reads per-feature values by index with no per-feature object
allocation. The layout is documented in full at the top of
`internal/pipeline/binary.go`.

**Read that layout before writing a decoder.** The property blocks are
grouped by WIDTH and interleaved within a block — `f32s` is
`[offsetPx₀, offFromPx₀, offToPx₀, offsetPx₁, …]`, stride 3 — not one
contiguous array per property. A decoder that assumes the latter reads
plausible-but-wrong values rather than failing, because every lane is a
valid number of the right width.

NYC band 15 is 1.44 MB of GeoJSON or 299 KB of PLNB — 4.9× smaller on
identical content, and 20.7× against the 6.05 MB all-bands GeoJSON a
client reads today.

(These figures were nearly double until v0.3.0, because `--band` used a
closed upper bound and returned two bands per request. A single band is
half what it used to be, so the saving against the union is twice what
was previously claimed.)

### Curation

Feeds get colours wrong, and there is no signal in the data to compute
the fix from, so it is curation. See [CORRIDORS.md](CORRIDORS.md) for the
document format.

| flag | meaning |
|---|---|
| `--style-dir` | Curation directory (default `style`). Reads `<dir>/_default.json` then `<dir>/<city>.json`, later winning field by field. **Both layers are optional** — the class defaults are compiled in, so a directory holding only your `<city>.json` is complete, and a missing directory resolves to the shipped defaults. |
| `--feed` | Feed key, selecting the second of those files. |
| `--style` | One pre-merged document instead, overriding `--style-dir`. |
| `--line-agencies` | Comma list of regional agencies whose per-line colours are real line identities rather than branch-diagram decoration, so they keep separate ribbons instead of collapsing into one agency trunk. |

#### If you are packaging portolan inside another application

**You almost certainly do not need to copy anything.** The class
defaults — mode widths, opacities, band floors, trunk policies — are
compiled into the binary, not read from a file. There is no
`_default.json` in the repo or in a release archive; `style/` holds the
nine shipped *city* curations and nothing else.

So a consumer that generates its own curation points `--style-dir` at a
directory containing only its own `<city>.json`, in any writable
location it likes. A missing directory is not an error either: a tree
with no `style/` at all resolves to exactly the shipped defaults.

Copy `style/` out of the archive only if you want one of the shipped
city curations — and then copy it somewhere writable and version-stamp
it, because a release archive inside a signed application bundle is
read-only.

`--style` is a different thing and rarely what you want here: it takes
ONE pre-merged document and replaces `--style-dir` entirely.

### Service

| flag | meaning |
|---|---|
| `--scenario` | Build one time-of-day service picture instead of the all-service union. Ids come from `portolan scenarios`. |
| `--cover` | Keep the largest route patterns until this fraction of trips is covered (default `0.99`). Agencies ship dozens of one-off shape variants; drawing them all is noise. |

### Examples

```bash
# a city, with curation and OSM station names
portolan chart --gtfs nyc.zip --rail nyc-rail.geojson \
  --stops nyc-stops.geojson --style-dir style --feed mta-subway --out mta-subway.geojson

# merge an overlay feed, clipped to the metro area
portolan chart --gtfs mta.zip,lirr.zip --rail nyc-rail.geojson \
  --bbox -74.26,40.49,-73.7,40.92 --out nyc.geojson

# what runs at 3am on a Sunday
portolan chart --gtfs nyc.zip --rail nyc-rail.geojson \
  --scenario sun-03 --out nyc-night.geojson

# feed a build's own corridor graph back in — the round trip
portolan chart --gtfs nyc.zip \
  --corridors nyc.geojson.trackcenter.geojson \
  --corridor-nodes nyc.geojson.nodes.geojson --out rebuilt.geojson

# one band, binary, for an interactive client
portolan chart --gtfs nyc.zip --rail nyc-rail.geojson \
  --format bin --band 15 --out nyc15.bin

# is this OSM extract usable at all?
portolan chart --rail london-rail.geojson --out /tmp/check.geojson
```

---

## sound

Grade a build against a network drawn by hand in the workbench's sketch
editor.

```
portolan sound --network <reference.json> --build <build.geojson>
```

| flag | meaning |
|---|---|
| `--network` | The drawn reference network (JSON). |
| `--build` | The build GeoJSON to grade. |

It reports, per drawn line, how far the built ink sits from the
reference (mean, p90, max), how much of the reference is covered, how
much of the build duplicates itself, and whether the colour matches —
then jaggedness and wobble over the whole map.

**Exits 1 when any gate fails**, so it works as a CI check or a Makefile
step. Note that `make nyc` and `tools/feed.sh` append `|| true`, because
there the score is information rather than a build failure.

```bash
portolan sound --network sketches/network-mta-subway.json --build build/nyc.geojson
```

---

## scenarios

List the distinct service pictures a feed contains — a weekday peak, a
Sunday daytime, an overnight network — and the ids `chart --scenario`
takes.

```
portolan scenarios --gtfs <feed.zip> [--routes]
```

| flag | meaning |
|---|---|
| `--gtfs` | GTFS zip; comma list for overlay feeds. |
| `--routes` | Also list each scenario's route short names. |

Derivation is the pipeline's own, so an id printed here is one `chart`
will accept. Output goes to stdout, one scenario per line, and is safe
to pipe.

```bash
portolan scenarios --gtfs nyc.zip
portolan scenarios --gtfs nyc.zip --routes | grep '^sun-'
```

---

## atlas

Run the workbench — the main way to use portolan.

```
portolan atlas [--config portolan.json] [--addr 127.0.0.1:8765]
```

| flag | meaning |
|---|---|
| `--config` | Workbench config: the cities, their feeds and paths (default `portolan.json`). |
| `--addr` | Listen address (default `127.0.0.1:8765`). |
| `--maplibre` | Dist directory of a MapLibre build with variable line offsets (default `../maplibre-gl-js/dist`). |

A city picker, a live map, a rebuild button that runs the pipeline and
reloads when it finishes, a time-of-day slider, a drawing tool for
reference networks, and a panel of tuning dials. Cities are rows in the
config file and nothing more — there is no city-specific code in the
repo ([CITIES.md](CITIES.md)).

Even spacing across a continuous zoom range needs the MapLibre fork.
Stock MapLibre renders the same build with fixed, pre-baked offsets.

---

## serve

A long-lived process that charts on request, for a client that rebuilds
interactively.

```
portolan serve [--addr 127.0.0.1:0]
```

| flag | meaning |
|---|---|
| `--addr` | Listen address. `:0` picks a free port (default `127.0.0.1:0`). |
| `--style-dir` | Curation directory for requests that name a city. |
| `--token` | Require `Authorization: Bearer <token>` on every request. |

**The first line on stdout is the bound port**, so a supervising process
can read it back from a `:0` request instead of guessing a free one and
racing for it.

```bash
PORT=$(portolan serve --addr 127.0.0.1:0 | head -1)
```

### POST /chart

Takes the same inputs as `chart`, as JSON. Returns `202` and
`{"id": "<job id>"}`.

| field | type | notes |
|---|---|---|
| `gtfs` | string | a zip or a directory; required unless `gtfs_inline` is given |
| `gtfs_inline` | object | the feed itself: table name → CSV text. See below. |
| `rail` | string | a path the server can read |
| `corridors` | string | a path — alternative to `rail` |
| `corridors_inline` | object | the corridor graph itself, as GeoJSON — for a client editing a network rather than storing one |
| `corridor_nodes` | string | |
| `stops`, `streets` | string | |
| `bbox` | `[w,s,e,n]` | |
| `anchor` | `[lat,lon]` | |
| `city`, `style_dir` | string | |
| `line_agencies` | `[string]` | |
| `scenario` | string | |
| `format` | string | `geojson` or `bin` |
| `band` | string | `"15"`, `"14"`, `"13"`, `"0"`, or omitted for all |
| `cover` | number | |

Exactly one of `rail`, `corridors` or `corridors_inline` is required,
and one of `gtfs` or `gtfs_inline`; anything else is `400`.

#### `gtfs_inline`

For a caller whose route and stop tables *are* live editor state, a
colour change touches `routes.txt` and every route edit touches
`stop_times.txt` — so writing a zip per rebuild is a filesystem round
trip bought for nothing. Send the tables instead:

```json
{
  "corridors_inline": { "type": "FeatureCollection", "features": [] },
  "gtfs_inline": {
    "routes.txt": "route_id,route_short_name,route_type,route_color\nR1,1,1,EE352E\n",
    "stops.txt":  "stop_id,stop_name,stop_lat,stop_lon\ns1,Alpha,40.7,-74.0\n",
    "trips.txt":  "route_id,service_id,trip_id\nR1,wk,t1\n",
    "stop_times.txt": "trip_id,stop_sequence,stop_id\nt1,1,s1\n"
  }
}
```

Values are raw CSV text, header row included. Names are accepted with or
without `.txt`. `routes.txt` is the only hard requirement — it is what
the corridor graph's route ids mean — and a feed with no `stops.txt`
draws lines and no stations.

Together with `corridors_inline` this makes a rebuild touch no file the
caller had to write.

Two limits: overlay feeds are a path-only feature (`gtfs` accepts a
comma list, `gtfs_inline` is one feed), and inline tables build **without
a service calendar**, so `scenario` needs a path. An authored network
has no timetable anyway.

### GET /chart/{id}/progress

Server-sent events. Each `data:` line is one JSON object:

```json
{"stage":"fair","pct":65}
{"log":"order: slots on 812 edges (2.1s)"}
{"stage":"done","pct":100,"done":true}
```

`pct` is monotonic. Granularity is per stage and no finer — sub-stage
percentages would be invented numbers.

A client that connects late gets everything it missed replayed before
the live stream, and a stream for a job that has already finished ends
rather than hanging. The connection closes on the `done` event, which
also carries `error` if the build failed.

### POST /chart/{id}/cancel

`204`. Cancellation is real: the layout stages check between passes and
between zoom bands, and a job cancelled while still queued never starts.
Interactive clients supersede builds constantly — the user edits a
corridor and the build in flight is already stale — so a build that
cannot be killed piles up behind the one that is wanted.

### GET /chart/{id}/build

The artifacts. `202` while the build is still running, `500` if it
failed.

| `?artifact=` | file |
|---|---|
| omitted, or `segments` | the ribbons |
| `stations` | stations, markers and bullets |
| `style` | the resolved style |
| `trackcenter` | the corridor graph used |
| `nodes` | its junctions |

### GET /chart/{id}

Job status: `{"id":…,"done":true}`, plus `"error"` if it failed.

### GET /version

What this binary is and what it speaks — so a caller can refuse to draw
against a contract it does not understand, rather than discovering the
mismatch in the geometry.

```json
{"version":"0.1.0","plnb":1,"formats":["geojson","bin"],
 "bands":[15,14,13,0],"auth":false}
```

`version` is the bare semver, no leading `v` and no revision: it is
compared, not displayed. An unstamped build reports `devel` rather than
claiming a release it does not have. `plnb` is the binary layout version
from the PLNB header — the number a renderer actually has to agree with,
and the one that changes when a column moves.

Open even when `--token` is set.

### Operational notes

**Builds are serialized.** Portolan's build configuration is still
process-wide state — dials, agency names, ferry route sets, the resolved
style — so two builds at once would read each other's settings. The lock
is correctness, not throughput management. Cancellation still works
while a build waits behind it.

**Set `--token` whenever anything else on the machine might reach the
port.** A request body names files to read — `gtfs`, `style_dir`,
`corridors` — so an open port is a file-read oracle for every local
process, including a plugin running inside the calling application.
Binding to loopback is not a boundary between processes on one host. The
token is compared in constant time. `/healthz` and `/version` stay open,
so a supervisor can tell "not up yet" from "wrong token".

**Idle cost is nothing.** No tickers, no background workers, no warmed
caches. A job's goroutine lives only while its build runs, and finished
jobs are reaped when the next job is created rather than on a timer.
Each job writes to its own directory, and its artifacts stay fetchable
for 30 minutes.

---

## sync

Reconcile the feed fleet against upstream — notice which GTFS feeds
changed, download them, rebuild what their change touches. The full
contract, including why a patch run must equal a global one, is
[SYNC.md](SYNC.md).

```
portolan sync check  --config portolan.json [flags]
portolan sync patch  --config portolan.json --feeds key1,key2 [flags]
portolan sync global --config portolan.json [flags]
```

| flag | meaning |
|---|---|
| `--config` | Feed registry (default `portolan.json`). |
| `--data` | Where GTFS zips live / are downloaded (default `data/gtfs`). |
| `--feeds` | Comma list of feed keys — `patch` only. |
| `--build` | Build fan output dir (default `build`). |
| `--tiles` | Tile pyramids + index.json (default `build/tiles`). |
| `--export-gtfs` | Corrected GTFS zips; empty skips export (default `build/export`). |
| `--state` | State manifest (default `<build>/sync-state.json`). |
| `--style-dir` | Curation directory (default `style`). |
| `--jobs` | Parallel feed builds (default min(4, NumCPU) — charts are memory-heavy). |
| `--dry-run` | Plan only: print what would happen, change nothing. |
| `--json` | Final stdout line is `RESULT {…}` for a supervising process. |

`check` asks transitland for each registered feed's current version (by
the registry's `onestop` id, `TRANSITLAND_API_KEY` from the environment),
diffs against the manifest, downloads what moved into `--data`, and runs
the patch flow on the changed set. A new upstream sha over identical
content records the sha and rebuilds nothing. `patch` rebuilds exactly
the builds whose inputs the named feeds touch — shared-steel
measurement, group re-derivation, registry rewrite, builds (each a
`portolan chart` child process), the ink-retention verify gate on every
group, tiling, GTFS export, the static tile index. `global` is the same
executor with every on-disk feed in the changed set — the oracle a patch
must match byte for byte; registry entries whose zip was never
downloaded are reported and skipped.

```bash
portolan sync check --dry-run              # what moved upstream?
portolan sync check --json                 # download + rebuild it, tell barrelman
portolan sync patch --feeds mta-subway     # rebuild one feed's closure
portolan sync global --json                # rebuild the world, report
```

---

## Recipes

```bash
# build every configured city and score it
make cities                     # what is set up, and which inputs you have
make city CITY=london

# fetch the OSM inputs for one city
tools/feed.sh rail london
tools/feed.sh stops london
tools/feed.sh streets london    # only if you want buses

# CI: build and fail on a gate
portolan chart --gtfs nyc.zip --rail nyc-rail.geojson --out /tmp/nyc.geojson \
  && portolan sound --network sketches/network-mta-subway.json --build /tmp/nyc.geojson

# rebuild a map from its own output, with no OSM and no map-matching
portolan chart --gtfs nyc.zip \
  --corridors nyc.geojson.trackcenter.geojson \
  --corridor-nodes nyc.geojson.nodes.geojson --out rebuilt.geojson

# drive a build from another process
PORT=$(portolan serve --addr 127.0.0.1:0 | head -1)
ID=$(curl -s localhost:$PORT/chart \
  -d '{"gtfs":"nyc.zip","rail":"nyc-rail.geojson","band":"15","format":"bin"}' \
  | jq -r .id)
curl -N localhost:$PORT/chart/$ID/progress          # watch it
curl -o nyc15.bin localhost:$PORT/chart/$ID/build   # take it
```

## See also

| doc | what is in it |
|---|---|
| [CORRIDORS.md](CORRIDORS.md) | the `--corridors` contract, and the curation document |
| [CITIES.md](CITIES.md) | adding a city, feed sources, per-city status |
| [SERVICE-SCENARIOS.md](SERVICE-SCENARIOS.md) | how `--scenario` derives time-of-day maps |
| [TOOLS.md](TOOLS.md) | the workbench and the scorer's checks |
| [ALGORITHM.md](ALGORITHM.md) | what happens between input and output |
