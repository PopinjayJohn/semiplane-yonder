# Repo Layout (locked)

## Tree
```
cmd/app/            main, flags (--vault/--data-dir/--addr), init, reindex, reset-password, rotate-session-key, transfer-ownership, archive-vault, clone-vault, rules lint (Lane E1)
internal/
  vault/            fsnotify watcher, rescan, atomic WriteFile helper (all writes), conflict handling (Lane B; F2 consumes API)
  markdown/         goldmark + frontmatter + secret/optional/unsupported ext (Lane A)
  secrets/          Filter(viewer, page), redaction, owner/editable-by checks (Lane F1 implements; Lane G tests)
  ruleset/          Base/overlay/homebrew resolve, evaluator, intents/hooks (code: Lane H2; demo vault packs live in fixtures/demo/rules/)
  dice/             generic engine + logs + replay (Lane H2)
  plugins/         registry, slots, plugin check incl. CSS/a11y lint (Lane I2)
  web/              routes (F1 read handlers, F2 write handlers, frozen registry), SSE, templates data (templates: Lane F1)
  auth/             users, argon2id, SessionStore HMAC, CSRF, claim tokens (Lane C)
  store/            SQLite, FTS, migrations/{index,app}/*.sql contents (Lane B; runner E2), login_attempts
web/
  templates/        shell + slots (Lane F1; header-*, sidebar-*, footer, page-actions, sheet-header)
  static/           core.css (tokens), themes/, mockups/ (Lane D1, P00 only), vendor/ (VERSIONS pin+checksums, Lane E1), a11y-tested markup
fixtures/           hand-written demo vault: demo/ (dev only, never embedded); srd/ read-only upstream mirror (out of scope)
testdata/           golden files: markdown/{input.md, expected.html, expected-index.json}
docs/               plan/ (this dir), gm-guide/, player-onboarding/, plugin-authoring/
.github/workflows/ ci.yml (templ generate + diff check, vet, fmt, lint, unit+fuzz-short, snapshot, axe)
```

## Rules
- `fixtures/` = runnable vaults served by the binary. `testdata/` = test-only goldens, never served.
- Migrations: `internal/store/migrations/{index,app}/NNN_name.sql` (per-target streams from 1: `index/` → `<name>.index.db` disposable rebuild, `app/` → `<name>.app.db` migrated), applied by `PRAGMA user_version`, embedded via `go:embed`.
- Lane ownership: `internal/<pkg>` + matching `docs/plan/pXX.md`; contracts (`SessionStore`, `Store`, `markdown.Parse`, `secrets.Filter`, `Viewer`, `ruleset.Evaluate`, slot registry, route registry) change only via amend PR after Phase 0.
- `campaign.yaml` + frontmatter keys append-only after Phase 1.
