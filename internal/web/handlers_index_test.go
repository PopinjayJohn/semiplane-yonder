package web

// UI-3 dead-route cleanup tests: exact "/" keeps the landing redirect,
// every other unregistered path 404s uniformly (byte-identical to the
// missing-page 404), and the retired /register + /settings routes stay
// dead with no shell links pointing at them.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndexExactRootRedirects(t *testing.T) {
	h := testHandlers()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.Index(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("/ = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/p/welcome" {
		t.Fatalf("/ Location = %q, want /p/welcome", loc)
	}
	// Demo identity survives the landing redirect.
	req = httptest.NewRequest(http.MethodGet, "/?as=gm", nil)
	rec = httptest.NewRecorder()
	h.Index(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/p/welcome?as=gm" {
		t.Fatalf("/?as=gm Location = %q, want /p/welcome?as=gm", loc)
	}
}

func TestIndexUnknownPaths404Uniformly(t *testing.T) {
	h := testHandlers()
	oracle := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		h.PageView(rec, req) // missing page → uniform 404
		return rec
	}
	missing := oracle("/p/no-such-page-zz9.md")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("oracle = %d, want 404", missing.Code)
	}
	for _, target := range []string{"/nope", "/register", "/settings", "/register?as=gm", "/nope?as=gm&preview_as=cass"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		h.Index(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, rec.Code)
			continue
		}
		// Oracle shares the viewer (identity chrome differs per viewer by
		// design; page content must not).
		query := ""
		if i := strings.IndexByte(target, '?'); i >= 0 {
			query = target[i:]
		}
		if rec.Body.String() != oracle("/p/no-such-page-zz9.md"+query).Body.String() {
			t.Errorf("%s 404 differs from missing-page 404 (oracle)", target)
		}
	}
}

func TestNoShellLinksToDeadRoutes(t *testing.T) {
	h := testHandlers()
	for _, target := range []string{"/dashboard?as=gm", "/p/welcome.md?as=gm", "/p/welcome.md"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		mux := h.NewMux()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", target, rec.Code)
		}
		for _, dead := range []string{`href="/settings"`, `href="/register"`, `action="/settings"`, `action="/register"`} {
			if strings.Contains(rec.Body.String(), dead) {
				t.Errorf("%s shell still links to dead route %s", target, dead)
			}
		}
	}
}
