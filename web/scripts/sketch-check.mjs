// Guards the sketch file-format contract. Run: npm run check (from web/).
//
// The drawn networks in sketches/ are hand-made ground truth accumulated
// over months and the scorer reads their `coords` directly, so a change to
// the bezier baking silently moves every reference line and every gate
// number with it. This re-bakes a real sketch from its anchors and demands
// the result be IDENTICAL to what is on disk.
import fs from 'node:fs'
import path from 'node:path'
import {
  bakeAll, bake, mergeInto, reverseLine, splitAt, insertAnchor, handlesOf,
} from '../src/lib/sketch.ts'

// cwd, not import.meta.url: this file is bundled into node_modules/.cache
// before it runs, which moves its own URL out from under it. npm scripts
// always run from web/.
const repo = path.resolve(process.cwd(), '..')
let failures = 0
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${name}${detail ? ' — ' + detail : ''}`)
  if (!ok) failures++
}

// ── 1. baking is bit-identical to the stored coords ───────────────────
const sketchDir = path.join(repo, 'sketches')
const files = fs.existsSync(sketchDir)
  ? fs.readdirSync(sketchDir).filter((f) => f.endsWith('.json'))
  : []
if (!files.length) console.log('note  no sketches/ to check against')
for (const f of files) {
  const doc = JSON.parse(fs.readFileSync(path.join(sketchDir, f), 'utf8'))
  if (!doc.lines?.length) continue
  const stored = doc.lines.map((l) => (l.coords ?? []).map((c) => [...c]))
  bakeAll(doc)
  let worst = 0
  let lenMismatch = 0
  let pts = 0
  doc.lines.forEach((l, i) => {
    if (l.coords.length !== stored[i].length) return lenMismatch++
    l.coords.forEach((c, j) => {
      worst = Math.max(worst, Math.abs(c[0] - stored[i][j][0]), Math.abs(c[1] - stored[i][j][1]))
      pts++
    })
  })
  check(
    `${f}: re-bake matches stored coords`,
    lenMismatch === 0 && worst === 0,
    `${doc.lines.length} lines, ${pts} points, ${lenMismatch} length mismatches, max delta ${worst}`,
  )
}

// ── 2. reverse swaps handles, so the curve is unchanged ───────────────
{
  const l = {
    id: 'x',
    routes: [{ label: '', color: '000000' }],
    anchors: [
      { p: [0, 0], hin: null, hout: [0.1, 0.1] },
      { p: [1, 1], hin: [0.8, 0.9], hout: [1.2, 1.1] },
      { p: [2, 0], hin: [1.8, 0.2], hout: null },
    ],
    coords: [],
  }
  const before = bake(structuredClone(l)).coords
  const after = reverseLine(structuredClone(l)).coords.slice().reverse()
  const worst = Math.max(
    ...before.map((c, i) => Math.max(Math.abs(c[0] - after[i][0]), Math.abs(c[1] - after[i][1]))),
  )
  check('reverse preserves geometry', worst < 1e-12, `max delta ${worst}`)
}

// ── 3. merge joins at the nearest ends and drops the duplicate seam ───
{
  const mk = (pts) => ({
    id: Math.random().toString(36).slice(2),
    routes: [{ label: 'A', color: 'FF0000' }],
    anchors: pts.map((p) => ({ p, hin: null, hout: null })),
    coords: [],
  })
  const a = mk([[0, 0], [1, 0]])
  const b = mk([[2, 0], [1, 0]]) // b's TAIL touches a's tail → b must reverse
  b.routes = [{ label: 'B', color: '00FF00' }]
  const net = { feed: 't', lines: [a, b] }
  mergeInto(net, a, b)
  check('merge leaves one line', net.lines.length === 1)
  check('merge concatenates without a duplicate seam anchor', a.anchors.length === 3,
    `${a.anchors.length} anchors`)
  check('merge keeps both routes', a.routes.length === 2)
  check('merge orients the joined half', Math.abs(a.anchors[2].p[0] - 2) < 1e-12,
    `ends at ${a.anchors[2].p}`)
}

// ── 4. insert picks the span by perpendicular distance ────────────────
{
  const l = {
    id: 'i',
    routes: [{ label: '', color: '000000' }],
    // a long first span and a short second one: a midpoint-based search
    // puts a click near (9.5, 0.2) on the WRONG span
    anchors: [[0, 0], [10, 0], [11, 0]].map((p) => ({ p, hin: null, hout: null })),
    coords: [],
  }
  const at = insertAnchor(l, [10.5, 0.2])
  check('insert chooses the nearest span, not the nearest midpoint', at === 2, `inserted at ${at}`)
}

// ── 5. split duplicates the shared anchor so the halves still meet ────
{
  const l = {
    id: 's',
    routes: [{ label: '', color: '000000' }],
    anchors: [[0, 0], [1, 0], [2, 0], [3, 0]].map((p) => ({ p, hin: null, hout: null })),
    coords: [],
  }
  const net = { feed: 't', lines: [l] }
  const b = splitAt(net, l, 2)
  check('split produces a second line', !!b)
  check('split halves share the cut anchor',
    l.anchors[l.anchors.length - 1].p[0] === b.anchors[0].p[0])
  check('split rejects an end anchor', splitAt(net, l, 0) === null)
}

// ── 6. auto handles stay auto (null), never frozen into coordinates ───
{
  const l = {
    id: 'a',
    routes: [{ label: '', color: '000000' }],
    anchors: [[0, 0], [1, 1], [2, 0]].map((p) => ({ p, hin: null, hout: null })),
    coords: [],
  }
  bake(l)
  check('baking does not freeze auto handles', l.anchors.every((a) => a.hin === null))
  check('auto handles produce a tangent', handlesOf(l, 1).hout[0] > l.anchors[1].p[0])
}

console.log(failures ? `\n${failures} FAILED` : '\nall checks passed')
process.exit(failures ? 1 : 0)
