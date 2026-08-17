#!/usr/bin/env python3
"""osmium-exported stop features -> the stops GeoJSON the matcher reads.

    osmium extract -b <bbox> build/pbf/<region>-stopsall.osm.pbf -o cut.osm.pbf
    osmium export cut.osm.pbf -f geojsonseq -u type_id -o cut.geojsonseq
    tools/pbf2stops.py cut.geojsonseq [more.geojsonseq ...] > build/<feed>-stops.geojson

Mirrors tools/overpass2geojson.py --stops: named features only, one
representative Point each, the same tag whitelist, Overpass-style ids
(node/123) so a feed can move between the Overpass and PBF paths without
its OSM ids churning. Multiple inputs (one per cached region) merge with
id-level dedup, since region cuts overlap at seams.
"""
import json, sys

TAGS = ['name', 'name:en', 'alt_name', 'short_name', 'official_name',
        'railway', 'aerialway', 'amenity', 'public_transport', 'station',
        'subway', 'light_rail', 'tram', 'train', 'monorail', 'funicular',
        'ferry', 'operator', 'network', 'wikidata', 'ref']
KIND = {'n': 'node', 'w': 'way', 'r': 'relation'}


def rep_point(geom):
    """A representative lon/lat for any geometry: the vertex centroid."""
    t = geom.get('type')
    c = geom.get('coordinates')
    if t == 'Point':
        return c
    pts = []

    def walk(v):
        if isinstance(v, (list, tuple)) and v and isinstance(v[0], (int, float)):
            pts.append(v)
        elif isinstance(v, (list, tuple)):
            for x in v:
                walk(x)
    walk(c)
    if not pts:
        return None
    return [sum(p[0] for p in pts) / len(pts), sum(p[1] for p in pts) / len(pts)]


seen = set()
feats = []
for path in sys.argv[1:]:
    with open(path) as f:
        for line in f:
            line = line.strip().lstrip('\x1e')
            if not line:
                continue
            try:
                ft = json.loads(line)
            except json.JSONDecodeError:
                continue
            props = ft.get('properties') or {}
            if not props.get('name'):
                continue
            raw = str(ft.get('id', ''))
            if raw and raw[0] in KIND and raw[1:].isdigit():
                fid = f'{KIND[raw[0]]}/{raw[1:]}'
            else:
                fid = raw
            if not fid or fid in seen:
                continue
            pt = rep_point(ft.get('geometry') or {})
            if not pt:
                continue
            seen.add(fid)
            feats.append({
                'type': 'Feature', 'id': fid,
                'properties': {k: props[k] for k in TAGS if k in props},
                'geometry': {'type': 'Point', 'coordinates': [round(pt[0], 7), round(pt[1], 7)]},
            })

json.dump({'type': 'FeatureCollection', 'features': feats},
          sys.stdout, ensure_ascii=False)
print(f'{len(feats)} stops', file=sys.stderr)
