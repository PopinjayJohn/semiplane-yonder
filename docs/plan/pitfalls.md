# Pitfalls & Footguns (read before coding)

## Secrets (leaks, not bugs)
- Client-side hiding is a leak. Filter server-side on every path: render, FTS **snippets**,
  graph, autocomplete, backlinks, embeds, SSE events, API, print stylesheet.
- Secret titles/paths are content. Never emit them to guests/other players — redacted, not absent.
- Query-time FTS filter must cover role **and** owner; single index means one missed join leaks.
- `owner` inheritance = nearest ancestor; orphan conflicts default writer + GM. No parent = no widening, ever.
- GM toggles affect future rolls/views only; history pins its modifier list.

## Auth & sessions
- `Secure` cookies never send over plain-HTTP LAN. Conditional `Secure` (HTTPS/proxy only) or table-day login is dead.
- Lockout state lives in SQLite, not memory — restarts must not clear it.
- argon2id 64MB × concurrent login floods = self-DoS. Rate-limit sits in front, always.
- CSRF double-submit on every state-changing route, including SSE-patched forms.

## Vault ↔ DB consistency
- Vault is truth; the *index* DB is disposable. Any search/graph that can't survive `rm *.index.db` is wrong. The *app* DB (users, ownership, clocks, VTT live state) is precious: migrated, backed up, never rebuilt.
- Fog/positions/visibility are ephemeral — fail closed (hidden) on rebuild.
- Watcher needs debounce + self-write tolerance, or saves cause reindex storms.
- Surgical frontmatter edits (preserve comments/order) or Obsidian users revolt.
- Conflict files: index with parent ACL, hide from search, never auto-merge.
- Vault assets serve only through the ACL-checked handler. A raw static mount is a leak.

## Single-binary discipline
- No cgo (kills cross-compile): modernc not mattn, stdlib + `x/image/draw` not `bimg`, hand-rolled evaluator not expr-lib.
- `scs` sqlite store drags mattn/cgo with it. Stdlib sessions behind an interface instead.
- No Node in the binary (CI-only for axe). Tailwind/CDN breaks offline convention basements.
- One writer to SQLite (dedicated single writer connection; reads from pool), WAL + pragmas via DSN (`_pragma=`), checkpoint on shutdown. `MaxOpenConns` is per-pool, not per-operation.
- Migrations embedded + `user_version`; reindex via temp-DB rename, never in place.

## Ruleset traps
- Core transports opaque results; evaluator is a dumb engine, bases supply the programs. Unknown functions fail `rules lint`, never silently ignore.
- Intent names validated per active base, never a global list. New base = new catalog.
- Homebrew `warns` on likely non-SRD text; it never blocks — GM owns table liability.

## UI / a11y
- SSE morphs must not steal focus; live regions polite (dice) / assertive (reveals); redactions announced as text.
- Badges never color-only; fog opacity re-checked per theme; 44px touch targets on tokens/dice.
- Plugin CSS namespaced (`[data-plugin]`, `@layer plugins`), no bare elements, 20KB cap — one global reset war ruins every theme.

## Ops / Windows
- Flags > env > defaults; session key 0600, never logged; version stamped via ldflags with template compat check.
- Windows: separators, case-insensitivity, reserved names (`CON`, `:`), long paths, firewall note for `:8080`.
- Uploads: MIME-sniff (not extension), stream to temp, EXIF strip, SVG blocklist, 5/10MB caps.

## Phase 1 gate additions
- goldmark fragments `[` into its own Text node, so `> [!secret]` never sits in one node → match markers against full paragraph text, strip across leading nodes, abort on structured inlines (Lane A).
- `Block.Lines()` panics on inline nodes (Link/Image/AutoLink) → resolve positions via nearest ancestor block, never call `Lines()` on inlines (Lane A).
- Go map iteration randomizes validation order → sort frontmatter keys before validating so quarantine reasons stay deterministic for goldens (Lane A).
- `argon2.Key` is Argon2i, not Argon2id — same PHC label, different output → both seed and verify sides must use `argon2.IDKey`; cross-verify E1 seed under C at the gate (E1/C).
- Go `flag` stops parsing at the first positional → subcommands must bind `--vault/--data-dir` themselves (E1).
- FTS5 `ordinal UNINDEXED` columns silently break `MATCH` if queried → MATCH only indexed columns, carry ordinals as payload (Lane B).
- `http.DetectContentType` returns `text/plain` for bare `<svg` → SVG blocklist needs its own prefix check, not the sniffer (Lane E2).
- Bare `gofmt -l .` in a check target never fails → gate on `test -z "$(gofmt -l .)"` (Lane E2).
- `:memory:` SQLite does not survive `database/sql` pooling → file-backed temp DBs in tests (Lane E2).
- `make check`'s `git diff --exit-code` is worktree-global; shared lane checkouts fail from siblings' dirt → scope cleanliness claims to `git diff <base>..<lane> --stat`, and always `git branch --show-current` before commit; prefer isolated worktrees over `checkout` in shared trees (D1/A/B/E1/E2).
- Migration runner tests must follow the schema owner's current SQL (B owns contents, E2 runs them) → E2's FTS sanity rewritten for chunk-granular `blocks_fts` + `path_fold`, no triggers (gate).
- Two DDL sources on one DB silently diverge (`CREATE TABLE IF NOT EXISTS` no-ops on the other's tables, then index/column refs fail at runtime) → interim `Ensure*Schema` must be covered by a migrate-then-Ensure test on a single DB, and serve-after-init must be a CI smoke, not just unit-tested (G2 rollback).
- `wireHandlers` computed the index path as `Dir(vault)/Base.index.db` while every other command uses the `dataPaths` sibling `-data/` dir → serve read an empty index and 404'd every page; single path source (G2 amend).
- A mux catch-all that re-serves its own mux recurses infinitely → stack overflow on the first request; catch-alls must never re-serve their own mux (G2 amend).
- Exact-string route matching silently drops parameterized routes → match prefix patterns by subtree (G2 amend).
- Stamping `PRAGMA user_version=N` without that era's tables makes the next migration fail → build the era, don't just stamp (G2 amend).
- Shell JSON assertions must grep quoted `"visible":false`, never bare `visible:false` — the latter silently never matches (G2 amend).
- Fixed past timestamps in migration seeds silently read as expired with `strftime('%s','now')` comparisons → seed time-relative wall-clock values (G2 amend).
- YAML allows a block sequence at the *same* indent as its parent mapping key (`key:\n- item`) → a strict `child.indent > key.indent` check misquarantines valid Obsidian output; branch on `== indent` + `- ` prefix instead of relaxing indentation globally (Lane A amend).
- `pkill -f` with a pattern matching its own command line SIGTERMs the invoking shell (hit twice during live-demo serve/kill cycles) → kill by exact child PID or port-derived PID, never by script-text pattern.

## Phase 3 gate additions (lanes H1/H2/I1/I2, appended by the G3 integrator)
- Naive CSS selector splitting breaks inside `@layer plugins { ... }` blocks (a split on `}` orphans nested rules, so scoped selectors read as bare elements and vice versa) → brace-aware splitter that descends into at-rule blocks and takes the text since the last `{`/`}` as the selector (I2 `plugin check`).
- The dice-log replay viewer key rides inside `envelope_json` (`"viewer"`, recomputed via `viewerKeyOf` at read) — not a new `dice_logs` column (schema frozen; a column would need a Lane B migration) → re-auth compares against the envelope-derived hash (H2 transport).
- Index sheet copies go stale under rapid surgical flows (watcher lag) and lie after owner transfers → `/me` treats index rows as existence hints only and re-reads vault truth + `secrets.Filter` per request (vault-truth funnel); stale rows 404 fail-closed before any value renders (I1, pinned by gate regression tests).
- YAML block sequences may sit at the *same* indent as their parent key (`enabled-features:\n- id`, the Obsidian shape) → a strict `child.indent > key.indent` check drops valid items; branch list-item parsing on `== indent` + `- ` prefix (I1 campaign parser; H1's `rewriteListKey` handles the same shape).
- `http.MaxBytesReader` needs a live `ResponseWriter` and aborts the connection past the cap (nil writer panics; overflow kills clean JSON errors) → bound API JSON decodes with `io.LimitReader` and return 400s from the handler instead (I1 `FieldsPatch`).
- JSON/PATCH API clients cannot send form-encoded CSRF fields → accept the `X-CSRF-Token` header fallback in `CSRFTokenFromRequest` (form field first) and send it from fetch/SSE clients (I1, Lane C owns the token mint).
- Registry ownership checks matching only exact/prefix patterns silently drop mid-pattern wildcard routes at the outer mux → map parametrized routes to static prefixes first; never map two routes to one prefix under first-wins (Lane K).
- Migration fixtures must build the FULL era shape, not just the tables the current migration touches — a partial 0001 fixture sailed through 0002 and broke 0003 (Lane K).

## Phase 4 gate additions (Lane L harden + docs)
- Sheet/wizard/write HTML funneled through `writeHTML` with no `Cache-Control` while the read path had a full no-store matrix → per-viewer pages one shared proxy away from cross-viewer leaks; set `private, no-store` centrally in `writeHTML` (+ the sheet JSON branch), pinned by `cache_headers_test` (Lane L).
- `tools/axe/check.sh` probes `/` and `/notes`, which never serve 200 (routes are `/p/…`, and the script never builds the p01 index) → the gate SKIPs on healthy servers; probe a real page (`/p/welcome.md`) after a `reindex`, and serve with an explicit `--data-dir` outside the tree (default sibling dir litters `fixtures/p01-data/` into the checkout) (Lane L, filed).
- Post-processing wikilinks before expanding `![[embed]]` leaves transclusion-transported anchors raw (unredacted secret links with relative hrefs, one live leak) and single-occurrence string replacement alias-corrupts duplicate links → expand embeds first, then secret-check + rewrite every anchor in one regex sweep keyed off each anchor's own `data-target` (B0, pinned by `TestEmbedTransportedSecretLinkRedacted`).
- `> [!unsupported]` is NOT preview-"unsupported": unknown callouts parse to block type `callout` (blockquote); preview `prev-unsupported` comes only from `UnsupportedNode` (e.g. ` ```dataview `) — assert the dataview shape in preview tests, not the callout shape (B5).
- `go:embed` patterns reject `..` — a package cannot embed files outside its own dir → keep a verbatim inline copy in Go pinned by a file-equality test; edit the source file first, then copy (B4).
- Test assertions on rendered sheet HTML must target markup (`<div class="chip-grid"`), not substrings — the inline `<style>` block legitimately contains class names (B4).
- Wikilinks to `characters/<pc>/index.md` pages MUST use the explicit `[[characters/<pc>/index|Alias]]` form: `resolveTarget` tries only `base`/`base.md`/`base.markdown`, so bare `[[characters/mira]]` dangles — renders unredacted with alias intact for guests and 404s for everyone (B7).
- When asserting server-rendered output that JS assembles at runtime (e.g. `"/events?map=" + encodeURIComponent(id)`), assert the static prefix + `data-map` attribute, not the concatenated literal (B2).
- `shell.templ`'s SSE bootstrap echoes `?as=` from `location.search` client-side: server-inert when the demo tier is retired, but `?as=`-substring assertions on bodies false-positive — gate the echo on `DemoAuth` if sessions-only deployments must not emit it (?as= wave).
- `path.Dir` returns `"."` at vault root while bundle rules normalize asset dirs to `""` — root pages silently never count as same-directory; only surfaces after fail-closed tightening (normalize `pdir == "."`; rulings trio).
- `templ` does not evaluate `if` directives inside `<script>` blocks (emits literal JS) → branch whole `<script>` elements at HTML level; keep the shipped branch byte-identical (rulings templates).
- `{...}` in template text (flow-style YAML) parses as a templ expression → use block-style YAML in template-visible examples (rulings templates).

## W1 additions (graph link-target normalization)
- Index-shape changeovers need the backfill BEFORE the purge sweep in the same Rescan: target-side purges (`links.target = ?` with page paths) miss legacy raw rows on the first post-changeover run, leaving dangling edges behind → order Rescan as walk → backfill → purge so one pass converges (W1, pinned by `TestRescanBackfillsRawLinkTargets`).

## Engine pack-pin additions
- Overlay switches must move the version/hash pins with the overlay id: a `campaign.yaml` pinning `overlay-version: "2024"` fails closed (`ErrPinMismatch`) after a bare `5e-2024`→`5e-2014` id swap — update `overlay-version`/`overlay-hash` in the same edit (pinned by `TestGateM2OverlaySwitchLive`).

## W2 additions (preview-write rule)
- Under `DemoAuth=true`, `applySessionPreview` early-returns for session viewers, so `?preview_as=` on a request is inert in default-tier tests → pin write-path preview tests by injecting `PreviewAs` directly on the context viewer (`withViewer`), never via query param (W2).

## W3 additions (static core/vendor serving)
- `ownedByRegistry` + the registry `/` catch-all silently swallows every ops path (even `/static/print.css` 404'd live) → carve public static out *before* the registry check (`isPublicStatic`); keep unknown paths flowing to the styled 404 (W3).
- Shared-tree branch hazard: a sibling `checkout` moved HEAD mid-task and a commit landed on the wrong branch (W3's commit shipped inside PR #25) → recovered via `git branch -f <branch> <sha>` (no worktree touch), fast-forward push, then `reset` + revert only own paths; prefer isolated worktrees for parallel agents (W3).

## Statblock additions (S1/S2: statblock-first UI)
- The YAML subset has no `[[...]]` scalar form: an unquoted `image: [[pic.png|Alt]]` arrives as nested one-element flow lists (`[]any{[]any{"pic.png|Alt"}}`), not a string → unwrap single-item lists recursively, then split a bare `|` for the alias; quoted values keep their brackets for the normal bracket parse.
- Unquoted `+2` is the integer `2` by the time statblock code sees it (correct YAML, wrong D&D display: "DEX 2") → re-sign numeric save/skill mods at render (`sbSigned`); desc strings are untouched.

## Admonition additions (S3: callout/admonition polish)
- The secret scrub matches the outer `<div class="callout callout-secret">` + `data-fold="expand|collapse"` vocabulary only: fold UI (`<details>/<summary>`) must nest INSIDE that div, never replace it — a `<details>`-as-outer refactor silently disables scrubbing (verified with a throwaway `secrets.Filter` test, then removed to stay in-package).
- Stripping the callout title from the first paragraph must pre-scan before mutating segments: a structured inline (wikilink) inside the title span aborts the strip (`TitleInBody`), otherwise the link edge is destroyed and FTS/graph lose it.
- `ad-` bodies indexed via sub-`Parse` must OR the enclosing secret flag into merged links/embeds and exclude inner-hidden text from open chunks (emitted as a separate secret block instead) — merging raw sub-block contents under `Secret=false` leaks hidden text into FTS snippets.

## Theme additions (T1: Grimoire + Cartographer)
- Unlayered CSS outranks every `@layer`: the shell's inline `<style>` (unlayered) would have beaten any layered theme token override, so themes were inert by construction → shell CSS moved into `@layer core` (web/static/core/app.css) and every theme file opens with `@layer core, plugins, overrides;` — without that statement, plugin stylesheets (delivered later, `@layer plugins`) could sort themselves AHEAD of themes, since layer order follows first appearance of each name.
- Inline `style="border-color:#b45309;color:#b45309"` badge attributes beat every stylesheet rule → themes (and any future restyle) can never reach them; moved to `.badge.gm` / `.badge.failed` / `.badge.locked` classes on tokens (identical default colors).
- `go:embed` silently skips files whose names begin with `_` or `.` → a `_shot-*.html` mirror served a 404 while the `web/static` source looked correct; after adding anything to an embedded dir, confirm it is actually served (the equality pin only covers listed pairs).
- Core component tokens have their own theme hooks with a *different* id convention (`statblock.css`/callouts re-point `--sb-*`/`--callout-*` under `[data-theme="dark"]`) → a new theme with any other id silently keeps light-scheme statblocks/callouts on a dark page unless it re-points those tokens per scheme (now pinned by `TestThemeStylesheetsCoverAllSlots`).
- `--line` was fetching double duty as text color in `.login-form .hint` (#888 on white = 3.5:1, an AA failure) → added `--hint` (default #6b7280, 4.8:1) and left `--line` for rules; themes re-point both.
- One token cannot serve both decorative rules and control boundaries: WCAG 1.4.11's 3:1 applies to boundaries you must identify/operate (buttons, the drawer), while dividers/rules are free to be any flavor — a themed `--line` at 2.2:1 therefore either fails axe on buttons or flattens the theme's gold rules → split `--line` (decorative) from `--control-line` (buttons + drawer, core default `var(--line)`), so a theme darkens only what the standard actually covers (Grimoire second pass: hint 3.7→4.7:1, control boundary 2.2→3.9:1, gold rules untouched).
- `templ.Attributes` renders in Go map order — random per request — so attributes built from a map make every page's HTML differ (byte-unstable output, flaky goldens) → join conditional attributes in Go (`web.HTMLOpenTag`; the template's own `htmlOpenTag`/`htmlCloseTag` pair is emitted raw to keep templ balanced).
- templ passes `<style>`/`<script>` element content through **literally**: an `@templ.Raw(...)` placed inside those elements renders as the text "@templ.Raw(...)", not the value → build the whole element in Go and emit only that raw (`PageData.pluginStyleBlock`/`pluginScriptBlock`).
- Campaign-level config failure is swallowed silently: `campaign.Load` rejects unknown keys (closed set) and `serve` does `if err == nil` → a hand-added key (e.g. a theme key before its amend) blanks the campaign name, landing page, and plugin enablement with no visible error → when adding keys, amend the validator in the same change, or the deployment degrades invisibly.
- Stamping a render funnel too late is invisible until a second template needs the data: `renderShell` appended slot mounts to its own `PageData` copy while the caller had already built the body component, so the mounts living inside `PageBody` (page-actions, sheet-header) rendered from the stale copy and never appeared — with plugins enabled, no less → pass the body as a `func(PageData) templ.Component` closure so every consumer (Shell and PageBody) sees the fully-stamped data (pinned by TestRenderShellFillsPageBodyMounts).
- Core fallback content must stand aside when a plugin fills its slot, not render alongside it: the page-actions slot showed Search/Edit twice (core buttons + quick-actions) and the sidebar showed the core nav list under the vault tree → the template gates each fallback on `slotFilled(data.Slots, slot)`, and the one core-only affordance an enabled plugin would otherwise swallow (`+ New subpage`) is threaded through `SlotShellData.NewSubpageURL` so the plugin renders it.
- Both sidebars lived in ONE `<details>` directly under `<main>` with `.layout { display: block }` → the rails stacked below the page like a second footer at every width, and no HTML-structure test could catch it (the DOM looked fine; the CSS did the stacking) → each rail is its own `<section class="rail">` sibling of main, DOM order stays main-first, and a CSS grid with named areas (`"main" "left" "right"`) places the rails beside the page (page+companions ≥900px, tree·page·companions ≥1200px, sticky with independent scroll) — pin BOTH halves (shell_layout_test.go for the DOM, TestCoreAppCSSLayoutGrid for the served CSS), since either alone regresses invisibly (T1-E).
- A slot fragment mounts INSIDE the shell's own landmark, so a fragment that renders its own landmark element nests it: the colophon's `<footer>` landed inside `footer.colophon` → two contentinfo landmarks, axe `landmark-no-duplicate-contentinfo` + `landmark-contentinfo-is-top-level` (CI axe gate, invisible to `make check` because axe is Node/CI-only) → a fragment FILLS the host landmark, it never renders its own; the colophon now renders a `<div>` (pinned by `TestShellFooterSlotKeepsOneContentinfo` in web/templates + the fragment's no-`<footer>` assertion).
- Label in Name (WCAG 2.5.3) on disclosure summaries: `aria-label="Toggle folder maps"` over a visible "maps 1" fails axe 4.14's `label-content-name-mismatch` (4.13 passed it — the rule set moves with the pin, so re-run the axe gate locally before pushing UI changes) → the count rides in the accessible name ("Toggle folder maps, 1 item") and the visual badge is `aria-hidden` (pinned in vaulttree_test.go).

## A-VTT2 additions (Pixi vendor pin, UVTT import, door-toggle)

- jsdelivr's `/v1/packages/npm/<pkg>` version list is NOT semver-sorted (the tail held `8.0.0-beta.0` while 8.22.0 sat mid-list) → sort client-side with a numeric key and filter to stable `8\.\d+\.\d+` before picking "newest 8.x", or the pin silently regresses (A-VTT2 pixi pin, spike D7: never float `@8` in VERSIONS).
- `go:embed` cannot reach outside the package dir, so a new first-party static subtree needs BOTH copies (web/static/vtt/ source + verbatim cmd/app/static/vtt/ embed), the `static/vtt` line in the serve embed directive, the `/static/vtt/` ops-mux mount, the `isPublicStatic` carve-out, AND the `w3StaticPairs` equality pin — miss the carve-out and the registry's `/` catch-all 404s the loader while the file looks served (W3 pattern, A-VTT2).
- A surgical frontmatter merge that re-emits the block header key duplicates it: `lines[:blockStart]` already ends with the existing `vtt-map:` line, so `renderBlock` must emit ONLY child entries — the duplicated key read back as two top-level keys and the second silently won (A-VTT2 import sidecar merge, pinned by TestApplyImportRoundTrip).
- Round-tripping an import through the real sidecar parser exposes off-grid hand edits: a hand-written `tokens:` default outside the NEW grid drops field-by-field (x/cols check), which looks like "the import ate my tokens" — import fixtures must use in-grid coordinates, and the merge only PRESERVES keys, never validates them (A-VTT2).
- The auth middleware verifies a SIGNED session cookie (`id|user|expiry|mac`, ParseAndVerifySessionCookie) but the write-path CSRF checks accept the raw id (they split on `|` and look the row up) → an end-to-end write test can pass with a raw-id cookie ONLY because `?as=` demo identity injects the viewer; a GM-only route under a raw cookie 401s at the viewer check, so mint real signed cookies (`auth.SignSessionCookie` + `auth.LoadSessionKeys`) for gate tests (A-VTT2 import gate).
- `pkill -f "<port>"` in a compound shell command SIGTERMs the invoking shell itself (the command line contains the pattern) — hit AGAIN this session while restarting a smoke server → kill by port-derived child PID (`ss -tlnp | grep :PORT | grep -oP 'pid=\K[0-9]+'`), never by script-text pattern (second entry; the first was in P08).
- The edit tool joins lines when old/new strings end at a line boundary → include the next line in both strings and run `gofmt -l` after every edit (session-local, hit repeatedly on serve.go/render.go/main.go heredoc-ish blocks).
- A dead-at-parse inline `<script>` fails SILENTLY in Go-side tests: the VTT client's fog-form fetch was missing one `)` at the MVP, a SyntaxError that killed the whole IIFE (SSE, moves, dice, fog) on every map — no Go test runs JS, and axe's rules do not flag console errors → browser-load every page once after touching emitted JS, and pin a bracket-balance check over the emitted scripts (`jsBalanced` in render_pixi_test.go, verified against the known-bad shape). Also note the naive scanner reads the closing `\/` of `replace(/\/api\/vtt\//, …)` as a `//` comment start: a `/` after `( , = : [ ! & | ? { ;` opens a regex literal, not a comment.
- Rewriting a URL path AFTER a query is attached silently no-ops: `stateURL.replace(/\/state$/, "/fragment")` never matches `/api/vtt/x/state?as=gm` (the `$` anchor sits past the query), so every SSE resync on the demo tier 404'd on the state route → split path/query, rewrite the path, re-attach the query (pinned in render_pixi_test.go).
- The `/assets/{path}` handler is a vault-root NAMESPACE (`/assets/<vault-relative>` → vault `<vault-relative>`), so a vault-relative sidecar value like `assets/maps/arena.png` legitimately serves as `/assets/assets/maps/arena.png` — the doubled segment is the contract, not a bug; conversely `maps/arena.png` (image beside the sidecar) serves as `/assets/maps/arena.png`. Both shapes are valid sidecar values; do not "fix" the URL shape.
- The asset ACL's same-directory-page ruling ("a directory holding zero pages serves nothing to anyone") fails a PURE-ASSET bundle dir closed to invisible for the GM too — imported UVTT art under `assets/maps/` was unloadable until `assetVisible` grew the amend's bundle ruling (`assets/maps/<stem>.<ext>` inherits `maps/<stem>.md`; missing sidecar fails closed). Any new asset-only directory needs an explicit ACL ruling or it is a black hole for everyone.
- A sidecar `background:` whose stem does not match its sidecar (`assets/maps/ford.png` referenced by `maps/arena.md`) is invisible under the bundle ruling — shared art must be named per-map (demo: ford.png renamed arena.png) or live in the sidecar's own directory.

## R3 additions (OOB-light glow retention)

- Sidecar floats serialize at 4 decimals (`formatFloat` rounds): a round-trip test asserting exact struct equality on 6-decimal UVTT coords (e.g. Ground-Floor x=-0.570313 → "-0.5703") fails on the tail — compare round-tripped geometry within 1e-4, not bit-exact (≤4dp goldens are unaffected).
- Retention-rule changes flip old fixtures silently: the pre-R3 OOB test light at (5,5) r2 on a 4x4 grid *passes* the new glow test (disc distance 1.41 ≤ 2) and would have inverted the test's meaning — keep drop-case coordinates far from the decision boundary ((9,9) r2) and pin near-boundary retention ((4.5,2) r1, Ground-Floor #25) as separate cases.

## A-VTT2 canvas-mount additions (browser-only defects)
- A `<script>`-loaded ESM namespace does NOT leak into sibling module functions: pixi-board.js's scene helpers referenced `PIXI` (bootPixi's parameter) and every mount threw ReferenceError into the DOM fallback — Go tests never noticed because the fallback is invisible server-side → bind the namespace at module scope for single-mount loaders, and browser-drive the mount path once before shipping it.
- Reading `app.canvas` AFTER `app.destroy(true)` throws (`canvas` is a getter over `renderer.canvas`, which destroy nulls), so a fail-closed cleanup path can itself crash — capture the node BEFORE destroying.
- "Empty rect list" has two opposite meanings in the fog codec: a revealed board legitimately has ZERO hidden rects, while a missing/corrupt mask must fail closed to FULL cover — an `if (!drew) fullCover` fallback blacked out every revealed map on canvas.
- An SSE morph (`root.innerHTML = html`) silently drops anything appended inside the fragment — the pixi canvas lived inside `[data-board]` and vanished on the first board event → publish the live view at page scope (`window.__pixiView`) and re-attach the same node in applyMorph; also re-resolve the target element AFTER dynamic imports (the hello resync replaces it mid-load).
- `aria-hidden="true"` on a subtree that still contains focusable controls takes the ONLY write path out of the a11y tree (axe's aria-hidden-focus rule set moves with the pin, so it may not fire today) → for a canvas enhancement, make the canvas the decorative layer (aria-hidden + `pointer-events:none`) and leave the DOM board fully operable.
- WCAG 2.5.3 (label-content-name-mismatch) counts aria-hidden spans' text as the VISIBLE label: a token button whose visible "B11/11" is not a substring of its aria-label violates — latent since the MVP because the demo never had live tokens to render → put the face in CSS-generated content (`--face`/`--hp` custom properties) so the button carries no DOM text, and never nest a link inside a button (nested-interactive): render it as a sibling and guard the board click handler against `a` clicks.
- A board with no background art collapses to zero height (the img was the only intrinsic size) → give art-less boards their grid's `aspect-ratio` inline so grid/tokens/fog still render; link-only UVTT imports depend on this.
- Import auto-create needs its own ACL stamp: a GM importing into a FRESH map id writes a brand-new sidecar, and the write-path's owner/secret stamping (which runs for player creates) never sees it → the importer stamps `secret: true` itself when `maps.md`/`maps/index.md` is secret, or the import would open a page inside a hidden folder (A-VTT2, pinned by TestImportRouteFreshSidecarSecretStamp). Distinguish "sidecar row missing" (new map, allowed) from "index read error" (500) — collapsing them lets a transient DB error overwrite calibration.
- JPEG flattening alpha to black is a real demo-data choice: the Tomb source art is a PNG/WebP with transparent surroundings; the 32MP write-path cap (D2) rejects the full-size png/jpeg, and the 2MB webp is too big to vendor, so the demo ships the pre-downscaled 1280px jpg — transparent surroundings read as black on the table. Fine for a demo, wrong for a GM's own map: prefer webp (stored as-is, dims-capped only) or a ≤32MP png.
