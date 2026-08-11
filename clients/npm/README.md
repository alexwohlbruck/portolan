# @alexwohlbruck/portolan

Automatic transit line maps, as a Node package. Spawns the
[portolan](https://github.com/alexwohlbruck/portolan) engine, speaks its
build API, and decodes its binary output.

**The engine ships inside the package.** One binary per platform, pulled
in as an `optionalDependency`, so npm installs only the one matching your
machine and a version bump here ships a new engine. No download step, no
`postinstall`, nothing to keep in sync.

```bash
npm install @alexwohlbruck/portolan
```

## Use

```ts
import { portolan } from "@alexwohlbruck/portolan";

await using p = await portolan();

const job = await p.chart(
  {
    gtfsInline: {
      "routes.txt": "route_id,route_short_name,route_type,route_color\nR1,1,1,EE352E\n",
      "stops.txt": "stop_id,stop_name,stop_lat,stop_lon\ns1,Alpha,37.77,-122.42\n",
    },
    corridorsInline: graph,   // a GeoJSON corridor graph
    format: "bin",
    band: 15,
  },
  { onProgress: (e) => console.log(e.stage, e.pct) },
);

const plnb = await job.plnb();
```

`await using` stops the engine when the scope ends. Without it, call
`p.stop()` — an orphaned engine per rebuild is the failure this most
wants to prevent.

Nothing above touches the filesystem. `gtfsInline` and `corridorsInline`
put the feed tables and the corridor graph in the request body, which is
what an editor wants: a colour change touches `routes.txt` and every
route edit touches `stop_times.txt`, so writing a zip per keystroke is a
round trip bought for nothing.

## Reading the output

`job.plnb()` returns a decoded build whose typed arrays are **views over
the response buffer** — no copying — so positions go straight into a GPU
buffer and per-feature values are read by index without allocating.

```ts
const plnb = await job.plnb();

plnb.positions;        // Int32Array, [lon, lat] pairs at 1e-7 degrees
plnb.starts;           // Uint32Array, featureCount + 1 vertex offsets

for (let i = 0; i < plnb.featureCount; i++) {
  const [a, b] = plnb.vertexRange(i);   // positions[a*2 .. b*2)
  plnb.colorHex(i);                     // "#ee352e"
  plnb.offsetPx(i);                     // lateral slot offset in px
  plnb.routes(i);                       // ["R1"] — verbatim, never normalised
  plnb.bandMin(i), plnb.bandMax(i);     // HALF-OPEN: [min, max)
}
```

`plnb.feature(i)` gives a plain object, and `plnb.toGeoJSON()` the whole
thing — both allocate, so neither is the fast path.

Positions are integers at 1e-7 degrees rather than `f32`: a float32 holds
about seven significant digits where a longitude needs nine, which would
quantise vertices to roughly two metres and visibly kink a ribbon.

## Version handshake

```ts
p.version;   // { version: "0.3.0", plnb: 1, formats, bands, auth }
```

**Gate on `plnb`, not `version`.** It is the binary layout number, and it
changes when a column moves — which is the thing a decoder depends on.
`version` tells you what you got; `plnb` tells you whether you can read
it. `portolan()` refuses to start on a mismatch by default; pass
`requireCompatible: false` and use `format: "geojson"` if you would
rather degrade than fail.

## Security

The engine is started with a **random bearer token per launch**, and you
should leave it that way. A chart request names files for the engine to
read (`gtfs`, `styleDir`, `corridors`), so an unauthenticated port is a
file-read oracle for every other process on the machine — binding to
loopback is not a boundary between processes on one host.

`/version` and `/healthz` stay open so a supervisor can tell "not up yet"
from "wrong token".

## Choosing a binary

Resolution order, first hit wins:

1. `portolan({ binary: "/path/to/portolan" })`
2. `$PORTOLAN_BIN` — a dev override, or a copy you bundled yourself
3. the per-platform optional dependency (the default)

If you are packaging this inside a signed application and would rather
place the binary yourself, use `PORTOLAN_BIN` and install with
`--no-optional` to skip the ~3 MB download.

Supported: `darwin-arm64`, `darwin-x64`, `linux-arm64`, `linux-x64`,
`win32-arm64`, `win32-x64`.

> Node says `x64` where Go says `amd64`, for the same architecture. The
> package names use Node's spelling because npm resolves them against
> `process.arch`; the release archives use Go's because `GOOS`/`GOARCH`
> built them. The mapping lives in one script and nowhere else.

## Curation

Colours, names, bullet shapes and fonts are curation, not feed data.
Point `styleDir` at a directory holding your `<city>.json`:

```ts
await p.chart({ ..., city: "sf", styleDir: "/writable/path/style" });
```

The class defaults are compiled into the engine, so a directory holding
only your own document is complete — there is nothing to copy out of the
package first. The document is **subject-keyed**:

```json
{ "routes": { "L": { "color": "EE352E", "shape": "diamond", "font": "italic" } } }
```

A document using the wrong shape is a hard error naming the right one,
rather than parsing cleanly and applying nothing.

## Versioning

This package's version tracks the engine it carries, and the two are
published together. Pin an exact version if your renderer depends on the
geometry: portolan is pre-1.0, so the map is still allowed to move
between minor versions.

See [docs/CORRIDORS.md](https://github.com/alexwohlbruck/portolan/blob/main/docs/CORRIDORS.md)
for the corridor graph contract and
[docs/CLI.md](https://github.com/alexwohlbruck/portolan/blob/main/docs/CLI.md)
for the full API.
