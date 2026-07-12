# PAR-12 — Transit layer (distilled)

Source: Linear PAR-12 "Transit layer" (Parchment / Web client), created
2023-11-22, in progress. This is the founding requirements document for
portolan; the full ticket lives in Linear.

## Requirements

- A clean, precise, toggleable map of transit routes and stops (rail, buses,
  ferries) over a clean base style; routes/stops clickable with detail panels
  (timetables, delays, names).
- **Interlined routes display as grouped side-by-side parallel lines** à la
  Apple Maps / Transit app, with common stations grouped.
- Geometry as true to life as possible while still reading as a line map.
- **Continuous zoom**: interlined groups keep relative spacing and smooth
  curve radii while zooming — no discrete re-render jumps.

## Case studies (the bar)

- **Apple Maps, Chicago Loop**: the crown. Neat lines, station platforms,
  groups stay coherent through zoom with smooth curves.
- **Google Maps**: spaghetti when zoomed out, cluttered stop icons — the
  anti-goal.
- **Transit app**: beautiful grouped lines and station pills, but discrete
  zoom re-renders.

## Key technical conclusions (from the ticket, validated in attempt two)

- **Offsets must be client-side** for continuous zoom; baked offsets breathe.
  MapLibre has no native mechanism to reconnect lines that change offset
  order — hence the parchment **MapLibre fork with variable line-offset
  (line-progress keyed)**, plus junction cutouts reconnected with smooth
  curves (the transitapp blog approach). Any solution must be portable to
  mobile SDKs.
- **Junctions and ordering**: when a line joins a group it needs a slot that
  minimizes crossings — LOOM's problem (ad-freiburg/loom solved it; C++,
  heavy, batch).
- **Path matching**: GTFS shapes conflated to OSM ways (pfaedle does this
  for whole feeds).
- **Data**: GTFS static + GTFS-RT from agencies; aggregators transitland /
  mobilitydata. Preprocessing needs per-agency overrides (missing colors,
  ugly names) and OSM conflation for stops too.

## Reference links (from the ticket)

- transitapp blog: "How we built the world's prettiest auto-generated transit
  maps" (junction cutout + curve reconnection technique)
- ad-freiburg/loom (line ordering), ad-freiburg/pfaedle (GTFS↔OSM matching)
- mapbox-gl-js issues #10374, #12729 (offset-order reconnection gap)
- Examples: anytrip.com.au, theweekendest.com, subwaysheds.com

## Division of labor (portolan vs parchment)

portolan owns: GTFS/OSM ingest → bundle graph → attribution → ordering →
fairing → emitted line-map geometry + gates/tools.

parchment keeps: the MapLibre fork rendering (variable offset), live-vehicle
layers, route detail UI, tile serving/registry, stops UI. portolan's output
must stay compatible with the `transit_line_segments` shape (slots,
travel-frame offsets, zoom bands) or an equivalent contract.
