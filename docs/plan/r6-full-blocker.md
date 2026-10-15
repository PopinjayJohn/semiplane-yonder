# R6 — Optional full-blocker flag for `objects_line_of_sight` (Foundry-style)

Status: data path LANDED (this PR); renderer treatment LANDED via
amend A-VTT2b (`docs/plan/amend-vtt2b-objects-block-sight.md`,
FragmentVersion 2 → 3 carrying `objects_block_sight` to renderers).
(no frozen-contract change in THIS PR — the bump lives in the amend.)

## Background

TODAY (amend A-VTT2 D3, Roll20 convention) `objects_line_of_sight` maps to
the `blocking-objects` sidecar sub-key and is movement-only. Foundry instead
lets a GM mark such geometry as a FULL blocker (movement AND line of sight).
Nothing consumes sight yet (vision is P3), so the flag is declarative for
now — but it must be stored, parsed, and visible to renderers.

## Design

New append-only sub-key inside `vtt-map` (same sanctioned pattern as
`grid-baked`, amend §2):

```yaml
vtt-map:
  blocking-objects: [[...]]   # unchanged, movement-only geometry
  objects-block-sight: false  # NEW: true = full blocker (movement + sight)
```

- Shape: plain boolean, default `false` = today's movement-only behavior.
  Absent key reads as `false` (same as `grid-baked`).
- Lenient, field-by-field: a broken value (non-bool string, number, list)
  drops to `false` and never quarantines the page — same `asBool` funnel as
  `grid-baked`/`baked-lighting`.
- Importer-owned: listed in `importerOwnedKeys`
  (`internal/vtt/import_apply.go`), so re-imports replace it and hand edits
  to every other sub-key survive. The importer ALWAYS writes the key
  (`objects-block-sight: true|false`), mirroring `grid-baked` — explicit
  posture, idempotent re-imports.

## Importer options (mirrors `--grid-baked` / `grid_baked` exactly)

- CLI: `import-uvtt --objects-block-sight | --no-objects-block-sight`
  (mutually exclusive; default false). `ImportOpts.ObjectsBlockSight *bool`;
  a non-nil opts value overrides the body field, same precedence as
  `GridBaked`.
- HTTP: `objects_block_sight` JSON body field on
  `POST /vtt/{mapID}/import` (read by `ParseWithOpts` with empty opts —
  no handler logic change; the import summary JSON echoes it back as
  `objects_block_sight`, next to `grid_baked`).

## Renderer treatment when the flag is set

- Pixi (`web/static/vtt/pixi-board.js` `drawWalls`): objects are drawn
  fainter today (`alpha 0.45` vs walls `0.9`) to read as movement-only.
  With the flag set, objects must read as full blockers — i.e. drawn at
  wall weight. Future P3 vision additionally feeds them into the LOS mask
  as sight blockers.
- DOM fragment: renders no wall/object geometry today (grid overlay only),
  so no DOM change is needed for the declarative phase.
- BLOCKED (no unilateral edit): the renderer learns the flag from the
  `StateSnapshot`, which carries `walls`+`objects` but no flag. Adding it
  needs a `FragmentVersion 2 → 3` amend bump carrying (proposed):

```go
ObjectsBlockSight bool `json:"objects_block_sight,omitempty"`
```

  `SnapshotFor` owns the filtering (board-visible to all map viewers, same
  gate as walls/objects). v2 clients ignore the unknown key; v3 Pixi
  `drawWalls(view, walls, objects, objectsBlockSight)` draws objects at
  full weight when set. Proposed diff is sketched in the final report, not
  implemented here.

## What this PR does

Parser option + `UVTTMap.ObjectsBlockSight`; `Calibration.ObjectsBlockSight`
+ lenient `ParseCalibration`; sidecar write + owned-key merge;
CLI flags + HTTP body field (parser-level) + summary echo;
`ImportResult.ObjectsBlockSight`. Tests in `import_uvtt_test.go` (options),
`vtt_test.go` (lenient parse), `import_apply_test.go` (round-trip +
idempotent re-import flip true→false). No `plugins/` change, no JS change.

## Open questions

- Should a hand-set `objects-block-sight: true` on a map with zero
  `blocking-objects` warn (GM typo) or stay silent (future geometry)? This
  PR stays silent (absent = none, flag orthogonal).
- P3 vision will need the same flag on a per-segment basis if exporters
  ever mix blocker kinds inside one `objects_line_of_sight` array — today
  the array is homogeneous per-file, so a map-level flag suffices.
