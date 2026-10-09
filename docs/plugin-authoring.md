# Plugin Authoring

How to extend Yonder without forking it. Read this whole page before
writing a line: the red lines are enforced by `plugin check` and by the
registry, not by convention.

## 1. What a plugin is

A plugin is compiled-in Go (there is no WASM/Lua surface in v1) implementing
`plugins.Plugin`:

```go
type Plugin interface {
    ID() string            // lowercase alnum + dashes, e.g. "random-tables"
    Name() string
    Version() string
    Init(ctx context.Context, reg *Registry) error
    Routes() []web.Route
    Slots() []web.SlotComponent
    CSS() string            // max 20KB, @layer plugins, [data-plugin] scoped
    JS() string             // CodeMirror 6 only; no Node in the binary
    OnEnable(ctx context.Context) error
    OnDisable(ctx context.Context) error
    ConfigSchema() string
    ValidateConfig(config map[string]any) error
    A11y() A11ySpec        // landmarks + labels + live regions
}
```

Features (`plugins.Feature`: `random-tables` first, then
`initiative-tracker`, `vtt-maps`, `character-wizard`, `secrets-advanced`)
are plugins with first-class SSE publishing: `Publish(ctx, FeatureEvent)`
emits onto the broadcast, and the transport drops or redacts per viewer —
features never branch on secrecy themselves.

**Red line (P06, enforced): UI plugins receive secret-filtered input
only.** Every `SlotComponent` must set `SecretFiltered=true`; `Register`
and `plugin check` reject anything else. Renderers take
`SheetInput`/`BlockInput` built only via `NewSheetInput`/`NewBlockInput`
from already-filtered HTML, and must refuse `Filtered=false` fail-closed.
The GM dashboard assembly (`plugins.DashboardSource`) likewise refuses
unmarked input. Client-side hiding is a leak, not a fix — filter
server-side on every path (render, search/snippets, graph, autocomplete,
embeds, SSE, API).

## 2. Routes: registry, never core routing

Core routing is frozen since Phase 0. Register via the frozen
`web.RouteRegistry` (`Method` + `Path` + `Handler` + flags):

- Set `ReadOnly`/`SecretFiltered`/`GMOnly`/`AuthRequired` honestly; the
  registry and serve wiring enforce them.
- Patterns use the stdlib form (`/vtt/{mapID}`, `/vtt/{mapID}/fragment`);
  the first registration per method+pattern wins. Map parametrized routes
  to static prefixes first — never map two routes to one prefix under
  first-wins (it silently drops one).
- A disabled plugin's routes answer a uniform 404 via the plugin gate
  (registry is append-only: gating, not removal, is how a plugin leaves no
  trace). Same for slots: read paths resolve through the enabled-aware
  `SlotsFor` provider, never the raw registry.
- Frozen VTT-adjacent contracts (owned by Lane I2, implemented by Lane K —
  do not move them): map-fragment URL `/vtt/{mapID}/fragment`,
  state-snapshot endpoint `/api/vtt/{mapID}/state`, and the SSE event-name
  catalog (`hello`, `secret-flip`, `dice-roll`, `dice-blind`,
  `initiative-update`, `token-move`, `token-hp`, `fog-update`,
  `encounter-spawn`, `session-recap`).

## 3. Slots + CSS

Frozen slot names: `header-left`, `header-center`, `header-right`,
`sidebar-left`, `sidebar-right`, `footer`, `page-actions`, `sheet-header`.
Declare `{component, priority, show-if}`; the GM can disable slots, and
shell renders are SSE-patchable (morphs must not steal focus).

CSS contract (linted by `CheckCSS`):

- Core design tokens only; plugin CSS lives in `@layer plugins`.
- Every selector scoped under `[data-plugin="<id>"]`; no bare element
  selectors (even scoped ones like `[data-plugin="x"] div`); no
  `!important`. The CI splitter is brace-aware — nested `@layer` blocks
  are fine, global resets are not (one reset war ruins every theme).
- 20 KB cap per plugin stylesheet (P10 budget, enforced in CI).
- Themes are token overrides via `[data-theme]`, not plugin CSS.

## 4. A11y contract (WCAG 2.2 AA target, linted by `CheckA11y`)

Declare landmarks, every control's accessible name, and live regions in
`A11y()`; `plugin check` fails on missing labels/landmarks, and a plugin
with dynamic updates must declare at least one live region (dice: polite;
reveals: assertive). Redaction badges are never color-only (a text-label
requirement, checked as such). Token/dice controls keep 44 px targets,
fog opacity is re-checked per theme, and `prefers-reduced-motion` is
respected. `plugin check` passing is necessary but not sufficient — the
keyboard-only walkthrough covers the rest.

## 5. Enablement

`campaign.yaml` `enabled-plugins` lists registry ids (see the GM guide for
the `enabled-features` vs `enabled-plugins` split). Serve resolves the
list at boot against the registry: unknown ids stay disabled with a
warning (never a crash), and `Enable` runs `OnEnable` + registers
routes/slots once. Config is validated against `ConfigSchema`.

## 6. Dice transport (if your plugin rolls)

Use the shared transport (`plugins.RollEvent`, `ViewerOf`, `RouteBlind`):
results are opaque to the core, envelopes carry `{intent, actor, targets,
tool, context}`, blind routing is per-viewer, and replay serves stored
values with per-viewer re-auth (`crypto/rand` source; maxima 100 dice /
1000 faces; bases may set lower values). Never invent a parallel log.
