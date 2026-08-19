#!/usr/bin/env python3
"""centerline_audit — is every drawn centerline actually centered?

The acceptance test for bundle.Refine, independent of the pipeline: it
re-derives what the centerline SHOULD be straight from the OSM track
geometry and reports how far the drawn line is from it, along the whole
route rather than at a spot the author happened to look at.

Per sample (every --step m of every corridor centerline):
  * cast the perpendicular and intersect it with every OSM track;
  * collapse each WAY's own repeat crossings (a straight cross-section
    cuts a curving track more than once) so one rail votes once;
  * cluster the crossings into strands by offset gap;
  * apply the hand-drawing rule (1 follow, 2 midpoint, 3 centre, 4
    middle-two midpoint, >4 drop outermost pairs) to get the EXPECTED
    offset — which is 0 exactly when the drawn line is already centered;
  * report |expected| as the centering error.

Strand count per sample is reported too: a sustained run of >=5 parallel
strands is a yard or terminal throat, where "centered" is a different
question (--yard-min tunes the threshold), so those samples are counted
and excluded from the corridor error.

  tools/centerline_audit.py --build build/chicago-cta.geojson \\
      --rail build/chi-rail.geojson --at 41.88548,-87.62979 --radius 250
  tools/centerline_audit.py --build build/nyc.geojson \\
      --rail testdata/nyc-rail.geojson --site jamaica
"""
import argparse, json, math, sys
from collections import defaultdict

MPD_LAT = 110540.0


def mpd_lon(lat):
    return 111320.0 * math.cos(math.radians(lat))


SITES = {  # name: (lat, lon, radius_m) — the multi-track terminals worth auditing
    "millennium":   (41.8842, -87.6222, 400),
    "union":        (41.8789, -87.6397, 500),
    "otc":          (41.8825, -87.6404, 400),
    "jamaica":      (40.7002, -73.8074, 600),
    "coney":        (40.5780, -73.9720, 700),
    "penn":         (40.7506, -73.9936, 500),
    "grandcentral": (40.7527, -73.9772, 500),
}


def load_lines(path, want=None):
    d = json.load(open(path))
    out = []
    for f in d["features"]:
        g = f["geometry"]
        if g["type"] != "LineString" or len(g["coordinates"]) < 2:
            continue
        if want and not want(f["properties"]):
            continue
        out.append((f["properties"], g["coordinates"]))
    return out


class Grid:
    """Uniform grid over segments, in metres, for perpendicular queries."""

    def __init__(self, lines, cell=60.0):
        self.cell = cell
        self.seg = []          # (x1,y1,x2,y2, wayid)
        self.idx = defaultdict(list)
        for wid, pts in lines:
            for i in range(len(pts) - 1):
                a, b = pts[i], pts[i + 1]
                si = len(self.seg)
                self.seg.append((a[0], a[1], b[0], b[1], wid))
                x0, x1 = sorted((a[0], b[0]))
                y0, y1 = sorted((a[1], b[1]))
                for cx in range(int(x0 // cell), int(x1 // cell) + 1):
                    for cy in range(int(y0 // cell), int(y1 // cell) + 1):
                        self.idx[(cx, cy)].append(si)

    def near(self, x, y, r):
        c = self.cell
        out = set()
        for cx in range(int((x - r) // c), int((x + r) // c) + 1):
            for cy in range(int((y - r) // c), int((y + r) // c) + 1):
                out.update(self.idx.get((cx, cy), ()))
        return out


def seg_intersect(p1, p2, p3, p4):
    x1, y1 = p1; x2, y2 = p2; x3, y3 = p3; x4, y4 = p4
    rx, ry = x2 - x1, y2 - y1
    sx, sy = x4 - x3, y4 - y3
    den = rx * sy - ry * sx
    if abs(den) < 1e-12:
        return None
    t = ((x3 - x1) * sy - (y3 - y1) * sx) / den
    u = ((x3 - x1) * ry - (y3 - y1) * rx) / den
    if t < 0 or t > 1 or u < 0 or u > 1:
        return None
    return (x1 + t * rx, y1 + t * ry)


def median_strand(cs):
    """The hand-drawing rule, identical to bundle.MedianStrand."""
    cs = list(cs)
    while len(cs) > 4:
        cs = cs[1:-1]
    k = len(cs)
    if k == 0:
        return None
    if k % 2 == 1:
        return cs[k // 2]
    return (cs[k // 2 - 1] + cs[k // 2]) / 2


def audit(build, rail, lat0, lon0, radius, step, reach, gap, yard_min, label):
    klon = mpd_lon(lat0)

    def xy(c):
        return ((c[0] - lon0) * klon, (c[1] - lat0) * MPD_LAT)

    rails = []
    for i, (props, coords) in enumerate(load_lines(rail)):
        pts = [xy(c) for c in coords]
        if any(abs(p[0]) < radius + 400 and abs(p[1]) < radius + 400 for p in pts):
            rails.append((props.get("id", i), pts))
    if not rails:
        return None
    grid = Grid(rails)

    cls = []
    for props, coords in load_lines(build + ".trackcenter.geojson"):
        pts = [xy(c) for c in coords]
        keep = [p for p in pts if abs(p[0]) < radius and abs(p[1]) < radius]
        if len(keep) >= 3:
            cls.append((props.get("edge"), pts))

    errs, counts, yard = [], defaultdict(int), 0
    per = defaultdict(list)   # edge -> errors, so a bad corridor is named
    worst = (0, None, 0)
    for edge, pts in cls:
        # walk the line at `step`
        acc = 0.0
        for i in range(1, len(pts)):
            ax, ay = pts[i - 1]
            bx, by = pts[i]
            seglen = math.hypot(bx - ax, by - ay)
            if seglen < 1e-9:
                continue
            n = max(1, int(seglen // step))
            for k in range(n):
                t = k / n
                px, py = ax + (bx - ax) * t, ay + (by - ay) * t
                if abs(px) > radius or abs(py) > radius:
                    continue
                tx, ty = (bx - ax) / seglen, (by - ay) / seglen
                nx, ny = ty, -tx
                r1 = (px - nx * reach, py - ny * reach)
                r2 = (px + nx * reach, py + ny * reach)
                # per-way offsets
                byway = defaultdict(list)
                for si in grid.near(px, py, reach + 5):
                    x1, y1, x2, y2, wid = grid.seg[si]
                    q = seg_intersect(r1, r2, (x1, y1), (x2, y2))
                    if q is None:
                        continue
                    byway[wid].append((q[0] - px) * nx + (q[1] - py) * ny)
                if not byway:
                    continue
                offs = []
                for wid, vs in byway.items():   # one vote per way per track
                    vs.sort()
                    run = [vs[0]]
                    for v in vs[1:]:
                        if v - run[-1] > gap:
                            offs.append(sum(run) / len(run))
                            run = []
                        run.append(v)
                    offs.append(sum(run) / len(run))
                offs.sort()
                strands, run = [], [offs[0]]
                for v in offs[1:]:
                    if v - run[-1] > gap:
                        strands.append(sum(run) / len(run))
                        run = []
                    run.append(v)
                strands.append(sum(run) / len(run))
                counts[len(strands)] += 1
                if len(strands) >= yard_min:
                    yard += 1
                    continue
                exp = median_strand(strands)
                if exp is None:
                    continue
                e = abs(exp)
                errs.append(e)
                per[edge].append(e)
                if e > worst[0]:
                    worst = (e, (px / klon + lon0, py / MPD_LAT + lat0), edge)
    if not errs:
        return None
    errs.sort()
    return {
        "label": label, "n": len(errs), "yard": yard,
        "mean": sum(errs) / len(errs),
        "p50": errs[len(errs) // 2],
        "p90": errs[int(len(errs) * 0.9)],
        "max": errs[-1], "worst": worst,
        "counts": dict(sorted(counts.items())),
        "per": {k: (len(v), sum(v) / len(v), sorted(v)[len(v) // 2])
                for k, v in per.items() if len(v) >= 5},
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--build", required=True)
    ap.add_argument("--rail", required=True)
    ap.add_argument("--at", help="lat,lon")
    ap.add_argument("--site", help="named site: " + ", ".join(SITES))
    ap.add_argument("--radius", type=float, default=300)
    ap.add_argument("--step", type=float, default=6.0)
    ap.add_argument("--reach", type=float, default=40.0)
    ap.add_argument("--gap", type=float, default=2.5,
                    help="offsets farther apart than this are distinct tracks")
    ap.add_argument("--yard-min", type=int, default=5,
                    help="strand count at/above which a sample is yard, not corridor")
    a = ap.parse_args()

    jobs = []
    if a.site:
        for name in a.site.split(","):
            lat, lon, r = SITES[name]
            jobs.append((lat, lon, r, name))
    elif a.at:
        lat, lon = (float(v) for v in a.at.split(","))
        jobs.append((lat, lon, a.radius, a.at))
    else:
        sys.exit("need --at or --site")

    print(f"{'site':<22}{'n':>6}{'mean':>8}{'p50':>8}{'p90':>8}{'max':>8}{'yard':>7}  strand histogram")
    for lat, lon, r, label in jobs:
        res = audit(a.build, a.rail, lat, lon, r, a.step, a.reach,
                    a.gap, a.yard_min, label)
        if not res:
            print(f"{label:<22}   (no centerline samples)")
            continue
        print(f"{res['label']:<22}{res['n']:>6}{res['mean']:>8.2f}{res['p50']:>8.2f}"
              f"{res['p90']:>8.2f}{res['max']:>8.2f}{res['yard']:>7}  {res['counts']}")
        w = res["worst"]
        if w[1]:
            print(f"{'':22}worst {w[0]:.2f} m at {w[1][1]:.5f},{w[1][0]:.5f} (edge {w[2]})")
        for eid, (n, mean, p50) in sorted(res["per"].items(),
                                          key=lambda kv: -kv[1][1]):
            flag = "  <== not centered" if p50 > 1.0 else ""
            print(f"{'':22}edge {eid}: n={n:<5} mean {mean:5.2f}  p50 {p50:5.2f}{flag}")


main()
