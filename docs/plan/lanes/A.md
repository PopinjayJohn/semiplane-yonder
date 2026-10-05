# Lane A — Parser (Phase 1)

Goal: goldmark + frontmatter + secret/optional/unsupported extensions with golden tests.
Files: `internal/markdown/**`, `testdata/markdown/**` only. Branch `lane/A-parser`.
Contracts: provide `markdown.Parse` (frozen stub — do not reshape it).
First tasks: (1) frontmatter parse + closed key set validation (p02), (2) `[[wikilink]]`/`![[embed]]`/callout extensions, (3) golden harness + 5 seed cases.
Demo: `go test ./internal/markdown/` green + round-trip check on `fixtures/srd/` excerpt.
Red lines: never rewrite source; quarantine bad files, never crash; no new deps.
Done: goldens + fuzz seeds in repo, `make check` clean. Report per AGENTS.md DoD.
