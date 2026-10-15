# R2 — `effectivePpg` downscale for large maps (design note)

Status: design + implementation that stays inside the unfrozen surface.
No `StateSnapshot` change, no `FragmentVersion` bump (verdict in §6).

## 1. What "effective ppg" means

Three different "pixels per grid" exist for one map; only the first two are
stored, the third is derived:

- **Source ppg** (`UVTTMap.PPG`, sidecar `vtt-map.ppg`): the exporter's
  `pixels_per_grid`. Info only. Tomb: 100; Ground-Floor: 150.
- **Display hint** (`grid.cell`, clamped 16..256): a render cell-size hint.
  It is NOT the art resolution and MUST NOT be used for geometry math.
- **Effective ppg**: the stored art's real pixels per grid,
  `storedArtWidth / cols` (≈ `storedArtHeight / rows`; imports preserve
  aspect). Tomb demo: 1280/96 ≈ 13.3. Ground-Floor demo: 1280/20 = 64.

The import pipeline (`processImportImage`, D2) downscales png/jpeg whose
longest edge exceeds `uploads.DisplayMaxDim` (1600); webp is stored as-is.
So for a png/jpeg map with source dims `cols*ppg × rows*ppg`, the stored
art is downscaled iff `max(srcW, srcH) > 1600`, by float factor
`1600 / max(srcW, srcH)`. The demo sidecars were hand-downscaled to 1280px
externally (same direction, exact stored dims unknowable from calibration
alone — the GM note in §5 is worded as a cap warning, not a measured value).

## 2. What "effective ppg" means for each renderer

- **DOM renderer** (`renderBoard`/`renderFog`): everything is positioned in
  grid-fraction percents (`left = x/cols*100`). The `<img>` fills the board
  (`width:100%`). Background and overlays scale by the SAME board box, so a
  downscaled art file cannot misalign anything. Effective ppg is irrelevant
  to DOM alignment — there is nothing to scale.
- **Pixi renderer** (`pixi-board.js`): `px(view, gx, gy)` maps grid squares
  to stage pixels (`gx/cols*stageW`), and the background sprite is stretched
  to the same stage (`view.bg.width = s.w`). Same single-normalization
  discipline as the DOM: overlays stay aligned at ANY art resolution with
  zero geometry rescaling. The loader additionally derives
  `view.effectivePpg = {x: tex.width / cols, y: tex.height / rows}` from the
  live texture for P3 (see §4); it is informational in P1.
- **P3 vision/LOS (future consumer)**: any pixel-space line-of-sight test
  must multiply grid-square geometry by the float art scale
  (`tex.width / cols`, NOT the rounded `grid.cell`, NOT source ppg). That
  multiplication is client-side, float-exact, per D11.

## 3. Where the scale factor is computed

Two sites, both outside the frozen contract:

1. **Go, `internal/vtt/effective_ppg.go` — `EffectivePPG(cal)`** (new,
   pure, no I/O): projects calibration onto
   `{SrcW, SrcH, Scale, Downscaled}`. Inputs are `grid.cols/rows`, `ppg`,
   and the background extension (webp → stored as-is, never downscaled;
   png/jpg → 1600px longest-edge cap; no background or no ppg → not
   downscaled). No vault reads, no image decode — the cap rule is
   deterministic, so the projection is exact for pipeline imports and a
   correctly-worded "may be" warning for hand-swapped art.
2. **JS, `pixi-board.js` — `view.effectivePpg`** (live texture dims over
   snapshot grid): the ground truth for the canvas. Computed after
   `Assets.load` resolves, refreshed in `renderSnapshot` if the sprite
   resizes. P3 reads this; P1 only exposes it.

Deliberately NOT computed/stored anywhere else: not in `StateSnapshot`
(frozen — see §6), not as a new sidecar sub-key (append-only keys change
only via amend; a structured `effective-ppg` key would also go stale under
hand-swapped art — derivation beats storage here).

## 4. How walls/objects/doors/lights keep exact float alignment

End-to-end grid squares, never pixels, never rounded:

- UVTT parse (`toGrid`: origin-subtract only, D4b) → sidecar floats
  (`formatFloat`, 4-decimal round) → `ParseCalibration` (field-by-field,
  clipped) → `SnapshotFor` (passed through as `WallSeg`/`DoorState`/`Light`
  floats) → renderers (percent / stage-fraction math).
- The importer does NOT fold any scale into the geometry: grid squares are
  resolution-independent, so scaling them by the art downscale factor would
  CREATE the misalignment R2 fears. (An early spike Shape divided geometry
  by ppg — D4b killed it for the same reason.)
- D11 ("client-side float scale; floats survive exactly") is honored as:
  the only scaling in the system is the renderers' single grid→stage
  normalization plus P3's future float art-scale multiply — both float,
  both applied uniformly to walls, objects, doors, AND lights (light
  `range` is in squares, `drawLights` already divides by `cols`).

## 5. What the GM sees

A GM-only note in the fragment (server-rendered, server-filtered —
`IsGM`-gated like the door panel and fog controls; players never receive
it, so source-dimensions stay GM-side):

```html
<section aria-label="Map art" data-art-note><h2>Map art</h2>
<p>Map art downscaled: source 100px per grid (9600×7200px), stored png/jpeg
art is capped at 1600px on the long edge. Walls, doors, and lights use grid
squares, so overlays stay aligned — no action needed.</p>
</section>
```

Placement: after the door panel, before fog controls — with the other
GM-only sections, inside `#vtt-map` so SSE morphs refresh it with the
fragment. Text-only (never color-only), static (no `aria-live` needed;
the existing polite/assertive regions are untouched), keyboard-neutral
(prose, no controls). Emitted only when `EffectivePPG.Downscaled` is true;
absent otherwise (byte-identical default path for small/webp/art-less
maps). The SSE status line (`[data-sse-state]`) is untouched — it carries
live-connection state, not calibration warnings. The sidecar import prose
(`sidecarBodyFor`) already documents grid/cell/counts and is unchanged.

## 6. Frozen-contract verdict: NO bump required

`plugins.StateSnapshot` / `FragmentVersion` are untouched. Rationale:

- Overlays need no per-frame scale value from the server: both renderers
  normalize grid squares against the same stage/board the background is
  stretched to, so alignment is structural, not data-driven.
- The GM warning derives from `Calibration` (available to `RenderFragment`
  today via `FragmentInput.Cal`) — no new snapshot field needed.
- P3's future pixel-space LOS derives its float scale from the live
  texture (`tex.width / cols`) — again no snapshot field needed.

A `FragmentVersion` bump carrying e.g. `effective_ppg: float` + `art_dims`
would only be required if a future renderer positioned overlays in stored
art pixels server-side (i.e. abandoned grid-square normalization). That
design is rejected here: it would quantize alignment to a stored
resolution and break hand-swapped art. If P3 ever wants it anyway, the
proposed diff is: `FragmentVersion 2 → 3`, add
`ArtW, ArtH int + EffectivePpg float64` (omitempty, v2 clients ignore) to
`StateSnapshot`, populate in `SnapshotFor` by decoding stored art dims —
explicit amend, not this note.

## 7. Tests

- `internal/vtt/effective_ppg_test.go`: projection pins — Tomb-shaped
  (96×72 @100ppg jpg → downscaled, scale 1600/9600), Ground-Floor-shaped
  (20×20 @150ppg jpg → downscaled), webp passthrough (never downscaled),
  small map (no downscale), missing ppg/background (no downscale),
  unknown extension (fail-closed: no warning).
- Fragment warning pins (in `render_pixi_test.go` alongside the other
  emitted-HTML pins): GM fragment of a downscaled calibration contains
  `data-art-note` with source dims; player fragment never contains it;
  non-downscaled calibration renders byte-identical (no note element).
- JS: `node --check` over both `pixi-board.js` copies (source +
  `cmd/app` embed mirror, equality pinned by `w3StaticPairs`); the
  `jsBalanced` guard covers emitted scripts only, static-file syntax is
  verified by `node --check` before shipping (pitfalls entry).
