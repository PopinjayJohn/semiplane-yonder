# R1 amend — wire page import into the reindex CLI (re-gate prerequisite)

## Problem
`runReindex` (`cmd/app/ops.go`) builds an EMPTY index: it applies the index
schema to a temp DB, counts `.md` files, and swaps — no page rows are ever
imported ("page-row import pending Lane A/B"). The documented
`make dev` / `go run ./cmd/app --vault fixtures/p01` flow therefore 404s
every page. Found during `fixtures/p01-demo` seeding (see G2-amend.md F1);
live serving there needed a throwaway `store.Reindex` helper. The re-gate
live M1 demo cannot use the documented flow until this lands.

## Key facts (verified)
- `store.Reindex(ctx, vaultRoot, indexPath, Parser)` (`internal/store/reindex.go`)
  already implements the full atomic path: temp DB + index migrations + page
  indexing via `Rescan` + checkpoint + rename. The CLI must call it instead of
  duplicating the schema-only half.
- The production `markdown.Parse → store.ParsedPage` adapter was explicitly
  deferred: `internal/store/parse.go:29-31` ("Lane F1 owns the real parse→
  index conversion at write time (amend request, not this diff)"). The audit
  harness's `auditParser` (`internal/audit/leakmatrix_test.go`) is the
  template: title/frontmatter/secret/owner/editable-by/tags passthrough,
  non-`unsupported` blocks → chunks, links/embeds → link edges + chunks,
  `optional` blocks → optionals.
- Suggested placement: adapter as an exported F1-owned helper in
  `internal/web` (which already imports both `markdown` and `store`; keeps
  Lane B's package from importing Lane A's), CLI calls
  `store.Reindex(ctx, vault, indexPath, web.<Adapter>)`. The brief does not
  mandate the name — document the choice.

## Tasks
1. Add the production adapter (F1-owned location) + unit test: SRD spot
   check (goblin/fighter/spell parse to sane `ParsedPage`s) and the
   `fixtures/p01` trio (links/embeds/secret-block chunk flags correct).
2. Rewrite `runReindex` to call `store.Reindex` with the adapter; delete the
   stub body (own temp+rename, `countMarkdown`). Keep the log line, now with
   real counts (`Rescan` stats are internal — page count via a follow-up
   `PageList` or `vault_files` count; keep it simple and honest).
3. Prove the documented flow end to end: fresh vault with the `fixtures/p01`
   trio → CLI `reindex` → boot `serve` → guest 200 open / guest 404 secret /
   owner 200 secret. No throwaway helpers left behind.
4. Full gate locally: `go build`, `go test ./...`, vet/gofmt/lint/budgets,
   `make smoke` still green.

## Red lines
- `cmd/app` (E1) + the adapter spot (F1) only. No migration changes, no
  parser changes, no handler changes, no new deps.
- Index DB stays disposable (temp + rename, app DB untouched) — `store.Reindex`
  already guarantees this; do not reimplement it.
- Out of scope: startup rescan on serve boot (separate follow-up), `/graph`
  edge-key mismatch (filed backlog in G2-amend.md F2).

## Done
Documented flow serves pages; `make check` components + `make smoke` green.
Report per AGENTS.md DoD (files changed, tests run, pitfalls or "none").
Do NOT merge, push, or open a PR — leave commits on the worktree branch.
