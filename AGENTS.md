# AGENTS.md — working rules for agents (human + subagent)

## Start here (every session)
1. Read `docs/spec.md`, then only your lane's plan file(s) from `docs/plan/`. Do not load all plans.
   Skim `docs/plan/pitfalls.md` first for similar past issues; append new notable pitfalls you hit
   (one bullet: cause → fix) in your final report so the file stays alive.
2. Check `docs/plan/roadmap.md` for your lane's contracts + join gate. Phase 0 stubs are law.
3. Verify with `go build ./...` + your lane's tests before finishing. Leak matrix (`p10.md`) must stay green.

## Commands
- `make dev` — `templ generate --watch` + run against `fixtures/p01`
- `make check` — `templ generate` + `git diff --exit-code`, `go vet`, `gofmt -l`, `golangci-lint run`, `go test ./...`
- `go run ./cmd/app --vault fixtures/p01` — offline smoke
- `go run ./cmd/app reindex --vault <dir>` — rebuild index

## File ownership (roadmap lanes)
Stay inside your lane: `internal/<your-pkg>` + your `docs/plan/pXX.md`. Shared contracts
(`SessionStore`, `Store`, `markdown.Parse`, `secrets.Filter`, `Viewer`, `ruleset.Evaluate`,
slot registry, route registry, `campaign.yaml` and frontmatter keys) change only via explicit
amend — never unilaterally. Frontmatter keys are append-only after Phase 1.

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
Code + golden/unit tests + `make check` clean + plan file updated if behavior differs from it.
End with: files changed, tests run, new pitfalls appended (or "none"), contract deviations (if any), open questions.
