# UI follow-ups — dice tray, encounter section, dead-route cleanup (post-ship)

## Context
Frontend audit (post-PR-20) found every shipped capability has a real UI
surface EXCEPT dice and encounters (engines proven, no UI), plus dead
routes masked by the `/` catch-all redirect. One agent, one branch, three
sequenced commits — all three touch dashboard/shell/routes, so do NOT
parallelize.

## UI-1 — Dice tray (engine exists, UI missing)
- Exists: H2 roller/notation/logs/transport (`internal/dice`), I2
  `transport.go` (RollEvent, RouteBlind, replay re-auth), `RouteDiceRoll`
  (`POST /api/dice/roll`, currently F2 501 stub), `RouteDiceReplay`.
- Build: dashboard "Dice" section — roll form (notation input, blind
  checkbox) → H2 roller → transport routing → `dice_logs`; recent-log list
  below, per-viewer filtered (blind totals GM-only, redacted otherwise);
  replay link per row (stored values, never re-rolled). Wire the POST
  handler to replace the F2 stub (same route, no registry changes).
- Red lines: blind routing + replay re-auth semantics from I2's contract;
  maxima enforced (100 dice / 1000 faces); no new deps.

## UI-2 — Encounter section (builder exists, UI missing)
- Exists: I2 `encounter.go` (BuildEncounter, EmitSpawnIntent, AdjustHP),
  K `vtt_tokens` + `vtt_initiative` tables, compendium data.
- Build: dashboard "Encounters" section — build form (creature picker from
  compendium Goblin/Fighter set, count), spawn writes `vtt_tokens` +
  `vtt_initiative` for the chosen map in one action, token list with
  HP adjust controls (floor 0 / cap max, no input mutation per I2).
  Replace the F2 `EncounterAction` 501 on the same route.
- Red lines: encounter spawn stays GM-only; token positions hidden from
  unauthorized viewers (K leak rules); no VTT board changes (K owns board).

## UI-3 — Dead-route cleanup + honest 404s
- Delete dead route constants `RouteRegister` (`/register`) and
  `RouteSettings` (`/settings`) — grep proves zero handlers reference
  them; they survive only via the `/` catch-all redirect.
- `Index` (`GET /`, `handlers_read.go`) currently 303-redirects EVERY
  unregistered path to landing (masks typos + dead features). Scope it:
  exact `/` keeps the landing redirect; anything else 404s (uniform,
  content-free). Verify smoke/axe probes (`/`, `/p/welcome.md`) and
  existing tests still pass — adjust tests that asserted the redirect.
- Leave F1 `Me`/`Table` placeholder bodies alone (`NewMux` test fallback
  uses them; removing means rewriting F1 suites — out of scope).
- Leave `/upload` 501 (staged by decision) and `/register`-adjacent
  claim flow (`/c/<token>`, I1 — real, untouched).

## Done (all three)
`go build`, `go test ./...`, vet/gofmt/lint, budgets, `make smoke` green;
live boot proving: dice roll + blind redaction + replay over HTTP, encounter
build→spawn→HP adjust on a real map, unknown path 404s, `/` still lands.
Report per AGENTS.md DoD (files changed, tests run, pitfalls or "none").
Do NOT merge, push, or open a PR.
