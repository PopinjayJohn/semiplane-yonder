# VTT upgrade — PixiJS + UVTT (plan, branch `plan/vtt-pixi-uvtt`)

Worktree: `/home/johan/projects/semiplane-yonder-vtt`. All VTT-upgrade work stays on this branch/worktree. `main` stays clean until a PR.

## 0. VTT v1 baseline (shipped)

DOM-only board (`internal/vtt/render.go:clientJS()`): `<img /assets/...>` background + `%`-positioned `<button data-token>` + `<div data-fog-rect>` hidden-rect overlays + initiative `<ol>` + dice tray form. No canvas/WebGL.

- Truth split: calibration in `maps/<id>.md` frontmatter `vtt-map` (`internal/vtt/vtt.go:140`); live token/fog/initiative in app DB `vtt_maps/tokens/fog/initiative` (`internal/store/migrations/app/0001_app_auth.sql:112`, `0003_vtt_state.sql:1`).
- Fog codec `v1;<cols>x<rows>;<x,y,w,h>` hidden-rects, fail-closed (`internal/vtt/vtt.go:291`). Grid ≤64×64, cell 16–256px (`vtt.go:106`).
- Frozen contracts: `FragmentVersion=1`, `MapFragmentPath`, `StateSnapshotPath`, SSE names (`internal/plugins/fragment.go:14`); routes `/vtt/{mapID}` `/api/vtt/{mapID}/state` `/events?map=`.
- Assets via ACL-checked `/assets/` only (`internal/web/handlers_read.go:1170`); uploads sniffed `png/jpeg/webp/pdf`, 5/10MB caps (`internal/uploads/uploads.go:32`).
- Limits: no pan/zoom/drag, no lighting/vision, fog = rects, initiative = order-only, rolls ephemeral on VTT path.

## 1. Non-negotiables (every phase)

- Spec §2 frozen deps: PixiJS needs the amend in `amend-vtt2-pixi-uvtt.md`. No Go dep change, no Node in binary.
- Server-side secret filtering on every read path (`SnapshotFor` owns it). Hidden tokens / blind totals / secret titles never leave the server.
- Vault Obsidian-safe: parsers never rewrite source; writes via `vault.WriteFile` preserving comments/order. Frontmatter keys append-only.
- A11y: DOM token listbox + text-coords mover stay the operable truth; canvas is enhancement. `aria-live`, 44px targets, visible focus, never color-only, `prefers-reduced-motion` disables ticker/tweens.
- Budgets: `<100KB rendered pages excl. vendored JS`, `<1s VTT resync`. Pixi lazy-loaded on `/vtt/*` only.
- Windows-safe IDs/paths; single-writer SQLite; `Cache-Control: private, no-store` on snapshots.

## 2. UVTT support (`.uvtt` / `.dd2vtt` / `.df2vtt`, one JSON schema)

Top-level fields: `format`, `resolution{map_origin, map_size, pixels_per_grid}`, `line_of_sight`, `objects_line_of_sight`, `portals[{position, bounds[2], closed}]`, `environment{baked_lighting, ambient_light}`, `lights[{position, range, color, intensity, shadows}]`, `image` (raw base64, no `data:` prefix). Units = grid squares (float), top-left origin, +y down. Pixels = `(coord - map_origin) * ppg`. No tokens/notes in spec.

Mapping: `map_size → grid.cols/rows` (reject >64 with 422 for now); `ppg → grid.cell` (clamp 16–256, keep source `ppg` as info); `line_of_sight → walls` (sight+movement); `objects_ → blocking-objects` (movement-only, Roll20 convention); `portals[].bounds → portals` (`closed=true` door, `false` window; ignore `rotation`); `lights/environment → lights/ambient`; `image` magic-sniffed (`PNG`/`RIFF`/`FFD8`) and written to `assets/maps/<id>.<png|webp|jpg>` bundle dir.

Caps problem: typical exports 5–50MB break `MaxImageBytes=5MiB` / HTTP 12MB body. This plan: CLI `import-uvtt` uncapped for large files; HTTP `POST /vtt/{id}/import` capped at 12MB with clean 413. Raising the HTTP cap needs a later amend + DoS review.

## 3. Phases

### P0 — Amend + vendor + spike (no behavior change)
Pin `pixi.js v8.x` in `web/static/vendor/VERSIONS` + SRI, immutable `/static/vendor` serve, lazy `import()` on `/vtt/*`. `Application.init({preference:'webgl', resizeTo, autoDensity, resolution:devicePixelRatio})`, `preference:'canvas'` fallback, then DOM. Done: `?renderer=pixi|dom`, `go build` + `vtt` tests green.

### P1 — Parity renderer
Layers `background / grid / fog / tokens / fx` as `Container(isRenderGroup)` + `Sprite` + `Graphics` mask, `Assets.load` for background. Same `StateSnapshot`, same ops, same `refetchFragment`. Done: pixel-parity (% pos), axe clean, reduced-motion = no ticker.

### P-UVTT — Importer (usable standalone, rect-fog approx)
New `internal/vtt/import_uvtt.go` (pure parser/mapper, no contract reshapes), GM-only route + CLI, sidecar write + `ResetToSidecar` + `Publish(fog-update)`. See amend for field/table details. Done: golden fixtures (walls+door+light+cropped origin), oob-clip, corrupt→422, >64→422, leak matrix green.

### P2 — Camera + interaction
Pan (drag-board/arrows), clamped zoom, drag-token (`eventMode='static'`, `pointerdown/up/globalpointermove` + `toLocal`) reusing debounced `PATCH /state`; click-to-move + text mover kept. Ephemeral ping + ruler/range + 1-spell template (no DB write). Done: mouse/touch/keyboard all move; one drag = one `token-move`.

### P3 — Vision/fog v2 (consumes UVTT walls/lights)
`v2` codec (segments + door states + lights + ambient), `DecodeMask` fail-closed, `StateSnapshot.version=2`, new `vtt_doors` migrated table (ephemeral, fail-closed closed). Client `Graphics` LOS mask + torch gradient + GM player-preview. Large-map policy: downscale `effectivePpg` + scale walls, or amend cap — downscale first. Done: rect maps auto-upgrade, corrupt → hidden.

### P4 — Juice + scale
HP bars + text labels, turn highlight (`aria-live assertive`), spawn tween gated by reduced-motion, atlas + viewport culling. Done: resync `<1s`, budgets held.

## 4. Order + open questions

Order: P0 → P1 → P-UVTT → P2 → P3 → P4.

1. HTTP import cap: CLI-only for large files, or raise to ~55MB with amend?
2. Grid >64: downscale PPG on import, or raise `MaxGridCols/Rows`?
3. `objects_line_of_sight` default: movement-only (proposed) or full blocker?
