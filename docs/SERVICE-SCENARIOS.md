# Service scenarios — time-of-day / day-of-week maps

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
