package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
)

// B3: the ?as= demo tier is gated by DemoAuth (serve --demo-auth,
// default true). Both modes are covered here: default honors ?as=,
// false ignores it entirely (sessions only).

func withDemoAuth(t *testing.T, v bool) {
	t.Helper()
	old := DemoAuth
	DemoAuth = v
	t.Cleanup(func() { DemoAuth = old })
}

func TestDemoAuthDefaultHonorsAs(t *testing.T) {
	withDemoAuth(t, true)
	h := testHandlers()
	// Demo GM reads the secret page; the owner path works too.
	if rec := get(t, h, "/p/cinder-pact.md?as=gm"); rec.Code != http.StatusOK {
		t.Errorf("?as=gm status = %d, want 200", rec.Code)
	}
	if rec := get(t, h, "/dashboard?as=gm"); rec.Code != http.StatusOK {
		t.Errorf("dashboard ?as=gm status = %d, want 200", rec.Code)
	}
}

func TestDemoAuthFalseIgnoresAs(t *testing.T) {
	withDemoAuth(t, false)
	h := testHandlers()
	mux := h.NewMux()
	serve := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	// ?as=gm is now a guest: secret page is a uniform 404, dashboard 403.
	secret := serve("/p/cinder-pact.md?as=gm")
	missing := serve("/p/no-such-page.md")
	if secret.Code != http.StatusNotFound {
		t.Errorf("secret ?as=gm = %d, want 404", secret.Code)
	}
	if secret.Body.String() != missing.Body.String() {
		t.Errorf("secret 404 differs from missing 404 (oracle)")
	}
	if rec := serve("/dashboard?as=gm"); rec.Code != http.StatusForbidden {
		t.Errorf("dashboard ?as=gm = %d, want 403", rec.Code)
	}
	// Open pages still read as guest.
	if rec := serve("/p/welcome.md?as=gm"); rec.Code != http.StatusOK {
		t.Errorf("open page ?as=gm = %d, want 200", rec.Code)
	}
	// preview_as rides along with ?as= and dies with it.
	if rec := serve("/p/welcome.md?as=gm&preview_as=cass"); rec.Code != http.StatusOK {
		t.Errorf("preview URL = %d, want 200 (as guest)", rec.Code)
	}
}

func TestDemoAuthFalseSessionStillAuthenticates(t *testing.T) {
	withDemoAuth(t, false)
	r := httptest.NewRequest(http.MethodGet, "/p/welcome.md?as=gm", nil)
	r = r.WithContext(auth.WithViewer(r.Context(), &auth.Viewer{UserID: "gm", IsGM: true}))
	if v := ViewerForRequest(r); v == nil || !v.IsGM {
		t.Errorf("session GM lost under DemoAuth=false: %+v", v)
	}
	// And a session viewer survives an ?as= override attempt untouched.
	r2 := httptest.NewRequest(http.MethodGet, "/p/welcome.md?as=cass", nil)
	r2 = r2.WithContext(auth.WithViewer(r2.Context(), &auth.Viewer{UserID: "gm", IsGM: true}))
	if v := ViewerForRequest(r2); v == nil || v.UserID != "gm" {
		t.Errorf("session viewer overridden by ?as=: %+v", v)
	}
}
