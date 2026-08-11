# The corridor graph: charting a network whose geometry is given

Portolan's usual input is `gtfs.zip + rail.geojson`, and most of a build
goes on working out something the caller may already know. BUNDLE reads
raw OSM track and infers where the corridors run; MATCH walks each route
onto them. On NYC that is 4.4 s of bundling and 7.1 s of berthing out of
about 15 s — and MATCH is probabilistic, so its failure modes (phantom
gap bridges, wrong track-pair selection, junction-interior artifacts)
land in the output even where the truth was never in doubt.

Any authored or synthetic network already has the answer. A transit
planning tool, a scenario editor, a simulator, a hand-drawn proposal, a
re-run of a previous portolan build: all of them hold an explicit graph
of corridors and an explicit statement of which routes ride which. For
those callers inference is not just wasted time, it is a downgrade.

    portolan chart --gtfs feed.zip --rail rail.geojson       --out build.geojson   # infer
    portolan chart --gtfs feed.zip --corridors corridors.geojson --out build.geojson   # given

`--rail` and `--corridors` are alternatives; exactly one is required.
With `--corridors`, portolan skips `internal/osm`, `internal/bundle`,
MATCH and SPLIT entirely and joins the shared pipeline at ORDER.
Everything downstream — ORDER, FAIR, terminal cuts, stations,
caterpillars, style resolution, every emitter — is the same code the OSM
path runs. Both call one `layout()`, so the two cannot drift apart.

## There is no new format

The corridor graph is **portolan's own network dump, read back in**.
Every build already writes it:

| file | what it holds |
|---|---|
| `<out>.trackcenter.geojson` | the corridors, as LineStrings |
| `<out>.nodes.geojson` | the junctions, as Points |

So a build's output is a valid input, the round trip is a free
regression test, and there is no schema to learn that portolan does not
already emit. Route and stop metadata stays in **GTFS static**, which
portolan already parses; presentation stays in the **curation document**
(`--style` / `--style-dir`). Nothing about appearance belongs in the
corridor file.

## The corridor file

A `FeatureCollection`. Points are junctions, LineStrings are corridors.
The two may share one file or arrive as two (`--corridors` plus
`--corridor-nodes`); GeoJSON permits a mixed collection and portolan's
own `.stations.geojson` already ships one. `-` reads stdin.

### Node properties

| property | type | meaning |
|---|---|---|
| `node` | string or number | the node's id, referenced by edges |
| `degree` | number | emitted for information; ignored on read |

`id` is accepted as a synonym for `node`, as is a feature-level `"id"`.

### Corridor properties

| property | type | meaning |
|---|---|---|
| `from`, `to` | string or number | the nodes this corridor joins |
| `routes` | CSV string, or array | GTFS `route_id`s riding it |
| `tracks` | number | physical track count, if known |
| `gap` | bool | no physical track here — drawn dashed |
| `oneway` | `forward` \| `backward` | see *Divided corridors* |
| `edge` | string or number | an id, used only in error messages |

Ids in `routes` **round-trip verbatim** into segment `routes` and
caterpillar `route` properties. They are not trimmed beyond surrounding
whitespace, not case-folded, not renumbered — callers map ribbons back
to their own identifiers by string equality, and a helpful
normalisation would break exactly that.

### Node identity

Prefer explicit `from`/`to`. Portolan emits them, and they make the
round trip exact.

Where an edge names neither, its endpoints snap to the nearest node
within **1.0 m**, and endpoints with no node in reach become new nodes.
That fallback is documented but lossy by construction: two junctions a
metre apart become one. Portolan reports how many nodes it had to invent
so the loss is never silent. Mixing is allowed per edge — an edge that
names one end snaps only the other.

This is why the emitted dump grew node ids. It used to carry `degree`
alone, which made topology ambiguous exactly where it matters: a
terminal throat where four endpoints sit within a metre of each other.

### Geometry precision

Corridors emit their **own vertices**. Earlier builds resampled the
trackcenter dump at 8 m, which was fine for a debug layer and lossy as
an input format — every junction corner came back rounded off, so a
re-chart of a dumped network was not the same network.

## Divided corridors

A caller whose model splits a corridor into a track per direction hands
over both, and both draw. Mark them:

```json
{"edge":"up","from":"a","to":"b","routes":"R1","oneway":"forward"}
{"edge":"dn","from":"b","to":"a","routes":"R1","oneway":"backward"}
```

Two edges between the same node pair with opposite `oneway` are a
divided corridor, not a duplicate, and the validator says so. Two joined
by more than one *two-way* edge is reported as a probable mistake.

ORDER slots each of the pair independently — portolan does not currently
pair them up or reason about handedness. A caller who wants one ribbon
should collapse the pair into a single centerline before export.

## Traversal: which junction movements a route makes

FAIR does not need a map-match. It needs one thing from the matched
walks: at a junction where two of a route's legs meet, does the route
actually ride from one to the other? A loop circulator carries its id on
three legs of a corner and rides two of the three pairings; the third
must not get a turn drawn.

That makes the requirement much smaller than MATCH's. **A walk is only
load-bearing where a route's own subgraph has a node of degree ≥ 3** — a
fork, or a corner the route crosses twice. Below that, every movement
the route could make is one it does make, and supplying a walk changes
nothing.

Portolan takes the best evidence available, in this order:

1. **`shapes.txt` lying on the corridors.** The walk *is* the shape, with
   no search at all. Coordinates straying more than 30 m from the
   corridors the route is listed on are treated as a different network's
   shapes, reported, and ignored.
2. **Ordered stop sequences** (`trips.txt` + `stop_times.txt`). Each stop
   snaps to the nearest corridor its route rides, and consecutive stops
   join by the shortest ride over that route's own subgraph. Ambiguous
   where a route traverses one corridor more than once.
3. **Structure.** A route whose own subgraph is a simple path or a simple
   ring has exactly one traversal, and it is read straight off. This is
   the common case for authored networks, and it requires the feed to
   state nothing.
4. **Nothing.** A route that forks or crosses itself, with no shape and
   no stop order, is named in the build log. FAIR then attests every
   junction movement — which is correct below degree 3 and a guess above
   it, so the log tells you exactly which routes to give stop order to.

## Feeds without a timetable

An authored network generally has no schedule, so `shapes.txt`,
`trips.txt` and `stop_times.txt` are all **optional**. The smallest legal
feed is `routes.txt` (what the route ids mean) and `stops.txt` (where
the stations are).

- **No calendar** — activity masks stay nil, edges carry no `Acts`, the
  terminal-cut pass returns its input untouched, and stations emit no
  `acts` property. The viewer falls back to route-level masks, which for
  a network with no timetable is the whole truth.
- **No trips** — stations are built from `stops.txt` plus edge route
  membership: a route calls at a stop when the stop lies within 120 m of
  a corridor that route rides. Platform merging, transfer complexes, the
  importance percentile and marker snapping all run unchanged on that.
- **No shapes** — patterns become distinct ordered stop sequences rather
  than distinct `shape_id`s, with a stable synthetic id hashed from the
  sequence.

## Which routes load

The graph decides. A route the corridor file never names is not loaded
at all — mode is the wrong question when the caller has already said
where everything runs, and it keeps the build fast: Atlanta's feed is 86
routes of which 5 are rail, and admitting the rest ran the `stop_times`
sweep over a whole bus network for nothing.

The corollary is that buses draw like anything else here. On the OSM
path they are gated behind `--streets` because they have no rail to
match onto; on an authored corridor there is nothing to match.

## Validation

Failures — the build stops, because guessing past any of these draws a
map that is quietly not the one that was asked for:

- an edge referencing a node that does not exist
- an edge with fewer than two coordinates
- a route id on an edge but absent from `routes.txt`
- duplicate node ids
- `oneway` that is neither `forward` nor `backward`

Reports — logged, never fatal:

- **disconnected components**, with a lat/lon sample per component. A
  disconnected graph is usually a mistake and occasionally the truth: a
  ferry that touches no track, a system in two halves, a window that cut
  a city in two. Rejecting it would make those networks unchartable.
- nodes synthesized because an edge named no endpoints
- edges carrying no routes, which will not draw
- divided corridors found, and duplicate two-way pairs

## Trunking and colour

Trunking stays colour-based (law 5): two routes drawn the same colour
*are* one line to a rider. That reasoning holds where colour carries
meaning and fails where a caller assigns colours arbitrarily — two
unrelated authored routes sharing a hex would silently merge into one
ribbon.

The escape hatch is per route, in the curation document:

```json
{ "routes": { "Heritage": { "trunk": "route" } } }
```

`trunk: "route"` keeps that route out of a colour trunk without changing
the class policy for everything around it.

Note the nesting: `trunk` sits on the SUBJECT, inside `routes`. The
`trunks` table in `style.Config` is the flattened internal form, not a
key a file may use — see the box under "Bullet curation".

## Bullet curation

Also the curation document. It is **subject-keyed**: name a route or an
agency once, and hang everything known about it off that name.

```json
{
  "agencies": { "AUTH": { "font": "mono", "bordered": true } },
  "routes":   { "L": { "shape": "diamond", "font": "italic" } }
}
```

> **Not the flat form.** `internal/style`.Config holds parallel tables
> keyed `"route:<id>"` / `"agency:<id>"` — `shapes`, `fonts`, `bordered`,
> `trunks`. That is the *internal* shape `Doc.Config()` flattens a
> document into, and it is **not** what a file may contain. Writing
> `{"shapes": {"route:L": "diamond"}}` into a curation file used to parse
> cleanly and apply nothing; it is now a hard error naming the right
> shape, because a curation format that silently does nothing is the
> worst kind.

| key | values |
|---|---|
| `shape` | `circle` `square` `rounded` `notch` `diamond` `hexagon` `octagon` `triangle` |
| `font` | `default` `mono` `bolder` `lighter` `italic` |
| `bordered` | `true` / `false` — a contrasting ring; a white bullet has no edge without one |
| `trunk` | a `Trunk*` policy, in practice `route` |

A route override beats its agency's. `bordered` distinguishes an
explicit `false` from unset, so a route can opt out of an agency-wide
ring.

`route_text_color` is read straight from `routes.txt` and emitted as the
caterpillar's `text_hex`. When the feed omits it the property is omitted
too, and the renderer's luminance rule decides — which is what gets
NYC's yellow N·Q·R·W its dark glyphs.

## The projection anchor

Every metric in the pipeline is computed in frame coordinates, so moving
the projection origin perturbs every float in the build. Derived from a
network's own extent, that origin moves whenever the network grows: an
editing client that adds one corridor at the edge of town gets different
rounding in the middle of the city.

On the corridors path the derived origin is therefore **quantized to a
0.25° grid**, which pins it for every network inside one cell. Pass
`--anchor lat,lon` to pin it absolutely — required if two builds of
*different* networks must be compared coordinate for coordinate.

The `--rail` path's anchor is unchanged, so the wired cities keep
building byte for byte.

## Determinism and locality

Identical input produces byte-identical output. A local change produces
a local change: a 4 m jog on one mid-edge vertex of a 20-route, 180-edge
network moves that edge's geometry and **none** of the 260 slot
assignments elsewhere. Both are tested
(`internal/pipeline/corridors_test.go`).

## Output

Same artifacts as the OSM path: `build.geojson`, `.stations.geojson`,
`.style.json`, plus the `.trackcenter.geojson` / `.nodes.geojson` pair
that can be fed back in.

### Per-band and binary emit

FAIR writes a full copy of the network per zoom band (15/14/13/0) and
exactly one is ever visible, so a client that knows its zoom is
downloading three copies it will not draw.

    --band 15          one band instead of the union
    --format bin       flat typed arrays instead of GeoJSON

Band filtering happens at emit, after every stage has seen the whole
network — ORDER and FAIR need all four bands to decide slots and
junction drawing, so dropping them earlier would change the map rather
than the download.

`--format bin` writes **PLNB**: positions, a start-index array, and
per-feature property blocks, so a client slices the buffer, uploads
positions straight to a vertex buffer, and reads per-feature values by
index with no per-feature object allocation. The layout is documented in
full at the top of `internal/pipeline/binary.go` — read it before
writing a decoder, because the blocks are grouped by width and
interleaved within a block, not one contiguous array per property.

NYC band 15: **3.08 MB of GeoJSON becomes 614 KB**, a 5.0× cut on
identical content and 10.3× against the 6.3 MB union a client reads
today.

Positions are `i32` fixed point at 1e-7 degrees rather than `f32`: a
float32 holds about seven significant digits where a longitude needs
nine, which would quantise vertices to roughly two metres and visibly
kink a ribbon.

## Performance

A small authored network — tens of routes, low hundreds of edges —
charts in well under a second. The measured case (20 routes, 180 edges,
100 stops, no timetable) is **36 ms**.

Round-tripping a real city is dominated by the GTFS zip, not by
portolan: Atlanta re-charts from its own dump in about 2.1 s, of which
roughly 1.7 s is two reads of a 144 MB feed and 169 ms is traversal.
Per-stage timings are logged on the corridors path the way `--rail`
logs them.
