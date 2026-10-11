# Backlog (post-ship)

Ship record: M1 Wiki → M2 CM → M3 VTT **SHIPPED** (G0–G4 GO 2026-10-09);
backlog sweep B0–B7 + UI-1/UI-2/UI-3 **DONE** 2026-10-10. Execution plans
(`p00–p13`, `overview.md`, `roadmap.md`, `lanes/`) were removed 2026-10-11 —
history lives in git. Work items W1–W3 below are **DONE** 2026-10-11; what
remains is the unscoped post-v1 feature ideas at the bottom.

Standing rules for every item: no new dependencies (spec §2 frozen); every
read path stays server-side secret-filtered; vault writes stay atomic via
`vault.WriteFile`; frontmatter keys append-only.

## W1 — Graph link-target normalization at write (+ backfill) — DONE (PR #25)

Shipped: `internal/store/links.go` (`NormalizeLinkTarget`), write-side
normalization in `reindex.go` + `backfillLinkTargets` in `Rescan` (backfill
before purge sweep), `resolveGraphTarget` probes normalized-first with legacy
fallback. Secret drop rule unchanged. Live proof: GM `/graph` returns 24
edges incl. `welcome.md→cinder-pact.md`.
(Note: W3's commit rode into `main` inside this PR's merge via shared-tree
contamination — see pitfalls; PR #26 auto-closed as a result.)

## W2 — Canonical preview-write rule — DONE (PR #27)

Rule: **preview-writes-403 everywhere — GM preview is read-only.** One
central `previewReadOnly()` helper (`handlers_write.go`) guards every
state-changing handler; reads (filtering as previewed user, banner, logging)
untouched. Login/logout exempt; GET forms stay readable.

## W3 — Serve `/static/core` + `/static/vendor` — DONE (PR #26, landed via #25)

Shipped: `cmd/app` embeds + serves `static/core/` (`public, max-age=86400`)
and `static/vendor/` (`public, max-age=31536000, immutable`); `isPublicStatic`
carve-out before the registry check (even `/static/print.css` 404'd live
before this). Public assets only, never vault bytes. Runbook §5 documents
the mounts.

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
V1 SHIPPED (inline `base-hash`/`overlay-hash` pins, descriptor-only sha256,
pin mismatch fail-closed via `ErrPinMismatch`, empty pins float, homebrew
audit-only; content-prose hash deferred); media upgrades (EXIF-orientation auto-rotate, webp
decode/resize, PDF page-count — each needs a maintained dep, propose first
per AGENTS.md).
