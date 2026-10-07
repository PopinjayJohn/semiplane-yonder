# G2 rollback + amend work order (2026-10-07)

## Decision (human-directed)
- G2 rolled back to pending: Phase-2 verification found serve-after-init crashes,
  so the M1 demo never ran against the real binary. Handler-level tests are green;
  the join gate is not.
- Amend owner: **Lane B writes migration `0002`** (B owns `migrations/*.sql`
  contents), **Lane C verifies** auth behavior against a migrated DB, **Lane E2**
  adds the CI serve smoke. Runner untouched (E2 owns it).
- Regression cover: CI serve-after-init smoke (E2), plus a migrate-then-Ensure
  round-trip test (C).

## Defect
Fresh `init --bare` + serve exits 1:
`auth: ensure schema: SQL logic error: no such column: attempted_at (1)`.
`runServe` runs app migrations (old shape) then `auth.NewSessionStore` /
`EnsureAuthSchema` (new shape) on the same DB; `CREATE TABLE IF NOT EXISTS` is a
no-op for the existing old tables, then `CREATE INDEX ... (ip, attempted_at)`
fails. Exposed by Phase-2 wiring (`f3d9155`); no test ever ran migration +
`EnsureAuthSchema` on one DB.

## Divergence to reconcile (0001 vs `EnsureAuthSchema`)
- `users`: 0001 `(name, role, password_hash, session_version, created_at)` vs
  Ensure `(name, password_hash, is_gm, created_at, updated_at, last_login,
  failed_logins, locked_until, session_version)`. Writers: `createGMUser`
  (`cmd/app/db.go`, old shape) vs `UserStoreSQL` (new shape). Existing GM rows
  must survive with `role` mapped to `is_gm`.
- `auth_sessions`: 0001 `(id, user_id, expiry, last_seen, revoked)` vs Ensure
  `(id, user_id, created_at, expires_at, idle_at, last_seen, csrf_token,
  version, revoked)` (what `SessionStoreSQL` reads/writes). Carry live rows
  with documented defaults for columns with no source.
- `login_attempts`: 0001 `(ip PK, fails, locked_until)` vs Ensure/ratelimit
  `(id, ip, username, attempted_at, success)` + `users.locked_until`. Window
  counts may reset; account lockouts must survive (pitfalls: restarts never
  clear lockout state).
- `claim_tokens`: 0001 `(token, slug, expires)` vs Ensure `(token_hash,
  created_by, new_username, is_gm, note, created_at, expires_at, redeemed_at,
  max_uses, uses)`. B justifies migrate-or-drop for outstanding tokens.

## Lane B tasks (0002 contents)
1. Write `migrations/app/0002_auth_reconcile.sql`: new-shape tables + data
   migration per above. `user_version` bump via the existing runner (no runner
   changes). Must apply cleanly on (a) fresh empty dir and (b) a 0001-era DB
   containing a GM user + sessions.
2. Red lines: touch no product code, no runner changes, no frontmatter/contract
   changes. Done: `TestRunAppFresh`-style coverage for 0002 both paths,
   `make check` clean.

## Lane C tasks (verify, no product-code changes to make tests pass)
1. Auth behavior tests run against a **migration-built** DB (0001→0002):
   `NewSessionStore`/`NewUserStore`/`NewRateLimiter` construct cleanly,
   user create/login round-trip, lockout persists across reopen.
2. File findings (leak-matrix style), never silent implementation fixes.

## Lane E2 tasks (CI serve smoke)
New `check`-adjacent job (or step): fresh `init --bare` into a temp dir, boot
`serve` on a test port, assert over HTTP: guest 200 on non-secret, guest 404
on secret (byte-identical to missing-page 404), owner 200 on own secret, GM
`POST /events` 202 + per-viewer SSE hello (guest payload carries no secret
title). Fails the build if serve exits or any assertion fails.

## Re-gate G2 (all required)
1. `0002` merged; C verification green; E2 smoke green in CI.
2. Live M1 demo re-run against the real binary (guest/owner/GM matrix, GM -/+
   flip over SSE, 3-OS binaries, 5k benchmark, leak matrix).
3. `make check` clean. Then advance roadmap `Current phase` on re-gate GO only.

## Verification (2026-10-07, branch `amend/g2-serve-schema`, commit `373620b`)
- Original repro fixed: fresh `init --bare` + serve boots (`/healthz` ok).
- `make smoke` 14/14 PASS (guest/owner/GM matrix, byte-identical 404s, GM
  `/events` 202, per-viewer SSE hello with no guest title leak).
- `go build`, `go test ./...` (incl. new `TestApp0002*` + `TestMigrated*`),
  `go vet`, gofmt, `golangci-lint` (0 issues), budgets, templ diff all clean.
- All 6 proposed pitfalls + deviations D1/D2 confirmed against the diff;
  D3 holds (no docs/plan edits in the amend commit). Pitfalls appended.
- O1 STILL OPEN (see below). O2 closed: claim_tokens void + lockout-window
  conversion are documented in the 0002 header and within amend permissions.
- Residual (low): 0002 assumes 0001-shape-or-empty tables — an
  Ensure-first/new-shape DB would fail it on missing `role`. Unreachable in
  product flows (init/serve always migrate first); noted, not blocking.

## Remaining for re-gate GO (not covered by smoke or unit tests)
Live M1 demo re-run: GM -/+ flip delivery over SSE against the real binary,
search/FTS filtering over HTTP (smokeseed inserts page rows only), 3-OS
binaries, 5k benchmark re-run. Leak-matrix unit tests are green in `go test`.

## Residual dispositions (human-directed 2026-10-07)
- R1 (0002 assumes 0001-shape-or-empty tables): ACCEPTED as documented above.
- R2 (budgets page-weight-proxy/theme-weight/plugin-css SKIP): ACCEPTED —
  SKIP-by-design until Phase 3+ UI (core/themes) and I2 (plugins) provide
  subjects. `rendered-page` proxy already PASSes.
