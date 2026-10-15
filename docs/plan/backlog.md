# Backlog (post-ship)

Ship record: M1 Wiki → M2 CM → M3 VTT **SHIPPED** (G0–G4 GO 2026-10-09);
backlog sweep B0–B7 + UI-1/UI-2/UI-3 **DONE** 2026-10-10. Execution plans
(`p00–p13`, `overview.md`, `roadmap.md`, `lanes/`) were removed 2026-10-11 —
history lives in git. Work items W1–W3 below are **DONE** 2026-10-11; what
remains is the unscoped post-v1 feature ideas at the bottom.

Standing rules for every item: no new dependencies (spec §2 frozen); every
read path stays server-side secret-filtered; vault writes stay atomic via
`vault.WriteFile`; frontmatter keys append-only.

## T1 — Two themes (Grimoire + Cartographer) — CSS/serving DONE, wiring PENDING

Shipped 2026-10-11: `web/static/core/app.css` (shell CSS extracted from the
template into `@layer core`, tokenized), `web/static/themes/{grimoire,
cartographer}.css` — all eight slots, light + dark + auto schemes, static
preview pages — plus the `/static/themes/` mount and the mirror pins. Design
record: `docs/plan/themes.md` (palettes, measured contrast, slot matrix,
WCAG stance: Cartographer AA clean, Grimoire flavor-first with four measured
deviations).

**PENDING (needs an explicit amend, not unilateral):** campaign-level theme
selection — `theme` + `theme-scheme` as append-only `campaign.yaml` keys
(fail closed to unthemed on unknown values). `--theme` / `--theme-scheme`
serve flags already ship (§7 of the design doc: one funnel threaded to the
shell, the standalone sheet page, and the VTT page), so the amend only adds a
source to `web.ResolveTheme`; the flags remain the deployment-level default.

Resolution order for selection is pinned in themes.md §7 as
**campaign default → viewer override (future) → OS**, so the two mechanisms
below land additively later, without a migration of existing campaigns:

### T1-B — Per-viewer theme preference (deferred)

Store a per-user theme + scheme override in the app DB and resolve it above
the campaign default. Needs an app-DB migration (precious DB — migrated
stream, never rebuilt), a settings surface (route + form + CSRF), a
GM-visible "who sees what" note in the dashboard, and tests for the
resolution order. Only worth it if tables actually split (GM wants
parchment, players want night watch) rather than the GM picking for the
table. Until then: campaign-level only.

### T1-C — Cookie-based theme override (deferred)

A tiny `theme` cookie + picker endpoint keeps a per-browser preference with
no DB change and works for guests. Downsides that kept it out of the first
pass: stickiness is a bug on a shared table tablet, it needs the same picker
UI as T1-B, and cookies invite questions the spec has not answered (guest
identity is `?as=` under DemoAuth). Revisit only if per-user settings (T1-B)
are declined but per-browser preference is still demanded.

### T1-D — Slot fragment delivery — DONE 2026-10-12

Slot mounts rendered but stayed empty: plugin CSS reached the page (the
`PluginAssets` seam) but no fragments did, so an enabled plugin produced a
styled-but-blank panel. Shipped a server-side fragment seam:
`web.SlotShellData` (already-filtered shell data) + `web.SlotRenderer`
(registry dispatch, enabled-only, fail-closed) with each of the six v1
plugins mapping filtered data onto its own Source type. The demo campaign
now enables `vault-tree slot-header page-companions vault-colophon
quick-actions sheet-tools`, and every fragment is per-viewer filtered
(guest/owner/GM tree contents differ; colophon counts are aggregates over the
filtered list only).

Fixed the funnel bug underneath: `renderShell` stamped `SlotMark`s after the
caller had built the body component, so the page-actions and sheet-header
mounts (which live inside `PageBody`) rendered from the pre-stamp copy and
never appeared — even with plugins enabled. `renderShell` now takes a
`func(PageData) templ.Component` closure. Pinned by
`TestRenderShellFillsPageBodyMounts`, `TestRenderShellSlotFragmentsFilteredByViewer`,
`TestRenderShellMayWriteMatchesWriteGate`, and the per-plugin tests in
`internal/plugins/slots_render_test.go`.

Remaining follow-ups (not blockers): fragment delivery to the standalone
sheet page's own mounts, if any plugin ever mounts there; SSE morph targets
for these fragments exist (stable ids) but the per-fragment event wiring is
unchanged (page-level secret-flip only).

CI axe caught two T1-D fragment bugs on this branch (fixed in the same PR,
pinned in pitfalls): the colophon fragment rendered its own `<footer>`
inside the shell's `footer.colophon` (nested contentinfo), and the
vault-tree folder `aria-label` omitted the visible count (Label in Name,
axe 4.14). `TestShellFooterSlotKeepsOneContentinfo` pins the first shape
at the shell level for any future fragment.

### T1-E — Shell layout: rails beside the page — DONE 2026-10-12

The shipped shell nested BOTH sidebars in a single `<details class="drawer">`
below `<main>`, with `.layout { display: block }` — so the vault tree and the
companions panel stacked under the page like a second footer at every
viewport width. Restructured, with the CSS extraction (T1) doing the heavy
lifting:

- Each sidebar is its own `<section class="rail">` (aria-labelled) sibling of
  main, holding a native open `<details>` drawer that collapses without JS.
- DOM order stays main-first (phones read the page before the chrome); a CSS
  grid with named areas (`main`/`left`/`right`) places the rails beside the
  page when there is room: page + companions at ≥900px, tree · page ·
  companions at ≥1200px, single column below.
- ≥1200px the rails are `position: sticky` with their own scroll, so a deep
  vault tree never pushes the page down; the drawer's stacked-screen border
  drops once the rail is a permanent column.

Pinned on both halves — DOM (`web/templates/shell_layout_test.go`:
`TestShellRailsAndFallbacks`) and served CSS (`cmd/app`:
`TestCoreAppCSSLayoutGrid`, which fails on the old `display: block` shape).
Verified live at 1440px, themed and unthemed.

## W1 — Graph link-target normalization at write (+ backfill) — DONE (PR #25)

Shipped: `internal/store/links.go` (`NormalizeLinkTarget`), write-side
normalization in `reindex.go` + `backfillLinkTargets` in `Rescan` (backfill
before purge sweep), `resolveGraphTarget` probes normalized-first with legacy
fallback. Secret drop rule unchanged. Live proof: GM `/graph` returns 24
edges incl. `welcome.md→cinder-pact.md`.
(Note: W3's commit rode into `main` inside this PR's merge via shared-tree
contamination — see pitfalls; PR #26 auto-closed as a result.)

## W2 — Canonical preview-write rule — DONE (PR #27)

Rule: **preview-writes-403 everywhere — GM preview is read-only.** One
central `previewReadOnly()` helper (`handlers_write.go`) guards every
state-changing handler; reads (filtering as previewed user, banner, logging)
untouched. Login/logout exempt; GET forms stay readable.

## W3 — Serve `/static/core` + `/static/vendor` — DONE (PR #26, landed via #25)

Shipped: `cmd/app` embeds + serves `static/core/` (`public, max-age=86400`)
and `static/vendor/` (`public, max-age=31536000, immutable`); `isPublicStatic`
carve-out before the registry check (even `/static/print.css` 404'd live
before this). Public assets only, never vault bytes. Runbook §5 documents
the mounts.

## A-VTT2 — PixiJS renderer + UVTT import — DONE (merged PR #33, 2026-10-14, main a41d3e7)

Shipped 2026-10-13 on the amend branch (`docs/plan/amend-vtt2-pixi-uvtt.md` is
the contract; decisions D1–D12, revisits R1–R6):

- **Pixi pin**: `pixi.js@8.22.0` ESM (`dist/pixi.min.mjs` via jsdelivr) pinned in
  `web/static/vendor/VERSIONS` with the committed `pixi.js` fallback
  (`vendor verify` + `--fetch` green). New `/static/vtt/` mount serves the
  first-party `pixi-board.js` loader; one inline module script on `GET
  /vtt/{mapID}` gates the dynamic `import()` on `?renderer=pixi` (D9), with the
  WebGL→Canvas→DOM fallback chain and `prefers-reduced-motion` honored. The DOM
  board stays the operable truth (aria-hidden, never removed); SSR is
  byte-identical on the default path.
- **UVTT import**: `POST /vtt/{mapID}/import` (GM-only, login + CanViewMap,
  CSRF, preview-403, 12MB cap → 413; a FRESH map id auto-creates the sidecar —
  stamped `secret: true` fail-closed inside a secret `maps/` tree) +
  `import-uvtt --vault --map [--background] [--grid-baked|--no-grid-baked] <file>`
  CLI (uncapped, D1).
  Image rides the frozen uploads pipeline into `assets/maps/<id>.<ext>`; the
  sidecar `vtt-map` merge is surgical (importer-owned keys replaced;
  hand-edited `tokens`/`fog-default`/comments preserved). Live state reseeds
  (ResetToSidecar + fog all-hidden) and one `fog-update` wakes open boards.
- **Door toggle**: `{op:"door", idx, open}` op (GM-only; owner-movable is R4)
  plus the GM door panel in the DOM fragment (text state + toggle buttons).
- **Demo**: `maps/tomb.md` (96x72, 1237 walls, 19 portals, baked-lighting) and
  `maps/ground-floor.md` (20x20, 91 walls, 31 portals, 28 lights, ambient
  #80668d) with small downscaled art (`assets/maps/tomb.jpg` ~320KB,
  `ground-floor.jpg` ~560KB — demo vault 1.4MB vs 7MB+ of sources; JPEG
  flattens the art's transparent surround to black). The verified 29→28 light
  drop is OOB light #25 (x = -0.57).

Deferred revisits (each needs its own amend; stance confirmed 2026-10-13 — R4
stays GM-only for now): R1 HTTP ~55MB + DoS review, R2 `effectivePpg` downscale,
R3 OOB-light glow retention, R4 owner-movable doors, R5 sticky renderer
preference, R6 full-blocker `objects_line_of_sight`.

## Post-v1 feature ideas (unscoped — carried from overview §8–§10)
DM lens: torch/puzzle timers + spotlight tracker; downtime
shopping/craft/train flow with GM approve; in-person mode (login-free player
mirror + projector theme); safety tools (lines/veils, X-card,
`content-warning:` frontmatter with opt-out blur).

Player lens: full inventory + prepared spells from compendium (equip/use/cast,
attunement, weight); private journal nudges (new session/loot/level-up SSE
badge); downtime request + GM approval inbox; own warnings / lines/veils /
X-card button paging GM; guest-to-player join link (temp character, account
later).

Engine: full expression language for the ruleset evaluator (graduating from
the v1 hand-rolled arithmetic/boolean core); pack version enforcement
V1 SHIPPED (inline `base-hash`/`overlay-hash` pins, descriptor-only sha256,
pin mismatch fail-closed via `ErrPinMismatch`, empty pins float, homebrew
audit-only; content-prose hash deferred); media upgrades (EXIF-orientation auto-rotate, webp
decode/resize, PDF page-count — each needs a maintained dep, propose first
per AGENTS.md).

## R2 — `effectivePpg` downscale — SHIPPED (branch `plan/r2-effective-ppg`)

Deferred revisit R2 from the A-VTT2 section above, implemented without a
frozen-contract change (design: `docs/plan/r2-effective-ppg.md`):

- `EffectivePPGOf` (`internal/vtt/effective_ppg.go`, pure, no I/O) projects
  calibration onto the stored-art posture: png/jpeg art is capped at
  `uploads.DisplayMaxDim` (1600px long edge), webp is stored as-is (D2).
- GM-only `data-art-note` section in the fragment (server-filtered;
  players never receive it): source ppg + source dims + "overlays stay
  aligned — no action needed". Non-downscaled maps render byte-identical.
- `pixi-board.js` derives live `view.effectivePpg` from the loaded texture
  (float-exact, D11) for the future P3 pixel-space LOS consumer; overlay
  alignment itself needs no rescaling on either renderer (grid squares
  end-to-end — the importer deliberately does NOT fold the scale into
  geometry).
- No `StateSnapshot` change, no `FragmentVersion` bump. Remaining R-items
  (R1, R3–R6) stay deferred as listed in A-VTT2.
