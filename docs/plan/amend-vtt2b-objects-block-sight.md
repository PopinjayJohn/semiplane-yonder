# Amend A-VTT2b: `objects_block_sight` snapshot flag (FragmentVersion 2 → 3)

Status: LANDED on this branch (renders the R6 data path's deferred bump;
based on `plan/r6-full-blocker`, which carries the data path as PR #37).

Parent: `docs/plan/amend-vtt2-pixi-uvtt.md` (A-VTT2, FragmentVersion 1 → 2).
Data path: `docs/plan/r6-full-blocker.md` (R6: `objects-block-sight`
sidecar key, importer options, lenient calibration parse — NOT re-litigated
here; this amend only carries the already-parsed value to renderers).

## 1. What changed in the frozen contract

`internal/plugins/fragment.go`:

- `FragmentVersion` **2 → 3** (bump only via amend, per the file's header —
  this document IS that amend).
- New field on `plugins.StateSnapshot`:

```go
// v3: objects double as sight blockers when the sidecar says so (R6).
ObjectsBlockSight bool `json:"objects_block_sight,omitempty"`
```

No other frozen name changes: no new SSE event, no fog-codec change (v1
rect encoding untouched), no route change, no frontmatter-key change.

## 2. Why it is authorized

The R6 note (`r6-full-blocker.md`, "Renderer treatment when the flag is
set") explicitly defers the renderer treatment behind this exact bump: the
snapshot carries `walls`+`objects` but no flag, so the Pixi renderer cannot
learn the posture without a contract change. The R6 data path (PR #37)
deliberately ships no `plugins/` or JS change; this amend lands the
rendering half after it. Same precedent as A-VTT2, which bumped
FragmentVersion 1 → 2 for the same reason (snapshot needed fields the
contract did not carry).

## 3. Compat semantics for v2 clients

- `omitempty`: a v2-shaped row (`ObjectsBlockSight: false`, the default)
  encodes byte-identically to v2 — no key emitted.
- A v2 client (Pixi or DOM) receiving a v3 snapshot ignores the unknown
  `objects_block_sight` key (JSON forward-compat) and keeps today's
  movement-only rendering; it tells v3 from v2 via the `version` field.
- A v3 Pixi client reads `snap.objects_block_sight` in
  `drawWalls(view, walls, objects, objectsBlockSight)` and renders objects
  at wall weight (alpha 0.9) when set, faint (0.45) otherwise.

## 4. Server ownership + gating

`SnapshotFor` (`internal/vtt/snapshot.go`) owns the value: set from
`cal.ObjectsBlockSight` (the R6 data path already parses it leniently,
absent/broken → false). Board-visible to ALL map viewers, behind the SAME
fail-closed gate as the rest of the geometry: inside `if !fog.Hidden`, so
a fully hidden board emits no geometry AND no flag.

## 5. Renderer behavior

- Pixi (`web/static/vtt/pixi-board.js`, `drawWalls` + `renderSnapshot`
  call site): objects at wall weight when the flag is set. Declarative
  only — no LOS math changes.
- DOM (`internal/vtt/render.go`): VERIFIED unchanged — the DOM renderer
  draws no wall/object geometry at all today (grid overlay, fog, tokens,
  initiative, dice, GM door/fog panels only), so there is nothing to
  re-weight. No DOM change in this amend.

## 6. What P3 will do with the flag

Vision / line-of-sight simulation (P3, out of scope here) feeds flagged
objects into the LOS mask as sight blockers, alongside walls. Until then
the flag is declarative: storage + parse + snapshot + wall-weight render.
P3 needs no further contract bump for the map-level posture; a per-segment
flag would need its own amend only if exporters ever mix blocker kinds
inside one `objects_line_of_sight` array (R6 open question — today the
array is homogeneous per file, so map-level suffices).

## 7. Tests

- `internal/plugins/contract_test.go`: version pin 3;
  `TestSnapshotJSONShape` asserts `"version":3` + v2/v3-key absence on
  v1-shaped rows; new `TestSnapshotJSONShapeV3ObjectsBlockSight` pins
  key-when-set / absent-when-unset / round-trip.
- `internal/vtt/handlers_test.go`: frozen-shape pin `"version":3`; new
  `TestSnapshotV3ObjectsBlockSight` (per-viewer: GM + player receive the
  flag with geometry; absent key reads false and encodes v2-identically;
  fully hidden board leaks neither geometry nor flag).
- `cmd/app/static_serving_test.go`: served `pixi-board.js` pins the new
  `drawWalls` signature, the wall-weight branch, and the snapshot-key
  passthrough (plus the byte-identical mirror via
  `TestW3StaticCopiesMatchSources`).
- Emitted Go JS untouched (`clientJS`, `pixiLoaderJS` byte-identical), so
  no new `jsBalanced` pin beyond the existing `TestRenderPageScriptsBalanced`.
