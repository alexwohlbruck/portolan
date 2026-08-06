#!/usr/bin/env python3
"""Cut one operator's services out of a national GTFS feed.

    tools/gtfssubset.py --agency 353 in.zip out.zip
    tools/gtfssubset.py --agency 353,62 --keep-modes 0,1 in.zip out.zip

Country-wide feeds are common (gtfs.de covers all of Germany in 240 MB and
21k routes) and unusable as a city: chart would try to match every German
bus against one city's rail extract. Subsetting first is also what makes
pfaedle affordable -- it map-matches trips, so the trip count is the bill.

The cascade is the whole job: agencies -> routes -> trips -> stop_times ->
stops (parents included) -> calendars -> shapes. Files the cascade does not
name are copied through untouched.
"""
import argparse
import csv
import io
import sys
import zipfile

CASCADED = {"agency.txt", "routes.txt", "trips.txt", "stop_times.txt",
            "stops.txt", "calendar.txt", "calendar_dates.txt", "shapes.txt"}


def read(z, name):
    try:
        with z.open(name) as f:
            return list(csv.DictReader(io.TextIOWrapper(f, "utf-8-sig")))
    except KeyError:
        return None


def write(z, name, rows, cols):
    buf = io.StringIO()
    w = csv.DictWriter(buf, fieldnames=cols, extrasaction="ignore")
    w.writeheader()
    w.writerows(rows)
    z.writestr(name, buf.getvalue())


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--agency", required=True, help="comma-separated agency_id list")
    ap.add_argument("--keep-modes", help="comma-separated route_type list (default: all)")
    ap.add_argument("src")
    ap.add_argument("dst")
    a = ap.parse_args()
    agencies = set(a.agency.split(","))
    modes = set(a.keep_modes.split(",")) if a.keep_modes else None

    zin = zipfile.ZipFile(a.src)
    names = set(zin.namelist())

    routes = [r for r in (read(zin, "routes.txt") or [])
              if r.get("agency_id") in agencies
              and (modes is None or r.get("route_type") in modes)]
    if not routes:
        sys.exit(f"no routes for agency {a.agency} — check agency.txt")
    route_ids = {r["route_id"] for r in routes}

    trips = [t for t in (read(zin, "trips.txt") or []) if t["route_id"] in route_ids]
    trip_ids = {t["trip_id"] for t in trips}
    service_ids = {t["service_id"] for t in trips}
    shape_ids = {t.get("shape_id") for t in trips if t.get("shape_id")}

    # stop_times is the one file that must stream: a national feed's copy is
    # multiple GB uncompressed, and materialising it as dicts costs minutes
    # and most of a laptop's RAM. Row lists, matched on one column index.
    times_csv = io.StringIO()
    stop_ids = set()
    with zin.open("stop_times.txt") as f:
        rd = csv.reader(io.TextIOWrapper(f, "utf-8-sig"))
        head = next(rd)
        ti, si = head.index("trip_id"), head.index("stop_id")
        wr = csv.writer(times_csv)
        wr.writerow(head)
        for row in rd:
            if row[ti] in trip_ids:
                wr.writerow(row)
                stop_ids.add(row[si])

    allstops = read(zin, "stops.txt") or []
    stops = [s for s in allstops if s["stop_id"] in stop_ids]
    parents = {s.get("parent_station") for s in stops if s.get("parent_station")}
    stops += [s for s in allstops if s["stop_id"] in parents and s["stop_id"] not in stop_ids]

    with zipfile.ZipFile(a.dst, "w", zipfile.ZIP_DEFLATED) as zo:
        agency_rows = [r for r in (read(zin, "agency.txt") or [])
                       if r.get("agency_id") in agencies]
        for name, rows in (("agency.txt", agency_rows), ("routes.txt", routes),
                           ("trips.txt", trips), ("stops.txt", stops)):
            src_rows = read(zin, name)
            write(zo, name, rows, list(src_rows[0].keys()))
        zo.writestr("stop_times.txt", times_csv.getvalue())

        for name, key in (("calendar.txt", "service_id"),
                          ("calendar_dates.txt", "service_id"),
                          ("shapes.txt", "shape_id")):
            rows = read(zin, name)
            if rows is None:
                continue
            keep = service_ids if key == "service_id" else shape_ids
            write(zo, name, [r for r in rows if r[key] in keep], list(rows[0].keys()))

        for name in names - CASCADED:
            if not name.endswith("/"):
                zo.writestr(name, zin.read(name))

    print(f"{a.dst}: {len(routes)} routes, {len(trips)} trips, {len(stops)} stops")


main()
