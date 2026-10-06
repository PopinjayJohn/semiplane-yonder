package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
)

func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestSanitizeDBName(t *testing.T) {
	cases := map[string]string{
		"My Campaign": "my campaign",
		"Vault/Data":  "vault-data",
		"CON":         "_con",
		"aux.md":      "_aux.md",
		"com1":        "_com1",
		"lpt9.txt":    "_lpt9.txt",
		"a<b>c":       "a-b-c",
		"normal-name": "normal-name",
		"../evil":     "evil",
	}
	for in, want := range cases {
		if got := sanitizeDBName(in); got != want {
			t.Errorf("sanitizeDBName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeDBName(""); got != "vault" {
		t.Errorf("empty -> %q, want vault", got)
	}
}

func TestDataPathsSiblingDefault(t *testing.T) {
	index, app, key := dataPaths("", "/vaults/curse-of-strahd")
	dir := "/vaults/curse-of-strahd-data"
	if filepath.Dir(index) != dir || filepath.Dir(app) != dir || filepath.Dir(key) != dir {
		t.Errorf("data files not in sibling dir: %q %q %q", index, app, key)
	}
	if !strings.HasSuffix(index, ".index.db") || !strings.HasSuffix(app, ".app.db") {
		t.Errorf("wrong suffixes: %q %q", index, app)
	}
	if filepath.Base(key) != ".sessionkey" {
		t.Errorf("wrong key name: %q", key)
	}
	// Key must never be inside the vault.
	if strings.HasPrefix(key, filepath.Clean("/vaults/curse-of-strahd")+"/") {
		t.Errorf("key inside vault: %q", key)
	}
}

func TestHashPasswordPHC(t *testing.T) {
	hash, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("wrong PHC params prefix: %q", hash)
	}
	if !verifyPassword(hash, "correct horse") {
		t.Error("verify failed for correct password")
	}
	if verifyPassword(hash, "wrong horse") {
		t.Error("verify passed for wrong password")
	}
	if _, err := hashPassword(""); err == nil {
		t.Error("empty password accepted")
	}
}

func TestEnsureSessionKeyPerms(t *testing.T) {
	dir := t.TempDir()
	keyPath, err := ensureSessionKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key perms = %o, want 600", fi.Mode().Perm())
	}
	before, _ := os.ReadFile(keyPath)
	// Idempotent: existing key never overwritten.
	if _, err := ensureSessionKey(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(keyPath)
	if string(before) != string(after) {
		t.Error("ensure overwrote existing key")
	}
}

func TestScaffoldBareVault(t *testing.T) {
	vault := filepath.Join(t.TempDir(), "my-campaign")
	if err := scaffoldBareVault(vault); err != nil {
		t.Fatal(err)
	}
	for _, folder := range scaffoldFolders {
		if _, err := os.Stat(filepath.Join(vault, folder, ".keep")); err != nil {
			t.Errorf("missing .keep in %s: %v", folder, err)
		}
	}
	cy, err := os.ReadFile(filepath.Join(vault, "campaign.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cy), "name: my-campaign") {
		t.Errorf("campaign.yaml missing name: %q", cy)
	}
	gi, err := os.ReadFile(filepath.Join(vault, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"*.db", ".sessionkey", "!.keep"} {
		if !strings.Contains(string(gi), want) {
			t.Errorf(".gitignore missing %q: %q", want, gi)
		}
	}
	if strings.Contains(string(gi), ".obsidian") {
		t.Errorf(".gitignore must not blanket-ignore dotfiles: %q", gi)
	}
	if _, err := os.Stat(filepath.Join(vault, "README.md")); err != nil {
		t.Errorf("missing README.md: %v", err)
	}
	// Refuses non-empty dirs.
	if err := scaffoldBareVault(vault); err == nil {
		t.Error("scaffold over non-empty dir succeeded")
	}
}

func TestRunInitBareEndToEnd(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "table")
	if err := runInit(initOptions{vault: vault, gmUser: "merlin", gmPassword: "s3cret-pass"}); err != nil {
		t.Fatal(err)
	}
	_, appPath, keyPath := dataPaths("", vault)
	if _, err := os.Stat(appPath); err != nil {
		t.Errorf("missing app db: %v", err)
	}
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key perms = %o", fi.Mode().Perm())
	}
	db, err := sql.Open("sqlite", "file:"+appPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var role, hash string
	var ver int
	if err := db.QueryRow(`SELECT role, password_hash, session_version FROM users WHERE name='merlin'`).Scan(&role, &hash, &ver); err != nil {
		t.Fatalf("GM row missing: %v", err)
	}
	if role != "gm" || ver != 1 {
		t.Errorf("bad GM row: role=%q version=%d", role, ver)
	}
	if !verifyPassword(hash, "s3cret-pass") {
		t.Error("stored GM hash does not verify")
	}
	// Second init with same GM fails naming reset-password.
	err = runInit(initOptions{vault: filepath.Join(root, "table2"), gmUser: "x", gmPassword: "y"})
	if err != nil {
		t.Fatalf("fresh second vault should init: %v", err)
	}
}

func TestRunReindexTempRename(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "v")
	if err := scaffoldBareVault(vault); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "notes", "a.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runReindex(vault, ""); err != nil {
		t.Fatal(err)
	}
	indexPath, _, _ := dataPaths("", vault)
	db, err := sql.Open("sqlite", "file:"+indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var uv int
	if err := db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv < 1 {
		t.Errorf("index schema not applied: uv=%d err=%v", uv, err)
	}
	// App rows untouched: run reindex again after creating an app user.
	if err := runInit(initOptions{vault: filepath.Join(root, "v2"), gmUser: "g", gmPassword: "p"}); err != nil {
		t.Fatal(err)
	}
}

func TestResetPasswordBumpsVersion(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "v")
	if err := runInit(initOptions{vault: vault, gmUser: "g", gmPassword: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := runResetPassword(vault, "", "g", "new-pass"); err != nil {
		t.Fatal(err)
	}
	_, appPath, _ := dataPaths("", vault)
	db, err := sql.Open("sqlite", "file:"+appPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var hash string
	var ver int
	if err := db.QueryRow(`SELECT password_hash, session_version FROM users WHERE name='g'`).Scan(&hash, &ver); err != nil {
		t.Fatal(err)
	}
	if ver != 2 {
		t.Errorf("session_version = %d, want 2", ver)
	}
	if !verifyPassword(hash, "new-pass") {
		t.Error("new hash does not verify")
	}
	if err := runResetPassword(vault, "", "nobody", "x"); err == nil {
		t.Error("reset for missing user succeeded")
	}
}

func TestTransferOwnership(t *testing.T) {
	vault := t.TempDir()
	page := "# T\n---\nowner: alice\nsecret: true\n---\nbody\n"
	other := "# O\n---\nowner: bob\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(vault, "a.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "b.md"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runTransferOwnership(vault, "alice", "carol"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(vault, "a.md"))
	if !strings.Contains(string(got), "owner: carol") {
		t.Errorf("owner not moved: %q", got)
	}
	untouched, _ := os.ReadFile(filepath.Join(vault, "b.md"))
	if string(untouched) != other {
		t.Errorf("unrelated page modified: %q", untouched)
	}
}

func TestArchiveExcludesDataFiles(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "vault.index.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".sessionkey"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.zip")
	files, skipped, err := archiveVaultDir(vault, out)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || skipped != 2 {
		t.Errorf("files=%d skipped=%d, want 1/2", files, skipped)
	}
}

func TestCloneRoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "notes", "a.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "clone")
	files, _, err := cloneVaultDir(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Errorf("files=%d, want 1", files)
	}
	got, err := os.ReadFile(filepath.Join(dest, "notes", "a.md"))
	if err != nil || string(got) != "hi" {
		t.Errorf("clone mismatch: %q %v", got, err)
	}
	// Refuses non-empty dest.
	if _, _, err := cloneVaultDir(src, dest); err == nil {
		t.Error("clone into non-empty dest succeeded")
	}
}

func TestZipSlipRefused(t *testing.T) {
	for _, bad := range []string{"../evil.md", "/abs.md", "..", "x.db", "d/y.index.db", ".sessionkey"} {
		if _, err := zipEntryName(bad); err == nil {
			t.Errorf("zipEntryName(%q) accepted", bad)
		}
	}
	if _, err := zipEntryName("notes/good.md"); err != nil {
		t.Errorf("good entry refused: %v", err)
	}
}

func TestHealthzVersionOnly(t *testing.T) {
	info := buildInfo{Version: "v", Commit: "c", Date: "d"}
	vaultDir := t.TempDir()
	sessionStore := &mockSessionStore{}
	mux := wireHandlers(info, vaultDir, sessionStore)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for k := range body {
		if k != "version" && k != "commit" && k != "date" {
			t.Errorf("healthz leaks field %q", k)
		}
	}
	// No vault info even when probed.
	req2 := httptest.NewRequest(http.MethodGet, "/healthz?vault=secret", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if strings.Contains(rec2.Body.String(), "vault") {
		t.Errorf("healthz reflects vault input: %q", rec2.Body.String())
	}
}

func TestVersionEndpoint(t *testing.T) {
	vaultDir := t.TempDir()
	sessionStore := &mockSessionStore{}
	mux := wireHandlers(buildInfo{Version: "1.2.3", Commit: "abc", Date: "today"}, vaultDir, sessionStore)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	if rec.Code != 200 {
		t.Fatalf("version = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "1.2.3") {
		t.Errorf("version body missing version: %q", rec.Body.String())
	}
}

func TestPrintCSSServed(t *testing.T) {
	vaultDir := t.TempDir()
	sessionStore := &mockSessionStore{}
	mux := wireHandlers(build(), vaultDir, sessionStore)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/print.css", nil))
	if rec.Code != 200 {
		t.Fatalf("print.css = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("content-type = %q", ct)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("missing cache header")
	}
	if !strings.Contains(rec.Body.String(), "@media print") {
		t.Error("no print rules")
	}
}

func TestVendorVerifyLocal(t *testing.T) {
	dir := t.TempDir()
	content := "hello vendor"
	sum := sha256Of([]byte(content))
	if err := os.WriteFile(filepath.Join(dir, "x.js"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	versions := filepath.Join(dir, "VERSIONS")
	line := "x 1.0 " + sum + " https://example.com/x.js file=x.js\n"
	if err := os.WriteFile(versions, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runVendorVerify(dir, versions, false); err != nil {
		t.Fatal(err)
	}
	// Mismatch fails.
	bad := "x 1.0 " + sum + " https://example.com/x.js file=missing.js\n"
	if err := os.WriteFile(versions, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runVendorVerify(dir, versions, false); err == nil {
		t.Error("missing fallback passed verify")
	}
}

func TestRulesLintGates(t *testing.T) {
	if err := runRulesLint("", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("lint on missing dir succeeded")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pack.json"), []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runRulesLint("", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{nope`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runRulesLint("", dir); err == nil {
		t.Error("lint on invalid JSON succeeded")
	}
}

func TestCommandDispatch(t *testing.T) {
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"bogus-cmd"}); err == nil {
		t.Error("unknown command succeeded")
	}
	if err := run([]string{"rules"}); err == nil {
		t.Error("bare rules succeeded")
	}
	if err := run([]string{"init", "--vault", filepath.Join(t.TempDir(), "v")}); err == nil {
		t.Error("init without --bare/--template succeeded")
	}
}

func TestDispatchSubcommandFlags(t *testing.T) {
	vault := filepath.Join(t.TempDir(), "v")
	if err := run([]string{"init", "--vault", vault, "--bare", "--gm-user", "g", "--gm-password", "pw"}); err != nil {
		t.Fatal(err)
	}
	// Flags after the subcommand (AGENTS.md form) ...
	if err := run([]string{"reindex", "--vault", vault}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"rules", "lint", "--vault", vault}); err != nil {
		t.Fatal(err)
	}
	// ... and before it.
	if err := run([]string{"--vault", vault, "reindex"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--vault", vault, "rules", "lint"}); err != nil {
		t.Fatal(err)
	}
}

// mockSessionStore implements auth.SessionStore for tests that don't need real sessions.
type mockSessionStore struct{}

func (m *mockSessionStore) Create(ctx context.Context, s *auth.Session) (string, error) {
	return "", nil
}
func (m *mockSessionStore) Get(ctx context.Context, id string) (*auth.Session, error) {
	return nil, auth.ErrSessionNotFound
}
func (m *mockSessionStore) Update(ctx context.Context, s *auth.Session) error      { return nil }
func (m *mockSessionStore) Delete(ctx context.Context, id string) error            { return nil }
func (m *mockSessionStore) DeleteByUser(ctx context.Context, userID string) error  { return nil }
func (m *mockSessionStore) DeleteByVersion(ctx context.Context, version int) error { return nil }
func (m *mockSessionStore) CleanupExpired(ctx context.Context) error               { return nil }
func (m *mockSessionStore) Touch(ctx context.Context, id string, idleAt, expiresAt int64) error {
	return nil
}
func (m *mockSessionStore) Revoke(ctx context.Context, id string) error { return nil }
func (m *mockSessionStore) FindByCSRF(ctx context.Context, token string) (*auth.Session, error) {
	return nil, auth.ErrSessionNotFound
}
