# Lane K — VTT (Phase 4)

Goal: image + fog + tokens over SSE with keyboard access and load proof.
Files: map UI + `vtt_maps/tokens/fog` usage, keyboard list, load test. Branch `lane/K-vtt`.
Contracts: consume I2's frozen fragment contract (URL + SSE events + snapshot endpoint); write no dashboard code.
First tasks: (1) background + grid calibration (sidecar) + tokens linked to character pages, (2) fog + visibility overrides (app DB, fail-closed hidden) + keyboard list + 44px targets, (3) debounced moves + resync + 6-client hotel-WiFi test.
Demo: 6 browsers move tokens without desync or leak; index rebuild keeps fog hidden.
Red lines: login required for all live state; morphs never steal focus; no WS, no lighting.
Done: M3 gate evidence complete, `make check` clean. Report per AGENTS.md DoD.
