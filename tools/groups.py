#!/usr/bin/env python3
"""Derive the feed GROUPS from the feeds' own shapes.

    tools/groups.py            # print what the data says, change nothing
    tools/groups.py --write    # rewrite portolan.json's group entries

Ribboning cannot cross a document: two agencies on the same steel draw as
two lines unless they are charted together. WHICH agencies those are is not
a curation decision — it is a measurement. Every feed's shapes.txt already
says where its trains run, so this walks them, finds the pairs that run
together for a sustained distance, and hands back the components.

Two roles come out of it, both by extent, because a group is a WINDOW:

  members  — feeds small enough to sit inside one regional window. The
             group replaces them in the world index, so the window is the
             union of their extents and they are drawn only by the group.
  overlays — feeds too wide for any window (Amtrak, VIA, the state
             corridors). They join every group they touch as an extra
             GTFS feed and cede those windows from their own build, so
             the world still draws each railroad exactly once.

Grouping is deliberately PERMISSIVE. Charting two feeds together does not
merge anything by itself — SPLIT's kiss and co-merge gates still decide,
on real geometry, whether a corridor is one ribbon. A false positive here
costs a slightly larger document; a false negative draws the same rails
twice. So the tolerance is loose enough to survive a wandering shape.
"""
import csv
import io
import json
import math
import os
import sys
import unicodedata
import zipfile
from collections import defaultdict

RAIL_TYPES = {"0", "1", "2", "5", "6", "7", "12",
              *[str(t) for t in range(100, 118)], "400", "401", "402", "403", "405", "900"}

# A shared corridor is worth grouping when the two feeds run together for
# this far. Well below any real shared corridor (the shortest genuine one
# in North America is the McKinney Avenue streetcar's 1.2 km beside DART)
# and far above a level crossing, which shares one cell and nothing more.
CELL_M = 60.0        # GTFS shapes wander; 60 m survives it
MIN_SHARED_M = 900.0 # sustained-run floor
STEP_M = 30.0        # shape sampling

# A feed whose rail extent covers more than this is a corridor, not a
# region: it cannot be a member of any window without dragging the window
# out with it. Amtrak is 1247, VIA 1200; the widest regional feed that
# stays a member is Brightline at 3.4.
MAX_MEMBER_AREA = 20.0  # square degrees

DEG = 111320.0


def shape_points(path):
    """Every rail-typed shape in the feed, sampled at ~STEP_M."""
    z = zipfile.ZipFile(path)
    names = set(z.namelist())

    def rows(name):
        # Metra writes "route_id, route_short_name, ..." — a space after
        # every comma, in the HEADER too, so a plain DictReader keys the
        # whole feed off " route_type" and sees no rail at all. Strip both
        # sides of every cell; the Go loader has always done this.
        r = csv.reader(io.TextIOWrapper(z.open(name), encoding="utf-8-sig"))
        try:
            head = [h.strip() for h in next(r)]
        except StopIteration:
            return
        for row in r:
            if len(row) < len(head):
                continue          # blank or truncated line
            yield dict(zip(head, (v.strip() for v in row)))

    if not {"routes.txt", "trips.txt", "shapes.txt"} <= names:
        return []
    rail = {r["route_id"] for r in rows("routes.txt")
            if (r.get("route_type") or "") in RAIL_TYPES}
    if not rail:
        return []
    shapes = {t["shape_id"] for t in rows("trips.txt")
              if t.get("route_id") in rail and t.get("shape_id")}
    if not shapes:
        return []
    by = defaultdict(list)
    for r in rows("shapes.txt"):
        if r["shape_id"] in shapes:
            by[r["shape_id"]].append((float(r["shape_pt_sequence"]),
                                      float(r["shape_pt_lon"]), float(r["shape_pt_lat"])))
    out = []
    for pts in by.values():
        pts.sort()
        pts = [(x, y) for _, x, y in pts]
        for a, b in zip(pts, pts[1:]):
            mx = DEG * math.cos(math.radians(a[1]))
            dx, dy = (b[0] - a[0]) * mx, (b[1] - a[1]) * 110540
            n = max(1, int(math.hypot(dx, dy) / STEP_M))
            if n > 4000:      # a shape with a teleport in it; skip the jump
                continue
            for i in range(n):
                out.append((a[0] + (b[0] - a[0]) * i / n, a[1] + (b[1] - a[1]) * i / n))
        if pts:
            out.append(pts[-1])
    return out


def load(cfg):
    """feed -> (sampled points, extent). Rail feeds with shapes only."""
    feeds = {}
    for k, v in sorted(cfg["feeds"].items()):
        if v.get("members"):
            continue          # a group is an output of this, never an input
        gtfs = (v.get("gtfs") or "").split(",")[0].strip()
        if not gtfs or not os.path.exists(gtfs):
            continue
        try:
            pts = shape_points(gtfs)
        except Exception as e:                       # a corrupt zip is not fatal
            print(f"  {k}: unreadable ({e})", file=sys.stderr)
            continue
        if not pts:
            continue
        xs = [p[0] for p in pts]
        ys = [p[1] for p in pts]
        feeds[k] = (pts, (min(xs), min(ys), max(xs), max(ys)))
    return feeds


def shared(feeds):
    """feed pairs that run together, and for how far. Everything is counted
    in OCCUPIED CELLS so a pair's shared run and a feed's own length are the
    same unit — the duplicate test compares the two."""
    cx, cy = CELL_M / DEG, CELL_M / 110540
    grid = defaultdict(set)
    own = defaultdict(set)
    for k, (pts, _) in feeds.items():
        for x, y in pts:
            c = (int(x / cx), int(y / cy))
            grid[c].add(k)
            own[k].add(c)
    hits = defaultdict(int)
    for fs in grid.values():
        if len(fs) < 2:
            continue
        fl = sorted(fs)
        for i in range(len(fl)):
            for j in range(i + 1, len(fl)):
                hits[(fl[i], fl[j])] += 1
    length = {k: len(v) * CELL_M for k, v in own.items()}
    return ({p: n * CELL_M for p, n in hits.items() if n * CELL_M >= MIN_SHARED_M},
            length)


def area(ext):
    return (ext[2] - ext[0]) * (ext[3] - ext[1])


def duplicates(length, pairs):
    """Feeds that are the SAME railway published twice — DC Streetcar also
    arrived as districtdepartmentoftransportation-dc-us, SEPTA twice, TTC
    twice. They share nearly all of both their lengths. Charting them
    together would draw the line twice side by side in one document
    instead of two, which is not an improvement, so the smaller one is
    held out of grouping and reported for a human to retire."""
    drop = {}
    for (a, b), m in pairs.items():
        la, lb = length[a], length[b]
        if la and lb and m / la > 0.85 and m / lb > 0.85:
            loser, keeper = (a, b) if la < lb else (b, a)
            drop[loser] = keeper
    return drop


def components(feeds, pairs):
    """Connected components over the MEMBER-eligible feeds; wide feeds are
    overlays and deliberately do not join components, or Amtrak would weld
    the continent into one document."""
    member = {k for k, (_, e) in feeds.items() if area(e) <= MAX_MEMBER_AREA}
    member -= set(DUPLICATE) | set(UNDRAWN)
    parent = {k: k for k in member}

    def find(a):
        while parent[a] != a:
            parent[a] = parent[parent[a]]
            a = parent[a]
        return a

    for (a, b) in pairs:
        if a in member and b in member:
            ra, rb = find(a), find(b)
            if ra != rb:
                parent[ra] = rb
    comps = defaultdict(list)
    for k in member:
        comps[find(k)].append(k)

    out = []
    for ms in comps.values():
        if len(ms) < 2:
            # a lone feed is still a group when a corridor feed rides over
            # it — that is the Hartford Line and the Rail Runner
            pass
        ms = sorted(ms)
        # a held-out feed is held out of BOTH roles: as an overlay it would
        # be charted into the group and keep its own tileset as well
        over = sorted({o for o in feeds
                       if o not in member and o not in DUPLICATE and o not in UNDRAWN
                       for m in ms if tuple(sorted((o, m))) in pairs})
        if len(ms) + len(over) < 2:
            continue
        if not over and len(ms) < 2:
            continue
        # The window is the union of the members' shape extents AND of the
        # windows they are already built with. Charlotte failed on exactly
        # this: derived from shapes alone the box came out a shade tighter
        # than the feed's own bbox, the clip took 138 of its 159 patterns,
        # and the group drew 4% of what the member drew by itself. A group
        # must never see less of a member than the member sees of itself.
        boxes = [feeds[m][1] for m in ms] + [CFG_BBOX[m] for m in ms if m in CFG_BBOX]
        ext = [min(b[0] for b in boxes), min(b[1] for b in boxes),
               max(b[2] for b in boxes), max(b[3] for b in boxes)]
        out.append({"members": ms, "overlays": over, "extent": ext})
    out.sort(key=lambda g: -sum(1 for _ in g["members"]))
    return out


DUPLICATE = {}
UNDRAWN = set()
CFG_BBOX = {}


def undrawn(cfg, feeds):
    """A feed that does not draw on its own today is not made a member: it
    would not appear on the map either way, and a pattern that cannot match
    can gate the whole group build, taking its co-members down with it."""
    out = set()
    for k in feeds:
        v = cfg["feeds"][k]
        b = v.get("out") or f"build/{k}.geojson"
        if not (os.path.exists(b) and os.path.getsize(b) > 3000):
            out.add(k)
    return out


# Display names. Prose only — the membership above is measured, and no
# entry here can add a feed to a group or take one out. A component with
# no label is named after its largest member.
LABELS = {
    "mta-subway": "Northeast Corridor",
    "sf-bay-area-rg": "Northern California",
    "chicago-cta": "Chicago",
    "wmata": "Washington\u2013Baltimore",
    "dallasarearapidtransit": "Dallas\u2013Fort Worth",
    "gotransit": "Golden Horseshoe",
    "socitdetransportdemontral": "Montr\u00e9al",
    "exo-reseaudetransportmetropolitain": "Montr\u00e9al",
    "tri-rail": "South Florida",
    "brightline-trails": "South Florida",
    "mts": "San Diego",
    "soundtransit": "Puget Sound",
    "boston": "Greater Boston",
    "barcelona-tmb": "Barcelona",
    "tokyo-metro": "Tokyo",
    "toei": "Tokyo",
    "riometroregionaltransitdistrict": "Rio Grande",
    "floridadepartmentoftransportation": "Central Florida",
    "uta": "Wasatch Front",
    "rtd": "Denver",
    "trimet": "Portland",
    "nstranslinkca": "Vancouver",
    "atlanta": "Atlanta",
    "charlotte": "Charlotte",
    "rta": "Cleveland",
    "metrostlouis": "St. Louis",
}

MARGIN = 0.03  # degrees of slack around the members' own shapes


def slug(name):
    name = unicodedata.normalize("NFKD", name)
    out = []
    for ch in name.lower():
        if ch.isascii() and ch.isalnum():
            out.append(ch)
        elif ch in " -\u2013_":
            out.append("-")
    return "-".join(x for x in "".join(out).split("-") if x)


def write(cfg, groups, length):
    feeds = cfg["feeds"]
    old = {k for k, v in feeds.items() if v.get("derived")}
    keep = {}
    for g in groups:
        # the label comes from whichever member has one, biggest first —
        # the Northeast component's longest member is NJ Transit, which
        # would have named the whole corridor after it
        ranked = sorted(g["members"], key=lambda m: -length.get(m, 0))
        anchor = next((m for m in ranked if m in LABELS), ranked[0])
        # an existing group entry that already covers these members keeps
        # its key, its curated name and its sketch — regrouping must not
        # orphan style/chicago.json or a hand-drawn network
        prev = None
        for k, v in feeds.items():
            if v.get("members") and set(v["members"]) & set(g["members"]):
                if prev is None or len(set(feeds[prev]["members"]) & set(g["members"])) \
                        < len(set(v["members"]) & set(g["members"])):
                    prev = k
        if prev:
            key = prev
            name = feeds[prev].get("name") or LABELS.get(anchor, anchor)
        else:
            name = LABELS.get(anchor, feeds[anchor].get("name") or anchor)
            key = slug(name)
            if key in feeds:
                key = key + "-region"
        gtfs = [feeds[m]["gtfs"].split(",")[0].strip() for m in g["members"]]
        gtfs += [feeds[o]["gtfs"].split(",")[0].strip() for o in g["overlays"]]
        w, s_, e, n = g["extent"]
        entry = dict(feeds.get(key, {}))
        entry.update({
            "name": name,
            "gtfs": ",".join(dict.fromkeys(gtfs)),
            "rail": entry.get("rail") or f"build/{key}-rail.geojson",
            "stops": entry.get("stops") or f"build/{key}-stops.geojson",
            "out": entry.get("out") or f"build/{key}.geojson",
            "bbox": [round(w - MARGIN, 4), round(s_ - MARGIN, 4),
                     round(e + MARGIN, 4), round(n + MARGIN, 4)],
            "members": g["members"],
            # the corridor feeds charted into this window. Not members —
            # they keep their own tileset everywhere else — but their
            # CURATION has to ride in, or Amtrak arrives with its raw GTFS
            # route colours while its own build draws it trunked intercity
            # blue, and one railroad changes colour at the group's edge.
            "overlays": g["overlays"],
            "derived": True,
        })
        # --allow-unmatched is not laxity, it is the clip. The match gate
        # asks whether a PATTERN mostly rides track; a corridor feed cut at
        # a group's window arrives as fragments, and the California Zephyr's
        # 10 km Bay bus leg — a rounding error against 3,900 km in Amtrak's
        # own build, which ships it as a bridge — becomes a fragment that is
        # 100% gap and fails the whole document. Members are still guarded,
        # by tools/groupverify.py, on ink rather than on this gate.
        entry["chart_args"] = "--set match_gap_cost=150 --allow-unmatched"
        # A member built with a streets extract draws its BUSES; without
        # one the group silently loses them. Atlanta failed exactly there
        # — 9% of its drawn kilometres survived, all four MARTA rail lines
        # intact and eighty bus routes gone. Members that share an extract
        # (chi-streets, nyc-streets) resolve to one path; genuinely
        # different ones are merged by groupbuild before the chart runs.
        st = list(dict.fromkeys(feeds[m]["streets"] for m in g["members"]
                                if feeds[m].get("streets")))
        if len(st) == 1:
            entry["streets"] = st[0]
        elif len(st) > 1:
            entry["streets"] = f"build/{key}-streets.geojson"
            entry["streets_from"] = st
        else:
            entry.pop("streets", None)
            entry.pop("streets_from", None)
        keep[key] = entry
    for k in old - set(keep):
        del feeds[k]                      # a group the data no longer supports
    for k, v in keep.items():
        feeds[k] = v
    open("portolan.json", "w", encoding="utf-8").write(json.dumps(cfg, indent=2))
    return keep


def main():
    cfg = json.load(open("portolan.json", encoding="utf-8"))
    print("reading shapes…", file=sys.stderr)
    feeds = load(cfg)
    print(f"{len(feeds)} feeds with rail shapes", file=sys.stderr)
    pairs, length = shared(feeds)
    print(f"{len(pairs)} feed pairs share track", file=sys.stderr)
    global DUPLICATE, UNDRAWN, CFG_BBOX
    CFG_BBOX = {k: v["bbox"] for k, v in cfg["feeds"].items()
                if isinstance(v.get("bbox"), list) and len(v["bbox"]) == 4}
    DUPLICATE = duplicates(length, pairs)
    UNDRAWN = undrawn(cfg, feeds)
    for lo, hi in sorted(DUPLICATE.items()):
        print(f"  duplicate: {lo} is {hi} again — held out", file=sys.stderr)
    for k in sorted(UNDRAWN):
        print(f"  undrawn:   {k} has no build — held out", file=sys.stderr)
    groups = components(feeds, pairs)
    for g in groups:
        w, s, e, n = g["extent"]
        print(f"\n[{w:.2f},{s:.2f},{e:.2f},{n:.2f}]  {area(g['extent']):.2f} deg2")
        for m in g["members"]:
            print(f"    member  {m}")
        for o in g["overlays"]:
            km = max(pairs.get(tuple(sorted((o, m))), 0) for m in g["members"]) / 1000
            print(f"    overlay {o}  ({km:.0f} km shared)")
    if "--write" in sys.argv:
        kept = write(cfg, groups, length)
        print(f"\nwrote {len(kept)} group entries to portolan.json", file=sys.stderr)
        for k in kept:
            print(f"    {k}", file=sys.stderr)
    print(f"\n{len(groups)} groups, "
          f"{sum(len(g['members']) for g in groups)} members, "
          f"{len({o for g in groups for o in g['overlays']})} corridor feeds", file=sys.stderr)


if __name__ == "__main__":
    main()
