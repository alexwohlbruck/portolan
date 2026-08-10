#!/usr/bin/env python3
"""Overpass `out geom;` JSON on stdin -> rail GeoJSON on stdout.

The shape internal/osm.Load reads (and testdata/nyc-rail.geojson carries):
one LineString Feature per way, id "way/<id>", and the handful of tags the
loader and the bundler look at. Ways are passed through untouched otherwise
-- service=yard/siding/spur filtering belongs to osm.Load, not here.
"""
import json
import sys

TAGS = ("railway", "aerialway", "route", "service", "bridge", "tunnel", "layer")
if "--streets" in sys.argv:
    TAGS = ("highway", "service", "bridge", "tunnel", "layer", "oneway")

# --stops: transit STOPS, not track. Points rather than lines, and the
# elements are a mix of nodes (most stop_positions and tram stops), ways
# (a station building) and relations (a multi-level complex), so each is
# reduced to a representative point. The name tags are the whole reason
# this extract exists — GTFS stop names are operator bookkeeping
# ("WESTLAKE STN - BAY A"); OSM carries what is on the sign.
if "--stops" in sys.argv:
    STOP_TAGS = ("name", "name:en", "alt_name", "short_name", "official_name",
                 "railway", "aerialway", "amenity", "public_transport",
                 "station", "subway", "light_rail", "tram", "train", "monorail",
                 "funicular", "ferry", "operator", "network", "wikidata", "ref")
    data = json.load(sys.stdin)
    feats = []
    for el in data.get("elements", []):
        tags = el.get("tags") or {}
        if not tags.get("name"):
            continue  # an unnamed stop can never win a name match
        if "lon" in el and "lat" in el:
            lon, lat = el["lon"], el["lat"]
        elif el.get("center"):
            lon, lat = el["center"]["lon"], el["center"]["lat"]
        else:
            geom = el.get("geometry") or []
            pts = [(p["lon"], p["lat"]) for p in geom if "lon" in p and "lat" in p]
            if not pts:
                continue
            lon = sum(p[0] for p in pts) / len(pts)
            lat = sum(p[1] for p in pts) / len(pts)
        feats.append({
            "type": "Feature",
            "id": "%s/%s" % (el["type"], el["id"]),
            "properties": {k: tags[k] for k in STOP_TAGS if k in tags},
            "geometry": {"type": "Point", "coordinates": [lon, lat]},
        })
    if not feats:
        sys.exit("overpass2geojson: no named stops in the response — check the bbox")
    json.dump({"type": "FeatureCollection", "features": feats}, sys.stdout)
    sys.exit(0)

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
    sys.exit("overpass2geojson: no ways in the response — check the bbox")
json.dump({"type": "FeatureCollection", "features": feats}, sys.stdout)
