# Feeds — the unit of everything

A GTFS FEED is portolan's unit of build, curation, refresh, and
delivery. There are no city bundles and no regions: `portolan.json`
holds one entry per feed (mta-subway, wmata, amtrak, …), each with its
own track extract, curation document (`style/<feed>.json`), tile
pyramid, and content-hash manifest. The world map is the sum of the
per-feed pyramids — operators overlay where they overlap, the way Apple
draws them, and no feed's layout ever depends on another feed's.

Why feeds and not groupings: a grouping is an editorial claim (which
operators belong together?) that someone must maintain, and it couples
updates — one operator's daily GTFS refresh forced a rebuild of
everything bundled with it. Per-feed, an update rebuilds one feed in
seconds-to-minutes and rewrites only the tiles that changed.

## The lifecycle

```
tools/feed.sh gtfs <key> [id]     # find/download the zip (Transitland)
tools/feed.sh rail <key>          # track from Overpass (city-scale OSM truth)
tools/feed.sh shapesrail <key>    # …or the feed's OWN shapes as track
tools/feed.sh build <key>         # chart + score
tools/feed.sh refresh <key>       # cron entry: rebuild only on content change
```

`shapesrail` exists for feeds with no city window — intercity rail,
national operators — where the feed's own shapes are denser truth than
any Overpass query. It Douglas-Peucker's each shape (~50 m) and snaps
every vertex to a CANONICAL grid (~45 m), so coinciding corridors
collapse onto identical point sequences. That canonicalization is
load-bearing: the track graph welds ways only where they touch exactly,
and un-snapped variants once entered the graph as disconnected islands —
MATCH could not hand off between them, and whole route legs fell to
crow-flies GAP chords.

Per-feed extras ride in the entry: `chart_args` carries tuned dials
(`"--set match_gap_cost=150"` on amtrak) through every rebuild.

At continent scale Overpass is not an option (it connection-throttles
burst clients), so geometry comes from cached Geofabrik region filters:
`tools/railall.sh <region>…` downloads each region ONCE and keeps two
small PBFs — `<region>-railall.osm.pbf` (the drawable track family) and
`<region>-stopsall.osm.pbf` (station/halt/stop nodes plus station ways,
which the way-only track filter cannot see). From those caches,
`tools/onboard-na.sh` cuts per-feed rail extracts and
`tools/feedstops.sh <key>` cuts per-feed stop extracts
(`tools/pbf2stops.py` mirrors the Overpass converter, same ids, same
tag whitelist — a feed can move between the two paths without churn).
The stops file is what powers OSM station naming; every feed entry
should carry a `stops` path.

## Intercity rail: one pooled line

The `intercity` trunk policy (style.TrunkIntercity) pools routes ACROSS
agencies into one drawn line — Apple's treatment of Amtrak, VIA, and
international operators. Opt-in per agency in the feed's curation:

```json
{ "agencies": { "Amtrak": { "trunk": "intercity" } } }
```

Never a class default: commuter rail shares route_type 2 and keeps its
per-agency identity. The pooled line takes the majority route_color of
its members and is labelled by operator until a corridor is genuinely
shared ("Intercity Rail").

## Groups: cross-feed bundling

Two feeds sharing steel cannot bundle from separate builds — SPLIT only
merges what it sees in one run. A GROUP is an ordinary feed entry whose
`gtfs` is a comma list and whose `members` names the feed keys it
absorbs:

```json
"chicago": {
  "gtfs": "data/gtfs/chicago-cta.zip,data/gtfs/metra.zip,data/gtfs/amtrak.zip",
  "members": ["chicago-cta", "metra"],
  "bbox": [-87.95, 41.7, -87.52, 42.08], ...
}
```

The engine needs nothing else: loadFeeds prefixes overlay ids (`f1:`,
`f2:`…), MATCH puts everyone on the same extract, and SPLIT bundles
whatever co-runs. Style layers `_default` → each member's document →
the group's own (`feed.sh` passes the comma list to `--feed`; lookups
strip the overlay prefix so member curation keeps matching).

The rules that keep the world drawn exactly once:

- A feed listed in `members` is ABSORBED: its own tileset stays on disk
  for per-feed views, but the global index skips it — the group draws
  it.
- A feed in the `gtfs` list but NOT in `members` (Amtrak, in every
  group) stays independent: its own build automatically cedes each
  group's window (`chart --exclude-bbox`, derived by `feed.sh` from the
  groups' gtfs lists). The group clips the feed TO its window, the
  feed's build clips it OUT, and the two cuts land on the same margin
  line — which by construction is lone-trunk territory between
  networks. That boundary is what makes patch updates local: a member
  feed's change rebuilds one group; an Amtrak change rebuilds its own
  background build plus the groups it rides through, never the world.

`fingerprint`/`refresh` hash every zip of the comma list, so a group
refreshes when ANY member changes.

## Delivery: tiles

`portolan tiles` slices a build into an MVT pyramid (internal/tiles;
hand-rolled encoder). Tile zoom = FAIR band (0 → z0-12, then 13, 14, 15
with overzoom); steady ribbons clip with a 256-unit buffer; transitions
and gap bridges ride whole per tile (their offset easing runs over
line-progress and must not be re-normalised). The tiler is a differ:
byte-identical tiles are not rewritten, stale tiles are pruned — rsync,
CDNs, and browser caches see the minimal delta.

The atlas serves pyramids at `/api/tiles/<feed>/…` and lists every cut
pyramid at `/api/tiles/index.json` — that list IS the world. The
console's picker carries a pinned **Global** entry that renders every
indexed pyramid at once; per-feed pages ask for a feed when entered in
global context.

## The other output: adjusted feeds

`chart --export-gtfs <dir>` writes the source zip back out with
shapes.txt replaced by the matched track geometry (ids/trips untouched —
still valid GTFS). Shapes clipped at a bbox keep their original rows.

## Known limits

- One local tangent frame per build: fine to ~region scale (~3% x-scale
  drift), continental feeds carry more distortion at their edges;
  Mercator-consistent emit is future work.
- SPLIT is the wall-clock bottleneck on continental feeds (~10-25 min);
  component-local ORDER + cached slots is the documented path to
  incremental re-layout (`affinity`/`colorSet` must stay global).
- Tile mode is the static all-service picture; the time-of-day dial
  needs materialized features and is parked on tiled feeds.
