# Lane L — Harden + Docs (Phase 4)

Goal: lockout persistence, budgets, cache headers, user docs, final cross-OS pass.
Files: hardening + `docs/` (gm-guide, player-onboarding, plugin-authoring) only. Branch `lane/L-harden`.
Contracts: runs after K lands for the final pass; docs match shipped behavior.
First tasks: (1) `login_attempts` enforcement + budget checks in CI, (2) P09 cache headers + GM/player/plugin docs + attribution page, (3) cross-OS fresh-laptop runbook verification.
Demo: docs build clean; fresh `init --bare` works per runbook on all 3 OS notes.
Red lines: no feature code; docs describe shipped behavior only.
Done: M3 gate evidence complete. Report per AGENTS.md DoD.
