# R5: Sticky renderer preference — decision note

Decides amend A-VTT2 §9 **R5** ("Sticky renderer preference (shared-tablet
stickiness unresolved — cf. T1-C)") in light of decision **D9**
(`?renderer=pixi|dom` per-load only, no sticky preference). This note is the
deliverable; no code, no migration, no route, and no key-set change ships
with it. Each option below lists the exact follow-up amend it would need.

## 0. Shipped baseline (verified against the tree, not the spike)

- `internal/vtt/render.go:340-383` (`pixiLoaderJS`): reads
  `new URLSearchParams(location.search)` on every load and returns before
  importing anything unless `?renderer=pixi` is present. DOM is the default
  path; zero vendor bytes fetched otherwise.
- The DOM board is the operable truth **in the shipped code, not just in
  prose**: the canvas is mounted as a decorative overlay —
  `app.canvas.setAttribute("aria-hidden", "true")` on the canvas element
  only (`web/static/vtt/pixi-board.js:102`), CSS
  `[data-pixi-canvas]{...pointer-events:none}` (`render.go:405`), token
  buttons / listbox / forms never removed, only visually covered. Mount
  announces `"Canvas board active. Keyboard controls remain available."`
  through the existing polite live region; every failure path announces and
  keeps the SSR'd DOM board.
- Note the correction from spike to ship: `docs/plan/vtt-pixi-spike.md` §2/§6
  sketched `board.setAttribute("aria-hidden", "true")` (board-level hiding),
  which would have taken the only write path out of the a11y tree. The
  shipped code deliberately does canvas-level hiding instead (pitfalls:
  "make the canvas the decorative layer … and leave the DOM board fully
  operable"). Any claim below about "the DOM board must remain operable in
  every mode" holds today and is pinned by the axe gate (`/vtt/{mapID}` with
  `?renderer=dom|pixi`: canvas mounted AND all DOM controls still operable).
- Themes precedent: resolution order is pinned as **campaign default →
  viewer override (future) → OS** (`docs/plan/themes.md` §7); **T1-B**
  (per-viewer app-DB theme pref, needs a migration — the app DB is precious:
  migrate, never rebuild) and **T1-C** (theme cookie, works for guests) are
  both deferred in `docs/plan/backlog.md`, T1-C explicitly because
  "stickiness is a bug on a shared table tablet". R5 is the same objection
  wearing a renderer hat.

## 1. Options

### (a) Cookie (`renderer`, T1-C pattern)

- Mechanism: `renderer=pixi|dom` cookie + a tiny picker endpoint (form +
  double-submit CSRF, like every other state-changing route); loader
  precedence becomes `?renderer=` > cookie > DOM default.
- State lives: the browser's cookie jar — per-browser, per-profile, no login
  required, guests included.
- What breaks on a shared tablet: everything T1-C already named. One
  browser = one sticky value, so player A's choice silently becomes the
  table's until someone notices and flips it back — last-writer-wins with no
  audit trail. Needs the same picker UI as (b) for a smaller payoff, and
  inherits the unanswered cookie questions (guest identity is `?as=` under
  DemoAuth; `Secure`-cookies-never-send-over-plain-HTTP-LAN per pitfalls, so
  the cookie must be set without `Secure` on the LAN the tablet actually
  uses).

### (b) Per-viewer app-DB preference (T1-B pattern)

- Mechanism: per-user `renderer` override resolved above the campaign
  default; precedence `?renderer=` > viewer pref > campaign default > DOM.
- State lives: the app DB (`<name>.app.db` — precious, migrated stream,
  never rebuilt).
- What breaks on a shared tablet: less than (a) *if* every viewer holds
  their own login on their own device — but the shared-tablet scenario is
  exactly one browser with (at best) one session, so the "per-viewer" value
  is still one person's choice wearing a user id. It additionally costs a
  login/logout round-trip mid-session to switch viewers, session-cookie
  sharing confusion on the communal device, and `preview_as` read-only
  interplay. Needs the heaviest amend (see §3) for a table that, by
  hypothesis, does not split logins.

### (c) Browser-local (`localStorage`)

- Mechanism: `localStorage.renderer` read by the loader before/instead of
  the query param; toggled by client-side UI only. No server change, no
  migration, no cookie header.
- State lives: the browser profile's local storage — per-browser like (a)
  but invisible to the server.
- What breaks on a shared tablet: the same silent-table-takeover as (a),
  minus even the server's ability to see, document, or clear it. SSR cannot
  read it, so first paint is always DOM followed by a flip to canvas —
  layout shift plus a focus/announce discontinuity on every load. No CSRF or
  auth surface, but also no funnel, no resolution order, no GM visibility.
  Strictly worse than (a) for a shared device: stickiness with no handle.

### (d) Campaign-level default in `campaign.yaml` (append-only key)

- Mechanism: a GM-owned default (name TBD in the amend, e.g.
  `vtt-renderer: dom|pixi`, fail closed to DOM on unknown values) resolved
  through the same funnel the theme flags already thread (`wireHandlers` →
  `vtt.Handlers` → `PageInput`, mirroring `Theme`/`Scheme` in
  `render.go:276-287`); per-load `?renderer=` still overrides it.
- State lives: the vault (`campaign.yaml`), versioned, reviewable, synced
  with the campaign like every other table decision.
- What breaks on a shared tablet: the least of the sticky options, because
  the choice is table-explicit, not silent — the GM sets it for the table,
  anyone can read it in the repo, and a dissenter overrides per-load with a
  query param. Failure mode is a visible default one person dislikes (fix:
  GM edits one key, or the dissenter bookmarks their own `?renderer=` URL),
  not a mystery flip. Still one choice for all viewers, so it does not serve
  split tables — but split tables are not the observed case (see §3).

### (e) Stay per-load only (today's D9 behavior)

- Mechanism: nothing stored anywhere; `?renderer=pixi|dom` per navigation.
- State lives: nowhere — the URL is the whole preference.
- What breaks on a shared tablet: nothing. The GM bookmarks (or prints a
  table QR code for) the preferred URL per device; a player who wants the
  other renderer edits the param. No silent carry-over between sitters, no
  picker UI, no migration, no cookie questions, no SSR/first-paint flip.

## 2. Shared-tablet analysis

Who is harmed by stickiness: the *next* person to sit at the shared browser,
who inherits a rendering mode they did not choose with no visible indicator
of who set it or how to change it back — plus the GM, who fields the "the
map looks different" report mid-session. On a single-user device stickiness
is convenience; on the table tablet it is one person's choice silently
becoming the table's. That is the identical objection that deferred T1-C,
and nothing about renderers weakens it: the tablet does not know who is
looking at it.

What the failure looks like mid-session: concretely, player A enables sticky
Pixi on the table tablet; player B (keyboard-only, or on a low-vision setup,
or just used to the DOM board) sits down, loads the map, and gets the canvas
overlay — visuals change, and the status line announces the canvas path. The
recovery is a toggle they may not know exists, during game time.

What the a11y consequences are: bounded, by construction — and this is
verified, not asserted. Because the shipped code keeps the canvas decorative
(canvas-level `aria-hidden`, `pointer-events:none`, DOM controls never
removed; §0), a sticky flip changes the *visual* layer, not the operable
one: the listbox keyboard path, the text-coordinate mover, focus retention
on SSE morphs, and the polite/assertive live regions survive in both modes.
So the harm is confusion and visual churn (including a `prefers-reduced-motion`
path the user did not ask for), not loss of operability — which cuts both
ways: it lowers the stakes of a wrong sticky value, and it therefore lowers
the value of building sticky machinery to manage it. (Had the spike's
board-level `aria-hidden` shipped, stickiness would be a safety issue rather
than an annoyance; it did not ship, per pitfalls.)

## 3. Recommendation: stay per-load (e)

A sticky renderer preference buys convenience only where the device already
identifies the viewer (own login, own browser), and the table tablet is
exactly where it does not. Every sticky mechanism re-litigates T1-C's
objection with a smaller payoff than themes (a theme is seen on every page;
the renderer matters on one), while the per-load param already composes
into the cheapest stickiness that matters: a bookmark, a home-screen icon,
or a printed QR code per device. If tables ever actually split — separate
devices, separate logins, persistent per-viewer demand — the correct
ordering mirrors themes (campaign default → viewer override, with `?renderer=`
always winning), which means (d) before (b), and (a)/(c) never: browser-scoped
state that cannot tell viewers apart is the wrong layer for a per-viewer wish.

Follow-up amend each option would need:

- (a) Cookie: cookie name/value/max-age + `SameSite`/non-`Secure`-on-LAN
  ruling, picker route + form + CSRF, resolution order (`?renderer=` >
  cookie > default), guest/`?as=` interaction note, tests.
- (b) Per-viewer app-DB pref: migration `0005_*` in
  `internal/store/migrations/app/` (next after `0004_vtt_doors.sql`;
  migrated stream, never rebuild), settings route + form + CSRF, resolution
  order + GM "who sees what" dashboard note, tests. (Best filed jointly with
  T1-B if per-viewer settings ever land — one settings surface, not two.)
- (c) Browser-local: loader precedence rule + client-side toggle + first-paint
  flip analysis; no server amend, but needs an a11y review of the
  paint-then-flip and a written reason it is not just (a) with fewer handles.
- (d) Campaign default: append-only `campaign.yaml` key (closed set — the
  validator must change in the same amend or the campaign silently blanks
  per the pitfalls entry), `ResolveRenderer`-style funnel threaded through
  `wireHandlers` to the VTT page like `Theme`/`Scheme`, `?renderer=`
  per-load override pinned, fail-closed-to-DOM tests.
- (e) Per-load: no amend. Document the bookmark/QR pattern where GMs look.

What would justify revisiting (the "only if tables actually split" test
T1-B used): observed evidence, not hypothetical — e.g. multiple tables
running separate devices with separate logins asking for persistent
renderer choice, or GM demand for a campaign-wide default (which justifies
(d) first, alone). A single shared-tablet complaint about re-adding the
query param does not meet the bar; that complaint is answered with a
bookmark.
