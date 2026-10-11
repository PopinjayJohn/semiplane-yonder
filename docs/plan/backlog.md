# Backlog (post-ship)

Ship record: M1 Wiki → M2 CM → M3 VTT **SHIPPED** (G0–G4 GO 2026-10-09);
backlog sweep B0–B7 + UI-1/UI-2/UI-3 **DONE** 2026-10-10. Execution plans
(`p00–p13`, `overview.md`, `roadmap.md`, `lanes/`) were removed 2026-10-11 —
history lives in git. What remains is below: three concrete work items, then
unscoped post-v1 feature ideas.

Standing rules for every item: no new dependencies (spec §2 frozen); every
read path stays server-side secret-filtered; vault writes stay atomic via
`vault.WriteFile`; frontmatter keys append-only.

## W1 — Graph link-target normalization at write (+ backfill)

Symptom: the indexer stores raw wikilink targets (`cinder-pact`) while the
graph handler matches against page paths (`cinder-pact.md`), so live
`/graph` can return `"edges":null` even for the GM.
Workaround today (do not regress): `normalizeLinkBase` shared by
`resolveTarget` and `resolveGraphTarget`; newly resolved secret targets fail
closed to redaction. `TestLinkTargetsStoredRaw` pins the raw `ForwardLinks`
shape and fails loudly if the index shape changes without updating the test.
Scope: `internal/store` (normalize `links.target` at write) + backfill for
mixed-shape DBs left by incremental rescan + handler-side `resolveGraphTarget`
follow-up in `internal/web/handlers_read.go`. Secret drop rule unchanged.
Acceptance: stored targets normalized; mixed-shape DB backfilled; pin test
updated to the new shape; leak-matrix + `go test ./...` green.

## W2 — Canonical preview-write rule

Symptom: `viewerOf` applies `preview_as` on the write path too, so a
previewing GM writes AS the previewed user — while the dashboard instead
403s all preview writes. Two shapes, one needs to be canonical.
Scope: `internal/web` (`viewerOf` / `applySessionPreview` + dashboard write
handlers) + tests pinning the chosen behavior.
Acceptance: one documented + tested rule (either preview-writes-as or
preview-writes-403 everywhere); no per-handler divergence.

## W3 — Serve `/static/core` + `/static/vendor`

Symptom: only `/static/print.css` is served (`cmd/app/serve.go`). The
`<link>` to `/static/core/sheet.css` and the `/static/vendor/codemirror.js`
hook are inert — inline fallbacks and textarea degradation carry use today.
Scope: `cmd/app` static mounts (embed or dir) for `core/` + `vendor/`;
immutable caching for versioned vendor bytes, `private, no-store` anywhere
per-viewer; public assets only, never vault bytes (vault assets stay on the
ACL-checked `/assets` handler).
Acceptance: sheet `<link>` resolves, CodeMirror boots from the vendored
bundle offline, `make check` + budgets green.

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
(manifest hashes, pin mismatch fail-closed, drift warnings — v1 is
display-only); media upgrades (EXIF-orientation auto-rotate, webp
decode/resize, PDF page-count — each needs a maintained dep, propose first
per AGENTS.md).
