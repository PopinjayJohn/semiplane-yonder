# Amend A-VTT2: PixiJS renderer + UVTT import (DRAFT, branch `plan/vtt-pixi-uvtt`)

Targets: `docs/spec.md:9,24,27,30`, `internal/plugins/fragment.go:15`, `internal/markdown/frontmatter.go:20`, `web/static/vendor/VERSIONS:1`. No code on `main` changes until this amend merges as its own PR.

## 1. Stack (§2) — one vendored JS dep

Add `pixi.js v8.x` (exact pin + SHA256 + URL + `file=pixi.js` fallback in `web/static/vendor/VERSIONS`), served via existing `/static/vendor` immutable mount. Lazy `import()` on `GET /vtt/{mapID}` only. No Go dep change. No Node in binary. `CodeMirror 6 only JS` becomes `CodeMirror 6 (editor) + PixiJS v8 (VTT canvas) only`. Budget rule unchanged (`<100KB excl. vendored JS` — Pixi never inlines into SSR HTML).

## 2. Frontmatter — append-only sub-keys inside `vtt-map`

No new top-level key (`AllowedKeys` untouched). Inside `vtt-map` (`internal/vtt/vtt.go:44`), all optional, broken values dropped field-by-field (never quarantine):

```yaml
vtt-map:
  background: assets/maps/ford.png   # unchanged, set by import
  grid: {cols, rows, cell}            # cols/rows 1..128 (raised for 96x72-class maps); cell := pixels_per_grid clamped 16..256
  grid-baked: false                  # true = art has its own grid, renderer skips overlay; false = render overlay
  ppg: 128                           # source pixels-per-grid (info only)
  ambient: "ffffff"                  # RRGGBB, lenient parse
  baked-lighting: false              # warning hint only
  walls: [[x1,y1,x2,y2], ...]        # floats, grid-square units, sight+movement
  blocking-objects: [[...]]          # movement-only (objects_line_of_sight)
  portals: [{a:[x1,y1], b:[x2,y2], closed: bool}]
  lights: [{x, y, range, color, intensity, shadows}]  # range in squares
  uvtt-source: ford.dd2vtt           # original filename (audit only)
```

## 3. Snapshot — `FragmentVersion 1 → 2`

`internal/plugins/fragment.go:15,72`: bump to `2`. v1 clients ignore new fields; v2 clients require them. New per-viewer-filtered fields on `StateSnapshot` (`SnapshotFor` owns filtering):

```go
Walls   []WallSeg   `json:"walls,omitempty"`
Objects []WallSeg   `json:"objects,omitempty"`
Doors   []DoorState `json:"doors,omitempty"` // live open/closed from app DB
Lights  []Light     `json:"lights,omitempty"`
Ambient string      `json:"ambient,omitempty"`
```

`SnapshotFog.Shape` keeps `v1;...` rect encoding. No new SSE names in this amend (import reuses `fog-update` + `encounter-spawn`).

## 4. Import surface — one route + one CLI

- `POST /vtt/{mapID}/import`: GM-only, login + `CanViewMap`, double-submit CSRF, `preview_as` 403. Body `.uvtt/.dd2vtt/.df2vtt` JSON (`application/json`), 12MB cap → 413 over. Larger files via CLI.
- `import-uvtt --vault <dir> --map <id> <file>`: same parser, no HTTP cap.
- Accept all three extensions interchangeably, sniff JSON. `image`: chunked base64 decode, magic-sniff, `uploads.Process` + `Vault.WriteFileBytes` to `assets/maps/<id>.<png|webp|jpg>` (bundle dir, inherits sidecar ACL). Never serve `.uvtt` bytes via `/assets`.
- Mapping per `vtt-pixi-uvtt.md` §2. `map_size` >128 or image dims >128MP → 422 with reason (no silent downscale). Grid cap raised 64→128 to admit 96x72-class maps (Tomb verification 2026-10-13: 96x72 @100ppg, 105 LOS polylines, 24 objects, 19 closed portals, 0 lights, `baked_lighting:true`).
- Background selection: importer writes the embedded `image` by default (Dungeondraft bakes nogrid into it — overlay rendered since `grid-baked:false`). A GM may replace `background` with external grid-baked art (e.g. `*-grid-*` files) and hand-set `grid-baked:true`; cols/rows must equal the baked squares or the overlay/table misaligns — hand-swapped art is GM's responsibility. Import takes `--background <file> --grid-baked/--no-grid-baked` (CLI) / `grid_baked` field (HTTP) to do both in one step.
- After sidecar write: `ResetToSidecar` + `SetFog(all-hidden)` + `Publish(fog-update)`.
- New app-DB table `vtt_doors(map, idx, open)` via migrated stream (`user_version`), ephemeral like `vtt_fog`, fail-closed closed on loss. Never write `vtt_maps.calibration` directly (legacy column).

## 5. Uploads (§7) + fences (§9)

- §7: add "`.uvtt` import source (JSON, GM-only, never served as asset)", `MaxUVTTBody=12MB` HTTP.
- §9: `VTT v1 = image+fog+tokens+dice+initiative` gains "+ Pixi parity renderer (DOM fallback) + UVTT import (rect-fog approx; full vision in P3)". Still no WS/WYSIWYG/OAuth.

## 6. Tests / gates

- `internal/vtt/import_uvtt_test.go`: goldens (walls+door+light+cropped `map_origin`), oob-clip, corrupt→422, >64→422, color/alpha leniency, baked-lighting warning.
- Leak matrix: walls/lights visible to all map viewers; hidden tokens/blind totals still stripped; secret-bundle asset ACL.
- `vendor verify` pin + SRI; axe on `/vtt/arena` with `?renderer=dom|pixi`; resync `<1s`; `make check` clean.

## 7. Rejected

- Raising HTTP cap to ~55MB now (DoS review deferred to P3).
- Silent downscaling of large maps (fail loudly instead).

## 8. Decisions (accepted 2026-10-12, implement as specified)

- D1 HTTP cap: CLI `import-uvtt` uncapped; HTTP `POST /vtt/{mapID}/import` capped 12MB → 413. (§4)
- D2 Grid cap: raised 64→128 (admits 96x72-class maps; verified against Tomb export). Image dims cap raised 32MP→128MP for the dims-only check (webp stored as-is, never fully decoded at import); png/jpeg write path still guarded by `uploads.Normalize` 32MP — over-cap png/jpeg must be converted to webp externally first (follow-up note for the implementer, not a new cap).
- D3 `objects_line_of_sight`: movement-only (Roll20 convention); `Objects` reuses `WallSeg`. (§2)
- D12 Grid overlay: new `grid-baked` sub-key, default false. `false` = renderer draws the grid (DOM now, Pixi P1); `true` = art carries its own grid, renderer skips overlay lines (tokens/fog/pings still render). Importer sets it via `--grid-baked` flag / `grid_baked` field; hand-edits allowed (Obsidian-safe, field-by-field fallback).
- D3 `objects_line_of_sight`: movement-only (Roll20 convention); `Objects` reuses `WallSeg`. (§2)
- D4 Light `range`: keep spike (pixels ÷ ppg → squares) until verified against one real Dungeondraft/Foundry export; verification required before this amend merges, then pin the interpretation here.
- D5 WebP VP8X dims: best-effort parse + verify against a real extended-WebP export before merge.
- D6 OOB lights: drop now; retain-for-glow deferred to P3.
- D7 Pixi pin: re-pin newest 8.x at P0 execution via `vendor sha256` + `vendor verify --fetch`, then freeze; ESM `dist/pixi.min.mjs` via jsdelivr, `file=pixi.js` fallback. SRI never fabricated.
- D8 Loader: new `web/static/vtt/pixi-board.js` (first-party), lazy `import()` gated on `?renderer=pixi`; vendor bytes stay in `/static/vendor`.
- D9 Renderer selection: query-param per load only (`?renderer=pixi|dom`); no sticky pref.
- D10 Door toggle: GM-only first (matches `SetFog`); owner-movable deferred.
- D11 Wall scaling at downscaled PPG (P3): client-side float scale; floats survive exactly.

## 9. Deferred revisits (P3 or later, each needs its own note/amend)

- R1 HTTP ~55MB + DoS review (rate-limit, base64 cost, single-writer impact).
- R2 `effectivePpg` downscale for large maps (scale walls/lights by same factor + warn).
- R3 OOB-light glow retention + culling/filtering rules.
- R4 Owner-movable doors (auth surface + griefing review).
- R5 Sticky renderer preference (shared-tablet stickiness unresolved — cf. T1-C).
- R6 Optional full-blocker flag for `objects_line_of_sight` (Foundry-style).
