#!/usr/bin/env python3
"""GTFS shapes.txt -> an AUTHORED corridor graph (docs/CORRIDORS.md).

    tools/gtfscorridors.py feed.zip > corridors.geojson

For a feed whose shapes ARE the ground truth (intercity rail: no city
window, denser than any OSM query), inferring corridors by matching the
shapes against themselves is circular — MATCH exists to reconcile shapes
with INDEPENDENT track, and feeding it its own input back produced every
pathology in the book (disconnected variant islands, forced-GAP chords,
oscillation between canonically-equal pieces). So skip the inference:
build the corridor graph directly and hand it to `chart --corridors`,
which skips osm/bundle/MATCH/SPLIT by design.

The graph: every shape is simplified (~50 m) and snapped to canonical
vertices (~45 m), so coinciding corridors share exact vertex sequences.
Vertices become graph NODES where the corridor structure changes —
degree != 2, the traversing route set changes, or a shape terminates —
and maximal constant-route chains between nodes become EDGES carrying
their union route set. Output is the trackcenter/nodes contract
portolan's own writeNetwork emits, so the round trip stays honest.
"""
import csv
import io
import json
import math
import sys
import zipfile
from collections import defaultdict

RAIL_TYPES = {"0", "1", "2", "5", "6", "7", "12",
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


TOL = 50 / 111320
CANON = 45 / 111320
canon = {}


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


# ---- load + canonicalize every rail shape, weighted by trips ----------
seg_routes = defaultdict(set)   # (a,b) sorted vertex pair -> route ids
adj = defaultdict(set)          # vertex -> neighbor vertices
terminals = set()

for path in sys.argv[1:]:
    z = zipfile.ZipFile(path)

    def rows(name):
        return csv.DictReader(io.TextIOWrapper(z.open(name), encoding="utf-8-sig"))

    rail_routes = {r["route_id"] for r in rows("routes.txt")
                   if r.get("route_type", "").strip() in RAIL_TYPES}
    shape_route = {}
    for t in rows("trips.txt"):
        if t.get("route_id") in rail_routes and t.get("shape_id"):
            shape_route[t["shape_id"]] = t["route_id"]

    shapes = defaultdict(list)
    for r in rows("shapes.txt"):
        if r["shape_id"] in shape_route:
            shapes[r["shape_id"]].append(
                (int(r["shape_pt_sequence"]), float(r["shape_pt_lon"]), float(r["shape_pt_lat"])))

    for sid, pts in sorted(shapes.items(), key=lambda kv: -len(kv[1])):
        pts.sort()
        line = simplify([(p[1], p[2]) for p in pts], TOL)
        snapped = []
        for p in line:
            q = canonical(p)
            if not snapped or snapped[-1] != q:
                snapped.append(q)
        if len(snapped) < 2:
            continue
        rid = shape_route[sid]
        terminals.add(snapped[0])
        terminals.add(snapped[-1])
        for i in range(1, len(snapped)):
            a, b = snapped[i - 1], snapped[i]
            key = (a, b) if a <= b else (b, a)
            seg_routes[key].add(rid)
            adj[a].add(b)
            adj[b].add(a)

# ---- nodes: structure changes; edges: contracted constant-route chains
def is_node(v):
    if v in terminals or len(adj[v]) != 2:
        return True
    n1, n2 = sorted(adj[v])
    k1 = (v, n1) if v <= n1 else (n1, v)
    k2 = (v, n2) if v <= n2 else (n2, v)
    return seg_routes[k1] != seg_routes[k2]

nodes = {v for v in adj if is_node(v)}
node_id = {v: f"n{i}" for i, v in enumerate(sorted(nodes))}

feats = [{"type": "Feature", "properties": {"node": nid},
          "geometry": {"type": "Point", "coordinates": [round(v[0], 6), round(v[1], 6)]}}
         for v, nid in node_id.items()]

visited = set()
eid = 0
for start in sorted(nodes):
    for nb in sorted(adj[start]):
        key = (start, nb) if start <= nb else (nb, start)
        if key in visited:
            continue
        # walk the chain from start through nb until the next node
        chain = [start, nb]
        visited.add(key)
        prev, cur = start, nb
        while cur not in nodes:
            nxts = [x for x in adj[cur] if x != prev]
            if len(nxts) != 1:
                break
            nxt = nxts[0]
            k2 = (cur, nxt) if cur <= nxt else (nxt, cur)
            if k2 in visited:
                break
            visited.add(k2)
            chain.append(nxt)
            prev, cur = cur, nxt
        routes = sorted(seg_routes[key])
        feats.append({"type": "Feature", "properties": {
            "edge": f"e{eid}", "from": node_id[chain[0]],
            "to": node_id.get(chain[-1], node_id[chain[0]]),
            "routes": ",".join(routes)},
            "geometry": {"type": "LineString",
                         "coordinates": [[round(x, 6), round(y, 6)] for x, y in chain]}})
        eid += 1

json.dump({"type": "FeatureCollection", "features": feats}, sys.stdout)
sys.stdout.write("\n")
print(f"{len(node_id)} nodes, {eid} edges from {len(sys.argv)-1} feeds", file=sys.stderr)
