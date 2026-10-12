# Amend A-VTT2: PixiJS renderer + UVTT import (DRAFT, branch `plan/vtt-pixi-uvtt`)

Targets: `docs/spec.md:9,24,27,30`, `internal/plugins/fragment.go:15`, `internal/markdown/frontmatter.go:20`, `web/static/vendor/VERSIONS:1`. No code on `main` changes until this amend merges as its own PR.

## 1. Stack (§2) — one vendored JS dep

Add `pixi.js v8.x` (exact pin + SHA256 + URL + `file=pixi.js` fallback in `web/static/vendor/VERSIONS`), served via existing `/static/vendor` immutable mount. Lazy `import()` on `GET /vtt/{mapID}` only. No Go dep change. No Node in binary. `CodeMirror 6 only JS` becomes `CodeMirror 6 (editor) + PixiJS v8 (VTT canvas) only`. Budget rule unchanged (`<100KB excl. vendored JS` — Pixi never inlines into SSR HTML).

## 2. Frontmatter — append-only sub-keys inside `vtt-map`

No new top-level key (`AllowedKeys` untouched). Inside `vtt-map` (`internal/vtt/vtt.go:44`), all optional, broken values dropped field-by-field (never quarantine):

```yaml
vtt-map:
  background: assets/maps/ford.png   # unchanged, set by import
  grid: {cols, rows, cell}            # unchanged; cell := pixels_per_grid clamped 16..256
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
- Mapping per `vtt-pixi-uvtt.md` §2. `map_size` >64 or image >32MP → 422 with reason (no silent downscale).
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
