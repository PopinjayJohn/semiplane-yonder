# Lane C — Auth (Phase 1)

Goal: users, argon2id, sessions, CSRF, claim tokens, key generation.
Files: `internal/auth/**` only. Branch `lane/C-auth`.
Contracts: provide `SessionStore` + `auth.GenerateKey()` (E1 wires it into `init`).
First tasks: (1) users table access + argon2id `m=65536,t=3,p=4` PHC + rehash-on-login, (2) HMAC cookie sessions (`auth_sessions` + `last_seen`, conditional `Secure`, CSRF double-submit), (3) claim-token issue/redeem + `reset-password` bumps `session_version`.
Demo: `init` creates GM → login → cookie works → logout/revoke kills it → login rate-limit trips.
Red lines: key file never in vault; no secret content in logs; no OAuth/email scope creep.
Done: unit tests incl. traversal-proof session handling, `make check` clean. Report per AGENTS.md DoD.
