#!/usr/bin/env python3
"""osmium-export geojsonseq -> the rail extract osm.Load reads.

    tools/pbf2rail.py a.geojsonseq b.geojsonseq ... > rail.geojson

Keeps LineString ways, the loader's tag subset, and way/<id> identity
(osmium export writes ids as properties or feature ids depending on
version — both are handled). Duplicate ways across regions dedupe by id,
mirroring tools/mergefc.py. Lifecycle tags resolve like overpass2geojson.
"""
import json
import sys

TAGS = ("railway", "aerialway", "route", "service", "bridge", "tunnel", "layer")

# --windows <file>: crop to "w s e n" boxes (tools/railwindows.py output).
# A continental filter still carries every yard in Kansas; the corridors
# the feed actually rides are the only track MATCH needs to see.
boxes = []
args = sys.argv[1:]
if args and args[0] == "--windows":
    with open(args[1]) as f:
        boxes = [tuple(map(float, l.split())) for l in f if l.strip()]
    args = args[2:]

def inside(x, y):
    if not boxes:
        return True
    for w, s_, e, n in boxes:
        if w <= x <= e and s_ <= y <= n:
            return True
    return False

feats = []
seen = set()
for path in args:
    with open(path) as f:
        for line in f:
            line = line.strip().lstrip("\x1e")
            if not line:
                continue
            try:
                ft = json.loads(line)
            except ValueError:
                continue
            if ft.get("geometry", {}).get("type") != "LineString":
                continue
            props = ft.get("properties") or {}
            fid = ft.get("id") or props.get("@id") or props.get("id")
            if fid is None:
                continue
            fid = str(fid)
            if not fid.startswith("w"):
                fid = "way/" + fid
            elif fid.startswith("w") and not fid.startswith("way/"):
                fid = "way/" + fid.lstrip("w")
            if fid in seen:
                continue
            cs = ft["geometry"]["coordinates"]
            if not any(inside(x, y) for x, y in cs[:: max(1, len(cs) // 8)]):
                continue
            seen.add(fid)
            rw = props.get("railway")
            if rw in ("disused", "construction"):
                props = dict(props)
                props["railway"] = props.get(rw + ":railway") or "rail"
            feats.append({
                "type": "Feature", "id": fid,
                "properties": {k: props.get(k) for k in TAGS},
                "geometry": ft["geometry"],
            })

json.dump({"type": "FeatureCollection", "features": feats}, sys.stdout)
sys.stdout.write("\n")
print(f"{len(feats)} rail ways from {len(sys.argv)-1} regions", file=sys.stderr)
