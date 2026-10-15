# Design: Snapshot v2 + `vtt_doors` (P3 vision/fog v2; NO contract change)

Status: **design-only**. This document pins the P3 shape so the amend PR
(`amend-vtt2-pixi-uvtt.md`, which bumps `FragmentVersion 1 → 2`) can
implement it. **No Go/SQL file changes in this commit** — the amend PR
implements; this doc only records the agreed shape.

Scope: `internal/plugins/fragment.go` (`StateSnapshot`), `internal/vtt`
(`ParseCalibration`, `snapshot.go` `SnapshotFor`), app-DB migration
`0004_vtt_doors.sql`. v1 fog codec (`vtt.go:291` `v1;...`) is untouched.

## 1. New `StateSnapshot` fields (exact)

Appended to `plugins.StateSnapshot` (`internal/plugins/fragment.go:74`);
all `omitempty` so v1-shaped rows encode identically and v1 clients
(`encoding/json` ignores unknown fields) keep working:

```go
Walls   []WallSeg   `json:"walls,omitempty"`
Objects []WallSeg   `json:"objects,omitempty"`
Doors   []DoorState `json:"doors,omitempty"` // live open/closed from app DB
Lights  []Light     `json:"lights,omitempty"`
Ambient string      `json:"ambient,omitempty"`
```

Existing fields (`Version`, `MapID`, `Tokens`, `Fog`, `Initiative`) keep
their names, tags, and semantics byte-for-byte. `Fog.Shape` keeps the
`v1;<cols>x<rows>;<x,y,w,h;...>` rect encoding — rect-fog maps keep
rendering under v2 with no re-import (auto-upgrade: walls/doors/lights
absent → client falls back to rect mask).

## 2. Go types (float grid-square units)

Units follow the UVTT convention (`vtt-pixi-uvtt.md` §2): **grid squares
as floats**, top-left origin, +y down. Pixel conversion is client-side:
`px = (coord - map_origin) * ppg`. Server never converts — it validates
ranges only.

```go
// WallSeg is one sight/movement blocker (from `line_of_sight`).
type WallSeg struct {
    X1 float64 `json:"x1"`
    Y1 float64 `json:"y1"`
    X2 float64 `json:"x2"`
    Y2 float64 `json:"y2"`
}

// DoorState is one portal with LIVE open/closed state.
// A/B are the portal endpoints (grid squares, floats); Closed is the
// effective state: sidecar `closed` default overridden by the `vtt_doors`
// row when present, fail-closed to closed on loss (§4).
// Idx is the portal's position in the sidecar `portals` list (0-based),
// the join key to `vtt_doors.idx`.
type DoorState struct {
    Idx    int     `json:"idx"`
    A      [2]float64 `json:"a"`
    B      [2]float64 `json:"b"`
    Closed bool    `json:"closed"`
}

// Light is one UVTT light (`lights[]`).
// X/Y/Range are grid squares (floats); Color is normalized `rrggbb`
// lowercase (no `#`, alpha dropped); Intensity is the source float as-is;
// Shadows passes through (client hint only).
type Light struct {
    X         float64 `json:"x"`
    Y         float64 `json:"y"`
    Range     float64 `json:"range"`
    Color     string  `json:"color"`
    Intensity float64 `json:"intensity"`
    Shadows   bool    `json:"shadows"`
}
```

`Objects` reuses `WallSeg` (movement-only blockers from
`objects_line_of_sight`; Roll20 convention per `vtt-pixi-uvtt.md` §2).
`Ambient` is the normalized `rrggbb` string from `environment.ambient_light`
(lenient parse, §5).

## 3. `SnapshotFor` filtering rules

`SnapshotFor` (`internal/vtt/snapshot.go:68`) keeps owning all filtering.
New fields follow the existing board-visibility principle:

- **Walls / Objects / Lights / Ambient: visible to ALL map viewers.**
  Geometry and lighting are board-visible like fog shape — any viewer who
  passes `CanViewMap` (login + sidecar ACL) receives the full lists.
  Rationale: walls/lights reveal nothing about secrets; hiding them per
  player would desync the shared LOS picture and is out of scope for P3
  (per-player vision/puzzle occluders deferred).
- **Doors: open/closed visible to all map viewers.** Door state is board
  state (a player at the table sees whether a door stands open). The
  `Idx` join key is a sidecar-list position, not a secret id — safe to
  emit. No per-door ACL in v1.
- **Unchanged (still stripped):** hidden tokens dropped entirely for
  non-GMs (id/name/position/HP/character link — not even existence);
  initiative rows for unseen tokens dropped; blind dice totals never ride
  the snapshot (dice path untouched); secret-bundle asset ACL unchanged
  (background serves via `/assets/` only); `SnapshotFog.Hidden=true`
  still suppresses the token list (existing `if !fog.Hidden` gate stays).
- **Fail-closed composition:** corrupt calibration sub-keys are dropped
  field-by-field at parse (§5), never quarantining the map; missing
  `vtt_doors` table/rows → doors render from sidecar `closed` default
  (which itself defaults to closed — see §4). Missing/corrupt fog mask →
  fully hidden for everyone including GM (existing `ResolveFog` rule).
- **Cache headers:** snapshot endpoint keeps `Cache-Control: private,
  no-store` (per-viewer JSON; walls/doors included).

Leak-matrix delta for the amend PR: GM / player / guest ×
walls-visible / doors-visible / hidden-token-stripped / blind-stripped —
all green; guests still 401 before any field emits.

## 4. `vtt_doors` migration SQL sketch (NOT applied here)

Next app-stream migration `internal/store/migrations/app/0004_vtt_doors.sql`
(design sketch only — the amend PR writes and applies the real file):

```sql
-- 0004 (app target): P3 live door states (ephemeral, like vtt_fog).
-- vtt_maps(map) is the parent; doors key off (map, idx) where idx is the
-- 0-based position in the sidecar `portals` list.
CREATE TABLE IF NOT EXISTS vtt_doors (
    map  TEXT NOT NULL REFERENCES vtt_maps(id) ON DELETE CASCADE,
    idx  INTEGER NOT NULL,
    open INTEGER NOT NULL DEFAULT 0, -- 0 = closed, nonzero = open
    PRIMARY KEY (map, idx)
);
```

Rules:

- **FK to `vtt_maps`:** `map REFERENCES vtt_maps(id) ON DELETE CASCADE`
  (same parent convention as `vtt_tokens`/`vtt_fog`, which key off the map
  id; cascade keeps door rows from outliving their map).
- **Ephemeral + fail-closed:** doors are live board state like fog, not
  vault truth. Data-dir loss / missing table / missing row → the door
  renders **closed** (movement-blocking). Import and `ResetToSidecar`
  reseed rows from sidecar `portals[].closed`; a GM door toggle upserts
  one `(map, idx, open)` row and publishes `fog-update` (no new SSE name
  in this amend — clients refetch the snapshot, same as fog writes).
- **Out-of-range `idx`:** rows whose `idx` exceeds the current sidecar
  portal list are ignored at read (sidecar list edit wins; no error, no
  leak — `Idx` is list position, never a stable id).
- **`user_version` stream rules** (per `repo-layout.md` + `migrate.go`):
  app target versions independently from 1 (`0001_app_auth`,
  `0002_auth_reconcile`, `0003_vtt_state` shipped → this lands as `0004`,
  purely additive `CREATE TABLE IF NOT EXISTS`, no row touched, no table
  rebuilt). The runner applies it once inside one transaction and bumps
  `user_version` to 4 with it; a half-applied file rolls back whole.
  Index DB (`<name>.index.db`) untouched — precious app rows migrate,
  disposable index rows rebuild; never the reverse.

## 5. `ParseCalibration` sub-key validation table

All keys live **inside** the existing top-level `vtt-map` key
(`AllowedKeys` untouched — append-only sub-keys per the amend). Parse
never fails: broken values are **dropped field-by-field** (never
quarantine, never brick the board). Bounds assume the already-parsed grid
(`map_size → cols/rows`, reject >64 upstream at import with 422; parser
defends anyway).

| Sub-key | Source | Type / rule | Clamp / drop rule |
|---|---|---|---|
| `ppg` | `resolution.pixels_per_grid` | number > 0 | Info only. `grid.cell` := clamp(ppg, 16..256); out-of-range or non-numeric → keep `DefaultGrid().CellPx` (48), still record nothing (no error). |
| `ambient` | `environment.ambient_light` | `rrggbb` hex, lenient | Strip leading `#`, lowercase; accept 6-hex (alpha suffix/prefix dropped, named colors dropped). Anything else → `""` (field absent, `omitempty` drops it). |
| `baked-lighting` | `environment.baked_lighting` | bool | Warning hint only (client may warn that baked shadows duplicate dynamic lights). Non-bool → `false`. |
| `walls` | `line_of_sight` | list of 4-float segs `[x1,y1,x2,y2]` | Per-seg drop: wrong arity, non-finite (NaN/Inf), or fully outside `[0,cols]×[0,rows]` → drop seg. Partially-oob segs **clipped** to the grid (import-time clip; parser re-clips defensively). Zero-length segs dropped. Sight+movement semantics (client LOS + movement). |
| `blocking-objects` | `objects_line_of_sight` | same shape as `walls` | Same clamp/drop as `walls`. Movement-only semantics (Roll20 convention; open question #3 in `vtt-pixi-uvtt.md` §4 confirms or revisits). |
| `portals` | `portals[]` | `{a:[x1,y1], b:[x2,y2], closed: bool}` | Per-portal drop: endpoints non-finite or both fully oob → drop portal. `rotation` ignored. `closed` non-bool → default `true` (fail-closed). `bounds[2]` pairs wider than one cell still accepted (double-door UVTT exports). Surviving list order defines `DoorState.Idx`. |
| `lights` | `lights[]` | `{x, y, range, color, intensity, shadows}` | Per-light drop (R3 glow rule, `r3-oob-light-glow.md`): x/y non-finite → drop; center kept as-is (never clamped) while its glow disc (radius `range`) touches `[0,cols]×[0,rows]`; fully-off-board glow → drop. List capped at `MaxLights` (512, first-in-file kept). `range` ≤ 0 or non-finite → drop. `color` leniency = same as `ambient` (default `ffffff` on garbage? **drop the light** — decided: a light with no parseable color is corrupt input, fail-closed to absent rather than inventing white). `intensity` non-finite → `1.0`. `shadows` non-bool → `false`. |
| `uvtt-source` | importer | filename string | Audit only (original `.uvtt`/`.dd2vtt`/`.df2vtt` name). Truncated to 128 chars; path separators stripped (bare name, never a path). Never served as an asset; never used for reads. |

Existing keys (`background`, `grid`, `fog-default`, `tokens`) keep their
current rules (`vtt.go:152`); the table above is purely additive.

## 6. Upgrade path (v1 untouched)

- **`Fog.Shape` v1 encoding frozen:** `v1;<cols>x<rows>;...` keeps its
  exact codec (`EncodeMask`/`DecodeMask`/`ResolveFog` unchanged). P3 adds
  LOS *rendering* on top; it does not re-encode fog.
- **v1 clients ignore new fields:** `encoding/json` drops unknown keys on
  decode, and `omitempty` keeps v1-shaped snapshots byte-identical, so a
  v1 DOM renderer keeps working against a v2 server (it just renders
  rect-fog without walls/lights/doors).
- **Rect maps auto-upgrade:** maps with no UVTT import (no walls/doors/
  lights keys) serve a v2 snapshot with the new slices absent; the Pixi
  client falls back to the rect mask. No re-import, no migration of
  sidecar files.
- **Corrupt → hidden, never open:** any undecodable geometry fails closed
  (fog hidden; doors closed; bad lights absent) for everyone including GM.
- **Contract bump lives in the amend PR:** `FragmentVersion 1 → 2` plus
  the five `StateSnapshot` fields plus `WallSeg`/`DoorState`/`Light` types
  plus `0004_vtt_doors.sql` all land together as one amend PR (this doc is
  the design it implements). No SSE name changes in that PR (door toggles
  reuse `fog-update` + snapshot refetch).

## Open questions (for the amend PR, not this doc)

1. `objects_line_of_sight` movement-only vs full blocker (carried from
   `vtt-pixi-uvtt.md` §4 Q3).
2. Door-toggle auth: GM-only, or owner-movable like tokens? (Propose
   GM-only first — matches `SetFog`.)
3. Large-map policy for walls at downscaled `effectivePpg`: scale
   float segs client-side, or reject? (Propose client-side scale —
   floats survive it exactly.)
