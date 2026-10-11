# Backlog plan (ordered; UI gaps first)

Source: UI verification 2026-10-10 of implemented app vs winning mockup
Variant B (`web/static/mockups/b/`, winner per `web/static/mockups/index.html`).
Status: all milestones SHIPPED (G0–G4 GO, see `roadmap.md`); everything below
is post-ship backlog. Pre-existing backlog carried at the bottom (from
`roadmap.md` Status + `overview.md` §8–§10); UI-followups UI-1/UI-2/UI-3
(`lanes/UI-followups.md`) are DONE — dice tray, encounter section, and honest
404s all ship in the dashboard/read path.

Conventions per item: symptom → scope (files) → red lines → acceptance demo.
No new dependencies (spec §2 frozen). Every read path stays server-side
secret-filtered; vault writes stay atomic via `vault.WriteFile`; frontmatter
keys append-only.

## B0 — Embed-transported secret links (FIXED 2026-10-10)

Symptom (user-reported on the live demo): guest view of `/p/welcome` rendered
lore's `[[cinder-pact|The Pact]]` — transported via `![[lore]]` — as a raw
clickable link with alias intact and a relative `href="cinder-pact"`.
Root cause (`internal/web/handlers_read.go:postProcessLinks`): the wikilink
pass ran BEFORE embed expansion, so anchors injected by `expandEmbed` were
never secret-checked or route-rewritten; both loops also replaced only the
first occurrence per target (duplicates alias-corrupted each other).
Fix: embeds expand first, then a single-pass regex pass secret-checks +
rewrites every anchor (target from the anchor's own `data-target`), embed
spans replace-all. Pinned by `TestEmbedTransportedSecretLinkRedacted` (fails
on pre-fix code with exactly the reported leak, passes after).

## B1 — Nav IA + graph HTML (DONE 2026-10-10)

Shipped: role-aware nav (guests Read only; players +Table `/vtt/` +Characters
`/me`; GMs +Dashboard, suppressed under preview-as-player) with `?as=`
threading; `GET /graph` content-negotiates (JSON for
`Accept: application/json`, HTML `GraphBody` view otherwise) over the factored
`graphPayload` (drop-secret rule unchanged). No new routes. Pinned by
`TestNavRoleAware` + `TestGraphContentNegotiation`. Follow-up considered:
Graph now URL-only (Search via page-actions button) — re-add to logged-in nav
if discoverability matters; Characters points at `/me` (GM sees "no sheet"
note) — flag if a character index page is preferred.

Symptom: mockup nav is `Read/Table/Characters` (`b/04-character.html:14-18`);
impl nav is `Read/Search/Graph` (`web/templates/shell.templ:136-140`). No
`Table` or `Characters` links; `/me` and `/dashboard` reachable only by direct
URL. `GET /graph` returns JSON only (`internal/web/handlers_read.go:800-840`),
so the nav Graph link lands on raw JSON — no HTML graph UI.
Scope: `web/templates/shell.templ` (nav), `internal/web/handlers_read.go`
(Graph HTML view reusing the existing filtered nodes/edges payload).
Red lines: secret nodes/edges dropped server-side, never redacted (existing
rule); no new routes (reuse `/graph` with HTML/JSON content negotiation like
`/search`); route registry untouched.
Demo: logged-in nav shows Read/Table/Characters (+GM Dashboard); guest sees
Read only; `/graph` renders an HTML page for browsers, JSON for
`Accept: application/json`; secret pages absent for unauthorized viewers.

## B2 — VTT board (DONE 2026-10-10; Lane K scope)

Shipped: `internal/web/handlers_vtt.go` (`vttBoard` GET + `vttStateUpdate`
snapshot/POST-op envelope), `web/templates/vtt.templ` (`VTTBoard`: `role=img`
map with text alternative, fog text `aria-live=assertive`, 48px initialed
tokens with text coords/HP, keyboard token list with move forms, dice tray +
`aria-live=polite` log, initiative drawer, SSE script on frozen event names),
`web/static/core/vtt.css` (`@layer core`, 48px/52px targets). Reuses encounter
backend + dice transport + `PublishVTT` `?map=` multiplex (no duplication);
others' files touched by one-line delegation only into the existing
registrations. Pinned by 20 new tests; full package green. Open: (1) RULING 2026-10-10 → add the GM create-first-map hint — DONE
2026-10-10: `VTTEmpty` 200 empty-state (status note + block-style `vtt-map`
example + `/p/new?parent=maps` link) when the map is unviewable AND viewer is
effective GM AND zero sidecars exist; non-GM uniform 404s byte-identical
(preview-as gets 404). Confirm at merge: 200-vs-404 status + broad trigger
(any unresolvable id, not just bare `/vtt/`); (2) RULING 2026-10-10 → KEEP player moves on any visible
non-hidden token; VTT faces a major overhaul later, no mapping work now.
Note: `vtt.css`
activates when ops mounts `/static/core/` (inline/text-content carries use
today). Note: `vttStateUpdate` also serves GET snapshots per the frozen
`StateSnapshotPath` contract — activates with registration only, no code
change.

Symptom: `b/06-vtt.html` (map + fog + 3 text-labeled 48px tokens + dice tray +
initiative drawer + keyboard token list with text coordinates) has no
implementation. `GET /vtt/` is a placeholder (`handlers_read.go:1489-1504`);
`POST /api/vtt/*/state` is 501. Only backend exists: dashboard token list +
`/events?map=` wire multiplex.
Scope: new VTT template + `internal/web` VTT handlers (Lane K owns board;
dashboard encounter code untouched), SSE fragment contract from I2.
Red lines: fog fail-closed on data loss; token positions/coords hidden from
unauthorized viewers (K leak rules); keyboard token list with text coordinates
(a11y: `role=img` map with full text alternative, never color-only); 48px
touch targets; no WebSockets (SSE per spec §2).
Demo: GM sees map + fog + tokens + initiative; player sees redacted/fogged
board; 6-client no-desync; fog fail-closed after index rebuild (G4 criteria).

## B3 — Character page: New-subpage button + sheet-header embed (DONE 2026-10-10)

Shipped: `PageView` calls `characterExtras` for `characters/<pc>/…` pages —
`+ New subpage` button (gated by the literal `WriteHandlers.authorizeWrite`),
sheet card (vault-truth re-read + `secrets.Filter` + `ParseSheet`, nil-safe no
oracle), subpages list (secret-dropping `PageList`, self/index excluded,
sorted; uniform 404s byte-identical). `PageData` gains
`ShowNewSubpage`/`NewSubpageParent`/`Sheet`/`Subpages`. Pinned by
`TestCharacterPageOwner` + `TestCharacterPageAccess` (leak assertions cover
secret titles/derived values/button only — open subpage titles legitimately
appear in the 404 shell nav).

Symptom: `b/04-character.html:26-49` shows `+ New subpage` (only under
`characters/<pc>/`) + `sheet-header` card + subpages list on the character
page. Impl has `GET/POST /p/new` but no button on character pages; sheets live
only at `/me` + `/characters/` routes, never embedded in page view; no
subpages list.
Scope: `internal/web/handlers_read.go` (PageView: conditional button +
subpages of `characters/<pc>/`), sheet read funnel reuse from
`handlers_sheet.go` for the embedded card.
Red lines: button visible only to writers of that subtree (same gate as
`authorizeWrite`); sheet embed re-reads vault truth + `secrets.Filter`
(I1 funnel rule); subpages inherit owner, secret titles dropped for
unauthorized viewers.
Demo: owner sees `+ New subpage` + sheet card + subpages on own character
page; guest on secret character page gets uniform 404.

## B4 — Sheet styles + one-handed phone layout (DONE 2026-10-10)

Shipped: three-way `?view=`/cookie switch (simple/chips/advanced, bogus falls
back to Simple fail-closed); chips renders the B-05 style-2 grid (6 native
ability `<button class="chip">` with derived SRD modifiers + condensed vitals
ledger table); B-08 order everywhere (sheet → `.thumb-zone` actions → collapsed
drawer with inspiration/death-save text pips); `writeSheetHTML` (viewport
meta, `private, no-store`, inline fallback CSS); real `web/static/core/sheet.css`
in `@layer core` (44px base, 480px breakpoint with stacked full-width ≥52px
actions, focus-visible, reduced-motion). Writes untouched. Pinned by 8 new
tests (switch, grid, drawer, no-store, `TestSheetCSSMatchesFile` equality pin,
phone-rule pin, modifier units). Notes: schema has no AC/Ember so chips are
the six abilities with HP/Spend/Rest/Level as thumb-zone actions; the `<link>`
activates whenever ops mounts `/static/core/` (inline fallback carries styling
today).

Symptom: `b/05-sheet.html` (ledger rows + big-tap chip grid, same data, two
styles) and `b/08-mobile-sheet.html` (390px frame, condensed table, full-width
52px thumb-zone actions, drawer collapsed below sheet) vs impl Simple view
(`handlers_sheet.go:338-411`): single ledger-ish layout, −1/+1 HP buttons,
44px targets, no chip style, no phone frame/thumb zone. `web/static/core/`
is empty (inline CSS only).
Scope: `internal/web/handlers_sheet.go` (chip-grid style switch via
`?view=`/cookie alongside simple/advanced), `web/static/core/` responsive CSS
(phone breakpoints, 52px action targets).
Red lines: derived widgets only, never raw frontmatter; pips/text labels,
never color-only; `private, no-store` on per-viewer sheet output (Lane L rule).
Demo: `?view=chips` renders the 6-button grid ≥44px; 390px viewport shows
condensed table + stacked full-width Roll/Spend/Rest ≥52px + collapsed drawer
below sheet, keyboard-reachable one-handed.

## B5 — Editor: side-by-side preview + CodeMirror + cheatsheet (DONE 2026-10-10)

Shipped: PageEdit stacked source + still preview (`editorPreviewHTML`,
parse-only via existing `renderPreviewBlocks`) + collapsed cheatsheet drawer
(`editorCheatsheetHTML`) + guarded same-origin CodeMirror hook degrading to
the textarea (vendored ESM re-export can't boot until Lane E1 ships a runnable
bundle — open question carried). Write gate unchanged, conflict flow
unchanged, no new routes. Pinned by `TestPageEditShowsStackedPreviewCheatsheetUpload`
+ `TestPageEditGateBeforeContent` + `TestPageEditPreviewStillSavesNothing`.

## B6 — Upload wiring (DONE 2026-10-10; Lane B amend LANDED)

Shipped: `Vault.WriteFileBytes` (atomic temp+rename `[]byte`, 0644, parents
created, NO clash memory/parking; post-write still records `known`/`self` for
watcher self-events) + `VaultWriter` interface extended (all fakes fixed
compile-driven) + live `POST /upload` (401 → 12MB cap → CSRF → multipart →
`cleanUploadDest` + `authorizeWrite`/`denyForWrite` → `uploads.Process`:
identical-bytes 303 idempotent, differing-bytes 409, else write → 303 to
`/assets/<rel>` with identity preserved). `TestUploadStaysStaged` deliberately
retired for 7 live round-trip tests + 3 vault-bytes tests; indexing needed no
changes (binaries index as `assets` rows, never reach the markdown parser).
DONE 2026-10-10 (rulings trio): (1) form carries hidden `from` (editor page
rel), successes 303 to `/edit/p/<from>` via `uploadRedirectTarget` (`/assets/`
fallback when absent/invalid); (2) `assetVisible` fail-closed on page-less
dirs (+ root `path.Dir "."` normalization fix); (5) `preview_as` enabled for
session GMs via `applySessionPreview` (DemoAuth=false only, GM-only,
bannered+logged). Flag: `viewerOf` also applies preview on the write path, so
a previewing GM writes AS the previewed user — dashboard instead 403s all
preview writes; rule needed on which shape is canonical.
Symptom: `POST /upload` deliberately 501 (`handlers_write.go:1556-1558`:
binary-safe vault write, destination/ACL policy, and UI consumer all missing).
Scope: Lane B amend (binary-safe `WriteFile` variant / clash-safe asset path)
+ ACL policy (GM-only vs `characters/<pc>/` subtree) + editor/form UI surface.
Red lines (pinned by stub comment): no improvisation past the three red
lines; MIME-sniff, 5/10MB caps, SVG blocklist, EXIF strip (spec §7); assets
served only via ACL-checked `/assets`.
Demo: `TestUploadStaysStaged` retained pinning the 501; landed: `uploadFormHTML`
(staged multipart form, CSRF, honest notice) + pure `cleanUploadDest`
(page-dir + name validation + `.md` refusal). Blocked on Lane B amend:
(1) clash-safe bytes write variant (`WriteFile(string)` + text conflict-park
can't carry png/pdf bytes); (2) destination/ACL product sign-off (proposed:
GM-anywhere + players confined to `characters/<pc>/…` via existing
`authorizeWrite`, 404-vs-403 via `denyForWrite`). Open: `/static/vendor/*`
serving (Lane E1 call) so the CodeMirror hook can boot; live-upload response
shape pre-constrained to multipart POST → 303-with-identity.

## B7 — Demo characters & journals (DONE 2026-10-10)

Shipped (content-only, `fixtures/demo/`): `characters/mira/index.md` (secret,
owner mira, valid Fighter sheet d10/L1, `-`/`+` blocks) + `journal.md`
(secret, owner mira) + `characters/bram/index.md` (secret, owner bram,
distinct Fighter array) + `journal.md`; `welcome.md`/`lore.md` gain one line
each linking both PCs (existing bytes untouched). Verified: all four
`quarantined=false` with valid sheets; reindex 14 pages; guest 404s, owners
200, other-player 404s, GM 200s, pact editable-by flow intact
(bram 200/cass 404); guest welcome/lore/search/graph leak nothing;
`/me?as=mira` renders "Fighter, HP 13 of 13" frontmatter-free; still exactly
ONE quarantined file; `go test ./...` green incl. leak matrix. Deviation:
journals carry the inherited `owner` explicitly — static files can't execute
write-path inheritance and demo `?as=` viewers carry no `OwnedSlugs`, so
ownerless journals would 404 for their own owner; same effective ACL as the PC
root.

Symptom (user-reported on the live demo): `fixtures/demo/` has no
`characters/` tree and no journals, yet `cinder-pact.md` names `owner: mira`
and the demo identities (`mira`/`bram`/`cass` via `?as=`) own nothing — so
`/me` sheets, character pages, subpages, `editable-by` flows, and GM
preview-as-player have zero demo content to exercise.
Scope: `fixtures/demo/` ONLY (no Go): `characters/mira/index.md` (secret,
owner-stamped sheet frontmatter valid per `internal/web/sheet.go`
`ParseSheet`/`Validate`, e.g. Fighter-flavored) + `characters/mira/` journal
subpage (owner-inheriting) + `characters/bram/index.md` (+ journal) matching
the pact's `editable-by: [bram]` shape. Link the PCs from `welcome.md`/`lore.md`
(open pages link, secret titles never leak to guests — B0-proof shape).
Red lines (from `lanes/Demo-vault.md`): closed frontmatter key set; still
exactly ONE quarantined file (add no broken frontmatter — prove new files
parse with `quarantined=false`); Obsidian-safe plain markdown, no remote URLs,
every file < 100KB, no credentials/real user data; existing IDs stable
(`welcome`, `cinder-pact`, `lore`, `campaign.yaml`, `rules/` untouched).
Demo: `make dev` boots; `/p/characters/mira?as=mira` 200 with sheet card,
guest uniform 404; `/me?as=mira` renders the sheet; leak-matrix tests +
`go test ./...` green.

## Carried backlog (after the UI gaps)

* Startup rescan follow-up — CLOSED 2026-10-10 (verify-and-close, zero diff:
  wiring complete at `cmd/app/serve.go:126-142`,
  `TestStartupRescanImportsLateVaultEdit` + `go test ./cmd/app/` green).
* Vault-zip download UI — CLOSED 2026-10-10 (verify-and-close, zero diff:
  dashboard link + GM-only handler + CLI parity all ship and tested,
  `zip_export_test.go` green).
* `web/campaign` vs `internal/campaign` unification — CLOSED 2026-10-10
  (verify-and-close, zero diff: single ownership in `internal/campaign/`,
  duplicate deleted, `TestUpsert*` fidelity suite green).
* `?as=` demo-identity retirement → P11 (DONE 2026-10-10): flag default
  verified true (current behavior preserved, no wiring changes); real gap found
  + fixed — `postProcessLinks` built `?as=` hrefs from the viewer
  unconditionally, now gated on `DemoAuth` (clean URLs in sessions-only mode);
  `preview_as` dies with the tier, real-auth paths untouched. Pinned by
  `TestDemoAuthFalseSessionRenderHasCleanURLs`,
  `TestDemoAuthTrueKeepsShippedBehavior`, `TestServeDemoAuthRetiresAsTier`
  (production wiring, both modes, 404 oracle). RULING 2026-10-10: (5)
  `preview_as` ENABLED for real session GMs (Phase-0c contract holds in
  production; agent dispatched); (8) SSE bootstrap `?as=` echo GATED on
  `DemoAuth` (agent dispatched). B1 follow-ups RULED: (7) leave both (Graph
  stays URL-only, Characters stays `/me`); upload ACL RULED: (6) keep mirrored
  page ACL (tighten to GM-only later in one line if needed). DONE 2026-10-10:
  echo gated in both shell and board scripts (`NoDemoEcho`, echo branch kept
  byte-identical); read-path `renderShell` sets `data.NoDemoEcho = !DemoAuth`
  (one-line follow-up, all lanes complete).
* Graph edge-key mismatch — workaround HARDENED 2026-10-10 (no index-shape
  fix): shared `normalizeLinkBase` adopted by both `resolveTarget` and
  `resolveGraphTarget` (rendered links and graph edges can't diverge; newly
  resolved secret targets fail closed to redaction). `TestLinkTargetsStoredRaw`
  pins the frozen raw `ForwardLinks` shape and fails loudly if Lane B ever
  normalizes. Lane B amend PROPOSED (not implemented): normalize
  `links.target` at write + backfill (incremental rescan leaves mixed shapes).
* B6 upload amend — RULING 2026-10-10: (1) write semantics = B (atomic
  temp+rename bytes write without clash memory; identical-bytes idempotent
  success, differing-bytes 409); (2) ACL = A (mirror page-write ACL: GM
  anywhere, players in own `characters/<pc>/…` via existing `authorizeWrite`/
  `denyForWrite`; tighten to GM-only in one line if abuse appears). Agent
  dispatched.
* `web/campaign` vs `internal/campaign` unification (H1 amend).
* Graph edge-key mismatch follow-up (F2-filed; handler-side `resolveGraphTarget`
  workaround at `handlers_read.go:842-861` — fix at the index shape).
* Startup rescan follow-up (`startup_rescan_test.go` B6 wiring).
* Vault-zip download UI: verify/close — dashboard already links
  `/dashboard/vault.zip` (`shell.templ:460-466`); confirm the filed F2 item's
  remaining scope or close it.
* `overview.md` §8–§10 post-v1 items (DM lens, player lens, engine) — unchanged,
  scheduled after B1–B6.

## Suggested order

B1 → B3 → B4 → B5 → B6 → B7 → B2, with carried items interleaved by owner
(`?as=` with any auth touch; unification/edge-key/rescan with store touches).
B2 last: largest, needs the I2 fragment contract + B1 nav slot. B7
(content-only) can land any time — no code dependency either way.
