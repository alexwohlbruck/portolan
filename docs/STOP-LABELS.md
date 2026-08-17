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
2. Same-named groups merge only where a rider doesn't pay again.
   Within one feed, **transfers.txt is the ground truth whenever the
   feed ships it**: NYC's two "Rector St" stations sit a block apart
   with NO transfer → two stations, two labels, forever; the four
   linked "Fulton St" platforms are one complex — and the G train's
   unconnected "Fulton St" stays its own. Feeds without transfers fall
   back to a tight 150 m name match (looser folds the two distinct
   "23 St" stations). Across feeds there are no ids or transfers to
   link — 300 m name match (terminals sprawl: the LIRR's Madison
   concourse is 276 m from Metro-North's Grand Central centroid).
   Cross-feed groups with DIFFERENT names stay separate:
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

1. ✅ **Stations data.** GTFS stop names/parents into `Feed`; stations
   builder + emission; `/api/stations`.
2. ✅ **Markers + labels in the console**, with rank-driven size,
   sort-key precedence, and the same dynamic time + class filters
   ribbons get.
3. ✅ **Snapping (SnapStations).** Every station moves onto the DRAWN
   map: per member route, the nearest band-15 ribbon carrying it;
   routes snapping to one spot become one `Marker` (a complex like
   Times Sq is one label + one marker per corridor). Markers carry
   `bearing`, `span_px` (bundle width = (nslots−1)·pitch) and
   `dot_off` (the line's own slot offset).
4. ✅ **Markers as generated icons.** Single line → borderless dot in
   the line's color, its slot offset BAKED INTO the image so
   `icon-rotate: bearing` carries it to the correct side; `icon-size`
   then scales image and offset together, matching zoomScaledOffset
   exactly. Multiple lines → a white rounded-rect pill spanning the
   bundle (corner radius = dot radius), rotated to lie across the
   corridor.
5. ✅ **Bullets in labels** (req 7, 8): MTA-style circles for 1–2 char
   labels, rounded-corner word pills for longer (the Chicago
   'Red'/'Brown' shape falls out for free); yellow bullets get dark
   glyphs by luminance. Express variants (7X, FX) fold into their
   parent. The strip is ONE composed canvas image per station rendered
   as the symbol's icon below the name.
6. ~~Inline bullets on trunks~~ (req 6) — **removed, owner call
   2026-08-08.** Every shape of it compromises: a strip icon can't
   follow curves and tilts or garbles digits; per-point placement in
   meters can't hold constant screen pitch across zoom; per-zoom
   rebuilds churn during pinch. Revisit only if the fork grows
   line-following icon placement with upright glyphs.
7. ✅ **Complexes split by zoom.** Below z15 a complex is one merged
   label with merged bullets; at z15+ each corridor marker gets its own
   label with ITS bullets (Fulton St becomes 4·5 / A·C·J·Z / 2·3), and
   the merged label bows out. Solo-marker stations keep one label at
   every zoom.
8. **Agency icons in labels** (rest of req 7): needs curated logo
   assets per agency — no honest way to generate them.
9. **World scale**: stations ride the same tile/feature-state path as
   ribbons (docs/DYNAMIC-SERVICE.md stage 5); rank thresholds per zoom
   keep tile size sane.

### Bullet ordering

Systems have local conventions for the order bullets read in, and no
single sort satisfies them all. What the world actually does:

- **NYC (MTA)**: bullets group by trunk color — the 1979 color scheme
  groups services by their Manhattan trunk — and the service listing
  runs letter groups before number groups (A,C,E / B,D,F,M / … /
  N,Q,R,W / 1,2,3 / 4,5,6 / 7). Apple renders W 4 St as A·C·E B·D·F·M
  and Columbus Circle as A·C B·D 1.
- **GTFS itself**: `route_sort_order` is the spec's own presentation
  order; MBTA, TriMet and PATH ship it, the Transit app consumes it.
  When an operator says what order its routes read in, believe them.
- **London, Chicago**: named lines, listed alphabetically.
- **Paris, Berlin, Mexico City**: numbered lines, listed ascending.

Portolan's policy knob is `bullet_order` in style config (global or
per city), one of:

- **`color`** (default): group by resolved bullet color, natural order
  within a group, letter groups before number groups. Where every line
  has its own color — London, Paris, Chicago, CDMX — this degrades to
  exactly the natural order those systems expect, which is why it can
  be the default everywhere.
- **`feed`**: obey `route_sort_order`, absentees last, natural
  fallback.
- **`natural`**: plain numeric-aware sort (1 2 10 A B).

The order is decided once, in `sortBullets` (pipeline), and every
aligned array, label strip and marker inherits it.

### Fork bug, worked around

Images inside `text-field` `format` expressions corrupt the fork's
per-tile glyph/image atlas: on dense tiles the bullet slots render as
unrelated glyphs (deterministic per tile, worse at lower zooms — z13.8
midtown garbled nearly every label). Bisect ruled out sort keys,
variable anchors, image registration timing, names and bullet sets;
the trigger is images-in-formatted-text itself. Workaround shipped:
bullets NEVER ride in text — each strip is one composed image on the
icon channel. Cost: `text-variable-anchor` is off for station labels
(the icon does not follow the text's variable anchor), so labels
anchor below the marker. Revisit if the fork picks up the upstream
atlas fix.

### Honest limits, known now

- A station's rank counts routes, not riders — Penn Station outranks
  Times Square by ridership but not by route count. If this ever reads
  wrong on the map, the fix is a curated rank boost in style config, not
  a data source we don't have.
- Bus stops are absent until a later stage decides how (and whether) to
  show them; the class toggle for bus governs ribbons only for now.
- Name-based merging is exact-match after normalization; "Court St" and
  "Borough Hall" stay separate stations (correct — Apple keeps them
  separate too, even though they interconnect).
- Dots reflect UNION slot offsets: when dynamic time-filtering
  re-centers a bundle's survivors, a dot can sit a few px off its
  re-centered ribbon. Same residual class as FAIR mask-inertness.
- Inline trunk bullets skip offset ribbons (an express/local pair shows
  bullets only on the centered one); following the ribbon offset needs
  renderer support.

## Station names: overrides, and matching OSM

Two mechanisms answer "what is this station called", and they are
deliberately different in kind.

**Manual overrides** are curation, and live in files — one document per
city under `style/`, not a database and not mixed into the feed registry.
A city's colours and names are source code: reviewed in a diff, blamed,
reverted. `portolan.json` stays what it always was, the list of inputs and
where they live on disk.

    style/_default.json     applies to every city
    style/<city>.json       one city, layered on top field by field

The document is **subject-keyed**: a route is named once and carries
everything known about it. The older tables were keyed by attribute —
`"route:501"` appeared once in `colors` and again in `names` — so one
line's identity was split across the file and every new attribute meant a
new table.

    {
      "routes": {
        "501":      { "name": "Blue", "color": "0067B1" },
        "Red Line": { "name": "Red" }
      },
      "agencies": { "IDFM:71": { "line_colors": true } },
      "stops":    { "place-pktrm": { "name": "Park Street" } },
      "modes":    { "tram": { "width": 0.8 } },
      "options":  { "caterpillars": true, "osm_stop_names": false }
    }

Subject keys are **matchers, not ids**: a route matches on its id, its
short name OR its long name, an agency on id or name, a stop on id or
name, all case-insensitively. Ids are feed bookkeeping (`f2:1`) and the
config should be able to say what a human would say.

A **route** override replaces the bullet text and the trunk label
(`stages.fair`'s `label()` and `displayLabel` both consult it). An
**agency** override replaces a multi-route agency trunk's label;
`line_colors` on an agency is the old `line_agencies` array, moved here so
one operator decision does not live in a different file from every other
operator decision. A **stop** override replaces the station's IDENTITY,
not just its drawn text: the same string feeds `normName` and the
same-name merge, so a renamed station cannot disagree with itself between
the pill and the complex it belongs to.

Both the CLI and the atlas resolve through `style.LoadDir`, so there is
exactly **one merge implementation**. The previous design had the shell
merge the layers with `jq` and Go merge them again for the dashboard, and
the two drifted: CLI builds silently dropped `bullet_order` for weeks.

Only what a human typed is persisted. Names discovered by the OSM matcher
below are derived at build time and are never written back — a config
that recorded them would freeze one day's OSM against every later build
and quietly stop tracking upstream.

**OSM matching** is automatic, and fills in the rest of the world.
`tools/feed.sh stops <city>` fetches the window's named transit stops
(`railway=station|halt|tram_stop`, `public_transport=station|stop_position`,
`aerialway=station`, `amenity=ferry_terminal`) and `internal/pipeline/
osmstops.go` pairs each drawn station with the one that is really the same
place. Three signals must agree, because a wrong rename is worse than no
rename: **proximity** (220 m ceiling, distance still weighted inside it),
**name** (token-set similarity after folding case, accents, punctuation,
"Station"/"Estación"/"Bahnhof", and the directional suffixes feeds
append), and **class** (a light-rail station may not match a bus
`stop_position` sharing its corner, however similar the names). Matching
is one-to-one and greedy by score. The extract is opt-in per city, and its
presence IS the switch — a city with no `stops` path is untouched.

A match always attaches the OSM object id, emitted as `osm` on the station
feature (`"node/5106080553"`), so a consumer can join a drawn station back
to OpenStreetMap. Whether it also renames is the `osm_stop_names` knob
(default on), and two laws constrain the rename:

**A rename must ADD information.** OSM is not uniformly better. Mexico
City's feed has "Tláhuac" and "Textitlán" where OSM has them unaccented,
and "Lomas de la Estancia" where OSM shouts "De La"; Boston's feed has
"Green Street" where OSM has a bare "Green". So: same name modulo case,
punctuation and noise words → richer diacritics win, then the shorter
spelling (which drops the feed's "Station" suffix without letting OSM's
"Back Bay Station" back in), and a dead heat keeps the feed's. And if
OSM's words are a strict subset of the feed's, the feed keeps its name —
OSM is the abbreviation there, not the improvement.

**A rename may not merge two distinct station names into one, and must
apply uniformly to every station that shared the old name.** Both halves
earn their keep on NYC. Without the first, OSM renames "Whitehall
St-South Ferry" to "South Ferry", which is a different station on the 1.
Without the second, the three genuinely distinct "Fort Hamilton Pkwy"
stations rename one at a time and the map shows one "Parkway" beside two
"Pkwy". Measured: NYC's duplicate-name count is identical before and
after matching (60 groups, 148 stations), which is the invariant to check
when touching this.

**NYC opts out of renaming** (`"osm_stop_names": false`) while keeping its
ids. The MTA's own abbreviations — "42 St-Port Authority Bus Terminal" —
are what the MTA and Apple print and what fits a label pill; OSM's
"42nd Street–Port Authority Bus Terminal" is better English and worse
cartography. That is a curation call, so it lives in config.

Match rates are a data-quality signal worth watching: Mexico City 221/225
stations, Boston 184/198, Charlotte 28/43, NYC 179/566 (NYC's low rate is
its enormous bus-stop population in the extract diluting nothing — the
rail stations that matter mostly match; the misses are minor complexes
OSM models as several nodes with none tagged as the station).

**Transitland onestop ids are reachable but not wired.** `GET /stops?
feed_onestop_id=…` returns `onestop_id` alongside the feed's own
`stop_id`, so the join is a straight `stop_id` lookup — verified against
`f-drt-mbta`. What it needs is a `feed_onestop_id` per city row, a
paginated fetch into a cache file (the pipeline must stay offline and
deterministic), and un-prefixing the `f2:` overlay tags loadFeeds stamps
on multi-feed ids.

## Ranking: which stations show first

Label and dot density is gated per zoom by **importance**, and importance
is a **percentile within the city**, not a raw count. That distinction is
the whole fix. The gate used to read `rank` = number of routes calling,
with absolute cut-offs (≥6 at z11, ≥2 at z13) tuned on NYC. Charlotte's
busiest station has two routes and every other has one, so the map drew
**one** label at z13 and **none** at z11 — with acres of empty space.

`rankStations` scores four signals the pipeline already knows:

    score = routes + 1.5·lines + 2·transfer-degree + 0.5·markers
          + 6 if terminus

**Transfer degree** — how many other stations `transfers.txt` links this
one to — is what lets a one-route station read as major, and it is the
honest signal: an interchange matters because you change there, which is
exactly what the file records. In Charlotte it puts CTC/Arena (31 links)
first and the downtown stops next, all of which route count called equal.

**Terminus** is the other end of that idea: the last stop of a line names
where the line GOES, so it earns a label ahead of the mid-line stops
around it however few routes call there. `Pattern.StopIDs` is sorted and
cannot answer this, so the loader now carries `TermAID`/`TermBID` beside
the existing `TermA`/`TermB` coordinates and the stations stage joins on
them exactly. The bonus is worth about three transfer links — enough to
lift a quiet end-of-line stop clear of its neighbours, not enough to
outrank a real interchange. Charlotte's UNC Charlotte–Main goes from
percentile 0 to 5 and I-485/South Boulevard to 43; NYC flags 74 of 566
stations and ranks Coney Island–Stillwell 98, Pelham Bay Park 87,
Flushing–Main St 87, Van Cortlandt Park–242 St 79. Short-turn terminals
count too, which is correct — they are destinations on the headsign.

The score is then converted to a 0–100 percentile, ties sharing a value
so that which of three equal stations survives is not decided by sort
order. The gate asks "top n% of THIS city", which is the question it
always meant.

**Labels crowd in progressively**, and getting there took overshooting in
both directions. Thresholds that are too tight label only the hubs and
leave the outer half of a line bare — the original failure, one label in
all of Charlotte. Thresholds that are too loose hand the decision to
collision, which fills every gap it can find and reads as a wall of text
(every station in Brooklyn labelled at z13). The balance: importance opens
by roughly a fifth of the city per zoom step, and collision thins whatever
still competes inside that. `symbol-sort-key` by importance settles who
wins where they do compete.

Dots start at **z12** with the majors and fill in by z14. Below that a
system reads better as clean ribbons than as a string of beads.

Admitted by the gate before collision trims:

| zoom | NYC dots | NYC labels | Charlotte dots | Charlotte labels |
|---|---|---|---|---|
| z12 | 307 | 123 | 20 | 10 |
| z13 | 413 | 227 | 38 | 18 |
| z14 | 600 | 273 | 44 | 25 |
| z16 | 600 | 566 | 44 | 43 |

Against the old rank gate, Charlotte drew 0 labels at z11 and 1 at z13.

**Known gap:** terminals score low when they have no transfers — Charlotte's
UNC Charlotte–Main sits at percentile 0 despite being the end of the line.
`Pattern.StopIDs` is sorted, so first/last stop is not recoverable there;
giving termini a bump needs the terminal set threaded out of the cut stage.
