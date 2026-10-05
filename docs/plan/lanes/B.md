# Lane B — Index + Vault (Phase 1)

Goal: SQLite schema, FTS + chunk flags, rescan/reindex, atomic WriteFile, conflicts.
Files: `internal/store/**` (schema + `migrations/*.sql` contents), `internal/vault/**` only. Branch `lane/B-index`.
Contracts: provide `Store`; consume `markdown.Parse` via fakes (not Lane A's code); own `vault.WriteFile` (all writers use it).
First tasks: (1) migrations 001 (index tables incl. `blocks`, `optionals`), (2) rescan + atomic reindex (temp+rename, index tables only), (3) watcher debounce + conflict rows with parent ACL.
Demo: delete index DB → `reindex` restores search; conflict file appears with correct ACL, hidden from search.
Red lines: reindex never touches app tables; pragmas via DSN; single writer; Windows-safe paths.
Done: benchmark harness hooks ready for E2, `make check` clean. Report per AGENTS.md DoD.
