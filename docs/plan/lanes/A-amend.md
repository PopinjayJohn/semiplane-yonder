# Lane A amend — same-indent block sequences in frontmatter (SRD-driven)

## Goal
Accept valid YAML/Obsidian frontmatter where a block sequence follows an
empty-valued key at the SAME indent (`cssclasses:\n- json5e-monster`), which
the subset parser currently rejects with `line N: unexpected content` →
quarantine. 1692/1694 `fixtures/srd/` files hit this (parse sweep 2026-10-07:
0 panics, 0 errors, 1692 quarantined, all one reason). Unknown keys
(`obsidianUIMode`, `cssclasses`, `aliases`) are already handled correctly
(kept for `rules lint`, no quarantine) — this amend is ONLY the sequence
shape. After it, SRD files must parse with unknown-keys reported but NOT
quarantined for this reason.

## Files
`internal/markdown/frontmatter.go` (suspect: `parseBlockMap` requires
`lines[i].indent > indent` for a nested sequence under an empty-valued key),
`testdata/markdown/**` (new golden for the shape), fuzz seeds if warranted.
Branch `amend/a-frontmatter-seq`. SRD snapshot present on this base for the
sweep; do NOT touch `fixtures/srd/` content.

## First tasks
1. Repro: parse `fixtures/srd/compendium/bestiary/fey/goblin-warrior-xmm.md`,
   confirm `line 3: unexpected content` quarantine.
2. Fix `parseBlockMap` (or the narrowest point) so an empty-valued key
   followed by same-indent `- item` lines parses as a sequence. Indented
   sequences, nested maps, and all existing goldens must behave identically.
3. Add golden(s): block-seq-after-key at same indent (scalar items), plus
   indented-seq regression coverage if missing. Unknown keys still reported
   for `rules lint`; truly-invalid YAML still quarantines (never crash).
4. Re-run the SRD sweep (throwaway `go run` helper inside the module,
   deleted afterwards): quarantine count for this reason must go to zero;
   remaining quarantines (if any) get listed as findings, not silently fixed.
5. `go test ./internal/markdown/` + `make fuzz-short` (or `FUZZ_TIME=5s`
   locally) + `go test ./...` for the read path (`internal/web`,
   `internal/secrets`, `internal/audit` — block visibility must not shift).

## Demo
Sweep before/after numbers + golden tests green + full `make check`
components (templ diff, vet, gofmt, lint, budgets).

## Red lines
Lane A owns `internal/markdown/**` + `testdata/markdown/**` only — touch
nothing else. Parsers never rewrite source; quarantine stays fail-closed
(GM-only) for genuinely bad frontmatter; no new deps (stdlib YAML subset
stays); unknown-key reporting semantics unchanged. Report per AGENTS.md DoD.
