# Amend A-VTT2: PixiJS renderer + UVTT import (merged PR #33, 2026-10-14, main a41d3e7)

Status 2026-10-13 (final): full surface LANDED on this branch — vendor pin
pixi.js@8.22.0 ESM + `/static/vtt/` loader mount, `POST /vtt/{mapID}/import`
(with fresh-map auto-create) + `import-uvtt` CLI, door-toggle op + GM door
panel, Tomb 96x72 + Ground-Floor demo sidecars with downscaled art. Parser
D4b/D4c/D5b fixes, grid cap 128, `grid-baked` + overlay, `FragmentVersion` 2 +
snapshot fields, `vtt_doors` 0004, door helpers + reseed landed earlier
(05254f7); the browser pass also fixed three shipped defects (client syntax
error, `?as=` refetch, bundle-dir asset ACL) and a latent WCAG 2.5.3 violation.
Merged to main 2026-10-14 (PR #33, a41d3e7) with CI green (check, axe,
serve-smoke, snapshot).

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

- `POST /vtt/{mapID}/import`: GM-only, login + `CanViewMap`, double-submit CSRF, `preview_as` 403. Body `.uvtt/.dd2vtt/.df2vtt` JSON (`application/json`), 12MB cap → 413 over. Larger files via CLI. A MISSING sidecar is not a 404 (2026-10-13 decision): a GM may create a map by importing into a fresh id — the import writes the sidecar itself (title + `vtt-map` + generated body), stamped `secret: true` fail-closed when the `maps/` tree is secret. Malformed ids still 404.- `import-uvtt --vault <dir> --map <id> <file>`: same parser, no HTTP cap.
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
- D4 Light `range`: VERIFIED 2026-10-13 against `00-Ground-Floor.dd2vtt` (format 0.2, 20x20 @150ppg, 29 lights range 5/1 at fractional-square positions) — range is grid SQUARES, passed through as-is. The spike's `rng = r / ppg` is wrong and must go.
- D4b Geometry units (new, same export): ALL UVTT coordinates (`line_of_sight`, `portals.bounds`, light `position`, `map_origin`) are grid squares — `toGrid` must subtract origin only, never divide by ppg. The spike divides everything by ppg (light 9.01 squares parses as 0.06) and must be fixed; ppg relates image pixels to squares only (dims check). Multi-point polylines must split into N-1 segments; recount walls/portals against the export (47 LOS / 31 portals in, verify all survive) and explain the 29→28 light drop.
- D4c Color alpha strip (new, same export): `parseUVTTColor` strips the WRONG end — `"ffeccd8b"` yields `"ffeccd"`, must be `"eccd8b"` (drop leading alpha pair, keep last 6). Same for `ambient`: `"ff80668d"` → `"80668d"`, not `"ff8066"`. Unparseable color on a light: drop the light (per snapshot design §5), don't invent white — spike currently defaults to `"ffffff"`.
- D5 VP8X dims: VERIFIED-WITH-BUG 2026-10-13. Real animated VP8X (`File:AnimatedWebPSample.webp` from Wikimedia Commons, 1920x1080, flags 0x12) parses as **1x1920**; crafted VP8X (9600x7200 real Tomb payload, odd 123x45) parse as 1x9600 / 1x123. Root cause filed as D5b. Plain VP8 verified good (Tomb embedded 9600x7200, nogrid webp 6720x5040). PNG `DecodeConfig` verified good (Ground-Floor 3000x3000).
- D5b VP8X off-by-3 (new, implementer must fix): `webpDims` reads width from `body[1..3]` — those are the RESERVED bytes. Correct layout (`doc/webp-container-spec.txt`): `body[0]` flags, `body[1..3]` reserved, `body[4..6]` width-1 (24-bit LE), `body[7..9]` height-1; require `len(body)>=10`, return `w+1, h+1`. Minimal fixture bytes for 123x45: `52 49 46 46 1e 00 00 00 57 45 42 50 56 50 38 58 0a 00 00 00 02 00 00 00 7a 00 00 2c 00 00`. Real sample re-fetchable from Commons (`File:AnimatedWebPSample.webp`, 2.9MB — keep out of repo, use the 30-byte header fixture in tests). (Plain VP8 verified via Tomb 9600x7200; PNG `DecodeConfig` verified via Ground-Floor 3000x3000.)
- D6 OOB lights: drop now; retain-for-glow deferred to P3.
- D7 Pixi pin: re-pin newest 8.x at P0 execution via `vendor sha256` + `vendor verify --fetch`, then freeze; ESM `dist/pixi.min.mjs` via jsdelivr, `file=pixi.js` fallback. SRI never fabricated.
- D8 Loader: new `web/static/vtt/pixi-board.js` (first-party), lazy `import()` gated on `?renderer=pixi`; vendor bytes stay in `/static/vendor`.
- D9 Renderer selection: query-param per load only (`?renderer=pixi|dom`); no sticky pref.
- D10 Door toggle: GM-only first (matches `SetFog`); owner-movable deferred.
- D11 Wall scaling at downscaled PPG (P3): client-side float scale; floats survive exactly.

## 9. Deferred revisits (P3 or later, each needs its own note/amend)

Stance confirmed 2026-10-13: R1–R3, R5, R6 stay deferred exactly as listed;
R4 stays deferred with GM-only door ownership for now (no owner-movable
doors without its own auth + griefing review).

- R1 HTTP ~55MB + DoS review (rate-limit, base64 cost, single-writer impact).
- R2 `effectivePpg` downscale for large maps (scale walls/lights by same factor + warn).
- R3 OOB-light glow retention + culling/filtering rules.
- R4 Owner-movable doors (auth surface + griefing review).
- R5 Sticky renderer preference (shared-tablet stickiness unresolved — cf. T1-C).
- R6 Optional full-blocker flag for `objects_line_of_sight` (Foundry-style).
