// Command demo-seed seeds the demo campaign's login accounts.
//
// The demo vault (fixtures/demo) has no seeded users: `init` is the only
// command that stamps one (the GM), and the demo vault is hand-authored, so
// nobody ever runs init against it. Password login therefore fails with an
// empty auth table until accounts exist. This tool is the reproducible
// one-liner that fixes that — and it is idempotent, so a wiped data dir is
// one command to restore rather than a lost transaction.
//
// It uses the product's own auth path (auth.CreateUser: argon2id hashing,
// username/password validation) and runs the same app migrations serve
// runs, so it works on a completely fresh data dir too. Nothing here
// invents auth logic or schema.
//
// Usage:
//
//	go run ./tools/demo-seed                       # default: fixtures/demo
//	go run ./tools/demo-seed --data-dir <dir>      # override the data dir
//	go run ./tools/demo-seed --vault <dir>         # seed a different vault's accounts
//
// The `?as=gm|mira|bram|cass` tier remains available (serve --demo-auth,
// default true) and needs no seeding; this tool exists for real password
// logins, which the demo tier does not cover.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
)

// demoAccounts are the demo vault's players, in creation order. `gm` is the
// GM (sees the dashboard, user management, vault export); the players own the
// character trees the demo vault ships under characters/<pc>/.
var demoAccounts = []struct {
	name     string
	password string
	isGM     bool
}{
	{"gm", "gm-demo-2026", true},
	{"mira", "mira-demo-2026", false},
	{"bram", "bram-demo-2026", false},
	{"cass", "cass-demo-2026", false},
}

func main() {
	vault := flag.String("vault", "fixtures/demo", "demovault dir (used to derive the default data dir)")
	dataDir := flag.String("data-dir", "", "data dir (default: sibling <vault>-data/, same rule as serve)")
	flag.Parse()

	// Same path resolution as serve/reindex/init: explicit --data-dir wins,
	// else the sibling <vault>-data dir. Never inside the vault.
	dir := strings.TrimSpace(*dataDir)
	if dir == "" {
		dir = *vault + "-data"
	}
	appPath := filepath.Join(dir, appDBName(*vault))

	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("cannot create data dir", "dir", dir, "err", err)
		os.Exit(1)
	}
	if fi, err := os.Stat(*vault); err != nil || !fi.IsDir() {
		slog.Error("vault dir not found", "vault", *vault, "err", err)
		os.Exit(1)
	}

	db, err := openAppDB(appPath)
	if err != nil {
		slog.Error("cannot open app db", "path", appPath, "err", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	users, err := auth.NewUserStore(db)
	if err != nil {
		slog.Error("cannot open user store", "err", err)
		os.Exit(1)
	}

	ctx := context.Background()
	now := time.Now().Unix()
	created, existing := 0, 0
	for _, a := range demoAccounts {
		// Idempotent: an account that already exists is reported, not
		// failed on — this tool is also how you confirm a data dir is
		// intact. Passwords are never changed (that is reset-password's
		// job, and it revokes the user's sessions).
		u, err := auth.CreateUser(ctx, users, a.name, a.password, a.isGM, now)
		if err != nil {
			if errors.Is(err, auth.ErrUserExists) {
				fmt.Printf("  %-5s already exists (skipped; use `reset-password` to change the password)\n", a.name)
				existing++
				continue
			}
			slog.Error("cannot create user", "user", a.name, "err", err)
			os.Exit(1)
		}
		role := "player"
		if u.IsGM {
			role = "GM"
		}
		fmt.Printf("  %-5s %-16s (%s)\n", u.Username, a.password, role)
		created++
	}

	fmt.Println()
	fmt.Println("Demo accounts seeded in " + appPath + " (created: " +
		itoa(created) + ", existing: " + itoa(existing) + ")")
	fmt.Println("Log in at http://localhost:8080/login — or keep using ?as=gm|mira|bram|cass")
	fmt.Println("with `serve --demo-auth` (default true; needs no seeding).")
	fmt.Println()
	fmt.Println("This data dir is disposable: wiping it drops the accounts.")
	fmt.Println("Re-run this command to restore them.")
}

// openAppDB opens the app DB with the same pragmas and migrations serve
// uses (P01 spike verdicts + the Lane B/E2 migration split). Held to the
// single-writer discipline: one connection.
func openAppDB(appPath string) (*sql.DB, error) {
	dsn := "file:" + appPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.NewMigrationRunner(db).RunApp(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// appDBName mirrors cmd/app's dataPaths naming (sanitized vault basename +
// .app.db) without exporting it: tool must never depend on cmd/app.
func appDBName(vault string) string {
	name := filepath.Base(filepath.Clean(vault))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "vault"
	}
	return name + ".app.db"
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
