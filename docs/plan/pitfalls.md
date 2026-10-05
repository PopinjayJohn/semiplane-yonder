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
