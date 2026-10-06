package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
)

const welcomeMD = `---
title: Welcome to Ashfall
---

The town of Ashfall sits where the river meets the cinder plains.

A sealed reference: [[cinder-pact|The Pact]] and [[welcome|self]].

> [!secret]-
> GM-only margin note here.

> [!secret]+
> Bearers dream of crows.
`

const pactMD = `---
title: The Cinder Pact
secret: true
owner: mira
editable-by: [bram]
---

Signed at midnight, the pact binds the bearer.

> [!secret]-
> The ember is hidden under the third flagstone.

![[welcome]]
`

const miraMD = `---
title: Mira
secret: true
owner: mira
---

Mira's sheet.
`

const brokenMD = `---
title: [unclosed
secret: true
owner: mira

Body with broken frontmatter.
`

// fakeStore is a dumb in-memory Store: Search does plain substring matching
// with NO acl filtering, so handler-level filtering is what the tests prove.
type fakeStore struct {
	pages     map[string]*store.Page
	backlinks map[string][]string
	forward   map[string][]string
}

func newFakeStore() *fakeStore {
	mk := func(p, title, content string) *store.Page {
		return &store.Page{Path: p, Title: title, Content: content}
	}
	fs := &fakeStore{
		pages: map[string]*store.Page{
			"welcome.md":         mk("welcome.md", "Welcome to Ashfall", welcomeMD),
			"cinder-pact.md":     mk("cinder-pact.md", "The Cinder Pact", pactMD),
			"characters/mira.md": mk("characters/mira.md", "Mira", miraMD),
			"broken.md":          mk("broken.md", "broken", brokenMD),
		},
		backlinks: map[string][]string{
			"cinder-pact.md": {"welcome.md"},
			"welcome.md":     {"cinder-pact.md"},
		},
		forward: map[string][]string{
			"welcome.md":     {"cinder-pact.md"},
			"cinder-pact.md": {"welcome.md"},
		},
	}
	// Mirror frontmatter-derived ACL onto index rows (as the indexer would).
	// Quarantined pages fail closed upstream: Parse forces Secret=true with
	// an empty owner (GM-only), so the index row says the same.
	fs.pages["cinder-pact.md"].Secret = true
	fs.pages["cinder-pact.md"].Owner = "mira"
	fs.pages["cinder-pact.md"].EditableBy = []string{"bram"}
	fs.pages["characters/mira.md"].Secret = true
	fs.pages["characters/mira.md"].Owner = "mira"
	fs.pages["broken.md"].Secret = true
	fs.pages["broken.md"].Owner = ""
	return fs
}

func (f *fakeStore) PageGet(_ context.Context, p string) (*store.Page, error) {
	for k, v := range f.pages {
		if strings.EqualFold(k, p) {
			return v, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (f *fakeStore) PageList(_ context.Context, opts store.PageListOptions) ([]*store.Page, error) {
	var out []*store.Page
	for _, p := range f.pages {
		if opts.Prefix != "" && !strings.HasPrefix(p.Path, opts.Prefix) {
			continue
		}
		if !opts.IncludeSecret && p.Secret {
			continue
		}
		out = append(out, p)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) PageUpsert(_ context.Context, _ *store.Page) error { return nil }
func (f *fakeStore) PageDelete(_ context.Context, _ string) error      { return nil }

func (f *fakeStore) Search(_ context.Context, query string, opts store.SearchOptions) ([]*store.SearchResult, error) {
	q := strings.ToLower(query)
	var out []*store.SearchResult
	for _, p := range f.pages {
		if strings.Contains(strings.ToLower(p.Title+"\n"+p.Content), q) {
			out = append(out, &store.SearchResult{
				Path: p.Path, Title: p.Title, Snippet: "…match…", Secret: p.Secret,
			})
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) Backlinks(_ context.Context, p string) ([]string, error) {
	return f.backlinks[p], nil
}
func (f *fakeStore) ForwardLinks(_ context.Context, p string) ([]string, error) {
	return f.forward[p], nil
}
func (f *fakeStore) AssetGet(_ context.Context, _ string) (*store.Asset, error) {
	return nil, sql.ErrNoRows
}
func (f *fakeStore) AssetList(_ context.Context, _ store.AssetListOptions) ([]*store.Asset, error) {
	return nil, nil
}
func (f *fakeStore) AssetUpsert(_ context.Context, _ *store.Asset) error { return nil }
func (f *fakeStore) AssetDelete(_ context.Context, _ string) error       { return nil }
func (f *fakeStore) Transact(_ context.Context, fn func(tx store.Store) error) error {
	return fn(f)
}
func (f *fakeStore) IndexDB() *sql.DB { return nil }
func (f *fakeStore) AppDB() *sql.DB   { return nil }
func (f *fakeStore) Close() error     { return nil }

func testHandlers() *ReadHandlers {
	return &ReadHandlers{Store: newFakeStore(), SlotRegistry: NewSlotRegistry()}
}

func get(t *testing.T, h *ReadHandlers, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := h.NewMux()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestGuestReadsNonSecret(t *testing.T) {
	rec := get(t, testHandlers(), "/p/welcome.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Welcome to Ashfall", "Bearers dream of crows", "[secret − hidden]", "▓▓ redacted: secret page"} {
		if !strings.Contains(body, want) {
			t.Errorf("guest page missing %q", want)
		}
	}
	for _, leak := range []string{"GM-only margin note", "The Pact</a>", "Cinder Pact"} {
		if strings.Contains(body, leak) {
			t.Errorf("guest page leaked %q", leak)
		}
	}
	if ct := rec.Header().Get("Cache-Control"); !strings.Contains(ct, "no-store") {
		t.Errorf("per-viewer page must be no-store, got %q", ct)
	}
}

func TestGuestBlockedOnSecretUniform404(t *testing.T) {
	h := testHandlers()
	secret := get(t, h, "/p/cinder-pact.md")
	missing := get(t, h, "/p/no-such-page.md")
	if secret.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d/%d, want 404/404", secret.Code, missing.Code)
	}
	if secret.Body.String() != missing.Body.String() {
		t.Errorf("secret 404 differs from missing 404 (oracle)")
	}
	for _, leak := range []string{"Cinder Pact", "ember", "flagstone", "mira"} {
		if strings.Contains(strings.ToLower(secret.Body.String()), strings.ToLower(leak)) {
			t.Errorf("secret 404 leaked %q", leak)
		}
	}
	// Other player blocked too.
	if rec := get(t, h, "/p/cinder-pact.md?as=cass"); rec.Code != http.StatusNotFound {
		t.Errorf("other-player status = %d, want 404", rec.Code)
	}
}

func TestOwnerAndGMReadSecret(t *testing.T) {
	h := testHandlers()
	for _, as := range []string{"mira", "gm", "bram"} {
		rec := get(t, h, "/p/cinder-pact.md?as="+as)
		if rec.Code != http.StatusOK {
			t.Fatalf("as=%s status = %d, want 200", as, rec.Code)
		}
	}
	owner := get(t, h, "/p/cinder-pact.md?as=mira").Body.String()
	if !strings.Contains(owner, "third flagstone") {
		t.Errorf("owner must see `-` block content")
	}
	// Editable-by sees the page AND `-` blocks (p03; gate-amended Phase 2).
	ed := get(t, h, "/p/cinder-pact.md?as=bram").Body.String()
	if !strings.Contains(ed, "third flagstone") {
		t.Errorf("editable-by must see `-` block content")
	}
	if !strings.Contains(ed, "Signed at midnight") {
		t.Errorf("editable-by must see non-secret body")
	}
	gm := get(t, h, "/p/cinder-pact.md?as=gm").Body.String()
	if !strings.Contains(gm, "third flagstone") {
		t.Errorf("GM must see `-` block content")
	}
}

func TestPreviewAsFiltersLikePlayer(t *testing.T) {
	h := testHandlers()
	rec := get(t, h, "/p/cinder-pact.md?as=gm&preview_as=cass")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("preview-as-other status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "GM preview as cass") {
		t.Errorf("preview banner missing on 404 page")
	}
	rec = get(t, h, "/p/welcome.md?as=gm&preview_as=cass")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview open-page status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "GM preview as cass") {
		t.Errorf("preview banner missing on page")
	}
}

func TestSearchHidesSecretTitles(t *testing.T) {
	h := testHandlers()
	guest := get(t, h, "/search?q=ember")
	if guest.Code != http.StatusOK {
		t.Fatalf("status = %d", guest.Code)
	}
	if strings.Contains(guest.Body.String(), "Cinder") {
		t.Errorf("guest search leaked secret title")
	}
	owner := get(t, h, "/search?q=ember&as=mira")
	if !strings.Contains(owner.Body.String(), "Cinder Pact") {
		t.Errorf("owner search must find own secret page")
	}
	// JSON API likewise filtered.
	req := httptest.NewRequest(http.MethodGet, "/search?q=ember&as=cass", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.NewMux().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "Cinder") {
		t.Errorf("JSON search leaked secret title to other player")
	}
}

func TestGraphAndAutocompleteFiltered(t *testing.T) {
	h := testHandlers()
	guest := get(t, h, "/graph")
	for _, leak := range []string{"cinder-pact", "Cinder Pact", "characters/mira", "Mira"} {
		if strings.Contains(guest.Body.String(), leak) {
			t.Errorf("guest graph leaked %q: %s", leak, guest.Body.String())
		}
	}
	gm := get(t, h, "/graph?as=gm")
	if !strings.Contains(gm.Body.String(), "cinder-pact") {
		t.Errorf("GM graph must include secret nodes")
	}
	ac := get(t, h, "/autocomplete?q=Cinder")
	if strings.Contains(ac.Body.String(), "Cinder") {
		t.Errorf("guest autocomplete leaked secret title")
	}
}

func TestTraversalRejected(t *testing.T) {
	h := testHandlers()
	// Go's ServeMux sanitizes `/p/../app.db` with a redirect before the
	// handler runs; the redirect target must itself 404 (no vault escape).
	rec := get(t, h, "/p/../app.db")
	if rec.Code == http.StatusMovedPermanently || rec.Code == http.StatusTemporaryRedirect {
		loc := rec.Header().Get("Location")
		if loc != "/app.db" {
			t.Fatalf("sanitized redirect = %q, want /app.db", loc)
		}
		if rec2 := get(t, h, loc); rec2.Code != http.StatusNotFound {
			t.Errorf("redirect target %s status = %d, want 404", loc, rec2.Code)
		}
	} else if rec.Code != http.StatusNotFound {
		t.Errorf("/p/../app.db status = %d, want 404/307", rec.Code)
	}
	for _, target := range []string{"/assets/welcome.md", "/assets/../app.db"} {
		rec := get(t, h, target)
		switch rec.Code {
		case http.StatusNotFound:
		case http.StatusMovedPermanently, http.StatusTemporaryRedirect:
			// Mux path sanitization: the cleaned target must itself 404.
			if rec2 := get(t, h, rec.Header().Get("Location")); rec2.Code != http.StatusNotFound {
				t.Errorf("%s redirect target status = %d, want 404", target, rec2.Code)
			}
		default:
			t.Errorf("%s status = %d, want 404", target, rec.Code)
		}
	}
	// Unit-level: cleanPagePath rejects traversal outright.
	for _, bad := range []string{"../x", "..", "a/../../b", "\\..\\x"} {
		if _, ok := cleanPagePath(bad); ok {
			t.Errorf("cleanPagePath(%q) accepted", bad)
		}
	}
}

func TestAssetsBundleRule(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vault, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault, "secretbundle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "notes", "pub.png"), []byte("\x89PNG\r\n\x1a\n0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "secretbundle", "hide.png"), []byte("\x89PNG\r\n\x1a\n0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := VaultDir
	VaultDir = vault
	defer func() { VaultDir = old }()

	h := testHandlers()
	// Index rows: public note beside pub.png, secret page beside hide.png.
	h.Store.(*fakeStore).pages["notes/pub.md"] = &store.Page{Path: "notes/pub.md", Title: "Pub", Content: "hi"}
	h.Store.(*fakeStore).pages["secretbundle/keep.md"] = &store.Page{Path: "secretbundle/keep.md", Title: "Keep", Content: "shh", Secret: true, Owner: "mira"}

	if rec := get(t, h, "/assets/notes/pub.png"); rec.Code != http.StatusOK {
		t.Errorf("public asset status = %d, want 200", rec.Code)
	}
	if rec := get(t, h, "/assets/secretbundle/hide.png"); rec.Code != http.StatusNotFound {
		t.Errorf("secret-bundle asset (guest) status = %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/assets/secretbundle/hide.png?as=gm"); rec.Code != http.StatusOK {
		t.Errorf("secret-bundle asset (GM) status = %d, want 200", rec.Code)
	}
	if rec := get(t, h, "/assets/secretbundle/hide.png?as=mira"); rec.Code != http.StatusOK {
		t.Errorf("secret-bundle asset (owner) status = %d, want 200", rec.Code)
	}
}

func TestQuarantineFailsClosed(t *testing.T) {
	h := testHandlers()
	if rec := get(t, h, "/p/broken.md"); rec.Code != http.StatusNotFound {
		t.Errorf("guest broken-frontmatter status = %d, want 404", rec.Code)
	}
	gm := get(t, h, "/p/broken.md?as=gm")
	if gm.Code != http.StatusOK {
		t.Fatalf("GM broken-frontmatter status = %d, want 200", gm.Code)
	}
	if !strings.Contains(gm.Body.String(), "quarantined") {
		t.Errorf("GM must see quarantine banner")
	}
}

func TestPublishFlipAuth(t *testing.T) {
	h := testHandlers()
	for _, target := range []string{"/events", "/events?as=cass", "/events?as=gm&preview_as=cass"} {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(url.Values{"path": {"welcome.md"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.NewMux().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403", target, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/events?as=gm", strings.NewReader(url.Values{"path": {"welcome.md"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.NewMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("GM POST /events = %d, want 202", rec.Code)
	}
}

// TestSSEFlipPerViewer connects two subscribers (guest + owner) to a secret
// page, broadcasts a flip, and asserts per-viewer filtering on the stream.
func TestSSEFlipPerViewer(t *testing.T) {
	h := testHandlers()
	ctxGuest, cancelGuest := context.WithCancel(context.Background())
	defer cancelGuest()
	ctxOwner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()

	recGuest := httptest.NewRecorder()
	recOwner := httptest.NewRecorder()
	doneGuest := make(chan struct{})
	doneOwner := make(chan struct{})
	go func() {
		defer close(doneGuest)
		req := httptest.NewRequest(http.MethodGet, "/events?path=cinder-pact.md", nil).WithContext(ctxGuest)
		h.NewMux().ServeHTTP(recGuest, req)
	}()
	go func() {
		defer close(doneOwner)
		req := httptest.NewRequest(http.MethodGet, "/events?path=cinder-pact.md&as=mira", nil).WithContext(ctxOwner)
		h.NewMux().ServeHTTP(recOwner, req)
	}()

	waitFor := func(rec *httptest.ResponseRecorder, want string) string {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if s := rec.Body.String(); strings.Contains(s, want) {
				return s
			}
			time.Sleep(10 * time.Millisecond)
		}
		return rec.Body.String()
	}
	if s := waitFor(recGuest, "event: hello"); !strings.Contains(s, "event: hello") {
		t.Fatalf("guest never got hello: %q", s)
	}
	if s := waitFor(recOwner, "event: hello"); !strings.Contains(s, "event: hello") {
		t.Fatalf("owner never got hello: %q", s)
	}
	// Hello is per-viewer filtered: guest sees no title, owner does.
	if s := recGuest.Body.String(); strings.Contains(s, "Cinder Pact") {
		t.Errorf("guest hello leaked secret title")
	}
	if s := recOwner.Body.String(); !strings.Contains(s, "Cinder Pact") {
		t.Errorf("owner hello missing title: %q", s)
	}

	hub.publish("cinder-pact.md")
	if s := waitFor(recGuest, "secret-flip"); !strings.Contains(s, "secret-flip") {
		t.Fatalf("guest never got flip: %q", s)
	}
	if s := waitFor(recOwner, "secret-flip"); !strings.Contains(s, "secret-flip") {
		t.Fatalf("owner never got flip: %q", s)
	}
	// Flip payloads stay per-viewer.
	guestEvents := recGuest.Body.String()
	if strings.Contains(guestEvents[strings.LastIndex(guestEvents, "secret-flip"):], "Cinder Pact") {
		t.Errorf("guest flip payload leaked secret title")
	}
	ownerEvents := recOwner.Body.String()
	if !strings.Contains(ownerEvents[strings.LastIndex(ownerEvents, "secret-flip"):], "Cinder Pact") {
		t.Errorf("owner flip payload missing title")
	}
	cancelGuest()
	cancelOwner()
	<-doneGuest
	<-doneOwner
}

func TestSlotRegistryFiltering(t *testing.T) {
	reg := NewSlotRegistry()
	reg.Register("header-right", SlotComponent{ID: "user", Component: "user", Priority: 1})
	reg.Register("header-right", SlotComponent{ID: "gm-tools", Component: "gm", Priority: 10, GMOnly: true})
	reg.Register("header-right", SlotComponent{ID: "inner", Component: "in", Priority: 5, ShowIf: ShowCondition{Grants: []string{"group:inner"}}})

	guestGot := reg.Get("header-right", nil, "welcome.md")
	if len(guestGot) != 1 || guestGot[0].ID != "user" {
		t.Errorf("guest slots = %+v, want [user]", guestGot)
	}
	gmGot := reg.Get("header-right", &auth.Viewer{UserID: "gm", IsGM: true}, "welcome.md")
	if len(gmGot) != 2 || gmGot[0].ID != "gm-tools" || gmGot[1].ID != "user" {
		t.Errorf("gm slots = %+v, want [gm-tools user] by priority", gmGot)
	}
	previewGot := reg.Get("header-right", &auth.Viewer{UserID: "gm", IsGM: true, PreviewAs: "cass"}, "welcome.md")
	if len(previewGot) != 1 || previewGot[0].ID != "user" {
		t.Errorf("preview slots = %+v, want [user] (GM tools hidden in preview)", previewGot)
	}
	grantGot := reg.Get("header-right", &auth.Viewer{UserID: "x", Grants: []string{"group:inner"}}, "welcome.md")
	if len(grantGot) != 2 {
		t.Errorf("grant slots = %+v, want [user inner]", grantGot)
	}
}
