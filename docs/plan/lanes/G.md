# Lane G — Audit (Phase 2)

Goal: prove the secret model with tests, not prose. No product code.
Files: leak-matrix tests + fuzz corpus only. Branch `lane/G-audit`.
Contracts: starts once F1 compiles; graded at the M1 join gate, not mid-phase.
First tasks: (1) leak matrix GM/owner/editable-by-non-owner/other-player/guest/revoked × secret/non-secret/+/-/conflict/embed, (2) FTS snippet + tag-pane + uniform-404 checks, (3) round-trip fuzz (never rewrites source, never panics).
Demo: matrix green on `main`; one intentional leak attempt red.
Red lines: no product code changes to make tests pass — file findings instead.
Done: M1 gate evidence complete. Report per AGENTS.md DoD.
