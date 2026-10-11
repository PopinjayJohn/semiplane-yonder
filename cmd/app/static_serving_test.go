package main

// W3: /static/core + /static/vendor serving. The embed FS (cmd/app/static)
// holds verbatim copies of the web/static sources — go:embed cannot reach
// outside the package dir, so a file-equality test pins the copies (same
// pattern as the sheetCSS inline copy pinned by TestSheetCSSMatchesFile in
// internal/web). HTTP tests prove the sheet <link> and the CodeMirror hook
// resolve with the runbook §5 cache directives; traversal/listings 404.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
)

var w3StaticPairs = []struct{ src, embedded string }{
	{"web/static/core/sheet.css", "static/core/sheet.css"},
	{"web/static/vendor/codemirror.js", "static/vendor/codemirror.js"},
	{"web/static/vendor/datastar.js", "static/vendor/datastar.js"},
}

// Embedded copies must stay byte-identical to the web/static sources.
// Edit the source first, then copy it over.
func TestW3StaticCopiesMatchSources(t *testing.T) {
	for _, p := range w3StaticPairs {
		src, err := os.ReadFile(filepath.Join("..", "..", p.src))
		if err != nil {
			t.Fatalf("read source %s: %v", p.src, err)
		}
		got, err := staticFS.ReadFile(p.embedded)
		if err != nil {
			t.Fatalf("read embedded %s: %v", p.embedded, err)
		}
		if string(got) != string(src) {
			t.Errorf("embedded %s drifted from %s (copy the file verbatim)", p.embedded, p.src)
		}
	}
}

func TestW3StaticServing(t *testing.T) {
	core := staticDirHandler("/static/core/", "static/core", staticCoreCache)
	vendor := staticDirHandler("/static/vendor/", "static/vendor", staticVendorCache)

	get := func(h http.Handler, method, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		return rec
	}

	// Sheet stylesheet: CSS type, day-long public cache, nosniff.
	rec := get(core, http.MethodGet, "/static/core/sheet.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("core sheet.css = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("core content-type = %q, want text/css", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != staticCoreCache {
		t.Errorf("core Cache-Control = %q, want %q", cc, staticCoreCache)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("core missing X-Content-Type-Options: nosniff")
	}
	if !strings.Contains(rec.Body.String(), ".sheet") {
		t.Error("core sheet.css body missing sheet rules")
	}

	// Vendor bundles: JS type, immutable year-long cache.
	for _, target := range []string{"/static/vendor/codemirror.js", "/static/vendor/datastar.js"} {
		rec := get(vendor, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", target, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("%s content-type = %q, want javascript", target, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != staticVendorCache {
			t.Errorf("%s Cache-Control = %q, want %q", target, cc, staticVendorCache)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s cache missing immutable", target)
		}
	}

	// Served vendor bytes must match the VERSIONS pins (`yonder vendor
	// verify` pins the files; this pins what we actually serve).
	pins := map[string]string{}
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "vendor", "VERSIONS"))
	if err != nil {
		t.Fatalf("read VERSIONS: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 4 || strings.HasPrefix(f[0], "#") {
			continue
		}
		for _, extra := range f[4:] {
			if file, ok := strings.CutPrefix(extra, "file="); ok {
				pins[file] = f[2]
			}
		}
	}
	for file, want := range pins {
		rec := get(vendor, http.MethodGet, "/static/vendor/"+file)
		if rec.Code != http.StatusOK {
			t.Fatalf("pinned vendor %s = %d, want 200", file, rec.Code)
		}
		sum := sha256.Sum256(rec.Body.Bytes())
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
			t.Errorf("served %s sha256 = %s, want pinned %s", file, got, want)
		}
	}

	// Directory roots, unknown files, traversal escapes, and non-read
	// methods never serve bytes.
	for _, tc := range []struct {
		h      http.Handler
		method string
		target string
		want   int
	}{
		{core, http.MethodGet, "/static/core/", http.StatusNotFound},
		{vendor, http.MethodGet, "/static/vendor/", http.StatusNotFound},
		{core, http.MethodGet, "/static/core/nope.css", http.StatusNotFound},
		{vendor, http.MethodGet, "/static/vendor/nope.js", http.StatusNotFound},
		{core, http.MethodGet, "/static/core/../print.css", http.StatusNotFound},
		{vendor, http.MethodGet, "/static/vendor/../../go.mod", http.StatusNotFound},
		{core, http.MethodPost, "/static/core/sheet.css", http.StatusMethodNotAllowed},
		{vendor, http.MethodPost, "/static/vendor/codemirror.js", http.StatusMethodNotAllowed},
	} {
		if rec := get(tc.h, tc.method, tc.target); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.target, rec.Code, tc.want)
		}
	}
}

// The carve-out covers exactly the embedded public subtrees: registry and
// vault-adjacent paths (and unknown paths, which keep the styled 404) must
// not divert to the ops mux.
func TestW3IsPublicStatic(t *testing.T) {
	for _, p := range []string{
		"/static/print.css",
		"/static/core", "/static/core/",
		"/static/core/sheet.css",
		"/static/vendor", "/static/vendor/",
		"/static/vendor/codemirror.js", "/static/vendor/datastar.js",
	} {
		if !isPublicStatic(p) {
			t.Errorf("isPublicStatic(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"/", "/healthz", "/version", "/p/welcome.md", "/assets/x.png",
		"/bogus", "/static/other.css", "/static/core-evil/sheet.css",
	} {
		if isPublicStatic(p) {
			t.Errorf("isPublicStatic(%q) = true, want false", p)
		}
	}
}

// End-to-end over HTTP through the real mux: the sheet <link> and the
// CodeMirror hook resolve (not just the handler in isolation), while the
// print.css mount keeps working.
func TestW3StaticOverHTTP(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "welcome.md"), []byte("---\ntitle: Welcome\n---\n\nHello.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "campaign.yaml"), []byte("name: W3 Static\ncreated: 2026-10-09T00:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	if err := runReindex(vault, dataDir); err != nil {
		t.Fatal(err)
	}
	db, _, err := openAppDB(dataDir, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessionStore, err := auth.NewSessionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(wireHandlers(buildInfo{Version: "w3"}, vault, dataDir, sessionStore).Handler)
	t.Cleanup(srv.Close)

	for target, want := range map[string][2]string{
		"/static/core/sheet.css":       {"text/css", staticCoreCache},
		"/static/vendor/codemirror.js": {"javascript", staticVendorCache},
		"/static/vendor/datastar.js":   {"javascript", staticVendorCache},
		"/static/print.css":            {"text/css", "public, max-age=86400"},
	} {
		resp, err := http.Get(srv.URL + target)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, want[0]) {
			t.Errorf("GET %s content-type = %q, want %q", target, ct, want[0])
		}
		if cc := resp.Header.Get("Cache-Control"); cc != want[1] {
			t.Errorf("GET %s Cache-Control = %q, want %q", target, cc, want[1])
		}
		_ = resp.Body.Close()
	}
	// Unknown static paths still 404 through the full mux.
	resp, err := http.Get(srv.URL + "/static/vendor/evil.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /static/vendor/evil.js = %d, want 404", resp.StatusCode)
	}
}
