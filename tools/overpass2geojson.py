#!/usr/bin/env python3
"""Overpass `out geom;` JSON on stdin -> rail GeoJSON on stdout.

The shape internal/osm.Load reads (and testdata/nyc-rail.geojson carries):
one LineString Feature per way, id "way/<id>", and the handful of tags the
loader and the bundler look at. Ways are passed through untouched otherwise
-- service=yard/siding/spur filtering belongs to osm.Load, not here.
"""
import json
import sys

TAGS = ("railway", "service", "bridge", "tunnel", "layer")

data = json.load(sys.stdin)
feats = []
for el in data.get("elements", []):
    if el.get("type") != "way":
        continue
    geom = el.get("geometry") or []
    coords = [[p["lon"], p["lat"]] for p in geom if "lon" in p and "lat" in p]
    if len(coords) < 2:
        continue  # a way clipped to a single node at the bbox edge
    tags = el.get("tags") or {}
    feats.append({
        "type": "Feature",
        "id": "way/%s" % el["id"],
        "properties": {k: tags.get(k) for k in TAGS},
        "geometry": {"type": "LineString", "coordinates": coords},
    })

if not feats:
    sys.exit("overpass2geojson: no rail ways in the response — check the bbox")
json.dump({"type": "FeatureCollection", "features": feats}, sys.stdout)
