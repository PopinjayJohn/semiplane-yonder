# Spike: PixiJS parity loader/layers design (no contract change)

Status: design spike for P0 on branch `plan/vtt-pixi-uvtt`. Changes NOTHING:
frozen contracts (`FragmentVersion=1`, `MapFragmentPath`, `StateSnapshotPath`,
SSE names, `StateSnapshot` shape, routes, templates, VERSIONS) are untouched.
No Go files changed. No new dependencies vendored here.

## 1. Pin recommendation (exact, vendored at P0 execution)

- Package: `pixi.js`, major v8 (WebGPU-capable `Application.init` API, `preference`
  fallback chain; v7 `Application` constructor options differ, so v8 is the floor).
- Recommended pin: **`pixi.js@8.9.1`**, file `dist/pixi.min.mjs` (ESM, `import()`-able):
  `https://cdn.jsdelivr.net/npm/pixi.js@8.9.1/dist/pixi.min.mjs` with committed
  fallback `file=pixi.js` in `web/static/vendor/VERSIONS`.
- Why jsdelivr `npm/pixi.js@.../dist/...`: matches the existing codemirror pin
  shape (`npm/codemirror@.../dist/index.js`); `gh/.../bundles/...` shape is
  datastar-specific. `dist/pixi.min.mjs` (not `pixi.min.js` UMD) because the
  loader is a `type="module"` dynamic `import()` — UMD has no ESM export to import.
- SRI: **not fabricated in this spike** (offline; committed vendor tooling only).
  At P0 execution, author with:
  `go run ./cmd/app vendor sha256 https://cdn.jsdelivr.net/npm/pixi.js@8.9.1/dist/pixi.min.mjs`
  for the VERSIONS hex-sha256 column, then derive the `<script>/<link>`
  `integrity="sha384-<base64>"` from the same bytes and verify with
  `yonder vendor verify` (offline, committed fallback) +
  `yonder vendor verify --fetch` (online, URL compare). If 8.9.1 is superseded
  by a newer 8.x at execution time, re-pin to the newest 8.x via the same two
  commands — never float (`@8` range) in VERSIONS.
- Serve: existing `/static/vendor` immutable mount
  (`public, max-age=31536000, immutable`, runbook §5) — no new mount, no route change.

## 2. Lazy `import()` loader sketch (`GET /vtt/{mapID}`, `?renderer=pixi|dom`)

Constraints: DOM output byte-identical by default; Pixi only on explicit
`?renderer=pixi`; Pixi bundle never inlines into SSR HTML (budget §8 below).

```html
<!-- appended by RenderPage in P0 execution (NOT in this spike): one module script -->
<script type="module" data-pixi-loader data-map-id="arena"
        data-state-url="/api/vtt/arena/state" data-vendor="/static/vendor/pixi.js">
const q = new URLSearchParams(location.search);
if (q.get("renderer") !== "pixi") return; // default DOM path untouched
const board = document.querySelector("[data-board]");
const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
try {
  const vendor = document.querySelector("[data-pixi-loader]").dataset.vendor;
  const PIXI = await import(/* @vite-ignore */ vendor);
  const ok = await bootPixi(PIXI, board, { reduceMotion });
  if (!ok) throw new Error("pixi boot declined");
  document.documentElement.classList.add("pixi-on");
  // Hide (never remove) the DOM board: it stays the operable/a11y truth (§6).
  board.setAttribute("aria-hidden", "true");
} catch (e) {
  // Any failure (no WebGL, no Canvas2d, import/SRI fail, boot throws):
  // stay on the shipped DOM renderer. Announce via the existing status line.
  document.querySelector("[data-sse-state]")?.append(" Canvas unavailable, DOM board kept.");
}
</script>
```

`bootPixi` (lives in a NEW `web/static/vtt/pixi-board.js`, also lazy — never a
render-blocking `<script src>`):

```js
export async function bootPixi(PIXI, board, { reduceMotion }) {
  let app;
  try {
    app = new PIXI.Application();
    await app.init({ preference: "webgl", resizeTo: board,
      autoDensity: true, resolution: devicePixelRatio || 1,
      backgroundAlpha: 0, antialias: true });
  } catch (e) { return false; }                    // -> Canvas attempt below
  if (!app?.renderer) return false;
  if (app.renderer.type === PIXI.RendererType.WEBGL) return mount(app, ...);
  try {
    await app.init({ preference: "canvas", resizeTo: board, ... }); // v8 canvas fallback
    return mount(app, ...);
  } catch (e) { try { app.destroy(); } catch {} return false; }     // -> DOM (§4)
}
```

State sourcing: same frozen endpoints, same ops. Initial paint from
`GET StateSnapshotPath` (per-viewer filtered server-side, `private, no-store`);
live updates reuse the existing `clientJS()` SSE subscription
(`token-move/token-hp/fog-update/...` → `refetchFragment()` + re-render Pixi
scene from the fresh snapshot). Pixi keeps NO authoritative state — identical
discipline to `clientJS()` ("clients cannot desync").

## 3. Layer map (DOM -> Pixi, v1 parity only)

One root `Container(isRenderGroup:true)` sized `cols*cell × rows*cell`, children
in fixed z-order; positions use the SAME percent math as `renderBoard`/`renderFog`
(`left=(x+0.5)*100/cols`), converted to pixels (`px = pct/100*stageSize`).

| DOM today | Pixi parity | Notes |
|---|---|---|
| `[data-board-image] img` (ACL `/assets/`) | `background`: `Container` + `Sprite(await PIXI.Assets.load(assetURL))` | Same ACL URL incl. `AsQuery`; `Assets.load` failure → keep DOM `<img>` visible, abort Pixi mount (fail-closed, no blank canvas) |
| grid (implicit cells) | `grid`: single `Graphics` stroking `cols+1`/`rows+1` lines | Cosmetic only; 1px lines, theme `currentColor` equivalent; no hit-testing |
| `[data-fog-rect]` divs / `[data-fog][hidden]` | `fog`: `Graphics` rects at snapshot `Fog.Shape` geometry + full-stage cover when `Hidden` | v1 rect codec only (`v1;...` opaque string parsed by the same client split the DOM uses); corrupt/missing → full-cover (fail-closed, mirrors `ResolveFog`) |
| `[data-token]` buttons (`left/top %`) | `tokens`: one `Sprite` (circle `Graphics` texture, initial letter via `Text`) per snapshot token, same x/y | Read-only markers in P1 (selection/announce via the DOM buttons underneath); drag comes in P2, not here |
| initiative `<ol>`, dice `<ol>`, fog text | `fx`: NOTHING in P1 | No HP bars/tweens/particles in parity (P4 juice); turn highlight stays DOM `aria-live` |

No `viewport`/`cull`/`atlas` in parity (P4). No lighting/vision (P3 consumes UVTT walls).

## 4. Fallback chain (WebGL -> Canvas -> DOM)

1. `Application.init({preference:'webgl'})` → `renderer.type===WEBGL`: mount.
2. Else `init({preference:'canvas'})` (v8 still ships the canvas renderer
   adapter): mount. Covers headless/test browsers + blocked-WebGL basements.
3. Else (either `init` throws, `renderer` missing, `Assets.load` fails, or any
   exception in scene build): destroy partial `app`, remove canvas element,
   leave the shipped DOM board exactly as SSR'd. The `catch` in §2 announces
   through `[data-sse-state]` — the same polite live region the DOM client uses.
4. `?renderer=dom` (or absent param): loader returns before importing Pixi —
   zero vendored bytes fetched, axe/baseline tests unaffected.

SSR never branches on renderer: the server always emits the full DOM fragment;
`?renderer=` is a client-only enhancement hint, so no route/template/contract change.

## 5. `SnapshotFor` stays server-side (no filtering moves client-side)

- Pixi consumes ONLY `GET /api/vtt/{mapID}/state` (= `SnapshotFor`) and the
  already-filtered fragment HTML. Hidden tokens are dropped server-side for
  non-GMs (id/name/position/HP/link absent — `snapshot.go:85`), initiative rows
  for unseen tokens dropped (`snapshot.go:106`), fog fail-closed hidden on
  corrupt mask, character links resolved per-viewer (`CharacterLinkFor`).
- The canvas NEVER receives unfiltered data to clip itself: there is no
  "all-tokens + client-side hide" mode. A compromised/patched loader can only
  reveal what the server already sent that viewer — the same guarantee the DOM
  renderer relies on. Blind dice totals stay redacted in SSE payloads; canvas
  shows the same `"redacted"` string path as `clientJS()` dice handler.
- GM preview (`PreviewAs`) keeps the W2 read-only rule: loader issues no new
  write surface; P2 drag reuses the debounced `PATCH /state` which already
  403s under preview.

## 6. DOM listbox stays the operable truth

- `renderTokenList` (`role=listbox` + `role=option` + text-coordinate mover form)
  is untouched and remains the keyboard/AT path: Tab → arrows choose →
  Ctrl+arrows or Column/Row form moves. Canvas tokens get `aria-hidden` via the
  board-level `aria-hidden="true"` when Pixi mounts (canvas is enhancement, per
  spec §6 "canvas is enhancement" for this VTT lane).
- Focus retention: the existing `focusedInside()`/`pendingMorph` discipline in
  `clientJS()` is reused — Pixi re-renders on the same `refetchFragment()`
  beat, never steals focus, `aria-live polite` (dice/moves) / `assertive`
  (fog reveals) unchanged. 44px targets + visible focus + text (never
  color-only) requirements stay satisfied by the DOM controls, which are never
  removed, only visually covered by the canvas.
- Axe gate: `/vtt/arena` tested with BOTH `?renderer=dom` (baseline, must stay
  clean) and `?renderer=pixi` (canvas mounted AND all DOM controls still
  operable/landmarked — nested-landmark and Label-in-Name pitfalls §T1-D apply).

## 7. Reduced-motion handling

- Loader reads `matchMedia("(prefers-reduced-motion: reduce)")` once; when set:
  no `PIXI.Ticker` animation loop (render on state-change only via
  `app.render()` / `renderer.render(stage)`), no spawn/move tweens (tokens snap
  to snapshot coordinates — same as DOM morph), no ping/ruler animation (P2
  ephemerals render static or not at all), CSS `@media (prefers-reduced-motion:
  reduce)` kill-switch in `pageCSS()` already covers DOM transitions.
- The flag threads into `bootPixi` opts (see sketch) so P4 juice cannot
  accidentally animate: tween helpers take `reduceMotion` and early-return to
  final state.

## 8. Budget argument (spec §8: `<100KB` excl. vendored JS, `<1s` resync)

- SSR HTML unchanged in bytes (one ~1KB module loader script added at P0
  execution; this spike adds zero). Pixi (~400KB–1MB min) is EXCLUDED by the
  existing rule ("excl. vendored JS") and additionally (a) served from the
  immutable `/static/vendor` mount (year cache, no re-download across maps),
  (b) fetched only on `?renderer=pixi` via dynamic `import()` — DOM users pay
  nothing, (c) `Assets.load` background reuses the already-cached `/assets/`
  image the DOM `<img>` fetched.
- Resync `<1s`: parity re-render is O(tokens+rects) `Graphics` redraws on the
  existing SSE→fragment beat; no new network round-trips (same snapshot the DOM
  path fetches). P4 (atlas + culling) is the scale lever if large maps regress.

## 9. What P0 execution does (not this spike)

1. `vendor sha256` the pinned URL, append the `pixi ...` line to VERSIONS,
   `vendor verify` + `verify --fetch` green.
2. Add `web/static/vtt/pixi-board.js` (loader+layers per §§2–3) + wire ONE module
   script in `RenderPage` (renderer-param gate per §4).
3. Axe `/vtt/arena?renderer=dom|pixi`, `go build ./...`, `vtt` tests green,
   `make check` clean — then amend PR for the VERSIONS/spec-line change.
