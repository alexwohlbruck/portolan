#!/usr/bin/env python3
"""Overpass fetch windows from a feed's own shape coverage.

    tools/railwindows.py feed.zip > windows.txt      # one "w s e n" per line

A national feed cannot be fetched as one Overpass query and does not need
to be: the trains only run where the shapes run. Mark every half-degree
cell any rail shape touches, pad by one cell (the real track wanders off
a coarse shape), and merge horizontal runs into boxes. The result is a
corridor-following cover — tens of modest windows instead of a continent.
"""
import csv
import io
import sys
import zipfile
from collections import defaultdict

CELL = 0.5
RAIL_TYPES = {"0", "1", "2", "5", "6", "7", "12",
              *[str(t) for t in range(100, 118)], "400", "401", "402", "403", "405", "900"}

cells = set()
for path in sys.argv[1:]:
    z = zipfile.ZipFile(path)

    def rows(name):
        return csv.DictReader(io.TextIOWrapper(z.open(name), encoding="utf-8-sig"))

    rail_routes = {r["route_id"] for r in rows("routes.txt")
                   if r.get("route_type", "").strip() in RAIL_TYPES}
    rail_shapes = {t["shape_id"] for t in rows("trips.txt")
                   if t.get("route_id") in rail_routes and t.get("shape_id")}
    for r in rows("shapes.txt"):
        if r["shape_id"] in rail_shapes:
            cells.add((int(float(r["shape_pt_lon"]) // CELL),
                       int(float(r["shape_pt_lat"]) // CELL)))

# pad one cell in every direction
pad = set()
for cx, cy in cells:
    for dx in (-1, 0, 1):
        for dy in (-1, 0, 1):
            pad.add((cx + dx, cy + dy))

# merge horizontal runs per row into boxes
rows_ = defaultdict(list)
for cx, cy in pad:
    rows_[cy].append(cx)
n = 0
for cy in sorted(rows_):
    xs = sorted(rows_[cy])
    start = prev = xs[0]
    for x in xs[1:] + [None]:
        if x is not None and x == prev + 1:
            prev = x
            continue
        print(f"{start*CELL:.2f} {cy*CELL:.2f} {(prev+1)*CELL:.2f} {(cy+1)*CELL:.2f}")
        n += 1
        if x is not None:
            start = prev = x
print(f"{n} windows over {len(pad)} cells", file=sys.stderr)
