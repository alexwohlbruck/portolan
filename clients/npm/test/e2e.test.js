import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

import { portolan, decodePlnb, SUPPORTED_PLNB } from "../dist/index.js";

// These run against a REAL engine, built from the repo this package
// lives in. That is the point: the decoder and the request shape are a
// contract with the Go side, and a mock would only ever confirm my own
// assumptions about it.
//
// The engine is built once into a temp dir. Skips cleanly if the Go
// toolchain is absent, so `npm test` still works for a JS-only checkout.

const repo = fileURLToPath(new URL("../../..", import.meta.url));
let dir, bin, p;

function haveGo() {
  try {
    execFileSync("go", ["version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

const graph = {
  type: "FeatureCollection",
  features: [
    { type: "Feature", properties: { node: "a" }, geometry: { type: "Point", coordinates: [-122.42, 37.77] } },
    { type: "Feature", properties: { node: "b" }, geometry: { type: "Point", coordinates: [-122.41, 37.77] } },
    { type: "Feature", properties: { node: "c" }, geometry: { type: "Point", coordinates: [-122.40, 37.77] } },
    {
      type: "Feature",
      properties: { edge: "e0", from: "a", to: "b", routes: "R1,R2" },
      geometry: { type: "LineString", coordinates: [[-122.42, 37.77], [-122.415, 37.7705], [-122.41, 37.77]] },
    },
    {
      type: "Feature",
      properties: { edge: "e1", from: "b", to: "c", routes: "R1" },
      geometry: { type: "LineString", coordinates: [[-122.41, 37.77], [-122.40, 37.77]] },
    },
  ],
};

const tables = {
  "agency.txt": "agency_id,agency_name\nA,Authored\n",
  "routes.txt":
    "route_id,agency_id,route_short_name,route_type,route_color,route_text_color\n" +
    "R1,A,1,1,EE352E,FFFFFF\nR2,A,2,1,0039A6,FFFFFF\n",
  "stops.txt":
    "stop_id,stop_name,stop_lat,stop_lon\n" +
    "s1,Alpha,37.77,-122.42\ns2,Beta,37.77,-122.41\ns3,Gamma,37.77,-122.40\n",
};

before(async () => {
  if (!haveGo()) return;
  dir = mkdtempSync(join(tmpdir(), "portolan-npm-"));
  bin = join(dir, "portolan");
  execFileSync("go", ["build", "-o", bin, "./cmd/portolan"], { cwd: repo, stdio: "inherit" });
  p = await portolan({ binary: bin, cwd: repo });
});

after(async () => {
  await p?.stop();
  if (dir) rmSync(dir, { recursive: true, force: true });
});

test("the engine reports a layout this decoder understands", { skip: !haveGo() }, () => {
  assert.equal(p.version.plnb, SUPPORTED_PLNB);
  assert.match(p.version.version, /^\d+\.\d+\.\d+$|^devel$/);
  // a random token is generated per launch, and the engine confirms it
  assert.equal(p.version.auth, true);
});

test("charts inline geometry and feed with no file on disk", { skip: !haveGo() }, async () => {
  const stages = [];
  const job = await p.chart(
    { gtfsInline: tables, corridorsInline: graph, format: "bin", band: 15 },
    { onProgress: (e) => e.stage && stages.push(e.stage) },
  );
  assert.ok(stages.includes("order"), `stages were ${stages}`);
  assert.ok(stages.includes("emit"));
  // exactly one terminal frame, and it is the last thing on the stream
  assert.equal(stages.filter((s) => s === "done").length, 1);

  const plnb = await job.plnb();
  assert.ok(plnb.featureCount > 0);
  assert.equal(plnb.band, 15);

  // route ids must survive verbatim — a caller maps ribbons back to its
  // own identifiers by string equality
  const ids = new Set();
  for (let i = 0; i < plnb.featureCount; i++) for (const r of plnb.routes(i)) ids.add(r);
  assert.deepEqual([...ids].sort(), ["R1", "R2"]);
});

test("a band-filtered build holds exactly one band", { skip: !haveGo() }, async () => {
  const job = await p.chart({ gtfsInline: tables, corridorsInline: graph, format: "bin", band: 15 });
  const plnb = await job.plnb();
  const pairs = new Set();
  for (let i = 0; i < plnb.featureCount; i++) pairs.add(`${plnb.bandMin(i)},${plnb.bandMax(i)}`);
  assert.equal(pairs.size, 1, `bands present: ${[...pairs]}`);
  // half-open: the band asked for is inside [min, max)
  const [min, max] = [...pairs][0].split(",").map(Number);
  assert.ok(min <= 15 && 15 < max, `band 15 not inside [${min},${max})`);
});

test("the decoder agrees with the GeoJSON emit, column by column", { skip: !haveGo() }, async () => {
  // the strongest check available: chart the SAME input twice, once in
  // each format, and compare. It validates the two emitters against each
  // other rather than either against my assumptions.
  const req = { gtfsInline: tables, corridorsInline: graph, band: 15 };
  const binJob = await p.chart({ ...req, format: "bin" });
  const jsonJob = await p.chart({ ...req, format: "geojson" });

  const plnb = await binJob.plnb();
  const gj = await jsonJob.json();

  assert.equal(plnb.featureCount, gj.features.length);
  for (let i = 0; i < plnb.featureCount; i++) {
    const want = gj.features[i].properties;
    assert.equal(plnb.kind(i), want.kind, `feature ${i} kind`);
    assert.equal(plnb.colorHex(i).slice(1).toUpperCase(), (want.color || "000000").toUpperCase(), `feature ${i} color`);
    assert.equal(plnb.routes(i).join(","), want.routes, `feature ${i} routes`);
    assert.equal(plnb.label(i), want.label, `feature ${i} label`);
    assert.equal(plnb.mode(i), want.mode, `feature ${i} mode`);
    assert.equal(plnb.slot(i), want.slot, `feature ${i} slot`);
    assert.equal(plnb.nslots(i), want.nslots, `feature ${i} nslots`);
    assert.equal(plnb.bandMin(i), want.band_min, `feature ${i} band_min`);
    assert.equal(plnb.bandMax(i), want.band_max, `feature ${i} band_max`);
    assert.equal(plnb.routeType(i), want.route_type, `feature ${i} route_type`);
    assert.ok(Math.abs(plnb.offsetPx(i) - want.offset_px) < 1e-4, `feature ${i} offset_px`);

    // geometry: fixed point at 1e-7 degrees, so ~1cm
    const got = plnb.coordinates(i);
    const exp = gj.features[i].geometry.coordinates;
    assert.equal(got.length, exp.length, `feature ${i} vertex count`);
    for (let k = 0; k < got.length; k++) {
      assert.ok(Math.abs(got[k][0] - exp[k][0]) < 1e-6, `feature ${i} vertex ${k} lon`);
      assert.ok(Math.abs(got[k][1] - exp[k][1]) < 1e-6, `feature ${i} vertex ${k} lat`);
    }
  }
});

test("stations and their caterpillars come back as JSON", { skip: !haveGo() }, async () => {
  const job = await p.chart({ gtfsInline: tables, corridorsInline: graph });
  const st = await job.json("stations");
  const cats = st.features.filter((f) => f.properties.ftype === "cat");
  assert.ok(st.features.length > 0);
  // route_text_color was plumbed through, so a feed that sets it gets it
  if (cats.length) assert.equal(cats[0].properties.text_hex, "FFFFFF");
});

test("cancelling reaches the server", { skip: !haveGo() }, async () => {
  const job = await p.client.start({ gtfsInline: tables, corridorsInline: graph });
  await job.cancel();
  // whichever way the race falls, the job must reach a terminal state —
  // a cancelled build that never finishes is the pile-up cancel exists
  // to prevent
  const deadline = Date.now() + 10_000;
  let st;
  do {
    st = await p.client.status(job.id);
    if (st.done) break;
    await new Promise((r) => setTimeout(r, 20));
  } while (Date.now() < deadline);
  assert.equal(st.done, true);
});

test("a bad request fails before any work starts", { skip: !haveGo() }, async () => {
  await assert.rejects(
    () => p.chart({ gtfsInline: tables }), // no geometry
    (e) => e.status === 400,
  );
  await assert.rejects(
    () => p.chart({ corridorsInline: graph, gtfsInline: { "stops.txt": "stop_id\ns1\n" } }),
    (e) => /routes\.txt/.test(e.message),
  );
});

test("the token actually guards the port", { skip: !haveGo() }, async () => {
  // no Authorization header at all
  const r = await fetch(`${p.server.origin}/chart`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  });
  assert.equal(r.status, 401);
  // but /version stays open, so a supervisor can tell "not up yet" from
  // "wrong token"
  const v = await fetch(`${p.server.origin}/version`);
  assert.equal(v.status, 200);
});
