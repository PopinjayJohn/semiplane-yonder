# Runbook — Fresh Laptop to Live Table

From zero to a running game on Linux, macOS, or Windows. Verified fresh on
Linux (see §7); macOS/Windows notes are code-verified (no platform-specific
syscalls, `filepath` everywhere, reserved-name + case handling covered by
tests) — confirm on hardware before a release if the binaries changed.

## 1. Install

Download the binary for your OS from the release (3-OS `goreleaser`
builds: Linux, macOS, Windows; pure Go, no cgo, single file). No Node, no
runtime, no installer. It runs fully offline after download.

```sh
# Linux / macOS
chmod +x yonder && ./yonder --help
```

```powershell
# Windows (PowerShell)
.\yonder.exe --help
```

> **Windows firewall:** on first `serve`, Windows asks to allow the port.
> Allow private networks for table play; the default listen address is
> `:8080` (all interfaces). For a single-laptop demo, `--addr 127.0.0.1:8080`
> avoids the prompt entirely.

## 2. Create a campaign (offline)

```sh
yonder init --vault ./my-campaign --bare
```

Prompts for the GM password (or set `YONDER_GM_PASSWORD`, or pass
`--gm-password`; `--gm-user` overrides the default `gm`). Refuses
non-empty dirs — it never scaffolds over existing files. Result:

- `my-campaign/` — the vault (Obsidian-compatible markdown; keep it in
  git if you like — the scaffolded `.gitignore` already excludes `*.db`
  and `.sessionkey` while keeping `.keep`/`.obsidian/` working).
- `my-campaign-data/` (sibling, never inside the vault) — `<name>.index.db`
  (disposable search/graph index), `<name>.app.db` (users, sessions, VTT
  live state — back this one up), `.sessionkey` (0600).

Online alternative: `init --template <https-url> [--template-sha256 <hex>
--template-cache-ttl 24h]` fetches a versioned template zip (pinned URL,
checksum-verified, local cache with TTL + re-verify; refuses non-empty
dirs; zip-slip/symlink/size guards on import).

## 3. Serve

```sh
yonder --vault ./my-campaign
# or: yonder serve --vault ./my-campaign --addr 127.0.0.1:8080
```

Open `http://localhost:8080/healthz` (version JSON, nothing else), then
`http://localhost:8080/?as=gm`. Flags beat env beat defaults:
`--vault`, `--data-dir`, `--addr`, `serve --demo-auth` (default true;
`false` retires the `?as=` demo tier — sessions only), `--theme` +
`--theme-scheme` (theme ids `grimoire` / `cartographer`, schemes
`light` / `dark` / `auto`; an unknown theme fails closed to unthemed —
palettes and WCAG notes in `docs/plan/themes.md`).

- **LAN play:** serve binds `:8080` by default; hand out
  `http://<your-lan-ip>:8080/?as=<name>`. Cookies stay non-`Secure` on
  plain HTTP by design (see GM guide §5) — do not put the binary directly
  on the open internet; TLS termination is out of scope (use a reverse
  proxy, and only then behind a trusted-proxy config).
- **Shutdown** is graceful: SSE connections drain before exit.

## 3b. Demo vault login accounts (dev only)

`fixtures/demo` ships **no** accounts — the demo vault is hand-authored, so
`init` never ran against it and there is nothing to log in with. Whether this
matters depends on the identity tier:

| Tier | Needs seeding | Who |
|---|---|---|
| `?as=gm` / `?as=mira` / `?as=bram` / `?as=cass` (`serve --demo-auth`, default true) | No — resolved from the vault per request | dev/smoke/axe |
| Real password login at `/login` | **Yes** — `make demo-seed` | demos where you hand out a URL and want a login prompt |

```sh
make demo-seed                                 # accounts into fixtures/demo-data
make demo-seed DATA_DIR=/path/to/demo-data     # ... or an explicit data dir
go run ./tools/demo-seed --data-dir <dir>      # tool directly
```

Seeds `gm` / `mira` / `bram` / `cass` (GM first, then the players who own the
`characters/<pc>/` trees the demo vault ships). Idempotent: accounts that
already exist are reported and left untouched, so re-running is also how you
confirm a data dir is intact. It hashes through the product's own
`auth.CreateUser` (argon2id, the same validation `init` and the GM dashboard
use) and runs the app migrations first, so it works on a brand-new data dir.

**The demo data dir is disposable** — wiping it drops the accounts (and every
session, VTT live state, and dice log). Re-run `make demo-seed` to restore
them. That is the whole point of the target: a reset should never cost more
than one command.

> The `demo-seed` target writes to the default sibling data dir
> (`fixtures/demo-data/`), which the repo's `.gitignore` excludes — never
> commit accounts, `.sessionkey`, or `*.db` from it.

## 4. Day-to-day ops

| Task | Command |
|---|---|
| Rebuild search/graph from vault alone | `yonder reindex --vault DIR` (index tables only — auth/app rows never touched) |
| Reset a password (revokes all their sessions) | `yonder reset-password --vault DIR --user NAME [--new-password …]` (`YONDER_NEW_PASSWORD` or prompt; never logged) |
| Invalidate every session | `yonder rotate-session-key --vault DIR` (announce a re-login) |
| Player changed identity | `yonder transfer-ownership --vault DIR --from old --to new` (atomic per file; ambiguous files reported, never force-rewritten) |
| Full vault backup | `yonder archive-vault --vault DIR [--out f.zip]` (skips `*.db`/`.sessionkey`/symlinks with a count; files >10 MB refuse) |
| Offline copy | `yonder clone-vault --vault SRC --dest DST` (dest must be empty) |
| Validate rules packs | `yonder rules lint [--vault DIR] [dir]` (errors fail; non-SRD warnings don't) |
| Verify vendored JS pins | `yonder vendor verify [--fetch]` |
| Import a Universal VTT map (A-VTT2) | `yonder import-uvtt --vault DIR --map ID [--background FILE] [--grid-baked] <file.uvtt\|dd2vtt\|df2vtt>` (GM-only surface; writes the image to `assets/maps/<ID>.<ext>` and merges `vtt-map` calibration into `maps/<ID>.md`; run `reindex` afterwards when serving is not running). The same import over HTTP is `POST /vtt/{mapID}/import` (12MB cap → 413). |

**Restore:** fresh `init --bare` + unzip a vault zip over the empty vault +
`reindex` = identical vault + index (proven by
`TestLaneLArchiveRoundTripIdentical`; P13 pass/fail). The app DB is
**not** in the zip — restore it from backup, or re-provision accounts.

**Vault zip over HTTP:** the GM dashboard has a one-click "Download vault
zip (.zip)" (`GET /dashboard/vault.zip`, GM-only + logged, same
`*.db`/`.sessionkey`/symlink exclusions as `archive-vault`).

## 5. Caching behavior (table-LAN reference, P09)

- Per-viewer HTML/JSON (pages, search, graph, autocomplete, sheets,
  wizard, editor, VTT, login-adjacent forms): `Cache-Control: private,
  no-store` — never shared-cache, never stored. (Lane L closed the last
  gap: the sheet/wizard/write funnel previously sent no directive.)
- Vault assets (`/assets/…`, ACL-checked per request): `private,
  max-age=3600`, `X-Content-Type-Options: nosniff`, sandboxed CSP; SVG
  serves as `text/plain` (never sniffed as image).
- Print stylesheet (`/static/print.css`): `public, max-age=86400`.
- Core shell stylesheet (`/static/core/app.css`): `public,
  max-age=86400`, `X-Content-Type-Options: nosniff`. Tokens + shell
  structure in `@layer core` — themes override it from `@layer overrides`.
- Sheet core stylesheet (`/static/core/sheet.css`): `public,
  max-age=86400`, `X-Content-Type-Options: nosniff`. Same file the sheet
  pages inline as fallback — identical bytes either way.
- Theme stylesheets (`/static/themes/grimoire.css`,
  `/static/themes/cartographer.css`, plus static `preview-*.html`): `public,
  max-age=86400`, `X-Content-Type-Options: nosniff`. Selected per campaign
  once the `theme`/`theme-scheme` `campaign.yaml` keys ship (design + gaps:
  `docs/plan/themes.md`).
- Vendored JS (`/static/vendor/…`, pinned by `web/static/vendor/VERSIONS`,
  `yonder vendor verify`): `public, max-age=31536000, immutable`,
  `X-Content-Type-Options: nosniff`. The pixi.js v8 ESM pin (A-VTT2) is
  fetched only by the VTT page's lazy loader (`?renderer=pixi`), never by a
  render-blocking tag.
- First-party VTT loader (`/static/vtt/pixi-board.js`, A-VTT2): `public,
  max-age=86400`, `X-Content-Type-Options: nosniff`. Unversioned like core;
  the page's inline module script `import()`s it (and the pinned vendor file)
  only when `?renderer=pixi` is present.
- All five mounts serve embedded public files only — vault bytes never
  appear here (vault assets stay on the ACL-checked `/assets` handler);
  directory roots and traversal escapes 404.
- SSE, `/healthz`, `/version`: `no-store`.
- No service worker in v1; the server stays offline-first regardless.

## 6. Cross-OS notes

- **Paths:** vault-relative posix ids internally; OS paths via `filepath`
  throughout. Lookup is case-insensitive, display preserves source case.
- **Windows reserved names** (`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`,
  `LPT1`–`LPT9`, trailing dots/spaces, `:` `<` `>` `|` `?` `*`): uploads
  and rules packs are sanitized/rejected (`rules lint` flags reserved
  stems); the zip round-trip test covers odd names and records OS-refused
  ones as skips rather than failures.
- **Long paths:** no fixed-size path buffers; deep trees (e.g.
  `deep/a/b/…`) round-trip.
- **Line endings:** CRLF vault files parse and survive byte-identical
  (parsers never rewrite source; writers are atomic temp+rename+mtime).
- **Case:** `README.md` vs `readme.md` resolve to the same page id on all
  three OSes (lookup folds case; renames are delete + create).
- **Firewall:** see §1; table tablets join over LAN HTTP.

## 7. Fresh-run evidence (this lane, Linux)

```sh
go build ./...                                            # clean
go test ./internal/auth/ ./internal/web/ ./cmd/app/       # green, incl.
                                                          # ratelimit_harness, cache_headers, archive_roundtrip
./tools/serve-smoke.sh                                    # M1 matrix over HTTP
go run ./tools/budgets                                    # via `make budgets`
```

Serve-smoke boots `init --bare` → seeds public + secret pages → `reindex`
→ asserts guest/owner/GM matrix + byte-identical 404s + per-viewer SSE
hello over live HTTP. Axe (`tools/axe/check.sh`, Node confined to CI)
runs in the `axe` CI job; the VTT resync <1 s LAN budget and 6-client
no-desync proof (`TestSixClientNoDesync`) are Lane K's G4 evidence, cited
not re-run.

## 8. Quality gates (for the release checklist)

`make check` = `templ generate` + `git diff --exit-code`, `go vet`,
`gofmt` gate, `golangci-lint`, `go test ./...`, `make budgets`. CI adds:
fuzz-short, 3-OS snapshot, axe on demo pages, serve-smoke. Budgets:
rendered pages <100 KB HTML/CSS excl. vendored JS, plugin CSS ≤20 KB,
upload caps 5 MB image / 10 MB PDF, VTT resync <1 s LAN. Leak matrix
(`docs/plan/pitfalls.md` secrets section: GM / owner / editable-by-non-owner / other-player /
guest / revoked-session × secret/non-secret / `+`/`-` / conflicted-file /
embed-of-secret) stays green on every PR.
