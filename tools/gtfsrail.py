#!/usr/bin/env python3
"""GTFS shapes.txt -> a rail extract osm.Load reads.

    tools/gtfsrail.py feed.zip [feed2.zip ...] > rail.geojson

For an INTERCITY region there is no city window to Overpass — the bbox is
a continent, and the mainline network at that scale is exactly what the
feed's own shapes already trace. So the shapes become the "track": one
railway=rail way per distinct shape geometry, Douglas-Peucker'd to ~20 m
(match tolerance is coarser than that). MATCH then walks each pattern
over its own family of shapes, and the bundler merges the near-duplicate
strands the same way it merges parallel OSM tracks. Rail-family route
types only — buses and ferries in a mixed feed are skipped.
"""
import csv
import io
import json
import math
import sys
import zipfile

RAIL_TYPES = {"0", "1", "2", "5", "6", "7", "12",
              # extended railway-service family (Google's route_type table)
              *[str(t) for t in range(100, 118)], "400", "401", "402", "403", "405", "900"}


def simplify(pts, tol):
    if len(pts) <= 2:
        return pts
    keep = [False] * len(pts)
    keep[0] = keep[-1] = True
    stack = [(0, len(pts) - 1)]
    while stack:
        i, j = stack.pop()
        if j <= i + 1:
            continue
        ax, ay = pts[i]
        bx, by = pts[j]
        dx, dy = bx - ax, by - ay
        den = math.hypot(dx, dy)
        best, bi = -1.0, -1
        for k in range(i + 1, j):
            if den == 0:
                d = math.hypot(pts[k][0] - ax, pts[k][1] - ay)
            else:
                d = abs(dx * (pts[k][1] - ay) - dy * (pts[k][0] - ax)) / den
            if d > best:
                best, bi = d, k
        if best > tol:
            keep[bi] = True
            stack.append((i, bi))
            stack.append((bi, j))
    return [p for p, k in zip(pts, keep) if k]


feats = []
seen = set()
for path in sys.argv[1:]:
    z = zipfile.ZipFile(path)

    def rows(name):
        return csv.DictReader(io.TextIOWrapper(z.open(name), encoding="utf-8-sig"))

    rail_routes = {r["route_id"] for r in rows("routes.txt")
                   if r.get("route_type", "").strip() in RAIL_TYPES}
    rail_shapes = {t["shape_id"] for t in rows("trips.txt")
                   if t.get("route_id") in rail_routes and t.get("shape_id")}

    shapes = {}
    for r in rows("shapes.txt"):
        sid = r["shape_id"]
        if sid not in rail_shapes:
            continue
        shapes.setdefault(sid, []).append(
            (int(r["shape_pt_sequence"]), float(r["shape_pt_lon"]), float(r["shape_pt_lat"])))

    tol = 50 / 111320  # ~50 m in degrees: a continental layer's finest tile
    # zoom is coarse, and 20 m detail tripled SPLIT's refinement cost past
    # what one machine survives

    # CANONICAL VERTICES — the load-bearing step. The track graph welds
    # ways only where they touch EXACTLY (shared vertices / metre-scale
    # endpoint snaps, built for OSM's shared nodes). Feed shapes never
    # share a node: 144 near-parallel variants entered the graph as
    # disconnected islands, MATCH could not hand off between them at
    # divergence points, and — with candidates present everywhere so the
    # gap gate stayed shut — the DP was FORCED through barredGap for
    # whole route legs: the hub-to-hub crow-flies chords. So every
    # vertex snaps to the first canonical vertex registered within ~45 m:
    # coinciding corridors collapse onto identical point sequences, the
    # graph's vertex-touch machinery welds and splits them exactly at
    # convergence boundaries, and a pattern can leave a shared trunk
    # wherever its variant actually diverges.
    CANON = 45 / 111320
    canon = {}  # cell -> canonical vertex

    def canonical(pt):
        cx, cy = int(pt[0] / CANON), int(pt[1] / CANON)
        best, bd = None, CANON
        for dx in (-1, 0, 1):
            for dy in (-1, 0, 1):
                v = canon.get((cx + dx, cy + dy))
                if v is not None:
                    dd = math.hypot(v[0] - pt[0], v[1] - pt[1])
                    if dd < bd:
                        best, bd = v, dd
        if best is not None:
            return best
        canon[(cx, cy)] = pt
        return pt

    order = sorted(shapes.items(),
                   key=lambda kv: -sum(1 for _ in kv[1]))
    for sid, pts in order:
        pts.sort()
        line = simplify([(p[1], p[2]) for p in pts], tol)
        if len(line) < 2:
            continue
        snapped = []
        for p in line:
            q = canonical(p)
            if not snapped or snapped[-1] != q:
                snapped.append(q)
        line = snapped
        if len(line) < 2:
            continue
        key = hash(tuple(line))
        if key in seen:
            continue
        seen.add(key)
        feats.append({
            "type": "Feature", "id": f"shape/{sid}",
            "properties": {"railway": "rail", "aerialway": None, "route": None,
                           "service": None, "bridge": None, "tunnel": None, "layer": None},
            "geometry": {"type": "LineString",
                         "coordinates": [[round(x, 6), round(y, 6)] for x, y in line]},
        })

json.dump({"type": "FeatureCollection", "features": feats}, sys.stdout)
sys.stdout.write("\n")
print(f"{len(feats)} shape-ways from {len(sys.argv)-1} feeds", file=sys.stderr)
