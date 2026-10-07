// Command smokeseed seeds page rows into an index DB for the serve-after-init
// smoke (tools/serve-smoke.sh, G2 amend). CI-only test tooling: it is NOT
// product code and NOT the real parse->index path (that conversion is
// Lane F1's: store.Parser over markdown.Parse; the `reindex` CLI is an
// honest stub until then).
//
// It walks a vault dir for .md files, stores each file's BYTES verbatim as
// pages.content (the read path re-parses content server-side with full ACL
// logic, so filtering behavior is the product's own), and fills the
// load-bearing index columns (title/secret/owner) via a minimal frontmatter
// scan of the leading --- block only. Blocks/FTS/links rows are out of scope:
// the smoke asserts page reads + SSE, never search.
//
// Usage: smokeseed <index.db> <vault-dir>
package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: smokeseed <index.db> <vault-dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "smokeseed:", err)
		os.Exit(1)
	}
}

func run(indexPath, vaultDir string) error {
	db, err := sql.Open("sqlite", "file:"+indexPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	n := 0
	err = filepath.WalkDir(vaultDir, func(fp string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if fp == vaultDir {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		rel, err := filepath.Rel(vaultDir, fp)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		content, err := os.ReadFile(fp)
		if err != nil {
			return err
		}
		title, secret, owner := scanFrontmatter(content, rel)
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		fi, err := d.Info()
		if err != nil {
			return err
		}
		secretInt := 0
		if secret {
			secretInt = 1
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO vault_files(path, hash, mtime, size) VALUES(?, ?, ?, ?)`,
			rel, hash, fi.ModTime().UnixNano(), fi.Size()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO pages(path, path_fold, title, content, frontmatter, secret, owner, editable_by, updated_at, hash)
			 VALUES(?, lower(?), ?, ?, '{}', ?, ?, '[]', ?, ?)`,
			rel, rel, title, string(content), secretInt, owner, fi.ModTime().Unix(), hash); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("smokeseed: %d pages -> %s\n", n, indexPath)
	return nil
}

// scanFrontmatter extracts title/secret/owner from the leading --- block.
// Dumb line scan on purpose (mirrors runTransferOwnership's approach, not a
// YAML parser); unreadable values fall back to safe defaults (untitled,
// non-secret, unowned). Quarantine semantics stay the product's own: the
// read path re-parses content and fails closed itself.
func scanFrontmatter(content []byte, rel string) (title string, secret bool, owner string) {
	title = strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t") != "---" {
		return title, false, ""
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "---" || trimmed == "..." {
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(strings.Trim(strings.TrimSpace(val), `"'`))
		switch key {
		case "title":
			if val != "" {
				title = val
			}
		case "secret":
			secret = val == "true" || val == "yes" || val == "on" || val == "1"
		case "owner":
			owner = val
		}
	}
	return title, secret, owner
}
