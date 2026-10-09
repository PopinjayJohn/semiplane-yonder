# Spec — TTRPG Wiki / Campaign Manager / VTT

Source of truth: `docs/plan/` (overview, p00–p13, roadmap, repo-layout). This file is the build-ready digest. On conflict, `docs/plan/pXX.md` wins over this file.

## 1. What we build
Single-binary (Linux/macOS/Windows) self-hosted app: Obsidian-compatible markdown Wiki first, Campaign Manager second, minimal VTT last. Each slice usable standalone with users. Order: M1 Wiki → M2 CM → M3 VTT (`docs/plan/roadmap.md`).

## 2. Stack (frozen)
Go 1.27, templ (SSR) + Datastar (SSE, no WS in v1), SQLite via modernc (pure Go, WAL, `busy_timeout=5s`, single writer). Vanilla CSS + design tokens + `@layer core, plugins, overrides`. CodeMirror 6 only JS. Deps: templ, datastar-go, goldmark(+obsidian), fsnotify, modernc, x/crypto, x/image (draw/resize only). stdlib HMAC sessions. No Node in binary (CI-only for axe). `make dev` = `templ generate --watch` + `go run ./cmd/app --vault fixtures/demo`.

## 3. Data truth
Vault filesystem = truth; SQLite index (`<name>.index.db`) is rebuilt from it (`reindex`: temp + rename, index tables only — auth/app rows in `<name>.app.db` never touched). Sheet lives in `index.md` frontmatter; DB holds read index. Calibration in sidecar frontmatter; live token/fog/visibility in app DB (fog fail-closed hidden on data loss).

## 4. Secrets + ACL (server-side everywhere)
Page `secret:true/false` (default false); non-secret = guest-readable, secret = GM + `owner`/`editable-by` (nearest-ancestor `owner` inheritance, PC root stamped by wizard). Blocks `> [!secret]-/+` (default `-`, owner-visible). Secret titles excluded from tree/search/autocomplete/backlinks/graph/SSE/API; `[[secret-link]]` renders redacted (alias dropped). Leak matrix: GM / owner / editable-by-non-owner / other-player / guest / revoked-session × secret/non-secret / +/- / conflicted-file / embed-of-secret green on every PR. Per-player `for=` deferred to `secrets-advanced`.

## 5. Rulesets (data-only, no Go for homebrew)
Base (dnd, coc…) declares stats/derived/dice/intent catalog; overlay (5e-2014/2024…) versions it; homebrew diffs it (`rules lint` validates, unknown keys fail). Optionals multi-per-file with stable ids → `campaign.yaml enabled-features`. Intents: `{intent, actor, targets, tool, context}` envelope; hooks = intent + phase (pre-roll/post-roll/interpret), layers compose base→overlay→homebrew (damage: sum, advantage: cancel, crit: narrowest-wins), full list pinned in log. Core P12 = generic dice engine + transport (log, blind routing, broadcast, replay of stored values with per-viewer re-auth, `crypto/rand`; maxima 100 dice/1000 faces, bases set lower values). v1 evaluator: hand-rolled arithmetic/boolean engine in core executing base data rules (bounds: depth 32, 256 literals, checked 64-bit).

## 6. UI contract
Slots: `header-*, sidebar-left/right, footer, page-actions, sheet-header` (`{component, priority, show-if}`, GM-disablable, SSE-patchable, secret-filtered input). Plugin CSS in `@layer plugins`, `[data-plugin]` prefix, 20KB cap, `plugin check` lints. Themes = token overrides (`[data-theme]`). A11y: WCAG 2.2 AA — landmarks, keyboard paths (tokens arrow-movable with text coords), focus retention on SSE morphs, `aria-live` polite/assertive, never color-only, reduced-motion respected.

## 7. Auth + ops
argon2id (64MB/3/4, PHC, rehash-on-login); HMAC cookie (`session_id` server table, 30d absolute + 24h sliding idle), key file `<data-dir>/.sessionkey` (0600, never in vault) + `rotate-session-key` CLI, `session_version` revoke-all (bumped by `reset-password`), `Secure` only behind trusted-proxy HTTPS (off on LAN + warning); login rate 5/5min per IP + per account, 15min lockout in SQLite. Flags `--vault/--data-dir/--addr`; `init --bare` scaffolds vault + creates GM; `init --template=` online (pinned URL, checksum, TTL cache); `/healthz` + `/version` (version only); vault-zip download GM-only + logged; secret-filtered print CSS. Migrations: embedded SQL + `user_version`. Uploads: png/jpg/pdf (+webp stored, not resized), 5/10MB, MIME sniff, stdlib + x/image resize, SVG blocklist, EXIF strip.

## 8. Quality gates (CI)
`templ generate` + `git diff --exit-code`, vet, gofmt, golangci-lint, unit + fuzz-short, 3-OS snapshot build, axe on P01 pages, leak matrix, golden markdown tests (`testdata/markdown/`), budgets (<100KB rendered pages excl. vendored JS, <1s VTT resync). Fuzz corpus in repo. Logging: `slog` JSON + request IDs, no secret content.

## 9. Scope fences
VTT v1 = image + fog + tokens + dice tray + initiative only. Timebox: Fighter + Goblin + 1 spell (full MM never bundled; SRD 5.2 under CC-BY-4.0, attribution in demo template). No: WS, WYSIWYG, trash/versions, auto-update, TLS-in-app, OAuth, native apps, CRDT. Backlogs: overview §8–§10.
