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

`await using` stops the engine when the scope ends. It needs TypeScript
5.2+ and `Symbol.asyncDispose` at runtime (Node 20+); on an older target
just call **`p.stop()`**, which is always available and is the same
thing. The syntax is sugar, not the interface.

Either way the package installs a `process.on("exit")` reaper, so an
engine cannot outlive its parent even on `process.exit()` or an uncaught
throw — see *Lifecycle* below for what that does and does not cover.

Nothing above touches the filesystem. `gtfsInline` and `corridorsInline`
put the feed tables and the corridor graph in the request body, which is
what an editor wants: a colour change touches `routes.txt` and every
route edit touches `stop_times.txt`, so writing a zip per keystroke is a
round trip bought for nothing.

## Two processes, one engine

In Electron, **main owns the process and the renderer owns the
traffic.** Routing artifacts through IPC structured-clones them, which
is exactly the main-thread cost this format exists to remove.

```ts
// main: own the lifecycle, hand out the address
const p = await portolan();
ipcMain.handle("portolan:addr", () => ({
  origin: p.server.origin,
  token: p.server.token,
}));
app.on("before-quit", () => p.stop());
```

```ts
// renderer (or a worker): own the traffic
import { PortolanClient } from "@alexwohlbruck/portolan/client";

const { origin, token } = await ipcRenderer.invoke("portolan:addr");
const client = PortolanClient.at(origin, token);
const job = await client.chart({ ... });
const plnb = await job.plnb();          // bytes never cross IPC
```

**Import from `/client` in the renderer, not from the package root.**
The root exports `PortolanServer`, so it pulls `node:child_process`, and
an Electron renderer with context isolation has no Node — the bundler
fails the build. `/client` and `/plnb` are Node-free by construction and
there is a test that keeps them that way.

| subpath | holds | needs Node |
| --- | --- | --- |
| `@alexwohlbruck/portolan` | everything, incl. `portolan()` and `PortolanServer` | yes |
| `@alexwohlbruck/portolan/client` | `PortolanClient`, `Job`, `PortolanError` | no |
| `@alexwohlbruck/portolan/plnb` | `decodePlnb`, `Plnb`, `SUPPORTED_PLNB` | no |

`PortolanClient.at()` is the piece that makes this split possible. The
one-liner at the top is the right shape for a CLI or a server; for a UI
process, use this one.

## Reading the output

`job.plnb()` returns a decoded build whose property arrays are **views
over the response buffer**, so per-feature values are read by index with
no allocation.

```ts
const plnb = await job.plnb();

plnb.degrees();        // Float64Array, flat [lon, lat, lon, lat, …] — for the GPU
plnb.starts;           // Uint32Array, featureCount + 1 vertex offsets
plnb.positions;        // Int32Array at 1e-7 degrees — the wire form, rarely what you want

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

### Getting it onto the GPU

Use **`degrees()`**, not `positions`. Every WebGL path — deck.gl,
MapLibre, raw GL — wants degrees in the vertex buffer, because
projection happens in the shader from lng/lat, and nothing off the shelf
divides by 1e7.

```ts
new PathLayer({
  data: { length: plnb.featureCount, startIndices: plnb.starts,
          attributes: { getPath: { value: plnb.degrees(), size: 2 } } },
  _pathType: "open",
});
```

It is `Float64Array` and that is not a preference: a float32 holds about
seven significant digits where a longitude needs nine, so an f32 buffer
quantises vertices to roughly two metres and visibly kinks a ribbon —
the same reason the wire format is `i32` and not `f32`. deck.gl takes
f64 and splits it into hi/lo f32 pairs for exactly this.

`degrees()` is one pass over the buffer, cached, so calling it per frame
costs nothing after the first.

## Lifecycle

The package **reaps**; it does not **supervise**.

Reaped for you: scope exit via `await using`, an explicit `stop()`, and
a `process.on("exit")` / SIGINT / SIGTERM hook that kills any live engine
on the way out. An engine cannot outlive its parent and sit on a port.

**Yours:** restart policy. Whether to retry a crashed engine, how many
times, with what backoff, and when to give up and degrade are product
decisions, and a library guessing them is worse than a caller choosing
them. One rule worth stating: an `IncompatibleEngineError` must **not**
be retried — it will fail identically every time.

```ts
p.server.running;   // false once it has died
```

## Version handshake

```ts
p.version;   // { version: "0.3.1", plnb: 1, formats, bands, auth }
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

The engine package is pinned to this package's exact version and
`resolveBinary()` checks it, because npm **skips an unresolvable
optional dependency without failing the install** — so a mismatch would
otherwise surface as a missing binary at the first chart call rather
than as the version problem it is. Both errors extend
`BinaryNotFoundError`; neither is worth retrying.

> Node says `x64` where Go says `amd64`, for the same architecture. The
> package names use Node's spelling because npm resolves them against
> `process.arch`; the release archives use Go's because `GOOS`/`GOARCH`
> built them. The mapping lives in one script and nowhere else.

## Curation

Colours, names, bullet shapes and fonts are curation, not feed data —
and for an editor they are live state, changing whenever the user
recolours a line. So send the document, not a path:

```ts
await p.chart({
  ...,
  styleInline: {
    routes: { L: { color: "EE352E", shape: "diamond", font: "italic" } },
    agencies: { CR: { name: "Commuter Rail", color: "80276C" } },
  },
});
```

`styleInline` replaces `styleDir`/`city` when set, and needs nowhere
writable — which matters more than it sounds: a packaged app that must
write a file first needs a writable location AND a way to reach it, and
that path is a surface rather than a detail.

`styleDir` still works when your curation genuinely lives on disk:

```ts
await p.chart({ ..., city: "sf", styleDir: "/writable/path/style" });
```

The class defaults are compiled into the engine, so a directory holding
only your own document is complete — there is nothing to copy out of the
package first. Either way the document is **subject-keyed**:

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
