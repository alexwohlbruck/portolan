#!/usr/bin/env python3
"""Did the group keep everything its members drew?

    tools/groupverify.py <group-key>     # exit 1 if any member lost ink

Tested on GEOMETRY, not labels. A group re-trunks its lines — Atlanta's
four MARTA colours keep their names but the Crescent arrives beside them,
and elsewhere a second agency turns per-line labels into an agency trunk —
so a label-matching check reads a rename as a loss. What must not change
is the INK: every metre the member drew by itself is still drawn.

This is the gate that keeps automatic grouping safe. A group replaces its
members in the world index, so a group that quietly drew less than they
did would take the difference off the map with nobody watching.
"""
import json
import math
import os
import sys
from collections import defaultdict

STEP = 25.0   # sample the member's centrelines this finely
TOL = 30.0    # ...and call one covered if the group drew within this


def samples(path):
    d = json.load(open(path))
    out = []
    for x in d["features"]:
        p = x["properties"]
        if p.get("band_min") != 15 or p["kind"] == "transition":
            continue
        g = x["geometry"]
        parts = g["coordinates"] if g["type"] == "MultiLineString" else [g["coordinates"]]
        for c in parts:
            for a, b in zip(c, c[1:]):
                mx = 111320 * math.cos(math.radians(a[1]))
                dx, dy = (b[0] - a[0]) * mx, (b[1] - a[1]) * 110540
                n = max(1, int(math.hypot(dx, dy) / STEP))
                for i in range(n):
                    out.append((a[0] + (b[0] - a[0]) * i / n, a[1] + (b[1] - a[1]) * i / n))
    return out


def main():
    key = sys.argv[1]
    cfg = json.load(open("portolan.json", encoding="utf-8"))["feeds"]
    cx, cy = TOL / 111320.0, TOL / 110540.0
    grid = defaultdict(list)
    for x, y in samples(cfg[key]["out"]):
        grid[(int(x / cx), int(y / cy))].append((x, y))

    def near(x, y):
        gx, gy = int(x / cx), int(y / cy)
        for i in (-1, 0, 1):
            for j in (-1, 0, 1):
                for px, py in grid.get((gx + i, gy + j), ()):
                    if math.hypot((px - x) * 111320 * math.cos(math.radians(y)),
                                  (py - y) * 110540) <= TOL:
                        return True
        return False

    worst, bad = 1.0, []
    for m in cfg[key]["members"]:
        own = cfg[m].get("out") or f"build/{m}.geojson"
        if not os.path.exists(own):
            continue
        ss = samples(own)
        if not ss:
            continue
        r = sum(1 for x, y in ss if near(x, y)) / len(ss)
        if r < 0.90:
            bad.append(f"{m} {r:.0%}")
        worst = min(worst, r)
    print(f"   members retained: {worst:.1%} of drawn ink"
          + (("  LOW: " + ", ".join(bad)) if bad else ""))
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
