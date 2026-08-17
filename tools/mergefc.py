#!/usr/bin/env python3
"""Merge GeoJSON FeatureCollections, first wins on duplicate feature id.

    tools/mergefc.py a.geojson b.geojson ... > merged.geojson

Rail/stops extracts overlap wherever a gap window meets a city window, and
the same OSM way must not enter the bundler twice — osm.Load would treat
the copies as parallel tracks and draw a phantom second line. Features
without an id (none of our extracts produce them) are kept unconditionally.
"""
import json
import sys

seen = set()
feats = []
for path in sys.argv[1:]:
    with open(path) as f:
        fc = json.load(f)
    for feat in fc.get("features", []):
        fid = feat.get("id")
        if fid is not None:
            if fid in seen:
                continue
            seen.add(fid)
        feats.append(feat)

json.dump({"type": "FeatureCollection", "features": feats}, sys.stdout)
sys.stdout.write("\n")
print(f"{len(feats)} features from {len(sys.argv)-1} files", file=sys.stderr)
