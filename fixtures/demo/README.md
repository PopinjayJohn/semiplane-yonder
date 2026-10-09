# Ashfall demo campaign (hand-written fixture vault)

One small vault that every demo, smoke test, and gate boots: `make dev`,
`tools/serve-smoke.sh` (temp-vault variant of the same flow),
`tools/axe/check.sh`, and the adapter/gate tests
(`internal/web/parse_adapter_test.go`,
`internal/campaign/resolve_test.go`,
`internal/ruleset/load_vault_test.go`, `cmd/app/cm_gate_test.go`).
Hand-written on purpose: every line is documentation, diffs stay
reviewable, no generator to own.

## Pages

- `welcome.md` — open hub: intro, `[[wikilink|alias]]`, `![[lore]]`
  embed, one `[!secret]-` block, one `[!secret]+` block, tasks, GFM
  table. The campaign landing page.
- `cinder-pact.md` — `secret: true, owner: mira,
  editable-by: [bram]`: secret body + `-` block + embed back to
  welcome. Kept byte-identical to the old `fixtures/p01` shape — the
  leak matrix pins it.
- `lore.md` — open gazetteer linking both hub and pact
  (graph/tree/search richness). Kept byte-identical to `fixtures/p01`.
- `quarantined-draft.md` — the ONE deliberate quarantine: `secret:`
  carries a list instead of a bool, so the parser quarantines the page
  and fails it closed (GM-only). No unknown keys — the demo lints
  clean.
- `farrier-log.md` — CRLF line endings (pinned by `.gitattributes`
  `-text` so git never normalizes it): the parser reality every demo
  hand-waves away. Parses clean (`quarantined=false`).

## Rules

- `campaign.yaml` — name, base/overlay (+ display-only versions),
  `enabled-features`, `enabled-plugins`, `landing-page: welcome`.
- `rules/` — the H1 timebox packs verbatim (dnd base + Fighter /
  Goblin Warrior / Fireball compendium, 5e-2014 + 5e-2024 overlays,
  grit homebrew) plus `campaign.example.yaml` (frozen campaign.yaml
  keys, documented — the resolve test asserts its features toggle).

## Budgets

Every file < 100 KB (page-weight budget); no remote image URLs (no
network in dev); no credentials, no real user data.
