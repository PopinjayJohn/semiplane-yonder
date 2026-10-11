# AGENTS.md — working rules for agents (human + subagent)

## Start here (every session)
1. Read `docs/spec.md` for the build contract and `docs/plan/backlog.md` for remaining work.
   Skim `docs/plan/pitfalls.md` first for similar past issues; append new notable pitfalls you hit
   (one bullet: cause → fix) in your final report so the file stays alive.
2. Check `docs/plan/repo-layout.md` for package ownership and the contract list. Shared contracts are law.
3. Verify with `go build ./...` + the affected package's tests before finishing. The leak matrix must stay green.

## Commands
- `make dev` — `templ generate --watch` + run against `fixtures/demo`
- `make check` — `templ generate` + `git diff --exit-code`, `go vet`, `gofmt -l`, `golangci-lint run`, `go test ./...`
- `go run ./cmd/app --vault fixtures/demo` — offline smoke
- `go run ./cmd/app reindex --vault <dir>` — rebuild index

## File ownership
Stay inside the task's package: `internal/<pkg>` + its tests. Shared contracts
(`SessionStore`, `Store`, `markdown.Parse`, `secrets.Filter`, `Viewer`, `ruleset.Evaluate`,
slot registry, route registry, `campaign.yaml` and frontmatter keys) change only via explicit
amend — never unilaterally. Frontmatter keys are append-only.

## Hard rules
- Never add a dependency (frozen list in spec §2). Propose in your final report instead.
- Every read path filters secrets server-side (render, search/snippets, graph, autocomplete,
  embeds, SSE, API). Client-side hiding is a leak, not a fix.
- Vault files stay Obsidian-safe: parsers never rewrite source; writes preserve comments/order.
- `secret:true` pages readable by GM + `owner`/`editable-by` only; blocks `-` hidden from party
  but visible to page owner. Never emit secret titles/paths to guests/other players.
- Sheets live in `characters/<pc>/index.md` frontmatter; the index DB holds a read copy only (app DB rows are authoritative for auth/VTT/app state).
- A11y: semantic landmarks, keyboard-reachable + visible focus, never color-only, respect
  `prefers-reduced-motion`, `aria-live` on SSE updates.
- Windows-safe paths (separators, case, reserved names, long paths) in every file op.

## Definition of done (per task)
Code + golden/unit tests + `make check` clean + backlog updated if behavior differs from it.
End with: files changed, tests run, new pitfalls appended (or "none"), contract deviations (if any), open questions.
