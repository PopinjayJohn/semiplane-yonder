package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/web"
	_ "modernc.org/sqlite"
)

// Ops subcommands: reindex, reset-password, rotate-session-key,
// transfer-ownership, archive-vault, clone-vault, rules lint.
// Each resolves --vault/--data-dir the same way serve does (flags > env >
// defaults). Wiring to Lane B/C stores happens through the frozen contracts;
// page-row import rides store.Reindex with the F1-owned web.ParsePage
// adapter (R1 amend); rule-schema validation stays an honest stub pending
// Lane H1/H2.

// runReindex rebuilds the disposable index DB from the vault alone via
// store.Reindex (temp file + rename in the data dir; app rows are never
// touched — Reindex does not even take the app DB as a parameter). Page
// rows are imported through the F1-owned web.ParsePage adapter
// (markdown.Parse → store.ParsedPage); the log line reports the real page
// count read back from the fresh index (Rescan stats stay internal).
func runReindex(vaultPath, dataDir string) error {
	if vaultPath == "" {
		return fmt.Errorf("vault path required (--vault)")
	}
	if fi, err := os.Stat(vaultPath); err != nil || !fi.IsDir() {
		return fmt.Errorf("vault dir not found: %s (run `init --bare` first)", vaultPath)
	}
	dataDir = resolveDataDir(dataDir, vaultPath)
	if err := ensureDir(dataDir); err != nil {
		return err
	}
	indexPath, _, _ := dataPaths(dataDir, vaultPath)
	ctx, cancel := contextTimeout(30 * time.Second)
	defer cancel()
	if err := store.Reindex(ctx, vaultPath, indexPath, web.ParsePage); err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	pages, err := countIndexedPages(indexPath)
	if err != nil {
		return err
	}
	fmt.Printf("reindexed %s (%d pages)\n", indexPath, pages)
	return nil
}

// countIndexedPages reads the page count back from a freshly built index.
// Rescan's own stats are internal to the store package; a follow-up count
// keeps the log line honest without widening any contract.
func countIndexedPages(indexPath string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+indexPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, fmt.Errorf("open index db: %w", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pages: %w", err)
	}
	return n, nil
}

// runResetPassword sets a new password and bumps session_version (revoking
// all sessions). Password from flag, YONDER_NEW_PASSWORD env, or prompt;
// never logged.
func runResetPassword(vaultPath, dataDir, username, newPassword string) error {
	if vaultPath == "" {
		return fmt.Errorf("vault path required (--vault)")
	}
	if username == "" {
		return fmt.Errorf("user required (--user)")
	}
	if newPassword == "" {
		newPassword = os.Getenv("YONDER_NEW_PASSWORD")
	}
	if newPassword == "" {
		var err error
		newPassword, err = promptPassword("New password: ")
		if err != nil {
			return err
		}
	}
	dataDir = resolveDataDir(dataDir, vaultPath)
	db, _, err := openAppDB(dataDir, vaultPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := setUserPassword(db, username, hash); err != nil {
		return err
	}
	fmt.Printf("password reset for %q (session_version bumped; all sessions revoked)\n", username)
	return nil
}

// runRotateSessionKey replaces <data-dir>/.sessionkey via auth.GenerateKey.
// All HMAC sessions stop verifying; operators must announce re-login.
func runRotateSessionKey(vaultPath, dataDir string) error {
	if vaultPath == "" && dataDir == "" {
		return fmt.Errorf("vault path required (--vault) or explicit --data-dir")
	}
	dataDir = resolveDataDir(dataDir, vaultPath)
	keyPath, err := rotateSessionKeyFile(dataDir)
	if err != nil {
		return err
	}
	fmt.Printf("rotated %s (all sessions invalidated; users must log in again)\n", keyPath)
	return nil
}

var ownerLineRe = regexp.MustCompile(`(?m)^owner:[ \t]*([A-Za-z0-9._-]+)[ \t]*$`)

// runTransferOwnership moves page/character ownership oldUser -> newUser
// (Phase 0c: owner is the immutable username; no rename in v1). Only exact
// top-level `owner: <old>` lines are rewritten, atomically per file;
// quoted, commented, or nested owner lines are reported as skipped for a
// later surgical pass (parsers never rewrite source blindly).
func runTransferOwnership(vaultPath, oldUser, newUser string) error {
	if vaultPath == "" {
		return fmt.Errorf("vault path required (--vault)")
	}
	if oldUser == "" || newUser == "" {
		return fmt.Errorf("both users required (old:new)")
	}
	if oldUser == newUser {
		return fmt.Errorf("old and new owner are identical")
	}
	var changed, skipped []string
	err := filepath.WalkDir(vaultPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		loc := ownerLineRe.FindSubmatchIndex(data)
		if loc == nil {
			return nil
		}
		if string(data[loc[2]:loc[3]]) != oldUser {
			return nil
		}
		// Refuse when the file also contains a quoted/commented owner line —
		// ambiguous frontmatter stays for manual review.
		rest := strings.Replace(string(data), string(data[loc[0]:loc[1]]), "", 1)
		for _, line := range strings.Split(rest, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "#") {
				continue
			}
			if strings.HasPrefix(t, "owner:") || strings.HasPrefix(t, "owner :") {
				skipped = append(skipped, relOf(vaultPath, path))
				return nil
			}
		}
		updated := append(append(append([]byte{}, data[:loc[0]]...), []byte("owner: "+newUser)...), data[loc[1]:]...)
		if err := writeFileAtomic(path, updated, 0o644); err != nil {
			return err
		}
		changed = append(changed, relOf(vaultPath, path))
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("transfer-ownership %s -> %s: %d files updated", oldUser, newUser, len(changed))
	if len(skipped) > 0 {
		fmt.Printf(", %d need manual review: %s", len(skipped), strings.Join(skipped, ", "))
	}
	fmt.Println()
	return nil
}

func relOf(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// runArchiveVault writes a vault zip for GM download (GM-only + logged at
// serve time; the CLI records the artifact). Data files never included.
func runArchiveVault(vaultPath, out string) error {
	if vaultPath == "" {
		return fmt.Errorf("vault path required (--vault)")
	}
	if out == "" {
		out = filepath.Base(filepath.Clean(vaultPath)) + "-" + time.Now().UTC().Format("20060102") + ".zip"
	}
	files, skipped, err := archiveVaultDir(vaultPath, out)
	if err != nil {
		return err
	}
	fmt.Printf("archived %s: %d files -> %s (%d data/symlink entries skipped)\n", vaultPath, files, out, skipped)
	return nil
}

// runCloneVault copies a vault to an empty destination (offline copy for
// table backups; TLS/backups policy out of scope per lane brief).
func runCloneVault(src, dest string) error {
	if src == "" || dest == "" {
		return fmt.Errorf("source and --dest required")
	}
	files, skipped, err := cloneVaultDir(src, dest)
	if err != nil {
		return err
	}
	fmt.Printf("cloned %s -> %s: %d files (%d skipped)\n", src, dest, files, skipped)
	return nil
}

// runRulesLint checks vault rules packs at file level. Schema validation
// (unknown keys fail) and the likely-non-SRD warning are Lane H1/H2's;
// this command fails only on missing dirs, unreadable files, symlinks, or
// Windows-reserved names so CI has a stable gate today.
func runRulesLint(vaultPath, dir string) error {
	if dir == "" {
		if vaultPath == "" {
			return fmt.Errorf("vault path required (--vault) or rules dir argument")
		}
		dir = filepath.Join(vaultPath, "rules")
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("rules dir not found: %s", dir)
	}
	var packs, problems int
	walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			problems++
			fmt.Printf("rules lint: unreadable %s: %v\n", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			problems++
			fmt.Printf("rules lint: refusing symlink %s\n", relOf(dir, path))
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			return nil
		}
		packs++
		stem := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		if isWindowsReserved(stem) {
			problems++
			fmt.Printf("rules lint: windows-reserved name %s\n", relOf(dir, path))
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 {
			problems++
			fmt.Printf("rules lint: empty/unreadable %s\n", relOf(dir, path))
			return nil
		}
		if ext == ".json" {
			data, err := os.ReadFile(path)
			if err != nil || !jsonValid(data) {
				problems++
				fmt.Printf("rules lint: invalid JSON %s\n", relOf(dir, path))
			}
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	fmt.Printf("rules lint: %d pack files, %d problems (schema validation pending Lane H1/H2)\n", packs, problems)
	if problems > 0 {
		return fmt.Errorf("%d rules problems", problems)
	}
	return nil
}
