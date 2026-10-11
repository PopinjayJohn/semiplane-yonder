# Git workflow

`main` is protected: every change lands via pull request with the `check`
status green. Direct pushes to `main` are rejected.

## Small change (chore/fix/docs)

1. Sync and branch from `main`:
   `git checkout main && git pull origin main`
   `git checkout -b <type>/<slug>` (e.g. `chore/gitignore-opencode`)
2. Commit. Verify first per `AGENTS.md` (`go build ./...` at minimum).
3. Push the branch: `git push -u origin <branch>`
4. Open a PR: `gh pr create --base main --head <branch> --title "<title>" --body "<what/why>"`
5. Link the PR to the working session (OpenChamber `session.link`
   with top-level `url`, `title`, `kind: change`).
6. Wait for `check` green: `gh pr view <n> --json state,mergeable,mergeStateStatus,statusCheckRollup`
7. Merge: `gh pr merge <n> --merge`
8. Sync and clean up:
   `git checkout main && git pull origin main`
   `git branch -d <branch>`
   `git push origin --delete <branch>`
   `git fetch --prune origin`

## Branching

One branch per change off `main` (`<type>/<slug>`), push early, never merge
`main` into the branch. Every change merges to `main` as a single PR. The steps
above (PR → check → merge → delete) apply to every PR.

## Stale branches

Delete remote branches whose PRs are merged; only `origin/main` should
normally remain: `git push origin --delete <branch>`.
