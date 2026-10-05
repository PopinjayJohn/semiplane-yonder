# TTRPG Wiki / Campaign Manager / VTT — Overview Plan

## 1. Vision
System-agnostic Wiki, Campaign Manager & VTT. Single executable (Linux/macOS/Windows).
Lean core + compiled-in plugins. Obsidian-compatible markdown vault as source of truth.
Order: Wiki -> Campaign Manager -> VTT (VTT non-negotiable but last).

Each slice (Wiki, CM, VTT) must be usable standalone with users for testing. A11y target: WCAG 2.2 AA (see P06 contract, P10 tests).

## 2. Stack (locked)
- Go + templ (SSR) + Datastar (SSE) + SQLite (index, WAL)
- Vault filesystem = truth, SQLite = rebuilt index/cache
- No Node/SPA/Postgres/Redis/Docker required
- CSS locked: vanilla + tokens + layers. Core owns `:root`/`[data-theme]` tokens; plugins scope under `[data-plugin]` in `@layer plugins`. No Pico/Tailwind in binary (Tailwind allowed as P00 throwaway only).
- Editor JS: CodeMirror 6 only
- Markdown: goldmark + goldmark-obsidian + custom [!secret] -/+ extension + YAML frontmatter
- FS watch: fsnotify, search: FTS5, embed: go:embed, release: goreleaser
- SQLite driver locked: modernc.org/sqlite (pure Go, easy cross-compile). Revisit only on measured slowness.
- Realtime: Datastar SSE + fetch. No WebSockets in v1 — if map drag latency fails, degrade to click-to-move + throttle (per P08). Publish(state-diff) stays transport-agnostic.

## 3. Decisions locked
1. **Users in Wiki:** Wiki includes auth for writes + secret reads. Test: guest reads non-secret, blocked on others' secret pages (owners read their own). No `public` flag in v1.
2. **Hosting:** GM dedicated server + GM laptop LAN. TLS/backups out of scope. Binary runs `:8080`, flags for vault dir + data dir (two SQLite files). One process serves one vault/campaign (multi-campaign = multiple processes/ports). Offline-first.
3. **Vault layout (special folders):**
   ```
   rules/base/*        installed base packs (never bundled; via template import)
   rules/overlay/*     installed overlay packs (campaign.yaml records base+overlay names)
   homebrew/           campaign-local diffs (hot-reload, `rules lint` validated)
   compendium/         spells/items/monsters reference
   characters/<pc>/    player-owned tree, subpages-only
   sessions/, maps/, assets/
   campaign.yaml       base, overlay, enabled-features[], enabled-plugins[]
   ```
4. **Editor:** web plain-markdown + server preview, round-trip safe. Toolbar for [[link]], ![[embed]], [!secret]. Saves go through atomic `vault.WriteFile` + reindex. Conflict = `*.conflict-<ts>.md` + in-app diff banner, manual merge v1, unresolved stays in vault.
5. **Obsidian compat:** allowlist render. Dataview/Canvas/etc stripped in view as `> [!unsupported]` placeholder, preserved in source/edit.
6. **Secrets core (simplified):**
   - Page: `secret: true/false` (default false). No `public` in v1 — non-secret = guest + member readable, secret = GM + `owner`/`editable-by` holders only (owner-read exception, so a player can always read their own secret sheet/journal; other players/guests blocked).
   - Block: `> [!secret]-` hidden from party, `> [!secret]+` revealed. Default `-`.
   - GM 1-click -/+ toggle. GM view shows badges, player view filters server-side (render+search+graph+API+autocomplete).
   - Secret titles/paths excluded from player autocomplete/backlinks/graph. `[[secret-link]]` renders as redacted block, not broken link.
   - No `reveal-after`. GM responsibility, no auto-reveal.
   - Per-player `for=` + audit view deferred to `secrets-advanced` Feature plugin.
7. **ACL:** players full CRUD (create/edit/upload/rename/delete) only under `characters/<pc>/...`, prefix-enforced, `owner` inherits nearest-ancestor (immutable usernames, PC root stamped by wizard). `editable-by` frontmatter for exceptions (inherits, confers read). Create-button only in own tree. Mature-table, no grief guard. No trash/versions — GM backs up (vault is git-friendly).
8. **Rulesets:** Base (dnd, coc) + Overlay (5e-2014, 5e-2024, coc-6e/7e) + Homebrew diff. No system content ships in the binary (copyright): bases/overlays install as vault content packs (`rules/base/`, `rules/overlay/`, e.g. via template import); `campaign.yaml` records names (v1: display-only, no enforcement), homebrew lives in `homebrew/` and hot-reloads. Optionals multiple-per-file: each optional has stable `id` (`## Flanking <!-- optional:id=flanking-2024 -->` or `> [!optional|id=grapple-v2]`), file-level `optional:true` = shorthand for single-id=file. Parser indexes `optionals[] {id, title, file, line}` in SQLite, GM toggles per-id grouped by file, writes `enabled-features: [...]`. Filename `optional-*` hint only.
9. **Plugins (3-way):**
   - Ruleset (data, hot-reload, no Go recompile)
   - Feature (Go compiled-in: random-tables, initiative, vtt-maps, character-wizard, secrets-advanced)
   - UI (templ slots)
   - Shell slots core owns: header-left/center/right, sidebar-left/right, footer, page-actions, sheet-header. Manifest declares `slots: {sidebar-left: {component, priority, show-if}}`. GM can disable. Slot output secret-filtered, SSE-patchable, no global JS.
10. **Character wizard:** part of Campaign Manager as `character-wizard` plugin, route `/c/<token>/create` (single-use GM-issued claim token). Driven by active overlay, writes `characters/<pc>/index.md` with sheet in frontmatter (single source; SQLite holds read index only). After basic CRUD, before VTT.
11. **VTT v1 minimal:** background image + fog + tokens (linked to character pages) + dice tray + initiative. No grid/lighting/smooth drag. Debounced PATCH + resync.
12. **Dice:** ruleset Base declares dice/rolls as config + data rules (numeric, Fate, symbol-face, percentile); core P12 runs the generic engine + transport (log, blind routing, broadcast, replay with per-viewer re-auth).
13. **Reference content:** User-supplied D&D 5e SRD Obsidian MD snapshot in `fixtures/srd/` is dev fixture. Not embedded in release. No bundled vault. `app init --bare` offline works; `app init --template=tutorial|demo` fetches versioned zip when online. SRD 5.1 under CC-BY-4.0 attribution.
14. **WYSIWYG deferred** post-VTT.

## 4. Architecture
```
vault-fs <-> parser/indexer -> sqlite -> http (templ+datastar)
                |
  ruleset resolver + secret filter + dice transport/log + plugin registry (Feature/UI/Ruleset)
```
Core owns: routing, auth/sessions (cookies, argon2), vault sync, markdown render, link graph, FTS5, users/characters/play-sessions, secrets enforcement, slots shell.

## 5. Plan of Plans
See `roadmap.md` for milestone/parallel order (M1 Wiki → M2 CM → M3 VTT).
- P00 UI prototype (first, purely visual, no functionality): 2 static design variants (Obsidian-like vs focused) as hardcoded HTML/CSS stills. Labeled slot boxes, separate shots per secret state. No templ logic, Datastar, DB, or interaction. Pick winner, loser becomes theme candidate.
- P01 Tech spike + clickable prototype: scaffold, sqlite modernc verification, cross-compile, fsnotify stub, templ+Datastar SSE hello. Then rebuild winning P00 variant for real serving fixtures/p01/*.md with server-side secret filter, redacted links, -/+ SSE toggle, and PC-subtree-only writes.
- P02 Markdown/Obsidian: supported syntax, unsupported inventory, round-trip tests.
- P03 Secrets+ACL: -/+, redacted links, path ACL, server-side audit of all enforcement points.
- P04 Data/Search: file<->DB sync, FTS5, graph, secret-aware, campaign.yaml schema.
- P05 Ruleset DSL: base/overlay/homebrew format, sheet rendering, dice semantics (faces/grammar/evaluator), multi-per-file optional toggles (id grammar + uniqueness check). Timebox: Fighter + Goblin + 1 spell.
- P06 Plugin SDK: Go interfaces, lifecycle, slots registry, build-tag strategy, no-code line.
- P07 Campaign UX: IA, wizard flow, ownership transfer, standalone test.
- P08 VTT-MVP: token/fog protocol, SSE sync, 6-client load test.
- P09 Dist/Ops: goreleaser, migrations, init/templates, Windows paths.
- P10 Security/Test: auth, fuzz parser/secrets.
- P11 Auth/Users: invite, sessions, roles (GM/player).
- P12 Dice/Rolls engine+transport: generic roller on base config, opaque log, blind routing, broadcast, per-viewer replay. Grammar lives in P05 base.
- P13 Import/Legal: zip export, SRD ingest, media limits, attribution.

## 6. Open items
- None. All grilling items resolved. Dependency set frozen: templ, datastar-go, goldmark(+obsidian), fsnotify, modernc, x/crypto, x/image (draw/resize only — imaging dropped as unmaintained); stdlib sessions; hand-rolled ruleset evaluator (no expr lib in v1).

## 7. V1 non-goals
Voice/video, dynamic lighting, WYSIWYG,trash/versions, auto-update, hosted SaaS, native mobile apps, auto-merge/CRDT.

## 8. Backlog (DM lens, post-v1)
- Torch/puzzle timers + spotlight tracker.
- Downtime shopping/craft/train flow with GM approve.
- In-person mode: login-free player mirror + projector theme.
- Safety tools: lines/veils, X-card, `content-warning:` frontmatter with opt-out blur.

## 9. Backlog (player lens, post-v1)
- Full inventory + prepared spells from compendium (equip/use/cast, attunement, weight).
- Private journal nudges: new session/loot/level-up SSE badge.
- Downtime request + GM approval inbox.
- Player safety half: own warnings, lines/veils, X-card button paging GM.
- Guest-to-player join link (temp character, account later).

## 10. Backlog (engine, post-v1)
- Full expression language for ruleset evaluator (`expr-lang/expr` or `gval`) with sandboxing, graduating from the v1 hand-rolled arithmetic/boolean core.
- Pack version enforcement: manifest hashes, pin mismatch fail-closed, drift warnings (v1: versions display-only).
- Media upgrades: EXIF-orientation auto-rotate, webp decode/resize (needs maintained pure-Go decoder), PDF page-count (needs parser dep).
