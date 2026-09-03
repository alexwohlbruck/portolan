# Changelog

Notable changes per release. Portolan is pre-1.0: the drawn geometry is
still allowed to move between minor versions, and when it does it is said
here plainly — a downstream renderer that pins pixel diffs cares about
that more than it cares about the API.

## 0.4.5

### A stop matches the station, not the rails

`MatchOSMStops` scored candidates on distance and name similarity alone,
with no term for what kind of OSM object a candidate was. A `stop_position`
— a point on the track where a train halts — carries the station's name and
sits metres from the station node, so the winner was whichever the feed's
coordinate happened to land beside. Clark St, with one station node and two
stop_positions, matched the station; Jay St–MetroTech, with two station
nodes and six stop_positions, matched a stop_position. Downstream that is
the difference between calling a place a subway station and calling it a
subway stopping location.

Assignment now runs in two passes. Station-type objects
(`public_transport=station`, `railway=station|halt`, `amenity=ferry_terminal`,
`aerialway=station`) are assigned first, and only the feed stations left over
fall back to a stop_position or platform. A fallback rather than a filter:
plenty of stations are mapped with no station node at all, and a
stop_position is a better answer there than none. The second pass stays gated
on radius, class and name similarity like any other candidate, so it cannot
reach further than the first, and each pass still assigns greedily by score,
so two adjacent stations each take their own node.

**Consumers who pin station ids**: `Station.OSMID` changes wherever a
stop_position was previously winning, and the adopted station name may change
with it. No drawn geometry moves.

## 0.4.4

### The GTFS stop to OSM object join is published

Shipped in 0.4.4 without notes. A build now writes `<out>/stops.json`
alongside the route index, mapping `<feed-onestop>:<stop_id>` to the OSM
object the matcher chose — `"node/597928315"`. A client holding a feed's own
stop id had no way to reach the OSM object portolan had already identified,
and was left to re-find it by name and coordinates, which cannot tell three
stations called Chambers St apart.

## 0.4.3

### The bundling mate range now reaches the bundler

`split_merge_dist` — the kiss-rule range within which two sustained-parallel
edges are read as one visual corridor — is 16 m, up from 12 m.

It was supposed to move a release ago and did not. The value is written in
three places: a package var in `bundle_edges.go`, the `dial()` default beside
it, and `pipeline.DefaultDials`. The pipeline installs `DefaultDials` before
every run and `dial()` prefers any non-zero tuned value, so `DefaultDials` is
the only one that decides — and it still said 12 while the other two were
moved to 18 and then to 16. Both intermediate values were inert: builds made
with them are byte-identical to 0.4.2, every output file, on both benchmark
networks. All three literals now agree, with a comment at the var block
naming which one wins.

**Consumers who pin pixels**: geometry moves on every network, because a
wider mate range bundles track pairs that were previously drawn as separate
ribbons. Measured on NYC subway — 1,413 drawn features to 1,443 (+2.1%) with
total ink flat at 2,065 km (+0.03%), so this is corridors being restacked
rather than track appearing or vanishing. It also takes a gate off the
`sound` score: the sharpest on-corridor turn drops from 51 degrees to 39,
clearing the jaggedness gate, while alignment to the reference network is
unchanged at sub-metre scale (forward mean 2.4 m to 2.5 m, p90 4.7 m to
4.8 m).

The effect is concentrated where tracks genuinely run 12-16 m apart, which
is why this is a patch rather than a redraw: Chicago CTA over the same
range is 1,060 features before and after, with ink flat to five significant
figures at 2,184 km. Expect movement on dense multi-track trunks and almost
none elsewhere.

### Uploaded saves are their own group in the atlas

Three things the workbench's `.metro` upload was missing once there was more
than one of them in the feed picker: uploaded saves now sort under their own
heading rather than among the 1,499 real agencies, they are named from the
uploaded filename rather than the save header (every Subway Builder save is
called "Current Game", so two uploads listed as two identical rows and the
second silently overwrote the first), and they can be deleted —
`POST /api/import/metro/delete?feed=<key>` removes the config entry and every
file the importer wrote. Deletion is guarded server-side on an explicit
`imported` marker, not the key prefix, so no curated feed can be removed.

The workbench does not ship in the release archives, so this reaches you only
from a repo checkout.

## 0.4.1

### Centerline refinement stops folding

Two rendering failures off one game-authored NYC network — a route
drawing a sine wave through a station, and another drawing 766 km of
scribble across four blocks — were one instability at two amplitudes.
Present since at least 0.3.2; that network is simply the first to hold
the trigger geometry at scale.

A corridor whose two directional tracks sit 5-22 m apart and BREATHE
crosses `StrandGap` and the kiss guards repeatedly, so the median's
vote set never settles. The metro median filter's short window passed
that flap through as a standing wave; where the wave outran the local
turn radius the polyline folded, and a fold compounds — it is in the
base line next iteration, densified into more samples whose normals
flip across it, while `moved` never falls to the convergence break.

- The ±60 m majority window over the offset series now runs for EVERY
  mode. It was street-only, with metro deferred to an all-modes
  centering session; the flap is a breathing-separation phenomenon, not
  a street one.
- `bundle.Refine` refuses any iteration that grows the line by more
  than 2% + 30 m. A lateral centering move conserves arc length, so
  growth is the fold signature: keep the last stable line and stop.

**Consumers who pin pixels**: metro centerlines are smoother through
sections where the track group's separation varies, and a corridor that
was previously drawn folded is now drawn as a line. Measured on the
failing network — turning sum 4,912° → 1,918° at the sine, 26,231° →
201° at the scribble, and drawn-vs-matched-path alignment from 35.3 m
mean (175.6 m worst) to 5.8 m mean (8.5 m worst).

## 0.4.0

### Yards are first-class regions

A downtown looked like a rail atlas instead of a transit map because
storage ladders and shop leads are drawn steel nobody rides. Yards are now
detected as REGIONS and kept out of the bundling pools.

- **Detection** — one OSM parse splits the extract into regular and
  service pools; regions come from parallel-track density, each traced to
  a polygonal outline from its cell mask, with entrances where track
  pierces that outline. Calibration is locked against the NYC fixture.
- **The oracle** — MATCH and SPLIT walk yard steel under penalty and keep
  it out of every pool, the stable-twin guard consults the region oracle,
  and region spine skeletons substitute through yards. A route that
  genuinely through-runs on yard steel still rides; the ladder stops
  bundling.
- **Centerlines** — a Prim-style Steiner forest over the entrance nodes
  picks which steel carries a corridor (a second route reuses the first
  one's trunk by construction), then a perpendicular cross-section centres
  the result on the bundle median. NYC: 1,080 centerlines over 336 km in
  89 of 91 regions. Emitted as `yard_centerline`.
- Yards are drawable in the sketch editor, with the detected result
  underneath, and there is a console debug overlay for region fills,
  skeletons, spines and entrances.

Two centerline rules need no drawn ground truth and are gated on the NYC
fixture: every entrance carries a centerline (98.2%, gate 95%), a
centerline sits on real track (p90 5.6 m, p99 15.4 m), and it does not
kink (p99 11.5°, max 53.9°). The remaining rules await drawn yards to
grade against; the yard IoU ratchet stands at 0.75 against a 0.98 target.

### Geometry change: the corner fillet is gone

`filletCorners` replaced a cluster of turning vertices with a circular arc
tangent to the straightened arms. It was built for genuine 90° street
corners, and to catch those on chord-straightened polylines it triggered
at 9° per joint — but a long sweeping curve, after the same straightening,
is an ~11°-per-joint polyline. It could not tell them apart, and redrew
real curves as straight–arc–straight: the 2/3 at Beekman St ran 6.5 m off
its centerline, the B/D through Chrystie St 10.6 m. Both now sit within
1.7 m.

**Consumers who pin pixels will see two changes**: sweeping curves follow
their steel, and 90° street corners draw faceted again until a fillet
gated on where the bend is CONCENTRATED replaces the one gated on its
total.

### Feeds

- `nyc-ferry` carries its Transitland onestop id, so the feed can be
  fetched rather than silently skipped.
- `toronto`, `ttc-surface`, `paris` and `london` ship with
  `--allow-unmatched`: each has a small set of patterns whose steel the
  extract cannot supply (TTC's 50x streetcar branches, new Paris
  tram-trains, the Bakerloo's shp_1_314). Drawing them with gap chords
  beats leaving four metros off the map.
