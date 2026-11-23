#!/usr/bin/env python3
"""match_audit — did every pattern ride the OSM ways it should have?

An acceptance check for MATCH that trusts nothing in the build: it reads
the matched paths, the feed's own GTFS shapes and the OSM extract, and
measures each path independently.

Per path (kind=path features of <build>.paths.geojson):
  offnet   metres of the drawn path not lying on a way of its layer
           (route=ferry lanes for ferries, highways for buses, rail for
           the rest) — a gap chord, or a ride on the wrong layer
  detour   farthest the path strays from its GTFS shape: a wrong turn,
           a loop, a lane on the far side of the river
  miss     farthest the shape strays from the path: a stretch the path
           never covered
  ratio    path length / shape length over the same stretch
  stopgap  farthest a stop of the trip sits from the path: a pier or stop
           the drawn line never reaches

A shape whose vertices average more than --sparse metres apart is a set of
chords between stops (common in ferry feeds), not a survey of the route:
its detour and miss are reported but not held against the path, and the
stops become the reference instead.

A path passes when offnet <= --offnet-tol, detour and miss <= --tol, and
ratio is within --ratio. The shape is compared over the stretch between
the path's own end points, so terminal trims and bbox clips don't count
as misses.

  tools/match_audit.py --paths build/nyc-ferry.geojson.paths.geojson \\
      --gtfs data/gtfs/nyc-ferry.zip --osm testdata/nyc-rail.geojson
  tools/match_audit.py ... --mode bus --osm build/mta-bus-streets.geojson
"""
import argparse, csv, io, json, math, sys, zipfile
from collections import defaultdict

FERRY = {4, 1000, 1200}
BUS = {3, 11} | set(range(700, 717)) | {800}
HIGHWAY = {"motorway", "trunk", "primary", "secondary", "tertiary", "unclassified",
           "residential", "living_street", "busway", "bus_guideway", "motorway_link",
           "trunk_link", "primary_link", "secondary_link", "tertiary_link", "service"}


def mode_of(rt):
    if rt in FERRY:
        return "ferry"
    if rt in BUS:
        return "bus"
    return "rail"


def layer_of(props):
    if props.get("route") == "ferry":
        return "ferry"
    if props.get("highway") in HIGHWAY:
        return "bus"
    if props.get("railway") or props.get("aerialway"):
        return "rail"
    return None


class Frame:
    def __init__(self, lon0, lat0):
        self.lon0, self.lat0 = lon0, lat0
        self.kx = 111320 * math.cos(math.radians(lat0))
        self.ky = 110540

    def xy(self, lon, lat):
        return ((lon - self.lon0) * self.kx, (lat - self.lat0) * self.ky)


def seg_dist(p, a, b):
    ax, ay = a
    dx, dy = b[0] - ax, b[1] - ay
    L = dx * dx + dy * dy
    t = 0 if L == 0 else max(0, min(1, ((p[0] - ax) * dx + (p[1] - ay) * dy) / L))
    return math.hypot(p[0] - ax - t * dx, p[1] - ay - t * dy)


class Index:
    def __init__(self, lines, cell=100.0):
        self.cell, self.grid = cell, defaultdict(list)
        for line in lines:
            for i in range(1, len(line)):
                a, b = line[i - 1], line[i]
                for key in self._cells(a, b):
                    self.grid[key].append((a, b))

    def _cells(self, a, b):
        c = self.cell
        x0, x1 = sorted((a[0], b[0]))
        y0, y1 = sorted((a[1], b[1]))
        for i in range(int(math.floor(x0 / c)), int(math.floor(x1 / c)) + 1):
            for j in range(int(math.floor(y0 / c)), int(math.floor(y1 / c)) + 1):
                yield (i, j)

    def dist(self, p, cap):
        """Nearest segment within cap, searching outward ring by ring."""
        c = self.cell
        ci, cj = int(math.floor(p[0] / c)), int(math.floor(p[1] / c))
        best = cap + 1
        for r in range(int(math.ceil(cap / c)) + 1):
            for i in range(ci - r, ci + r + 1):
                for j in range(cj - r, cj + r + 1):
                    if max(abs(i - ci), abs(j - cj)) != r:
                        continue
                    for a, b in self.grid.get((i, j), ()):
                        d = seg_dist(p, a, b)
                        if d < best:
                            best = d
            if best <= r * c:
                break
        return best


def length(line):
    return sum(math.hypot(line[i][0] - line[i - 1][0], line[i][1] - line[i - 1][1])
               for i in range(1, len(line)))


def resample(line, step):
    out = [line[0]]
    carry = 0.0
    for i in range(1, len(line)):
        a, b = line[i - 1], line[i]
        L = math.hypot(b[0] - a[0], b[1] - a[1])
        s = step - carry
        while s <= L:
            t = s / L
            out.append((a[0] + t * (b[0] - a[0]), a[1] + t * (b[1] - a[1])))
            s += step
        carry = L - (s - step)
    if out[-1] != line[-1]:
        out.append(line[-1])
    return out


def project_arc(line, p):
    best, arc, run = float("inf"), 0.0, 0.0
    for i in range(1, len(line)):
        a, b = line[i - 1], line[i]
        dx, dy = b[0] - a[0], b[1] - a[1]
        L2 = dx * dx + dy * dy
        L = math.sqrt(L2)
        t = 0 if L2 == 0 else max(0, min(1, ((p[0] - a[0]) * dx + (p[1] - a[1]) * dy) / L2))
        d = math.hypot(p[0] - a[0] - t * dx, p[1] - a[1] - t * dy)
        if d < best:
            best, arc = d, run + t * L
        run += L
    return arc


def project_arc_within(line, p, lo, hi):
    best, arc, run = float("inf"), lo, 0.0
    for i in range(1, len(line)):
        a, b = line[i - 1], line[i]
        dx, dy = b[0] - a[0], b[1] - a[1]
        L2 = dx * dx + dy * dy
        L = math.sqrt(L2)
        t = 0 if L2 == 0 else max(0, min(1, ((p[0] - a[0]) * dx + (p[1] - a[1]) * dy) / L2))
        s = run + t * L
        if lo <= s <= hi:
            d = math.hypot(p[0] - a[0] - t * dx, p[1] - a[1] - t * dy)
            if d < best:
                best, arc = d, s
        run += L
    return arc


def sub_line(line, s0, s1):
    out, run = [], 0.0
    for i in range(1, len(line)):
        a, b = line[i - 1], line[i]
        L = math.hypot(b[0] - a[0], b[1] - a[1])
        for s in (s0, s1):
            if run <= s <= run + L and L > 0:
                t = (s - run) / L
                out.append((run + t * L, (a[0] + t * (b[0] - a[0]), a[1] + t * (b[1] - a[1]))))
        if s0 < run + L and run > s0 and run < s1:
            out.append((run, a))
        run += L
    out.sort()
    pts = [p for _, p in out]
    return pts if len(pts) >= 2 else line


def read_gtfs(path):
    z = zipfile.ZipFile(path)
    names = {n.split("/")[-1]: n for n in z.namelist()}

    def rows(name):
        if name not in names:
            return []
        return csv.DictReader(io.TextIOWrapper(z.open(names[name]), "utf-8-sig"))

    route_type = {r["route_id"]: int(r.get("route_type") or 3) for r in rows("routes.txt")}
    stops = {r["stop_id"]: (float(r["stop_lon"]), float(r["stop_lat"]))
             for r in rows("stops.txt") if r.get("stop_lat")}
    trip_shape = {r["trip_id"]: r.get("shape_id", "") for r in rows("trips.txt")}
    # one trip per shape is enough: every trip on a shape rides it
    want, seq, seen = {}, defaultdict(list), set()
    for tid, sid in trip_shape.items():
        if sid and sid not in seen:
            want[tid] = sid
            seen.add(sid)
    for r in rows("stop_times.txt"):
        sid = want.get(r["trip_id"])
        if sid and r["stop_id"] in stops:
            seq[sid].append((int(r["stop_sequence"]), stops[r["stop_id"]]))
    shape_stops = {k: [p for _, p in sorted(v)] for k, v in seq.items()}
    shapes = defaultdict(list)
    for r in rows("shapes.txt"):
        shapes[r["shape_id"]].append((int(r["shape_pt_sequence"]),
                                      float(r["shape_pt_lon"]), float(r["shape_pt_lat"])))
    return route_type, {k: [(x, y) for _, x, y in sorted(v)] for k, v in shapes.items()}, shape_stops


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--paths", required=True)
    ap.add_argument("--gtfs", required=True, help="feed zip; comma list matches chart --gtfs (overlays prefixed f1:, f2: …)")
    ap.add_argument("--osm", required=True, action="append", help="extract(s) the feed matched against")
    ap.add_argument("--mode", choices=["ferry", "bus", "rail", "all"], default="all")
    ap.add_argument("--step", type=float, default=10)
    ap.add_argument("--tol", type=float, default=60, help="detour/miss limit, m")
    ap.add_argument("--offnet-tol", type=float, default=0, help="off-network metres allowed per path")
    ap.add_argument("--snap", type=float, default=4, help="distance that still counts as on a way, m")
    ap.add_argument("--ratio", type=float, default=0.15, help="allowed |ratio-1|")
    ap.add_argument("--sparse", type=float, default=150, help="mean shape vertex spacing above which the shape is only chords, m")
    ap.add_argument("--stop-tol", type=float, default=60, help="stop-to-path limit, m (piers sit farther from lanes: use ~400)")
    ap.add_argument("--json", help="write per-path results here")
    a = ap.parse_args()

    route_type, shapes, shape_stops = {}, {}, {}
    for k, g in enumerate(a.gtfs.split(",")):
        rt, sh, ss = read_gtfs(g)
        pre = "" if k == 0 else "f%d:" % k
        route_type.update({pre + r: t for r, t in rt.items()})
        shapes.update({pre + s: v for s, v in sh.items()})
        shape_stops.update({pre + s: v for s, v in ss.items()})

    feats = [f for f in json.load(open(a.paths))["features"] if f["properties"].get("kind") == "path"]
    if not feats:
        sys.exit("no paths")
    lon0 = sum(f["geometry"]["coordinates"][0][0] for f in feats) / len(feats)
    lat0 = sum(f["geometry"]["coordinates"][0][1] for f in feats) / len(feats)
    fr = Frame(lon0, lat0)

    layers = defaultdict(list)
    for path in a.osm:
        for f in json.load(open(path))["features"]:
            g = f.get("geometry") or {}
            lyr = layer_of(f.get("properties") or {})
            if lyr and g.get("type") == "LineString":
                layers[lyr].append([fr.xy(*c[:2]) for c in g["coordinates"]])
    index = {k: Index(v) for k, v in layers.items()}

    results = []
    for f in feats:
        p = f["properties"]
        mode = mode_of(route_type.get(p["route"], 3))
        if a.mode != "all" and mode != a.mode:
            continue
        line = [fr.xy(*c[:2]) for c in f["geometry"]["coordinates"]]
        if len(line) < 2:
            continue
        samples = resample(line, a.step)
        idx = index.get(mode)
        off = 0.0
        for s in samples[1:]:
            if idx is None or idx.dist(s, a.snap) > a.snap:
                off += a.step
        sid = p["shape"].split("#")[0]
        shape = [fr.xy(*c) for c in shapes.get(sid, [])]
        detour = miss = ratio = detour_at = detour_on = None
        sparse = len(shape) >= 2 and length(shape) / (len(shape) - 1) > a.sparse
        pidx = Index([line], 200)
        sstops = [fr.xy(*c) for c in shape_stops.get(sid, [])]
        if len(shape) >= 2 and sstops and "#" in p["shape"]:
            # a clipped piece answers only for the stops in its stretch
            s0, s1 = sorted((project_arc(shape, line[0]), project_arc(shape, line[-1])))
            sstops = [q for q in sstops if s0 - a.stop_tol <= project_arc(shape, q) <= s1 + a.stop_tol]
        stopgap = max((pidx.dist(q, 5000) for q in sstops), default=None)
        if len(shape) >= 2:
            s0, s1 = sorted((project_arc(shape, line[0]), project_arc(shape, line[-1])))
            stops_all = [fr.xy(*c) for c in shape_stops.get(sid, [])]
            if "#" not in p["shape"] and len(stops_all) >= 2:
                # the whole trip, first stop to last: a path cut short
                # must not shrink the stretch it is held to
                s0, s1 = project_arc(shape, stops_all[0]), project_arc(shape, stops_all[-1])
                if abs(s1 - s0) < length(shape) / 4:
                    # a loop ends where it starts: hold each end to its half
                    half = length(shape) / 2
                    s0 = project_arc_within(shape, stops_all[0], 0, half)
                    s1 = project_arc_within(shape, stops_all[-1], half, length(shape))
                s0, s1 = sorted((s0, s1))
            sub = sub_line(shape, s0, s1)
            # detour against the whole shape: a shape that loops back on
            # itself projects the path's ends ambiguously
            sidx = Index([shape], 200)
            ds = [sidx.dist(s, 5000) for s in samples]
            k = max(range(len(ds)), key=ds.__getitem__)
            detour = ds[k]
            detour_at = k / max(1, len(ds) - 1)
            detour_on = idx is not None and idx.dist(samples[k], a.snap) <= a.snap
            miss = max(pidx.dist(s, 5000) for s in resample(sub, a.step))
            ratio = length(line) / max(length(sub), 1)
        ok = (off <= a.offnet_tol and detour is not None
              and (sparse or (detour <= a.tol and miss <= a.tol and abs(ratio - 1) <= a.ratio))
              and (not sparse or abs(ratio - 1) <= max(a.ratio, 0.5))
              and (stopgap is None or stopgap <= a.stop_tol))
        results.append(dict(route=p["route"], shape=p["shape"], mode=mode, len_m=round(length(line)),
                            offnet_m=round(off), detour_m=None if detour is None else round(detour),
                            miss_m=None if miss is None else round(miss),
                            ratio=None if ratio is None else round(ratio, 3),
                            stopgap=None if stopgap is None else round(stopgap), sparse=sparse,
                            detour_at=None if detour_at is None else round(detour_at, 3),
                            detour_on=detour_on, ok=ok))

    bad = [r for r in results if not r["ok"]]
    bad.sort(key=lambda r: -(r["offnet_m"] + (r["detour_m"] or 9999) + (r["miss_m"] or 0)))
    print("%d paths, %d pass, %d fail" % (len(results), len(results) - len(bad), len(bad)))
    print("%-10s %-14s %-6s %8s %8s %8s %8s %6s %8s" % ("route", "shape", "mode", "len", "offnet", "detour", "miss", "ratio", "stopgap"))
    for r in bad:
        print("%-10s %-14s %-6s %8d %8d %8s %8s %6s %8s%s" % (r["route"][:10], r["shape"][:14], r["mode"], r["len_m"],
              r["offnet_m"], r["detour_m"], r["miss_m"], r["ratio"], r["stopgap"], " sparse" if r["sparse"] else ""))
    if a.json:
        json.dump(results, open(a.json, "w"), indent=1)
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
