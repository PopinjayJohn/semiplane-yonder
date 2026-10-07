package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/semiplane/yonder/internal/store"
	_ "modernc.org/sqlite"
)

// openAppDB opens the migrated app database (<name>.app.db) with the
// single-binary pragmas (WAL, busy_timeout=5s) and runs pending app
// migrations. The index DB is never opened here: reindex builds it from a
// temp file, and serve-time reads go through Lane B's Store.
//
// Connection discipline (pitfalls: one writer, WAL via DSN, checkpoint on
// shutdown): MaxOpenConns(1) until Lane B's Store provides the split
// writer/reader pools.
func openAppDB(dataDir, vaultPath string) (*sql.DB, string, error) {
	_, appPath, _ := dataPaths(dataDir, vaultPath)
	dsn := "file:" + appPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, "", fmt.Errorf("open app db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("ping app db: %w", err)
	}
	if err := store.NewMigrationRunner(db).RunApp(ctx); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("migrate app db: %w", err)
	}
	return db, appPath, nil
}

// createGMUser inserts the initial GM row in the post-0002 (EnsureAuthSchema)
// shape: is_gm=1 with the remaining account columns at their fresh defaults.
// Returns an error naming reset-password when the user already exists.
//
// Amend note (G2 rollback): 0002 rebuilt users from (name, role, ...) to
// (name, password_hash, is_gm, ...); this writer moved with it. Lane split:
// B owns the migration SQL, C owns auth logic, E1 owns this file -- this
// one-spot writer update is filed as an amend deviation in the final report.
func createGMUser(db *sql.DB, username, passwordHash string, now int64) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE name = ?`, username).Scan(&count); err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("user %q already exists (use `reset-password` to change it)", username)
	}
	if _, err := db.Exec(
		`INSERT INTO users(name, password_hash, is_gm, created_at, updated_at, last_login, failed_logins, locked_until, session_version) VALUES(?, ?, 1, ?, ?, 0, 0, 0, 1)`,
		username, passwordHash, now, now); err != nil {
		return fmt.Errorf("insert GM user: %w", err)
	}
	return nil
}

// setUserPassword replaces password_hash and bumps session_version, which
// revokes all existing sessions for the user (Lane C checks the version).
func setUserPassword(db *sql.DB, username, passwordHash string) error {
	res, err := db.Exec(
		`UPDATE users SET password_hash = ?, session_version = session_version + 1 WHERE name = ?`,
		passwordHash, username)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("user %q not found", username)
	}
	return nil
}
