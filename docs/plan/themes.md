# Themes — Grimoire + Cartographer

Two themes, four color schemes, all eight plugin slots. This file is the
design record: palettes with measured contrast ratios, slot chrome, WCAG
stance, and the wiring that is still owed. The CSS under
`web/static/themes/` is the implementation; this file is why it looks the way
it does.

Status: **CSS + serving shipped; selection wiring pending an explicit
`campaign.yaml` amend (see §7).** Nothing selects a theme yet, so every page
renders with core tokens until that amend lands.

## 1. What shipped

| File | Role |
|---|---|
| `web/static/core/app.css` | Core shell CSS extracted out of `shell.templ`'s inline `<style>`, tokenized, wrapped in `@layer core`. Themes override *this*. |
| `web/static/themes/grimoire.css` | Theme 1: D&D-first. Parchment (light) + Torchlit (dark). |
| `web/static/themes/cartographer.css` | Theme 2: chart-table, WCAG-AA. Daylight Chart (light) + Night Watch (dark). |
| `web/static/themes/preview-*.html` | Static, backend-less previews of each theme (no vault, no secrets). Set `data-theme` + `data-scheme` themselves. |
| `cmd/app/static/**` (mirrors) | Verbatim served copies — `go:embed` cannot reach outside `cmd/app` (pinned byte-for-byte by `TestW3StaticCopiesMatchSources`). |
| `internal/web/theme.go` | Selection: theme/scheme ids, validation (fail closed to unthemed), the `<html>` tag + stylesheet link builders, and the `PluginAssets` seam. |
| `internal/web/handlers_read.go` (renderShell) | The one shell funnel: stamps theme + delivered plugin assets onto every page. |
| `web/templates/shell.templ` | Renders the theme attributes, the theme + print stylesheet links, and delivered plugin CSS/JS (all conditional on being non-empty). |

Selection: `--theme` / `--theme-scheme` serve flags (§7), threaded to the
shell, the standalone sheet page, and the VTT page from one place.

Serving: `/static/themes/<file>` is mounted like `/static/core`
(`public, max-age=86400`, `nosniff`, traversal/roots 404). Budgets:
`theme-weight` = 46 KB of the 100 KB rendered-page budget.

Tests that pin the contract (`cmd/app/static_serving_test.go`):

- `TestW3StaticCopiesMatchSources` — mirror drift fails.
- `TestThemeStylesheetsCoverAllSlots` — every theme carries chrome for
  **all eight slots**, a light + dark + `auto` scheme, a re-point of every
  core/statblock/callout token **in every scheme**, `@layer core, plugins,
  overrides` pinning, and no selector outside `[data-theme]`.
- `TestW3StaticServing` / `TestW3IsPublicStatic` / `TestW3StaticOverHTTP` —
  the mount, its cache headers, and its 404/405 shape.

## 2. How a theme applies

```html
<html lang="en" data-theme="grimoire" data-scheme="light|dark|auto">
```

- `data-theme` picks the theme; `data-scheme` picks the scheme. Absent
  `data-scheme` = light. `auto` = the dark tokens under
  `prefers-color-scheme: dark`.
- Themes are **token overrides in `@layer overrides`**, exactly as
  `docs/spec.md` §6 and `docs/plugin-authoring.md` §3 prescribe. They are not
  plugin CSS and are not linted by `plugin check`.
- **Layer order is pinned in the file** with `@layer core, plugins,
  overrides;`. This matters more than it looks: the shell CSS used to be an
  unlayered inline `<style>`, and *unlayered rules outrank every layer* — a
  layered theme could never have overridden it. Moving the shell CSS into
  `@layer core` (a file, so the preview pages can link it too) is what makes
  themes possible at all. The explicit statement also fixes core < plugins <
  overrides no matter when a plugin stylesheet is first delivered.

What a theme does NOT do: restructure the shell, hide content, or touch
secrets. Everything a theme can reach is already server-filtered markup.

## 3. Theme anatomy

Each theme file is one `@layer overrides` block:

1. **Token blocks** — one per scheme (`light`, `dark`, `auto`). Each block is
   *complete*: it re-states scheme-independent tokens (fonts, radii) as well
   as colors, so a reviewer can read a whole scheme in one place. The
   contract test fails a theme that leaves a core token un-re-pointed in any
   scheme.
2. **Reading surface** — body texture (parchment grain / chart grid),
   typography, markdown table + blockquote chrome. Token-driven, so the same
   rules serve both schemes.
3. **Slot chrome** — all eight slots (§6).
4. **Print guard** — `@media print` strips theme chrome (fills, textures,
   header band) so handouts stay black-on-white. `print.css` is unlayered and
   wins for what it covers, but it knows nothing about theme chrome; the
   themes must clean up after themselves.

Motion: themes add no animation. `prefers-reduced-motion` is honored by core
(`app.css`).

Shell layout (core, app.css): `.layout` is a grid — three columns (rail ·
page · rail) at ≥1200px, page + right rail at ≥900px, single column with main
first below that. Each rail is a native, open `<details>` (collapses without
JS) inside a `section.rail`; core renders its fallback content (nav list,
backlinks, action buttons) only when no enabled plugin fills that slot, so a
plugin replaces the fallback instead of duplicating it.

## 4. Palettes

All ratios below are measured WCAG 2.2 contrast (script: throwaway, values
reproduced here). `--ink`/`--paper` pairs are AAA; small text ≥ 4.5:1; UI
boundaries and focus rings ≥ 3:1.

### Grimoire · Parchment (light)

| Token | Value | Role | On | Ratio |
|---|---|---|---|---|
| `--paper` | `#f1e4c6` | aged parchment page | — | — |
| `--surface` | `#fbf3df` | raised parchment (buttons, panels) | — | — |
| `--ink` | `#2b1a0d` | sepia-black text | paper | 13.3:1 |
| `--accent` | `#8c1d10` | handbook oxblood: links, primary buttons | paper | 7.3:1 |
| `--on-accent` | `#fbf3df` | text on oxblood fills | accent | 8.3:1 |
| `--topnav-bg` | `#5f1a0c` | leather-spine header band | — | — |
| `--topnav-ink` | `#f5e9cd` | header text | band | 10.6:1 |
| `--topnav-link` | `#f0c98a` | header links | band | 8.2:1 |
| `--topnav-warn` | `#f2c879` | GM/preview badge in the band | band | 8.1:1 |
| `--line` | `#b99456` | aged-gold rules | paper | 2.2:1 ⚠ deviation (decorative only) |
| `--control-line` | `#8f6a2c` | control boundaries: secondary buttons, drawer | paper | 3.9:1 |
| `--input-line` | `#8a6f4d` | form boundaries | paper | 3.7:1 |
| `--hint` | `#7a5f38` | faded-ink hints | paper | 4.7:1 |
| `--focus` | `#6e4f0e` | focus ring | paper | 6.0:1 |
| `--warn` / `--danger` | `#8a5a00` / `#a12a2a` | GM badge / errors | surface | 5.4 / 5.8:1 |
| `--rule-accent` | `#e69a28` | signature gold rules | paper | 1.9:1 (decorative) |
| `--sb-*` | `#f6ead0` `#7a200d` `#922610` `#e69a28` `#2b1a0d` | statblock paper/red/rule/amber/ink | sb-paper | 14.0 / 8.6 / 3.6 ⚠ / — |
| `--callout-bg`/`-ink` | `#fbf3df` / `#2b1a0d` | callout boxes | — | 15.1:1 |

### Grimoire · Torchlit (dark)

| Token | Value | Role | On | Ratio |
|---|---|---|---|---|
| `--paper` / `--surface` | `#17110a` / `#211a10` | near-black umber | — | — |
| `--ink` | `#e8d9b8` | torch-pale text | paper | 13.4:1 |
| `--accent` | `#d9a441` | torch-gold links/buttons | paper | 8.3:1 |
| `--topnav-bg` / `-link` | `#0d0906` / `#e8b64c` | band / header links | band | 14.2 / 10.6:1 |
| `--line` | `#6b4f2a` | torch-lit brown rules | paper | 2.5:1 ⚠ deviation (decorative only) |
| `--control-line` | `#8a6a3c` | control boundaries: secondary buttons, drawer | paper | 3.8:1 |
| `--hint` | `#a98f68` | hints | paper | 6.1:1 |
| `--warn` / `--danger` | `#e8a084` / `#ff9a8a` | badges / errors | paper | 8.0 / 9.1:1 |
| `--sb-*` | `#241c12` `#e8a084` `#c07a4a` `#e6a23c` `#f3e9d2` | statblock | sb-paper | 13.9 / 7.8 / 7.7 |

### Cartographer · Daylight Chart (light)

| Token | Value | Role | On | Ratio |
|---|---|---|---|---|
| `--paper` | `#eef2f6` | pale chart vellum | — | — |
| `--surface` | `#ffffff` | card stock | — | — |
| `--ink` | `#16283c` | ink-navy text | paper | 13.3:1 |
| `--accent` | `#0b5a8a` | marine links/buttons | paper | 6.6:1 |
| `--line` | `#6b8a9e` | chart rules + form boundaries | paper / surface | 3.2 / 3.7:1 |
| `--topnav-bg` / `-link` | `#16283c` / `#9ecbe8` | ink band / links | band | 12.9 / 8.7:1 |
| `--rule-accent` | `#7a5c12` | brass rules, slot labels | paper | 5.5:1 |
| `--hint` | `#4a6172` | secondary text | paper | 5.8:1 |
| `--warn` / `--danger` | `#8a5a00` / `#a12a2a` | GM badge / errors | surface | 5.9 / 6.5:1 |
| `--sb-*` | `#f7f3e6` `#7a200d` `#922610` `#b97f16` `#1d2b3a` | statblock (SRD parchment survives as an artifact) | sb-paper | 13.0 / 9.3 |

### Cartographer · Night Watch (dark)

| Token | Value | Role | On | Ratio |
|---|---|---|---|---|
| `--paper` / `--surface` | `#0e1a24` / `#16283a` | deep night navy | — | — |
| `--ink` | `#dbe7f0` | chart-pale text | paper | 14.0:1 |
| `--accent` | `#6fb6e0` | horizon-blue links | paper | 7.9:1 |
| `--line` | `#5a7d94` | chart rules | paper / surface | 3.5 / 3.4:1 |
| `--rule-accent` | `#d9b45b` | brass on dark | paper | 8.9:1 |
| `--topnav-bg` / `-link` | `#081119` / `#8ecbec` | band / links | band | 15.1 / 10.8:1 |
| `--warn` / `--danger` | `#e0b25a` / `#ff9a8a` | badges / errors | paper | 9.7 / 8.6:1 |
| `--sb-*` | `#101d26` `#e8a084` `#c07a4a` `#d9b45b` `#dbe7f0` | statblock | sb-paper | 13.6 / 8.0 |

Callout tokens (`--callout-*`, 14 per scheme) follow the same pattern:
Grimoire re-tints them as illuminated-manuscript inks; Cartographer keeps
the Obsidian palette on white / its AA dark equivalent.

## 5. WCAG stance

**Cartographer: no deviations.** Every text pair ≥ 4.5:1, `--line` ≥ 3:1 for
UI boundaries, focus rings ≥ 3:1 (6.6:1 light / 8.4:1 dark). It is the
reference implementation of "a theme can be AA and still have a look".

**Grimoire: flavor wins, on purpose** — this is the theme's brief. After the
second pass (text + control boundaries tightened), exactly two deliberate,
measured deviations remain, and neither is a control boundary:

| Where | Value | Ratio | Why it stays |
|---|---|---|---|
| `--line` (h2 rules, blockquote edge, sidebar edge, panel + banner + colophon dividers) | `#b99456` on parchment | 2.2:1 | Aged-gold rules are the handbook look. Decorative dividers, not UI components — 1.4.11 does not reach them, and labels never ride color alone. |
| torchlit `--line` | `#6b4f2a` | 2.5:1 | Same role, dark variant. |

Everything else in Grimoire passes the same targets as core: text ≥ 4.5:1
(hint 4.7:1), control boundaries ≥ 3:1 (`--control-line` 3.9:1 light /
3.8:1 dark; `--input-line` 3.7:1), focus ring 6.0:1, badges 8.0:1+, statblock
13.9:1. The split that makes this possible: **`--line` stays decorative,
`--control-line` is the control-boundary token** (core default
`var(--line)`), so a theme can darken only what WCAG actually covers.

Two core fixes rode along (both make themes possible and both are strict
improvements):

- `.login-form .hint` used `--line` (#888 = **3.5:1**, an AA failure) as text
  color; it now uses `--hint` (default `#6b7280`, 4.8:1).
- Nav links inherited the UA default blue; they now ride `--topnav-link`
  (default `#1d4ed8`, 6.7:1). Badges that were inline-styled
  (`style="border-color:#b45309;color:#b45309"` — inline styles beat every
  stylesheet, so no theme could ever reach them) are now `.badge.gm` /
  `.badge.failed` / `.badge.locked` classes on tokens.

## 6. Slot chrome — all eight

| Slot | Where it lives | Chrome the themes give it |
|---|---|---|
| `header-left` | topnav, before the brand | Inline chip strip; inherits the band colors; plugin mounts render as inline rows. |
| `header-center` | topnav, after primary nav | Same as `header-left`. |
| `header-right` | topnav, pushed right by core | Same chips + band-colored `.btn` and `.badge.*` (an unadapted `--warn` would disappear on the dark band). |
| `sidebar-left` | left rail (own column ≥1200px, full-width below <900px) | Panel: border, fill, radius, padding; the rail's `<details>` summary is the labelled heading; plugin tree mounts inside. |
| `sidebar-right` | right rail | Same panel + a 3px accent edge on the left. |
| `footer` | `footer.colophon` | Double-rule (Grimoire) / brass-rule (Cartographer) strip; centered small text. |
| `page-actions` | above the article | Action bar: panel + flex row; `.actions` and plugin mounts share the row. |
| `sheet-header` | inside `.sheet-card` | Card chrome comes from core `.sheet-card` structure + `--card-*` tokens (head band, row rules, small-caps labels); the slot itself stays a plain mount host so `sheet-tools` mounts sit inside. |

Slot *labels* (`slot: page-actions`) exist only behind
`[data-debug-slots]` in core app.css — no shipped page sets it, so production
chrome stays clean; the preview pages set it so all eight mounts are
visibly present.

## 7. Selection wiring — SHIPPED via flags, amend still owed

Themes are selectable **today** through serve flags (flags > env > defaults,
per the runbook's precedence rule). Both the root invocation and the
`serve` subcommand bind them, since `yonder --vault DIR` defaults to serve:

```sh
yonder --vault ./my-campaign --theme grimoire --theme-scheme dark
yonder serve --vault ./my-campaign --theme cartographer --theme-scheme auto
```

- `--theme` accepts `grimoire` / `cartographer`; anything else (or empty)
  **fails closed to unthemed** with a warning — never a broken attribute, never
  a 500 (`web.ResolveTheme`).
- `--theme-scheme` accepts `light` / `dark` / `auto`; ignored without
  `--theme` (a scheme alone is inert — every theme selector is scoped under
  `[data-theme]`), and defaults to `light` when a theme is set.
- The resolved pair is threaded through `ReadHandlers` (`PageData`),
  `SheetHandlers` (standalone sheet page), and `vtt.Handlers` (standalone
  table page) from one place in `wireHandlers`, so all three surfaces always
  agree.

**Still owed (T1, needs the explicit amend):** `campaign.yaml` `theme` +
`theme-scheme` as append-only keys, so a theme is a campaign property instead
of a launch flag. Without the key-set change a hand-added key makes
`campaign.Load` fail *silently* (`serve` swallows the error and falls back to
default campaign name/landing page/plugins) — that is why this is an amend and
not a unilateral edit. When it lands it resolves into the same
`web.ResolveTheme` funnel; the flags become the deployment-level default.

Resolution order is pinned as **campaign default → viewer override (future)
→ OS**, with per-viewer (T1-B) and cookie (T1-C) mechanisms deferred in the
backlog — they land additively on this funnel, no migration of existing
campaigns.

### Gaps filed while building T1 — all now fixed

- **`/static/print.css` was served but never linked anywhere.** FIXED: the
  shell, sheet page, and VTT page heads all carry
  `<link rel="stylesheet" href="/static/print.css" media="print">`. It is
  unlayered, so it still wins over theme chrome for the rules it has.
- **Plugin CSS/JS never reached a page** (`Registry.CSS()/JS()` had no
  consumer). FIXED: a `PluginAssets` seam beside `SlotProvider` (serve injects
  the registry; web never imports plugins — cycle) delivers the concatenated
  CSS/JS into the shell head, unescaped (first-party plugin bytes, never vault
  content). Layer order was already pinned for this day: core < plugins <
  overrides.
- **`--border` was referenced by `vault-tree` plugin CSS but defined nowhere
  in core** (silently falling back to `currentColor`). FIXED: app.css owns
  `--border: var(--line)`.
- **The standalone sheet page and VTT page did not load app.css** and so
  could never be themed. FIXED: both load app.css + the theme stylesheet and
  carry the theme attributes; their own inline CSS stays as the offline
  fallback it always was. VTT fog is board-relative and unaffected either way.
- **`[data-theme="dark"]` in `statblock.css` named a theme that never
  shipped** — dead code that also confused the new scheme model. FIXED:
  removed (both the statblock and callout blocks); themes own `--sb-*` /
  `--callout-*` per scheme, pinned by the theme contract test.

### New gap found while fixing them — FIXED (T1-D)

**Slot mounts rendered but stayed empty.** The shell mounted
`<div data-plugin-slot>` per enabled component, but nothing filled them: all
six v1 plugins ship empty `JS()` and nothing rendered fragments server-side,
so enabling `vault-tree` produced a styled-but-blank panel. FIXED with a
fragment seam — `web.SlotShellData` (already-filtered shell data) +
`web.SlotRenderer` (the registry's dispatch) — so each plugin maps filtered
data onto its own Source type and renders server-side. With
`vault-tree slot-header page-companions vault-colophon quick-actions
sheet-tools` enabled in `campaign.yaml`, the demo vault now renders the
breadcrumb, search palette, live badge, vault tree, page companions,
colophon, quick actions, and sheet tools — every one of them per-viewer
filtered (a guest's tree carries no secret page, title, or path; the owner
and the GM see theirs).

**The funnel bug behind it:** `renderShell` stamped `Slots` onto its own copy
of `PageData` *after* the caller had already built the body component, so the
two mounts living inside `PageBody` (page-actions, sheet-header) rendered
from the stale copy and never appeared — even before T1-D, with plugins
enabled. `renderShell` now takes a `func(PageData) templ.Component` closure
so the body is built from the fully-stamped data; pinned by
`TestRenderShellFillsPageBodyMounts` (which fails on the old shape).

Still open: backlog T1-B/T1-C (future selection mechanisms) and T1 (the
`campaign.yaml` theme-key amend).

## 8. Previewing

```sh
go run ./cmd/app --vault fixtures/demo --addr 127.0.0.1:8080
# open http://localhost:8080/static/themes/preview-grimoire.html
#      http://localhost:8080/static/themes/preview-cartographer.html
```

The previews are static HTML with no backend and no vault: they set their own
`data-theme`/`data-scheme`, load the real `app.css` + theme CSS, and render a
hull that mirrors the shell (topnav, rails, article, sheet card) with all
eight slot mounts labelled. Flip `data-scheme` in the source to `"dark"` or
`"auto"` to see the other scheme.

Themes also preview live, on the real read path (real vault, real
secret filtering, real slot chrome):

```sh
go run ./cmd/app --vault fixtures/demo --theme grimoire --theme-scheme dark
# http://localhost:8080/p/welcome
```

Note the `templ.Attributes` trap that shaped the implementation: templ
renders attribute maps in Go map order, which is **random per request**, so
the `<html>` theme attributes are joined in Go (`web.HTMLOpenTag` and the
template's `htmlOpenTag`) for byte-stable output — pinned by a same-input
double-render assertion in `web/templates/shell_theme_test.go`.
