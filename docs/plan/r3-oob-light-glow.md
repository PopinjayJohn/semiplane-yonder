# R3 — OOB-light glow retention + culling/filtering rules

Revisit of A-VTT2 decision D6 ("OOB lights: drop now; retain-for-glow
deferred to P3", `docs/plan/amend-vtt2-pixi-uvtt.md` §8). Implements amend
§9 item R3. No frozen-contract change: lights already ride snapshot v2
(`plugins.StateSnapshot.Lights`), and the sidecar `vtt-map/lights` shape is
unchanged — previously-dropped lights now survive as ordinary entries.

## 1. Retention rule (import + sidecar read path, identical)

A light is **retained** iff its glow disc — center `(x, y)`, radius `range`
(grid squares, D4 units) — **overlaps the board box** `[0,cols]×[0,rows]`:

```
cx = clamp(x, 0, cols), cy = clamp(y, 0, rows)
retain  <=>  (x-cx)² + (y-cy)² <= range²
```

- A centered-on-board light always passes (distance 0 ≤ range; `range > 0`
  is still required — zero/negative/non-finite range drops, unchanged).
- A **partially-OOB** light (center off-board, disc touching the board)
  is retained with its center **as-is** (never clamped): clamping would
  move the glow source and lie about exporter intent, and off-board
  centers already round-trip through `formatFloat`/YAML cleanly.
- A **fully-OOB** light (disc entirely off-board) is dropped, exactly like
  a fully-outside wall/portal segment — nothing it illuminates is visible.

## 2. How far out can be kept

At most `range` squares beyond the box edge, by construction (the disc must
reach the box). Verified case: Ground-Floor light #25 at `x = -0.570313`,
`y = 19.539101`, `range = 1.3` on a 20×20 grid — nearest box point is
`(0, 19.539101)`, distance `0.57 <= 1.3`, so it is **retained** (import now
yields 29 lights, not 28). Tomb (`96×72`, 0 lights) is unaffected. `range`
itself is uncapped pass-through per D4; an absurd range degrades to a
full-board wash, which is the exporter's stated intent anyway.

## 3. Culling bound — `MaxLights = 512` (new)

There was **no light-count cap** before R3 (verified by inspection of
`internal/vtt/import_uvtt.go`: only the 128-grid, 128MP-image, and
96MB-decode caps bound an import; `parseLightStates` bounded nothing).
`MaxLights = 512` now bounds both paths:

- Real exports carry dozens of lights (29 observed max); 512 is ~17×
  headroom, so no legitimate map trips it.
- A hostile 100k-light list can no longer balloon the sidecar, the
  snapshot JSON (~90 B/light → ≤ ~46 KB at cap), or the Pixi light pass
  (4 fills/light, linear).
- Truncation keeps the **first 512 in file order** (deterministic;
  exporter order is authorial intent — usually brightest/most-central
  first). The sidecar read path applies the same cap so a hand-edited
  10k-light list cannot bypass it at serve time.

## 4. How the clipped geometry renders

No renderer change was needed:

- **Pixi** (`web/static/vtt/pixi-board.js:drawLights`): `px()` maps grid
  coords linearly, so a negative center becomes a negative pixel center
  and the 4 concentric alpha circles paint partially on-board — the
  visible board edge gets its soft glow. Static glow only, no shadow
  casting: true LOS/occlusion stays P3 vision scope (not implemented here).
- **DOM**: renders no lights at all (unchanged); it stays the operable
  truth, canvas the enhancement.
- **Snapshot** (`internal/vtt/snapshot.go:SnapshotFor`): lights ride the
  same fail-closed gate as walls — a fully-hidden board emits no geometry
  to anyone including the GM (kept as-is).

## 5. Demo / doc posture — choice (a)

`fixtures/demo/maps/ground-floor.md` (28 lights) and `tomb.md` are **left
untouched** — no re-import. `docs/plan/backlog.md` (A-VTT2 §) wording is
amended from "the verified 29→28 light drop" to "dropped before R3;
retained under the R3 rule": the file on disk still holds 28, and the text
now says so honestly. A future re-import would write 29.

## 6. Policy consistency kept

- `Parse` (import) and `parseLightStates` (sidecar read) share the same
  `lightGlowOverlaps` test + `MaxLights` cap, so the sidecar round-trip
  (`ApplyImport` → real frontmatter reparse → `ParseCalibration`) holds
  for OOB-but-glowing lights — pinned by `TestApplyImportOOBLightRoundTrip`.
- Field-by-field drop discipline unchanged: bad color still drops the
  light (never invents white), non-finite intensity still falls back to
  1.0, non-bool shadows to false. Frontmatter keys append-only; no vault
  write-path change; secrets filtering untouched (lights are board-visible
  to all map viewers per the snapshot-v2 note).
