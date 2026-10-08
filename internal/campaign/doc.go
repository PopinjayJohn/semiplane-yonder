// Package campaign implements Lane H1's slice of P05: campaign.yaml
// validation, base/overlay/homebrew resolution from vault packs, vault-wide
// optional-id scanning, GM per-id toggle writes, and ruleset pack structure
// checks ("rules lint" structure half; H2 wires the command and owns the
// evaluator, intent execution, and expression-function validation).
//
// Contracts (frozen): campaign.yaml keys are append-only after Phase 1. The
// frozen set is name, created, base, base-version, overlay, overlay-version,
// enabled-features[], enabled-plugins[] (ruleset optionals and feature
// plugins live in separate namespaces per P04/P06). Versions are recorded,
// never enforced: the resolver surfaces them but never gates on them.
//
// Red lines: no evaluator code here (H2's engine executes pack rules), no
// system content beyond the Fighter/Goblin/1-spell timebox, stdlib only
// (frozen deps — the YAML subset reader below is hand-rolled on purpose).
//
// Consumers: I1's wizards (campaign.yaml create/validate, optionals list,
// toggle writes) and H2's `rules lint` (ValidatePack).
package campaign
