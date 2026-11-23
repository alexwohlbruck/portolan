# Fairway: ferries down the middle of the river

OSM maps ferry lanes per operator, and a busy river carries several of them
side by side. Matched onto those lanes, every ferry route on the East River
drew its own line, and together they read as a wide blue band. Apple draws
one line down the middle of the river. The fairway stage does the same.

It is opt-in per feed: set `water` in `portolan.json` to a shoreline extract
(`tools/feed.sh water <feed>`). Without it, ferries ride the OSM lanes as
before.

## How the centerline is built (`internal/fairway`)

1. **Water.** Coastline ways split the window into regions. Each region is
   voted sea or land by which side of the coastline it lies on (OSM keeps
   land on the left). Water areas (`natural=water`, `waterway=riverbank`) are
   filled on top, even-odd, so islands in a relation stay land.
2. **Shoreline smoothing.** The water raster (25 m cells) is opened, then
   closed, by 60 m: slips between piers fill in and the piers vanish. Land
   patches under 0.25 km² (barges, buoys, small piers) become water.
3. **Medial axis.** Zhang–Suen thinning, then any pixel whose neighbours stay
   connected without it is removed, so every junction is one pixel.
4. **Pruning.** Dead ends shorter than 2.5× the channel half-width at their
   junction are spurs from shoreline bumps and go. Only reaches whose
   half-width is between 60 m and 1,100 m are kept: wider is open water,
   where a medial axis means nothing. Pieces shorter than 1.5 km go.
5. **Smoothing.** Each reach is resampled at 20 m and Gaussian-smoothed along
   its arc (σ 150 m) with its ends pinned, so junctions still meet.

## How ferries use it (`internal/pipeline/fairway.go`)

Each ferry shape is resampled at 20 m and pulled toward the nearest
centerline point. The pull is full where the shape is within 0.75 of the
half-width of the middle and runs within 25° of the river, and fades to
nothing at the bank or at 50°. The weight is blurred along the shape, so the
line eases off the centerline into a pier instead of kinking.

OSM ferry lanes are dropped from the match graph for that feed. With nothing
to match, MATCH chords each snapped shape verbatim. Matching onto the
centerline as track was tried and abandoned: a route zig-zagging between
piers on both banks walked dead-end reaches and gap-jumped across land.

Ferries trunk by `class` (one ribbon for every ferry in the feed), so a shared
stretch is one line rather than parallel ribbons.

## Limits

- Snapped shapes are separate gap pieces, not shared edges. Routes on one
  river coincide on the centerline but are drawn one on top of another.
- Each feed is built on its own, so two operators on one river land on the
  same centerline only as closely as their snaps agree.
- In open water (a harbour, a sound) ferries follow their own shapes.
