package web

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedExportVault builds a small vault dir: one open page, one secret page,
// plus data artifacts (.db, .sessionkey) that must never cross the boundary.
func seedExportVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"welcome.md":     "---\ntitle: Welcome\n---\n\nHello.",
		"cinder-pact.md": "---\ntitle: Pact\nsecret: true\nowner: mira\n---\nThe ember is hidden.",
		"campaign.yaml":  "name: Test\ncreated: today\n",
		"notes/app.db":   "fake-db-bytes",
		".sessionkey":    "fake-key",
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readZip(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("response is not a valid zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			_ = rc.Close()
			t.Fatal(err)
		}
		_ = rc.Close()
		out[f.Name] = buf.String()
	}
	return out
}

func TestVaultZipGMGetsEverythingExceptArtifacts(t *testing.T) {
	h := testHandlers()
	h.VaultRoot = seedExportVault(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/vault.zip?as=gm", nil)
	rec := httptest.NewRecorder()
	h.NewMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type = %q, want application/zip", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="`) || !strings.HasSuffix(cd, `.zip"`) {
		t.Errorf("content-disposition = %q, want attachment .zip", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("export must be no-store, got %q", cc)
	}
	got := readZip(t, rec.Body.Bytes())
	// GM owns the files: secret page ships WITH its secret content.
	if !strings.Contains(got["cinder-pact.md"], "The ember is hidden") {
		t.Errorf("GM zip missing secret content: %v", keysOf(got))
	}
	if !strings.Contains(got["welcome.md"], "Hello") {
		t.Errorf("GM zip missing open page")
	}
	for name := range got {
		if strings.HasSuffix(strings.ToLower(name), ".db") || filepath.Base(name) == ".sessionkey" {
			t.Errorf("data artifact %q crossed the boundary", name)
		}
	}
}

func TestVaultZipForbiddenContexts(t *testing.T) {
	h := testHandlers()
	h.VaultRoot = seedExportVault(t)
	mux := h.NewMux()
	for _, target := range []string{
		"/dashboard/vault.zip",                       // guest
		"/dashboard/vault.zip?as=cass",               // other player
		"/dashboard/vault.zip?as=mira",               // page owner (not GM)
		"/dashboard/vault.zip?as=gm&preview_as=cass", // GM previewing: filtered context
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", target, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "ember") || strings.Contains(body, "PK\x03\x04") {
			t.Errorf("GET %s leaked vault content in 403 body", target)
		}
	}
}

func TestVaultZipNeedsVaultRoot(t *testing.T) {
	h := testHandlers() // no VaultRoot
	req := httptest.NewRequest(http.MethodGet, "/dashboard/vault.zip?as=gm", nil)
	rec := httptest.NewRecorder()
	h.NewMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 without VaultRoot", rec.Code)
	}
}

func TestDashboardRendersDownloadLink(t *testing.T) {
	h := testHandlers()
	h.VaultRoot = seedExportVault(t)
	rec := get(t, h, "/dashboard?as=gm")
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `href="/dashboard/vault.zip"`) {
		t.Errorf("dashboard missing vault-zip download link")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
