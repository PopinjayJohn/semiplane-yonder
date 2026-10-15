# R1 — HTTP import cap + DoS review (`POST /vtt/{mapID}/import`)

Status 2026-10-15: **review complete — raise with guards.** `maxImportBody`
12MB → **55MB**, paired HTTP image cap 5MB → **40MB**, plus an import
admission guard (1 in flight process-wide + 3 imports / 5 min per GM →
429). Parser caps unchanged (96MB decoded, 128MP dims, 32MP png/jpeg
write path). CLI `import-uvtt` stays uncapped. Spec §7, runbook §4, and
amend §§4/7 pin the new numbers; code + tests ship in this PR.

## 1. What the route does today

`internal/vtt/handlers_import.go:42` (`Import`), in order: malformed id →
404; login → 401; `preview_as` → 403 (W2); CSRF → 403; non-GM → 403;
existing-sidecar `CanViewMap` → 404; **then** `io.ReadAll(LimitReader(body,
12MB+1))` → 413; parse → 422; `ApplyImport` (image through
`uploads.Normalize`, 5MB byte cap) → 422; `ResetToSidecar` + `SetFog`
(all-hidden); `OnAfterWrite` full rescan; one `fog-update` SSE.

Two facts carry the whole review:

- **No unauthenticated byte is ever allocated.** 401/403/404/415 all
  return before `ReadAll`. An unauthenticated (or player, or preview)
  attacker gets TCP-level bandwidth only — the same as any route. There
  is no pre-auth parse, no pre-auth base64, no pre-auth allocation.
- **The VTT routes have no rate limiter.** Auth has one, but only for
  logins (`internal/auth/ratelimit.go`: 5/5min per IP + per account,
  15min lockout, DB-backed). Dice has an in-memory per-actor window
  (`internal/dice/transport.go:300`). Imports have neither.

## 2. Worst-case per-request cost at a 55MB body

Measured 2026-10-15 on this worktree (throwaway harness, deleted after;
Linux, `go test`): all costs scale linearly with body size.

| Case | Measured | Projected at 55MB |
|---|---|---|
| Image-envelope body (JSON + base64, decode then sniff-fail) | 8.4MB → 109ms wall, 36MB transient alloc (~4.3× body), heap back to 0.6MB after | ~700ms CPU, ~235MB transient |
| Geometry-heavy body (100k two-point segments, full clip path) | 3.0MB → 283ms wall, 52.5MB transient (~17× body: one `RawMessage` + polyline slice per element) | ~5s CPU, ~1GB transient for a 55MB all-geometry body |
| png write path (`uploads.Normalize`: decode + 32MP guard + re-encode) | 4.1MB noise PNG (1.08MP) → 171ms, 15MB transient | bounded by `MaxPixels`=32MP: ≤128MB RGBA + CatmullRom scale + re-encode, regardless of cap |
| Live reseed (`ResetToSidecar` / `SetFog`) | 291µs / 67µs | size-independent (one map's rows) |
| Index refresh (`Rescan`, small vault) | 1.2ms | scales with **vault pages**, not import size |

Transient budget for the worst plausible 55MB import (40MB webp-class
image): 55MB body + ~53MB JSON string copy + ~53MB whitespace-strip copy
(`parseImage` `Fields`+`Join`) + ~40MB decoded bytes ≈ **~200MB**, all
freed at return. Worst png/jpeg under the paired image cap: add ≤128MB
RGBA + re-encode buffers ≈ **~330MB peak**, single in-flight (see §5).
`decodeBase64Chunked` works in 32KB quanta (one 24KB scratch live);
`imageDims` is header-only for all three formats (stdlib `DecodeConfig`
for png/jpeg, manual RIFF parse for webp) — no full decode before the
dims gate. The 96MB decoded cap check runs first (`len(s)*3/4`
estimate), so a 55MB body can never approach it via the image field
(55MB base64 ⇒ ≤41MB decoded).

## 3. Single-writer SQLite impact

One import = **2 vault writes** (image bytes + sidecar, both atomic
temp+rename via `vault.WriteFile`/`WriteFileBytes` — filesystem, not
SQLite) + **2 tiny app-DB transactions** (`ResetToSidecar`: delete +
re-insert one map's tokens/initiative/fog/doors; `SetFog`: one upsert;
measured <0.4ms combined) + **1 full index rescan on the request path**
(`OnAfterWrite` → `Rescan`: temp DB + rename; the dominant latency, and
it scales with vault size, not import size — unchanged by this cap).

Writer serialization: app DB and index DB are separate files, each
`MaxOpenConns(1)` + WAL + `busy_timeout=5s` (`internal/store/sqlite.go`).
The reseed txns are sub-millisecond, so table traffic (dice, token
moves) stalls imperceptibly behind one import. The hazard is
**stacked imports**: each holds a full rescan on its request path, so N
concurrent large imports serialize N rescans while each holds ~200MB
transient. That is what the in-flight semaphore (§5) exists to prevent —
not the sub-millisecond SQLite writes.

## 4. Threat model: who can reach the parse?

| Attacker | Reaches |
|---|---|
| Unauthenticated | 401 before the first body byte is read. No allocation. |
| Authenticated non-GM | 403 before body read. No allocation. |
| GM in `preview_as` | 403 (preview is read-only). No allocation. |
| CSRF-forged GM browser | Blocked by double-submit token (header or form field). |
| **Compromised GM session / malicious GM** | **Full reach: 55MB parse + decode + vault writes + reseed + rescan.** |
| GM accident (double-click, 50MB export retry loop) | Same as above, without malice. |

A GM can already rewrite any vault file through the editor, so
import-DoS grants no new privilege — the realistic threat is
**self-DoS of the shared table** (single-writer stalls, stacked
rescans, a 1GB transient on a Pi-class host), not data theft or
escalation. The guard therefore needs to be **admission control**
(bound concurrent + repeated heavy imports → 429), not identity
lockout: reusing the login `RateLimiter` shape (DB rows, 15min account
lockout) would be wrong — lockout state must survive restarts (pitfalls),
while import admission is ephemeral backpressure that must *not*
persist, and it must never lock a GM out of the table for importing
maps. The dice transport's in-memory per-actor window is the correct
precedent: no migration, no precious-DB writes, resets on restart.

## 5. Rate-limit options considered

1. **Reuse `auth.RateLimiter`** — rejected: DB-backed windows + account
   lockout punish the GM account for a bursty-but-legit setup session
   (3 maps in 5 minutes is normal when building a campaign) and add app-DB
   writes to the import path.
2. **Per-IP throttling at the mux** — rejected: the table sits on one LAN
   (often NAT'd to one IP); per-IP punishes all players for one GM's
   import and does nothing against the actual actor (the GM session).
3. **In-memory per-GM sliding window + process-wide in-flight semaphore
   (chosen)** — `ImportLimiter`: at most **1 import running** (a second
   concurrent import → 429 "already running") and at most **3 imports /
   5 min per GM user** (past budget → 429). Keyed by `UserID`, checked
   after the GM/CanViewMap gates (rejected requests never consume
   budget) and before `ReadAll` (rejected requests never allocate);
   known-oversize `Content-Length` 413s before acquiring (no budget
   consumed). Follows the dice-transport shape; `Handlers.ImportLimit`
   is explicit, lazily defaulted per instance (same `diceSvcMu` idiom).

## 6. Why the image cap must move with the body cap

The body cap is **not** the binding constraint on legit traffic — the
image is. Body ≈ image×4/3 + envelope, so with the 5MB image cap every
body over ~8MB fails at `ApplyImport` with 422 *after* the server paid
parse + decode. Raising the body cap to 55MB while keeping the 5MB
image cap would admit no real 5–50MB export (`vtt-pixi-uvtt.md` §2's
class) and would only enlarge the geometry-bomb surface (§2 row 2).
The coherent pair:

- `maxImportBody` 12MB → **55MB**: admits the 5–50MB export class with
  headroom; worst-case cost bounded per §2 and single-flight per §5.
- HTTP `ImageByteCap` 5MB → **40MB**: the largest image physically
  encodable in a 55MB body is (55MB − envelope)×3/4 ≈ 40MB, so 40MB is
  the cap that makes the two numbers agree instead of cliffing. webp
  (the format large exports should use per D2) is stored as-is — no
  decode transient; png/jpeg stay under the 32MP `Normalize` guard and
  downscale to 1600px, so stored bytes stay small. Anything larger keeps
  the "use the `import-uvtt` CLI" 422 (D1 preserved; CLI bound 64MB +
  parser caps unchanged).

## 7. Residual risks (accepted, documented)

- A 55MB all-geometry body (~1.8M segments, no legit map looks like
  this — Tomb's 1237 walls are ~50KB JSON) costs ~5s CPU + ~1GB
  transient, once, GM-only. The body cap *is* the geometry bound; no
  separate segment-count cap is added (keeps the parser format-agnostic).
- A 40MB webp is stored and served as-is; `archive-vault` / vault-zip
  skip files >10MB (with a count), so GMs with large art must back up
  the vault directory itself, not the zip. Ops consequence only —
  flagged in the runbook row, not fixed here.
- In-memory admission resets on restart (by design, §4). A restart
  during a pile-up clears the window; the semaphore cannot wedge (defer
  release on every path past acquire).

## 8. What this PR changes

- `internal/vtt/import_limit.go` (new): `ImportLimiter` + defaults.
- `internal/vtt/handlers.go`: `ImportLimit` field + lazy default.
- `internal/vtt/handlers_import.go`: 55MB cap, Content-Length pre-check,
  limiter acquire/release, 40MB image cap, updated 413 text.
- `internal/vtt/import_apply.go`: `maxHTTPImportImageBytes` + docs.
- Tests: `import_limit_test.go` (new); body-cap/429/busy cases in
  `handlers_import_test.go`; new-cap 413 + 429-wiring through the
  production mux in `cmd/app/import_uvtt_gate_test.go`.
- Docs: spec §7 (`MaxUVTTBody=55MB`, 40MB import art), runbook §4 row,
  amend §§4/7 + §9 R1 line.
