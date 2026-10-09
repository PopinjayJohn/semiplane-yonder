package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
)

// B6: serve runs store.Rescan at startup (the shared code path, wired in
// wireHandlers). A page added to the vault AFTER the last reindex — the
// offline-external-edit case — must be servable the moment the wiring
// boots, with no manual reindex.
func TestStartupRescanImportsLateVaultEdit(t *testing.T) {
	vault := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(vault, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("welcome.md", "---\ntitle: Welcome\n---\n\nHello.\n")
	write("campaign.yaml", "name: Rescan B6\ncreated: 2026-10-09T00:00:00Z\n")

	dataDir := t.TempDir()
	if err := runReindex(vault, dataDir); err != nil {
		t.Fatal(err)
	}
	// External edit lands after the index build (Obsidian while offline).
	write("late.md", "---\ntitle: Late Edit\n---\n\nArrived after reindex.\n")

	db, _, err := openAppDB(dataDir, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessionStore, err := auth.NewSessionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	wiring := wireHandlers(buildInfo{Version: "b6"}, vault, dataDir, sessionStore)
	srv := httptest.NewServer(wiring.Handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/p/late.md")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Arrived after reindex") {
		t.Fatalf("late page not imported by startup rescan: %d\n%s", resp.StatusCode, body)
	}
}
