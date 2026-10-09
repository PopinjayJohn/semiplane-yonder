package web

// Lane L verification (Phase 4, P09): every per-viewer HTML/JSON response
// must carry `Cache-Control: private, no-store` so shared/proxy caches on
// table LANs can never store one viewer's secrets for another. Read-path
// shell/search/graph/assets/SSE coverage predates this lane (see
// TestGuestPageFilteredNoLeak for the read-path assertion); this file pins
// the sheet/wizard/write funnel (writeHTML) plus the sheet JSON branch.
//
// Tests only: no product-code change beyond the two header lines.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
)

func assertNoStore(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("%s: Cache-Control = %q, want per-viewer no-store", name, cc)
	}
}

// Sheet funnel: /me with no characters renders the writeHTML "no sheet"
// page for a logged-in viewer.
func TestLaneLMeSheetNoStore(t *testing.T) {
	sh, _, _ := testSheets(t)
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(req, gm()))
	if rec.Code != http.StatusOK {
		t.Fatalf("MeSheet = %d, want 200", rec.Code)
	}
	assertNoStore(t, "MeSheet", rec)
}

// Wizard funnel: GM setup form renders via writeHTML.
func TestLaneLSetupFormNoStore(t *testing.T) {
	wh, _, _ := testWizard(t)
	req := httptest.NewRequest(http.MethodGet, "/wizard/setup", nil)
	rec := httptest.NewRecorder()
	wh.SetupForm(rec, withViewer(req, gm()))
	if rec.Code != http.StatusOK {
		t.Fatalf("SetupForm = %d, want 200", rec.Code)
	}
	assertNoStore(t, "SetupForm", rec)
}

// Write funnel: editor GET renders the editable page via writeHTML.
func TestLaneLEditFormNoStore(t *testing.T) {
	wh, v, s := testSetup(t)
	ctx := context.Background()
	content := "---\ntitle: Notes\nowner: alice\n---\n\nhello\n"
	if err := v.WriteFile(ctx, "notes.md", content); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, "notes.md", "Notes", false, "alice", nil)
	req := httptest.NewRequest(http.MethodGet, "/p/notes.md/edit", nil)
	rec := httptest.NewRecorder()
	wh.PageEdit(rec, withViewer(req, gm()))
	if rec.Code != http.StatusOK {
		t.Fatalf("PageEdit = %d, want 200 (%s)", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	assertNoStore(t, "PageEdit", rec)
}

// Denied funnel: writeDenied rides writeHTML, so even 403s are no-store.
func TestLaneLDeniedNoStore(t *testing.T) {
	wh, _, _ := testSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/p/notes.md/edit", nil)
	rec := httptest.NewRecorder()
	wh.PageEdit(rec, withViewer(req, bob()))
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("PageEdit for stranger = %d, want deny", rec.Code)
	}
	assertNoStore(t, "writeDenied", rec)
}

// Sheet JSON branch: PATCH /characters/<id>/fields answers JSON with
// per-viewer state; it must be no-store too.
func TestLaneLFieldsPatchJSONNoStore(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	// Wire the alice session the rig understands: SheetRouter resolves the
	// viewer from context in tests (session cookie + CSRF for the mutating
	// route).
	sh.SessionStore.(*stubSessionStore).sessions["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"] = &auth.Session{
		ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", UserID: "alice", CSRFToken: testCSRF,
		CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	req := httptest.NewRequest(http.MethodPatch, "/characters/alice/fields", strings.NewReader(`{"field":"hp","adjust":-1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(auth.CSRFHeaderName, testCSRF)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName,
		Value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|alice|9999999999|sig"})
	rec := httptest.NewRecorder()
	sh.SheetRouter(rec, withViewer(req, alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("FieldsPatch = %d, want 200 (%s)", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	assertNoStore(t, "FieldsPatch JSON", rec)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
