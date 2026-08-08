# Stop labels — stations, markers, and the label economy

Status: **design adopted 2026-08-08; stage 1–2 in progress.** The reference
is Apple Maps' transit cartography; refs/apple screenshots are the ground
truth this doc paraphrases.

## Why the pipeline owns this (and Transitland doesn't)

Raw GTFS `stops.txt` is a flat list of platforms with no route metadata —
no colors, no classes, nothing to size or style a marker with. Transitland
renders exactly that, which is why its stop layer reads as noise. Portolan
already knows, per shape, every stop it serves (the `shapeStops` sweep that
trims turnaround trackage), and knows every route's class, resolved color
and trunk — so the join GTFS forgot is one map lookup here. Stops become
**stations**: merged, classed, colored, ranked.

## What Apple does (the requirements)

1. ~~Class-scaled line widths~~ — already ours (`style.Class.Width`).
2. ~~Zoom-scaled line widths~~ — already ours (`widthExpr`).
3. **Markers**: a stop served by ONE line is a small dot in that line's
   color. A stop where several lines run parallel is a white dot with a
   shadow, sized to span the bundle. (Hoyt St's 2/3 is a *red* dot — two
   routes but one line. Jay St's A/C/F/R is white.)
4. **Importance**: bigger labels for bigger stations, and render
   precedence strictly by importance — Grand Central always beats a local
   stop for space. Rank ≈ how many lines call there.
5. **The map is always clean**: no overlapping labels, ever; labels take
   whatever side of the marker has room.
6. Route bullets can sit inline ON the line (1·2·3 along the red trunk).
7. A stop label = name + agency icon + route bullets.
8. Bullets render faithfully to the system's own iconography: MTA circles,
   CDMX's rounded-corner cards, Chicago's 'Red'/'Brown' word pills.

## Architecture

**STATIONS is a pipeline stage, not a viewer trick.** It runs after FAIR,
emits `<out>.stations.geojson` (Point features), served at
`/api/stations?feed=`. Like ribbons, stations are timetable-independent:
the union build carries every station, and the viewer applies time and
class filters dynamically (a station is visible while ANY of its routes'
masks is lit — no new data needed, `/api/activity` already has the masks).

### Grouping: platforms → stations

1. Every stop served by a drawn pattern joins, keyed by `parent_station`
   when the feed provides one, else by its own id. (Bus stops are
   excluded for now — thousands of poles are a different rendering
   problem, and Apple only shows them deep in the zoom range.)
2. Groups merge when their normalized names match within walking
   distance — tiered: 150 m within one feed (parent_station already did
   the real grouping; looser folds NYC's two distinct "23 St" stations),
   300 m across feeds (no shared ids, and terminals sprawl — the LIRR's
   Madison concourse is 276 m from Metro-North's Grand Central
   centroid). Cross-feed groups with DIFFERENT names stay separate:
   Apple labels "Atlantic Terminal" (LIRR) and "Atlantic Av–Barclays
   Ctr" (subway) as two stations, and so do we.
3. Station position = centroid of member platforms. (Refinement, later:
   snap to the drawn ribbon so the marker sits ON the line.)

### Properties per station

Aligned per-route arrays, same conventions as ribbon `routes`:
`routes` (ids — join key for activity masks), `labels` (short names — the
future bullet text), `route_colors` (resolved with FAIR's precedence:
config override → class canonical → the route's own), `modes` (class per
route — the class-toggle join key). Derived: `nroutes`, `nlines`
(distinct colors — the marker rule keys on this), `name`, `rank`.

`rank` is `nroutes` for now. It is the ONLY importance signal: label
size, render precedence and zoom-of-first-appearance all derive from it.

### Rendering (MapLibre gives us the label economy for free)

- **Markers**: two circle layers. `nlines == 1` → dot in `route_colors[0]`
  with a white stroke; `nlines > 1` → white dot, dark stroke, larger.
- **Labels**: one symbol layer.
  - `symbol-sort-key: -rank` — important stations place first, so when
    space runs out the locals drop, never the hubs (req 4, 5).
  - `text-variable-anchor: [top, bottom, left, right]` +
    `text-radial-offset` + `text-justify: auto` — the label slides to
    whichever side has room (req 5).
  - Collision is MapLibre's default symbol behaviour — overlapping labels
    simply don't draw. "Always clean" is the engine's guarantee once
    sort-key expresses precedence.
  - `text-size` steps up with rank; hubs also appear earlier in zoom.
- The style gains a `glyphs` endpoint (labels are text; the console style
  had none because ribbons never needed it).

### Staging

1. ✅→ **Stations data.** GTFS stop names/parents into `Feed`; stations
   builder + emission; `/api/stations`.
2. ✅→ **Markers + labels in the console**, with rank-driven size,
   sort-key precedence, variable anchors, and the same dynamic time +
   class filters ribbons get.
3. **Bullets and agency icons in labels** (req 7, 8). Bullet images are
   generated client-side (canvas → `map.addImage`) from a per-system
   bullet spec: `{shape: circle|card|pill, bg, fg, text}` — default
   derived from route color/short name, curated per agency in
   `internal/style` config the way colors already are. Labels become
   `text` + `icon-image` composites (or formatted sections with inline
   images once we count on the fork).
4. **Inline bullets on trunks** (req 6): `symbol-placement: line` with
   the same bullet images, spaced by `symbol-spacing`, on high-rank
   trunk ribbons.
5. **Marker-spans-the-bundle**: size the white dot from the bundle's
   drawn width at the station (slots × pitch), so it reads as a bar
   across the parallel lines the way Apple draws Nevins St.
6. **World scale**: stations ride the same tile/feature-state path as
   ribbons (docs/DYNAMIC-SERVICE.md stage 5); rank thresholds per zoom
   keep tile size sane.

### Honest limits, known now

- A station's rank counts routes, not riders — Penn Station outranks
  Times Square by ridership but not by route count. If this ever reads
  wrong on the map, the fix is a curated rank boost in style config, not
  a data source we don't have.
- Bus stops are absent until stage 3+ decides how (and whether) to show
  them; the class toggle for bus governs ribbons only for now.
- Name-based merging is exact-match after normalization; "Court St" and
  "Borough Hall" stay separate stations (correct — Apple keeps them
  separate too, even though they interconnect).
