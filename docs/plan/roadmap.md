# Milestones + Phased Implementation (parallel-subagent order)

## Status (single source — phase agents read this, humans update it)
- Current phase: **Phase 2** (G1 GO — Phase 1 lanes A/B/C/D1/E1/E2 merged as `phase/1-wiki-foundations`; `make check` green, G1 demo checklist passed)
- Gates: G0 **GO** · G1 **GO** · G2 pending · G3 pending · G4 pending
- Lanes: Phase 1 done (`lane/A-parser`, `lane/B-index`, `lane/C-auth`, `lane/D1-design`, `lane/E1-ops`, `lane/E2-gates`); Phase 2 (F1/F2/G) ready, none dispatched yet
- Rule: the phase agent runs the phase named here and dispatches exactly its lanes
  (briefs in `docs/plan/lanes/`). Advance `Current phase` only on its gate's GO.

## Milestones
- **M1 Wiki:** browse/search/read/edit vault with secrets enforced, auth, single binary. Demo: guest reads non-secret, owner reads own secret, GM toggles -/+ live.
- **M2 Campaign Manager:** rulesets (base+overlay+homebrew), wizard + `/me` Simple sheet, GM dashboard, encounter builder, dice transport, sessions/clocks/quests schema.
- **M3 VTT:** image+fog+tokens over SSE, keyboard list, a11y, load test. Non-negotiable, last.

## Phase 0 — Contracts + scaffold first (2 agents, blocks all)
- **Phase 0a (scaffold):** Go 1.27 module path, full tree, `go.mod` (pin exact dep versions), Makefile, `goreleaser` stub, `make dev` loop, CI skeleton. Lands first; all lanes build inside it.
- **Phase 0b (contracts):** `SessionStore` interface + `auth.GenerateKey()` pure function (C provides, E1 wires), `Store` (SQLite) interface + migration runner split (B owns `migrations/*.sql` contents, E2 owns runner), markdown API (`Parse->Page{...}`), secret-filter signature over frozen `Viewer{user_id?, is_gm, owned_slugs[], grants[], preview_as?}` (GM-only impersonation, bannered + logged), slot registry, `ruleset.Evaluate(intent)->modifiers` + transport call convention, shared `handlers_read.go`/`handlers_write.go` split. Output: compiling stubs. No logic. (`routes.go` frozen-then-amendable: new routes register via the Phase-0 route registry, never by editing core routing.)
- **Phase 0c (locked model decisions):** one process = one vault/campaign (files `<name>.index.db` disposable + `<name>.app.db` migrated; `--db` becomes `--data-dir`); `Viewer{user_id?, is_gm, owned_slugs[], grants[], preview_as?}` (GM-only impersonation, bannered + logged); page ID = vault-relative posix path (lookup case-insensitive, display preserving; rename = delete + create); `owner` = immutable username (no rename in v1; `transfer-ownership` CLI on change); all vault writes via atomic `vault.WriteFile` (re-read + temp + rename + mtime check); bad frontmatter quarantined (GM warning banner, never crash); binary ships ruleset machinery only — bases/overlays install as vault packs under `rules/` (never bundled); vault assets served only through ACL-checked handler (never raw static).

## Phase 1 — Parallel lanes (6 agents, inside Phase 0 scaffold, no shared files)
- **Lane A (parser):** Plan `p02.md`. Owns `internal/markdown/**`, `testdata/markdown/**`. Provides `markdown.Parse` (frozen stub). Touches nothing else.
- **Lane B (index):** Plan `p04.md`. Owns `internal/store/**` (schema + `migrations/*.sql` contents), `internal/vault/**` (watcher, `WriteFile`, conflicts). Provides `Store`. Uses `markdown.Parse` fakes, not Lane A's code.
- **Lane C (auth):** Plan `p11.md`. Owns `internal/auth/**` (`auth_sessions`, `claim_tokens`, `GenerateKey`, CSRF). Provides `SessionStore`.
- **Lane D1 (UI prototype P00):** Plan `p00.md`. Owns `web/static/mockups/**` only. No Go. Exit picks the winning shell; F1/F2 build on it.
- **Lane E1 (binary/ops):** Plans `p09.md` (flags/init/healthz/zip/print/CLI), `p13.md` (upload pipeline + docs skeleton). Owns `cmd/app/**`, `web/static/vendor/VERSIONS`. Calls `auth.GenerateKey`; writes no auth logic.
- **Lane E2 (quality gates):** Plans `p10.md` (CI/matrix/budgets), `p04.md` (benchmark harness). Owns `.github/workflows/**`, migration runner, `Makefile` check targets, P01 spike verdicts (modernc/FTS5/pragmas). Writes no schema content.

Join gate (staged): Phase-0 exit = scaffold + stubs compile. Phase-1 exit = golden tests (Lane A) + CI skeleton green; leak matrix graded at M1 gate, not here.

## Phase 2 — Wire Wiki (3 agents)
- **Lane F1 (read path):** Plans `p01.md` (prototype), `p02.md` (render). Owns `internal/web/handlers_read.go`, `internal/secrets/**` (implements `Filter`; Lane G tests it), `web/templates/**` (winning shell). Uses `markdown.Parse`, `Store` via frozen contracts. Provides working SSE -/+ toggle.
- **Lane F2 (write path):** Plans `p01.md` (editor), `p03.md` (conflict UX). Owns `internal/web/handlers_write.go`. Consumes `vault.WriteFile` + `Store` (Lane B API); touches no read handlers. (`routes.go` registry frozen since Phase 0.)
- **Lane G (audit):** Plans `p03.md`, `p10.md`. Owns leak-matrix tests + fuzz corpus only (no product code). Starts once F1 compiles; graded at the join gate, not mid-phase.
Join gate M1 demo passes: guest/owner/GM matrix green, 3-OS binaries, 5k benchmark recorded.

## Phase 3 — CM (4 agents, after M1)
- **Lane H1 (schema/fixtures):** Plan `p05.md` (layers/format/optionals). Owns `rules/` fixture packs (Fighter/Goblin/spell), `campaign.yaml` validation. Unblocks I1 early. Writes no evaluator code.
- **Lane H2 (evaluator/hooks/dice engine):** Plans `p05.md` (hooks/policies), `p12.md`. Owns `internal/ruleset/**`, `internal/dice/**`, `rules lint`. Provides `ruleset.Evaluate` + generic roller.
- **Lane I1 (onboarding):** Plan `p07.md` (wizards, `/me`, Simple/Advanced, level/rest). Owns `internal/web` wizard + sheet handlers, `wizard_drafts` usage. Consumes H1 packs; mocks H2 `Evaluate` until ready.
- **Lane I2 (run-mode + SDK):** Plans `p06.md` (Feature/UI interfaces, slots, `plugin check`), `p07.md` (dashboard, encounter, sessions/recap), `p12.md` (transport interfaces). Owns `internal/plugins/**`. First consumer: random-tables. Exit freezes the map-fragment URL + SSE event names + state-snapshot endpoint that Lane K implements.
Join gate M2 demo: overlay switch with no rebuild, wizard-to-sheet, dashboard run, dice replay.

## Phase 4 — VTT (2 agents, after M2)
- **Lane K (VTT):** Plan `p08.md`. Owns map UI + `vtt_maps/tokens/fog` usage, keyboard token list, touch targets, load test. Consumes I2's frozen fragment contract; writes no dashboard code.
- **Lane L (harden + docs):** Plans `p10.md` (lockout/budgets), `p13.md` (docs/export), `p09.md` (cache headers). Owns hardening + docs only; final cross-OS pass after K lands.
Join gate M3: 6-client no-desync, fog fail-closed verified, axe clean, zip round-trip identical.

## Join gates (numbered; a phase starts only on its gate's GO)
- **G0 (Phase 0 exit):** scaffold builds on all 3 OS stubs, all contract stubs compile, contracts human-approved. Unlocks Phase 1.
- **G1 (Phase 1 exit):** golden tests green (A), migrations apply clean on empty dir (B), login round-trip works (C), winner shell picked with mobile stills (D1), `init --bare` + `serve` + `/healthz` offline (E1), CI skeleton green + benchmark harness runs (E2). Unlocks Phase 2.
- **G2 = M1 demo:** guest reads non-secret, owner reads own secret, guest blocked on secret, GM -/+ flips live over SSE, 3-OS binaries, 5k benchmark recorded, leak matrix green. Unlocks Phase 3.
- **G3 = M2 demo:** overlay switch with no rebuild, wizard → owned sheet, dashboard run with encounter spawn, dice log replays identically. Unlocks Phase 4.
- **G4 = M3 demo:** 6-client no-desync, fog fail-closed after index rebuild, axe clean, zip round-trip identical. Ships.
- Lane-exit evidence: D1's winner pick is G1 evidence; H1's packs gate I1's start; I2's frozen fragment contract gates K's start.

## Critical path
D1 → F1 → H1 → I1 → K. Everything else fans out.

## Conflict rules
Lanes own their files (`git` CODEOWNERS-style by `internal/<lane>` + `docs/plan/pXX.md`); executable work orders live in `docs/plan/lanes/<LANE>.md` (goal, files, contracts, first tasks, demo, red lines); `internal/web` split by manifest (`handlers_read.go` F1, `handlers_write.go` F2, `routes.go` registry frozen); `internal/store` split (migrations content B, runner E2); `internal/vault` single owner (B; F2 consumes its API). Shared contracts change only in Phase 0 or via explicit amend PR. `campaign.yaml` + frontmatter keys are append-only after Phase 1.

## Branch strategy (parallel lanes)
One branch per lane off the phase base (`lane/A-parser`, `lane/B-index`, …; Phase 0 on `main`). Push early, rebase onto phase base before the gate — never merge `main` into a lane. Merge order at each gate: contracts/stubs first, then leaves in dependency order (F1 before F2 consumers, H1 before I1, I2 fragment before K); the phase agent resolves conflicts, lane agents never touch another lane's files to "fix" a merge. A lane silent or red past one retry gets its branch reviewed as-is, not poked. Gate verdicts merge to `main` as a single phase PR.
