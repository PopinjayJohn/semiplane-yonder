# GM Guide

Operator + game-master manual for Yonder. Status labels matter: **SHIPPED**
describes `origin/main` (what the binary does today); **PENDING** describes
the merged-or-reviewed future that is explicitly not in the binary yet.

## 1. Setup

```sh
yonder init --vault ./my-campaign --bare [--gm-user gm] [--gm-password ...]
yonder --vault ./my-campaign [--addr :8080] [--data-dir DIR]
```

- `init --bare` works fully offline: scaffolds `campaign.yaml`, `README.md`,
  the special folders (`characters/ sessions/ quests/ maps/ assets/ rules/
  notes/`, each with `.keep`), a vault `.gitignore` (`*.db`, `.sessionkey`,
  with `!.keep` negations), then creates the first GM account. The GM
  password comes from `--gm-password`, `YONDER_GM_PASSWORD`, or an
  interactive prompt — it is never logged.
- The data dir defaults to a sibling `<vault>-data/` holding
  `<name>.index.db` (disposable), `<name>.app.db` (precious: users,
  sessions, VTT live state — migrated, never rebuilt), and `.sessionkey`
  (0600). It is never inside the vault.
- Serve is the default command. `/healthz` answers 200 with version info
  only (no vault names/counts); `/version` answers the version only.

## 2. Identity today (SHIPPED): the `?as=` demo tier

There is no HTTP login form in the shipped binary. Identity is the demo
query tier:

- `?as=gm` is the GM; `?as=<name>` (e.g. `mira`, `bram`, `cass`) is that
  player; absent (or `?as=guest`) is a guest. State-changing routes still
  require a session-backed CSRF token — identity alone never authorizes a
  write.
- GM-only impersonation: `?as=gm&preview_as=<name>` filters exactly as that
  player, renders a persistent banner, and is logged. Any previewed request
  is treated as the player everywhere (dashboard, writes, VTT).

## 3. Identity (SHIPPED — PR #16)

A session-cookie tier is shipped: `RouteRegistry.BuildHandler` session middleware
(`AuthenticateRequest` → `Viewer` with owned slugs/grants), `GET/POST
/login` (CSRF double-submit, 429 on rate-limit, 403 on lockout, generic
401 otherwise — no oracle), `POST /logout`, and a GM dashboard with user
management (create user, reset password with revocation, revoke sessions,
grant/remove GM with last-GM guard). Real sessions take precedence; `?as=`
remains only as fallback.

**P11 follow-up (recorded, not implemented): when HTTP login is the norm,
the `?as=` demo identity must be retired or gated** (behind an explicit
dev-only flag). Until then, treat any `?as=` identity as untrusted input:
it is convenient for demos and tests, not authentication.

## 4. Secrets + ACL (the part you must not get wrong)

- Page `secret: true/false` (default `false`). Non-secret pages are
  guest-readable. Secret pages are visible to the GM plus the page `owner`
  and `editable-by` list — and nobody else. Not even the title or path:
  secret titles are excluded from the tree, search, autocomplete,
  backlinks, graph, SSE payloads, and the API; `[[links]]` to unreadable
  secret pages render redacted with the alias dropped.
- `owner` is inherited from the nearest ancestor that sets it; PC roots
  created by the claim wizard are stamped with their player. `owner` is an
  immutable username (no rename in v1) — use
  `yonder transfer-ownership --vault DIR --from old --to new` when a player
  changes identity. Only exact top-level `owner: <old>` lines are
  rewritten, atomically per file; ambiguous files are reported for manual
  review.
- Blocks: `> [!secret]-` (default, hidden from the party but visible to
  the page owner) vs `> [!secret]+`. The GM `-`/`+` flip broadcasts live
  over SSE; every subscriber re-reads and re-filters server-side, so the
  flip itself never leaks content.
- Missing and forbidden pages are byte-identical 404s. Do not "confirm"
  secrets out-of-band: if a player asks whether a page exists and they
  cannot see it, the answer is the same 404 everyone else gets.
- Conflict files (`*.conflict-*.md`) are indexed with the parent's ACL but
  hidden from search/links/graph/tags, and never auto-merge. The editor
  shows a text-first (never color-only) banner with a manual-merge flow;
  only the owner or GM can resolve.

## 5. Accounts + sessions (SHIPPED)

- Passwords: argon2id (64 MiB, 3 passes, 4 lanes, PHC encoded), rehashed on
  login when parameters change; minimum length 8. Usernames: 1–64 chars of
  `[A-Za-z0-9._-]`, case-insensitively unique, immutable.
- Sessions: HMAC cookie backed by a server-side row, 30-day absolute +
  24-hour sliding idle expiry (extended only when within 12 h of lapsing,
  to bound write amplification). `reset-password --user <name>` sets a new
  password and bumps `session_version`, revoking every session.
  `rotate-session-key` replaces `<data-dir>/.sessionkey` (all sessions
  stop verifying — announce a re-login).
- Login abuse: 5 failures per 5 minutes per IP **and** per account, then a
  15-minute account lockout. State lives in SQLite (`login_attempts` +
  `users.locked_until`), so restarts never clear it — proven by the Lane L
  restart harness (`internal/auth/ratelimit_harness_test.go`), which trips
  the budget through the real login flow, closes/reopens the DB mid-lockout,
  and asserts the refusal persists. The limiter sits in front of argon2id:
  64 MiB × login floods is self-DoS without it.
- Cookies are `Secure` only behind a configured trusted proxy serving
  HTTPS. On a plain-HTTP LAN they stay non-`Secure` (with a warning):
  `Secure` cookies simply never send over plain HTTP, so forcing the flag
  would silently break table-day login.

## 6. Campaign + rulesets

`campaign.yaml` keys are append-only: `name`, `created`, `base`,
`base-version`, `overlay`, `overlay-version`, `enabled-features`,
`enabled-plugins`, `landing-page`. Unknown keys fail validation.

- `enabled-features` = ruleset optionals (stable per-file ids declared by
  data packs). `enabled-plugins` = feature plugins (registry ids). The two
  namespaces are separate so neither lane's keys trip the other.
- `landing-page` (default `welcome`) is where `/` redirects. The GM setup
  wizard (`/wizard/setup`, GM-only) writes `campaign.yaml` surgically —
  comments and key order preserved.
- Rulesets are data-only: base declares, overlay versions, homebrew diffs.
  Nothing ships bundled — bases/overlays install as vault packs under
  `rules/`. `yonder rules lint` validates packs (unknown keys/functions
  fail; likely-non-SRD monster text warns — you own table liability).
  GMs invite players with claim links (`/wizard/claims`); redemption mints
  the account and stamps the PC root.

## 7. Running the table

- **Dashboard** (`/dashboard`, SHIPPED: campaign settings, user management,
  run view per §3): initiative + selected-token HP + secret toggles
  + dice log + session notes. Assembly only — it renders caller-filtered
  rows and refuses unmarked input fail-closed.
- **Dice:** generic roller, maxima 100 dice / 1000 faces; the log replays
  stored values with per-viewer re-auth (blind rolls stay blind).
- **VTT** (`/vtt/<map>`): boards are defined by sidecar pages at
  `maps/<id>.md` (grid calibration, background, fog/token defaults in the
  `vtt-map` frontmatter key). Live tokens/fog/initiative live in the app
  DB. Fog fails closed: any data loss hides, never reveals (proven by
  `TestFogFailClosedAfterDataLoss` / `TestResolveFogFailClosed`). If the
  data dir is lost, the GM op is **Reset to sidecar** (reseed from sidecar
  defaults). Tokens are keyboard-movable with text coordinates; 6-client
  no-desync is proven by `TestSixClientNoDesync`. Guests never receive VTT
  events; GM previews filter as the previewed user.
- **Print:** `/static/print.css` is the player-safe handout stylesheet —
  it renders the same secret-filtered HTML, never the source.
- **Export:** `yonder archive-vault --vault DIR [--out f.zip]` writes a
  vault zip (data artifacts and symlinks skipped with a count, never
  stored; >10 MB files refuse). The archive carries **no owner filter** —
  every file ships regardless of frontmatter owner, because the GM owns
  the files. Round-trip identical is proven by
  `TestLaneLArchiveRoundTripIdentical` (archive → fresh dir → byte-equal
  tree). There is **no HTTP download endpoint and no one-click UI button**
  in the shipped binary — the P09/P13 "Download vault zip" UI is FILED as
  pending (see runbook), not silently present. `clone-vault` copies to an
  empty dir for offline table backups; template import
  (`init --template <https-url>`) is checksum-verified with zip-slip,
  symlink, and size guards.

## 8. When things break

- `yonder reindex --vault DIR` rebuilds the **index** DB from vault alone
  (temp file + rename). It never touches the app DB (users, sessions,
  VTT live state). If search/graph look stale after a crash, reindex.
- Bad frontmatter quarantines the page (treated as secret until fixed)
  with a GM warning banner — the app never crashes on authoring mistakes.
- Logs are JSON on stderr with request IDs; secret content is never
  logged. Serve drains SSE connections before exiting on shutdown.
