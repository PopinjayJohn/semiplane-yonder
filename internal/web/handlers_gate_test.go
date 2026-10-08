package web

// Gate G3 regression tests: the characters read-copy rule (seam #5) and the
// vault-backed pack/engine resolve (seam #1).
//
// Lane B's Store exposes NO characters-table API and this gate must not
// widen it: sheet values + ACLs re-derive from vault truth per request.
// These tests pin the fail-closed behavior under a stale index.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/store"
)

// Stale index claims alice owns the sheet, but vault truth (owner: bob)
// denies her: /me must not render values, titles, or HP.
func TestGateStaleIndexOwnerDeny(t *testing.T) {
	sh, v, s := testSheets(t)
	bobSheet := strings.Replace(aliceSheetMD, "owner: alice", "owner: bob", 1)
	if err := v.WriteFile(context.Background(), "characters/alice/index.md", bobSheet); err != nil {
		t.Fatal(err)
	}
	// Lagging watcher: index still says alice.
	if err := s.PageUpsert(context.Background(), &store.Page{
		Path: "characters/alice/index.md", Title: "Alice", Content: bobSheet,
		Secret: true, Owner: "alice", UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(req, alice()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/me stale-owner = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "HP 11 of 11") {
		t.Fatalf("stale index leaked sheet values")
	}
}

// Reverse staleness: index says bob, vault says alice. Alice's /me lists
// nothing (no title, no values); bob's /me lists nothing either (vault
// funnel denies him).
func TestGateStaleIndexOwnerList(t *testing.T) {
	sh, v, s := testSheets(t)
	if err := v.WriteFile(context.Background(), "characters/alice/index.md", aliceSheetMD); err != nil {
		t.Fatal(err)
	}
	if err := s.PageUpsert(context.Background(), &store.Page{
		Path: "characters/alice/index.md", Title: "Alice", Content: aliceSheetMD,
		Secret: true, Owner: "bob", UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(req, alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("alice /me = %d, want 200 with no character", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No character yet") {
		t.Fatalf("alice /me should report no character:\n%s", rec.Body.String())
	}
	breq := httptest.NewRequest(http.MethodGet, "/me", nil)
	brec := httptest.NewRecorder()
	sh.MeSheet(brec, withViewer(breq, bob()))
	if strings.Contains(brec.Body.String(), "HP 11 of 11") {
		t.Fatalf("bob /me leaked alice's sheet values")
	}
}

// Vault-backed resolve: LoadPack reads base/overlay identity from a real
// vault dir; pack-less vaults fall back without error.
func TestGateLoadPackVault(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("campaign.yaml", "name: Gate\nbase: dnd\noverlay: 5e-2024\n")
	if err := os.MkdirAll(filepath.Join(root, "rules", "base", "dnd"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("rules", "base", "dnd", "pack.yaml"), "id: dnd\nname: Dungeons\ntype: base\nversion: \"5.2\"\n")
	if err := os.MkdirAll(filepath.Join(root, "rules", "overlay", "5e-2024"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("rules", "overlay", "5e-2024", "pack.yaml"), "id: 5e-2024\nname: Fifth\ntype: overlay\nversion: \"2024\"\nparent: dnd\n")
	p := LoadPack(root)
	if p.Base != "dnd" || p.Overlay != "5e-2024" {
		t.Fatalf("vault identity lost: %+v", p)
	}
	// Overlay switch is a file edit: the next resolve follows it.
	write("campaign.yaml", "name: Gate\nbase: dnd\noverlay: 5e-2014\n")
	if err := os.MkdirAll(filepath.Join(root, "rules", "overlay", "5e-2014"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("rules", "overlay", "5e-2014", "pack.yaml"), "id: 5e-2014\nname: Fifth\ntype: overlay\nversion: \"2014\"\nparent: dnd\n")
	p2 := LoadPack(root)
	if p2.Overlay != "5e-2014" {
		t.Fatalf("overlay switch not followed: %+v", p2)
	}
	// Pack-less vaults fall back, never nil, never an error.
	p3 := LoadPack(t.TempDir())
	if p3 == nil || p3.Base == "" {
		t.Fatalf("pack-less fallback broken: %+v", p3)
	}
}
