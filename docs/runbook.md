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
`false` retires the `?as=` demo tier — sessions only).

- **LAN play:** serve binds `:8080` by default; hand out
  `http://<your-lan-ip>:8080/?as=<name>`. Cookies stay non-`Secure` on
  plain HTTP by design (see GM guide §5) — do not put the binary directly
  on the open internet; TLS termination is out of scope (use a reverse
  proxy, and only then behind a trusted-proxy config).
- **Shutdown** is graceful: SSE connections drain before exit.

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
(`docs/plan/p10.md`: GM / owner / editable-by-non-owner / other-player /
guest / revoked-session × secret/non-secret / `+`/`-` / conflicted-file /
embed-of-secret) stays green on every PR.
