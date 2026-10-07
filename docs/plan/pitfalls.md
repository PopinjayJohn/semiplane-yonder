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
