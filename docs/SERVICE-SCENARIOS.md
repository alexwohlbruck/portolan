# Service scenarios — time-of-day / day-of-week maps

> **Direction change (2026-08-08):** the rendering target is now FULLY
> DYNAMIC — one union layout per region, any timestamp applied at render
> by filtering + re-centering within the union slot order. See
> [DYNAMIC-SERVICE.md](DYNAMIC-SERVICE.md). The derivation below stays
> (it powers the Service grid and the QA harness), but per-scenario
> prebuilt layouts stop being the product: at world scale, with every
> GTFS feed combined, per-timetable artifacts cannot be the unit of
> rendering.

NYC runs different railroads at different times: routes short-turn, swap
express/local tracks, reroute entirely, or stop running at night and on
weekends. One union map drawn from every pattern that ever runs shows
phantom service (the R's weekend Manhattan-Bridge reroute sliced through
SoHo on the all-service map). The scenario system derives the distinct
maps a rider can actually experience and gives each one a full pipeline
layout.

## Derivation (internal/gtfs/service.go — fully generic)

1. **Activity histograms.** calendar.txt day masks × stop_times.txt trip
   spans → per pattern, a 7×24 grid of "trips in service during this
   (day, hour)". GTFS hours ≥ 24 spill into the next day.
   calendar_dates.txt (holidays) is deliberately ignored. Only rail
   route_types count — buses would fragment everything.
2. **Per-hour pattern sets.** Most-active patterns per route until the
   `cover` dial (0.99), with a 5% share floor (`floorFrac`) — per-hour
   trip counts are too small for coverage pruning alone to cut one-off
   variants.
3. **Drawn-map equivalence.** Two hour cells are the same scenario when
   they draw the same ink: each pattern's shape is dilated (60 m) onto a
   100 m grid, bucketed per route color; cells merge when no color's
   footprint differs by more than a few jitter cells (`mergeMaxDiff`).
   This is the load-bearing idea — trip mixes churn hourly (put-ins,
   short-turns, opposite-direction shape pairs, expresses riding the
   local's corridor) without changing what is drawn, while a night
   local pattern, a weekend reroute, or a suspended branch changes the
   ink. Hours chain along the week ring, blocks then merge pairwise,
   and lone transitional hours (5 am ramp-ups) are absorbed into their
   nearest neighbor.
4. **Stable IDs.** A scenario's ID is a hash of its final pattern set.
   Derivation must be process-deterministic (the atlas lists scenarios
   in one process; a build re-derives them in another): never let map
   iteration order leak into geometry — the raster frame anchor is
   pinned to the lexicographically-smallest shape id (this exact bug
   shipped once: per-process hash seeds shifted the grid and flipped
   borderline merges).

NYC (feed 5) yields ~15 scenarios: weekday midday block, weekend day,
overnight, and each weekday rush/evening hour whose extra services
genuinely add ink (Rockaway Park branch, rush-only routes). Chicago
(feed 29) yields 5. Route lists confirm real structure: overnight drops
B·C·W·Z·GS and the expresses; weekends drop B·W·Z·6X·7X but keep C.

## Derivation vs selection — they are NOT the same set

Scenarios are **derived** from the primary feed's rail and **select**
every drawable pattern. The split is load-bearing in both directions.

Deriving from more than the backbone shatters the week: the MTA
railroads issue a `service_id` per date, so their pattern mix churns from
Tuesday to Wednesday without the drawn map changing, and folding them
into derivation turned NYC's clean 15 scenarios into 36 ragged ones
(`Tue 10–16,20–22; Wed 10–15; Fri 20–21`). Bus timetables would be
worse. The rail backbone is also what a rider reads a service change off.

Selecting only the derived set is the mirror bug, and it shipped: a
scenario derived from the primary feed named patterns by unprefixed route
id, while every overlay route carries an `f<i>:` prefix — so NYC's
Saturday map came out **subway-only**, no LIRR, no ferries, no buses.
Selection therefore runs over all feeds and all drawable classes, keyed
on the scenario's hour cells: buses and ferries now appear and vanish on
the backbone's boundaries. (Late night correctly draws no ferry at all —
NYC Ferry does not run 00:00–05:00.)

## Feed shapes that broke this

Three feed conventions each emptied the feature on their own:

- **No `calendar.txt`.** LIRR and Metro-North ship `calendar_dates.txt`
  only, one `service_id` per date, and derivation errored out. Weekly
  masks now fall back to the dates themselves, each weekday weighted by
  how often it recurs, so a ten-Wednesday service reads as Wednesday
  service while a holiday one-off carries weight 1 and dies against the
  per-route floor. Feeds that DO have `calendar.txt` still ignore
  `calendar_dates` — the map shows regular service, not holidays.
- **Frequency-based feeds.** JFK's AirTrain templates every trip at
  `00:00:00` and states the real window in `frequencies.txt`. Read
  literally, a 24-hour service appeared in the late-night scenario and
  nowhere else. `frequencies.txt` now sets the span (first start to last
  end, plus one trip's running time — the final departure still finishes).
- **Clip order.** `clipPatterns` rewrites `ShapeID` to `<shape>#clipN`,
  so a scenario filter running after it can never match a pattern that
  touched the window edge. Scenario selection runs **before** the bbox
  clip.

`portolan scenarios --gtfs <list> [--routes]` lists the ids and, with
`--routes`, each scenario's routes — the fastest way to check a city's
service logic without building anything.

## Builds

`pipeline.ChartOpts.Scenario = <id>` restricts the rail patterns to the
scenario's set; MATCH→SPLIT→ORDER→FAIR then lay out only what runs.
Because each scenario is a full re-layout, lines re-center and re-pack
when others vanish — Brighton is a [B|Q] pair weekdays and a single
centered Q on weekends, with the Flatbush bundle re-packing from 4 slots
to 3. No client-side filtering could do that.

Outputs land beside the union build: `build/nyc.geojson` →
`build/nyc.scen-<id>.geojson`. Scenario builds are faster than the union
(~20 s vs ~40 s for NYC — fewer patterns). The union build, score card,
and gates are untouched: scoring always runs against the all-service
build.

## Atlas

- `GET /api/scenarios?feed=` — `{available, scenarios:[{id,label,
  patterns,built}], grid[7][24]→id}`. Derivation cached on the GTFS
  zip's mtime (the stop_times scan costs a few seconds).
- `POST /api/run?cmd=chart&feed=&scenario=<id>` — build one scenario.
- `GET /api/build.geojson?feed=&scenario=<id>` — serve its layout.

The map page grows a **service** section: day-group buttons (grid rows
that match collapse — NYC shows all/Mon/Tue–Fri/Sat/Sun), an hour
slider, and a "now" button. Selecting a time resolves (day, hour) →
scenario and swaps the build source's data; sliding within one scenario
never re-fetches. Unbuilt scenarios show the all-service map plus an
inline ⟳ build button. Selection persists per feed (localStorage) and in
the URL as `?service=<day>.<hour>` for captures.

Debug layers (paths, trackcenter, nodes) always show the union build's
dumps — the scenario's own stage dumps exist beside its geojson if you
need them.

## Diagnostics

`internal/gtfs/service_diag_test.go` prints the derived scenarios per
feed; `service_det_test.go` guards process determinism. Both skip when
the local GTFS zips are absent.

## Picking a time

The viewer takes a **timestamp**, not a scenario id. `/api/scenarios`
already returns `grid[day][hour] -> scenario id`, so an instant resolves
by weekday and hour — which is the structure GTFS calendars actually
carry. Any date in the year works; it resolves to its weekday's service.
Two consequences worth stating plainly:

- **Holidays follow regular service.** Derivation ignores
  `calendar_dates` for feeds that have a `calendar.txt` (see above), so
  25 December resolves to ordinary Friday service. The map shows the
  regular timetable, not the holiday one.
- **Resolution is hourly.** A cell is an hour, so 14:05 and 14:55 are the
  same map. Sub-hour precision would need the activity histogram to be
  finer than the 7x24 grid.

**No time is a value, not a gap:** an empty timestamp means the
all-service union map — every pattern that ever runs. That is the default
state, and clearing the field returns to it.

An hour whose scenario has not been laid out yet keeps the current map on
screen and offers to build it, rather than blanking. An hour with no
service at all says so.

The moment lives in the URL as `?t=<local ISO>`, so a time is linkable;
no parameter is the union map. Choosing a map from the list instead drops
the timestamp, so the two controls never disagree on screen.

## Storage and transport

A scenario ships as a complete redraw (`build/<city>.scen-<id>.geojson`),
which is a strange price for "the same city with some lines not running":
69% of a scenario's geometry is byte-identical to the union build's, and
only 28 features need a different offset. Strip coordinates and a
scenario's information content is 0.46 MB against an 10.9 MB file.

Two transport fixes ship today and need no pipeline change: the workbench
gzips responses (3.1x), and `/api/build.geojson?band=N` serves one zoom
band — FAIR emits a complete copy of the map per band and exactly one is
ever visible. NYC over the wire at default zoom: **11.54 MB -> 1.32 MB**.

Geometry sharing across scenarios IS built, at the transport layer:
`POST /api/build-delta` content-addresses every geometry and sends only
what the client lacks. A scenario switch costs **0.39 MB** on first visit
and **0.05 MB** on a revisit, against 11.54 MB for the old whole-file
fetch, and the assembled result is byte-identical to
`/api/build.geojson`. See [SCENARIO-DELTA.md](SCENARIO-DELTA.md).

The remaining pipeline change — stable segmentation and transitions as
offset ramps, which would shrink first visits too — is specced but not
built; the blocker is that FAIR's junction treatment is set-dependent, so
stable segmentation needs FAIR to know when a junction is inert.
