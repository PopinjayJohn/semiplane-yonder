# Demo vault — one hand-written campaign proving all features (post-ship)

## Goal
Replace the split fixtures (`fixtures/p01/` toy vault + `fixtures/rules/`
timebox packs) with ONE hand-written demo campaign vault, `fixtures/demo/`,
that `make dev`, axe, smoke, and every present/future demo boot. Hand-written
(deliberate choice over generated): every line is documentation, diffs stay
reviewable, no generator to own. Borrow SRD-dialect flavor (aliases,
tag shapes, statblock code fences) where it proves real-world handling —
but keep files small and authored, never pasted snapshot bulk.

## Manifest (small on purpose — richness over volume)
- `welcome.md` — open hub: intro, `[[wikilink|alias]]`, `![[embed]]`,
  one `[!secret]-` block, one `[!secret]+` block, tasks + GFM table.
- `cinder-pact.md` — `secret: true, owner: mira, editable-by: [bram]`:
  secret body + `-` block + embed back to welcome. (Migrate from `p01`
  verbatim; the leak matrix already pins this exact shape.)
- `lore.md` — open page linking both (graph/tree/search richness).
- `campaign.yaml` — name, base/overlay (+versions), `enabled-features`,
  `enabled-plugins`, `landing-page: welcome`.
- `rules/` — migrate the H1 timebox packs verbatim (dnd base + Fighter /
  Goblin / Fireball compendium, 5e-2014 + 5e-2024 overlays, grit homebrew).
- One quarantined file (broken frontmatter, fail-closed GM-only) + one CRLF
  file — the two parser realities every demo hand-waves away.
- `README.md` — what the vault demonstrates and which test/CI job boots it.

## Migration (migrate, don't duplicate)
- Move `p01/*` → `demo/` (keep page IDs stable: `welcome.md`,
  `cinder-pact.md`, `lore.md`, `campaign.yaml`); move `rules/*` →
  `demo/rules/`; delete the emptied dirs (keep or drop `.gitkeep`s with them).
- Repoint consumers (all mechanical, no behavior change): `Makefile` dev
  target, `tools/axe/check.sh` default vault, `tools/serve-smoke.sh` if it
  names p01, `internal/web/parse_adapter_test.go` trio paths,
  `internal/campaign/resolve_test.go`, `internal/ruleset/load_vault_test.go`,
  `cmd/app/cm_gate_test.go`, and docs mentions (`docs/runbook.md`,
  `AGENTS.md` dev loop, `docs/plan/p01.md`).
- `fixtures/srd/` (read-only upstream mirror) and `testdata/markdown/`
  (golden harness) stay exactly where they are — out of scope.

## Red lines
- Closed frontmatter key set only (+ `vtt-map` where a map page earns it);
  unknown keys fail `rules lint` — the demo must lint clean.
- Exactly ONE quarantined file (the deliberate one); everything else parses
  with `quarantined=false` (prove with the parser before committing).
- Obsidian-safe plain markdown; no remote image URLs (no network in dev);
  every file < 100KB (page-weight budget); no credentials, no real user data.
- `make dev` serves the vault with zero extra steps (init → seed → reindex
  must already work — R1 landed).

## Done
Single `fixtures/demo/` boots via `make dev`; axe + smoke + adapter/gate
tests repointed and green; `make check` clean. Report per AGENTS.md DoD.
