# Changelog

Notable changes per release. Portolan is pre-1.0: the drawn geometry is
still allowed to move between minor versions, and when it does it is said
here plainly — a downstream renderer that pins pixel diffs cares about
that more than it cares about the API.

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
