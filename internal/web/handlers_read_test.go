package web

import (
	"context"
	"database/sql"
	"encoding/json"
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
			// Raw indexer shape: extensionless wikilink targets, exactly
			// as the links table stores them (B5: the graph handler
			// normalizes these; extensioned fakes masked the live bug).
			"welcome.md":     {"cinder-pact"},
			"cinder-pact.md": {"welcome"},
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
func (f *fakeStore) IndexDB() *sql.DB                   { return nil }
func (f *fakeStore) AppDB() *sql.DB                     { return nil }
func (f *fakeStore) Close() error                       { return nil }
func (f *fakeStore) RefreshIndex(context.Context) error { return nil }

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

// getJSON is get with an explicit JSON Accept: /graph (like /search)
// content-negotiates, serving HTML to browsers by default.
func getJSON(t *testing.T, h *ReadHandlers, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := h.NewMux()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept", "application/json")
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

// TestEmbedTransportedSecretLinkRedacted pins the B0 leak (2026-10-10): links
// arriving via an expanded `![[page]]` transclusion must be secret-checked and
// route-rewritten exactly like body links. The links pass used to run before
// embed expansion, so an embedded page's `[[cinder-pact|The Pact]]` rendered
// raw — clickable, alias intact, relative href — to guests. It also pins that
// repeated same-target links each keep their own alias (the old first-match
// loop rebuilt the first anchor twice, alias-corrupting duplicates).
func TestEmbedTransportedSecretLinkRedacted(t *testing.T) {
	h := testHandlers()
	fs := h.Store.(*fakeStore)
	fs.pages["lore.md"] = &store.Page{Path: "lore.md", Title: "Lands Around Ashfall",
		Content: "---\ntitle: Lands Around Ashfall\n---\n\nEast of [[welcome|Ashfall]] lie songs of [[cinder-pact|The Pact]].\n"}
	fs.pages["outer.md"] = &store.Page{Path: "outer.md", Title: "Outer",
		Content: "---\ntitle: Outer\n---\n\nTwice: [[welcome|first]] and [[welcome|second]].\n\n![[lore]]\n"}
	guest := get(t, h, "/p/outer.md").Body.String()
	if rec := get(t, h, "/p/outer.md"); rec.Code != http.StatusOK {
		t.Fatalf("guest status = %d, want 200", rec.Code)
	}
	for _, leak := range []string{"cinder-pact", "The Pact</a>", `href="cinder-pact"`, `href="welcome"`} {
		if strings.Contains(guest, leak) {
			t.Errorf("guest embed view leaked %q", leak)
		}
	}
	if !strings.Contains(guest, "▓▓ redacted: secret page") {
		t.Errorf("guest embed view missing redacted span")
	}
	for _, want := range []string{">first</a>", ">second</a>", ">Ashfall</a>", `href="/p/welcome"`} {
		if !strings.Contains(guest, want) {
			t.Errorf("guest embed view missing %q", want)
		}
	}
	// Owner sees the transported secret link routed, alias intact.
	owner := get(t, h, "/p/outer.md?as=mira").Body.String()
	if !strings.Contains(owner, "/p/cinder-pact") || !strings.Contains(owner, "The Pact</a>") {
		t.Errorf("owner embed view missing routed secret link")
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

// TestSessionGMPreviewAs pins the Phase-0c contract in production
// (DemoAuth=false): a session-authenticated GM passing ?preview_as= filters
// as that user with the persistent banner + server-side logging; non-GM
// preview_as is ignored (no filter, no banner). Each case flips per-case
// and restores via withDemoAuth (demo_auth_test.go).
func TestSessionGMPreviewAs(t *testing.T) {
	withDemoAuth(t, false)
	h := testHandlers()
	gmSess := &auth.Viewer{UserID: "gm", IsGM: true}

	// Secret owner page: previewing as an outsider 404s on the uniform
	// oracle — bannered.
	preview := serveAs(t, h, "/p/cinder-pact.md?preview_as=cass", gmSess)
	if preview.Code != http.StatusNotFound {
		t.Fatalf("session GM preview-as-cass on owner secret = %d, want 404", preview.Code)
	}
	missing := serveAs(t, h, "/p/no-such-page-zz9.md?preview_as=cass", gmSess)
	if preview.Body.String() != missing.Body.String() {
		t.Errorf("preview 404 differs from missing 404 (oracle)")
	}
	if !strings.Contains(preview.Body.String(), "GM preview as cass") {
		t.Errorf("session preview missing persistent banner on 404")
	}

	// Open page: 200 and bannered, but filtered as the previewed player —
	// the GM-only `-` block stays hidden.
	open := serveAs(t, h, "/p/welcome.md?preview_as=cass", gmSess)
	if open.Code != http.StatusOK {
		t.Fatalf("session GM preview open-page = %d, want 200", open.Code)
	}
	ob := open.Body.String()
	if !strings.Contains(ob, "GM preview as cass") {
		t.Errorf("session preview missing persistent banner")
	}
	if strings.Contains(ob, "GM-only margin note") {
		t.Errorf("session preview leaked GM-only block (must filter as cass)")
	}
	full := serveAs(t, h, "/p/welcome.md", gmSess)
	if !strings.Contains(full.Body.String(), "GM-only margin note") {
		t.Errorf("session GM without preview lost full content")
	}
	if strings.Contains(full.Body.String(), "GM preview as") {
		t.Errorf("non-preview render shows a preview banner")
	}

	// Non-GM preview_as is ignored: the owner keeps full content, no banner.
	owner := serveAs(t, h, "/p/cinder-pact.md?preview_as=cass", &auth.Viewer{UserID: "mira"})
	if owner.Code != http.StatusOK {
		t.Fatalf("owner with preview_as = %d, want 200", owner.Code)
	}
	if strings.Contains(owner.Body.String(), "GM preview as") {
		t.Errorf("non-GM preview_as must not banner")
	}
	if !strings.Contains(owner.Body.String(), "third flagstone") {
		t.Errorf("non-GM preview_as must not filter the owner")
	}

	// Guest preview_as is ignored: uniform 404, no banner.
	guest := serveAs(t, h, "/p/cinder-pact.md?preview_as=cass", nil)
	if guest.Code != http.StatusNotFound {
		t.Fatalf("guest with preview_as = %d, want 404", guest.Code)
	}
	if strings.Contains(guest.Body.String(), "GM preview as") {
		t.Errorf("guest preview_as must not banner")
	}

	// GM-only state change honors the preview: a previewing session GM
	// cannot broadcast flips (publishFlip forbids PreviewAs).
	req := httptest.NewRequest(http.MethodPost, "/events?preview_as=cass",
		strings.NewReader(url.Values{"path": {"welcome.md"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(auth.WithViewer(req.Context(), gmSess))
	rec := httptest.NewRecorder()
	h.NewMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("previewing GM POST /events = %d, want 403", rec.Code)
	}
}

// TestSessionGMPreviewAsDemoAuthTrueUnchanged pins shipped session behavior
// under the default tier: ?preview_as= on a session GM is ignored (full GM
// content, no banner) — the demo ?as= tier rules instead.
func TestSessionGMPreviewAsDemoAuthTrueUnchanged(t *testing.T) {
	withDemoAuth(t, true)
	h := testHandlers()
	rec := serveAs(t, h, "/p/cinder-pact.md?preview_as=cass", &auth.Viewer{UserID: "gm", IsGM: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("session GM with preview_as under DemoAuth=true = %d, want 200 (ignored)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "GM preview as") {
		t.Errorf("DemoAuth=true session render must not banner preview_as")
	}
	if !strings.Contains(rec.Body.String(), "third flagstone") {
		t.Errorf("DemoAuth=true session GM must keep full content")
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

// TestGraphEdgesResolveRawTargets pins the B5 fix: the links table stores
// extensionless targets (`cinder-pact`) while page IDs carry `.md`. Edges
// must resolve through the handler, for GM (both directions, secret node
// included) and for guests (open side only — secret endpoints stay absent,
// never redacted).
func TestGraphEdgesResolveRawTargets(t *testing.T) {
	h := testHandlers()
	decode := func(body string) (nodes []map[string]any, edges []map[string]string) {
		t.Helper()
		var doc struct {
			Nodes []map[string]any    `json:"nodes"`
			Edges []map[string]string `json:"edges"`
		}
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("graph is not JSON: %v", err)
		}
		return doc.Nodes, doc.Edges
	}
	hasEdge := func(edges []map[string]string, from, to string) bool {
		for _, e := range edges {
			if e["from"] == from && e["to"] == to {
				return true
			}
		}
		return false
	}
	gm := getJSON(t, h, "/graph?as=gm")
	_, gmEdges := decode(gm.Body.String())
	if !hasEdge(gmEdges, "welcome.md", "cinder-pact.md") {
		t.Errorf("GM graph missing welcome -> cinder-pact edge: %v", gmEdges)
	}
	if !hasEdge(gmEdges, "cinder-pact.md", "welcome.md") {
		t.Errorf("GM graph missing cinder-pact -> welcome edge: %v", gmEdges)
	}
	guest := getJSON(t, h, "/graph")
	_, guestEdges := decode(guest.Body.String())
	if len(guestEdges) != 0 {
		t.Errorf("guest graph must have no edges into the secret node, got %v", guestEdges)
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
	if err := os.MkdirAll(filepath.Join(vault, "lone"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "notes", "pub.png"), []byte("\x89PNG\r\n\x1a\n0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "secretbundle", "hide.png"), []byte("\x89PNG\r\n\x1a\n0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "lone", "orphan.png"), []byte("\x89PNG\r\n\x1a\n0123456789"), 0o644); err != nil {
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
	// Fail closed (RULING over fail-open): a directory holding zero pages
	// serves nothing to anyone — guest, owner of nothing, even the GM.
	for _, target := range []string{"/assets/lone/orphan.png", "/assets/lone/orphan.png?as=gm", "/assets/lone/orphan.png?as=mira"} {
		if rec := get(t, h, target); rec.Code != http.StatusNotFound {
			t.Errorf("page-less asset %s status = %d, want 404", target, rec.Code)
		}
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

// TestNavRoleAware pins the B1 IA: guests see Read only; players add
// Table/Characters; GMs add Dashboard; a GM previewing as a player sees
// exactly the player's nav (no Dashboard link).
func TestNavRoleAware(t *testing.T) {
	h := testHandlers()
	guest := get(t, h, "/p/welcome.md").Body.String()
	if !strings.Contains(guest, ">Read</a>") {
		t.Errorf("guest nav missing Read")
	}
	for _, leak := range []string{">Table</a>", ">Characters</a>", ">Dashboard</a>"} {
		if strings.Contains(guest, leak) {
			t.Errorf("guest nav leaked %q", leak)
		}
	}
	player := get(t, h, "/p/welcome.md?as=bram").Body.String()
	for _, want := range []string{">Read</a>", ">Table</a>", ">Characters</a>"} {
		if !strings.Contains(player, want) {
			t.Errorf("player nav missing %q", want)
		}
	}
	if strings.Contains(player, ">Dashboard</a>") {
		t.Errorf("player nav leaked Dashboard link")
	}
	gm := get(t, h, "/p/welcome.md?as=gm").Body.String()
	for _, want := range []string{">Table</a>", ">Characters</a>", ">Dashboard</a>"} {
		if !strings.Contains(gm, want) {
			t.Errorf("GM nav missing %q", want)
		}
	}
	preview := get(t, h, "/p/welcome.md?as=gm&preview_as=cass").Body.String()
	if strings.Contains(preview, ">Dashboard</a>") {
		t.Errorf("preview nav leaked Dashboard link")
	}
	for _, want := range []string{">Table</a>", ">Characters</a>"} {
		if !strings.Contains(preview, want) {
			t.Errorf("preview nav missing %q", want)
		}
	}
}

// TestGraphContentNegotiation pins the B1 HTML view: browsers get an HTML
// graph page reusing the filtered payload, JSON stays behind an explicit
// Accept, and secret titles stay absent for unauthorized viewers in both.
func TestGraphContentNegotiation(t *testing.T) {
	h := testHandlers()
	htmlGuest := get(t, h, "/graph")
	if ct := htmlGuest.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("default graph content-type = %q, want HTML", ct)
	}
	body := htmlGuest.Body.String()
	if !strings.Contains(body, "<h1>Graph</h1>") {
		t.Errorf("HTML graph missing heading")
	}
	for _, leak := range []string{"cinder-pact", "Cinder Pact", "characters/mira", "Mira"} {
		if strings.Contains(body, leak) {
			t.Errorf("guest HTML graph leaked %q", leak)
		}
	}
	htmlGM := get(t, h, "/graph?as=gm").Body.String()
	if !strings.Contains(htmlGM, "cinder-pact") || !strings.Contains(htmlGM, "The Cinder Pact") {
		t.Errorf("GM HTML graph missing secret node")
	}
	js := getJSON(t, h, "/graph?as=gm")
	if ct := js.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("JSON graph content-type = %q", ct)
	}
	var doc struct {
		Nodes []map[string]any    `json:"nodes"`
		Edges []map[string]string `json:"edges"`
	}
	if err := json.Unmarshal(js.Body.Bytes(), &doc); err != nil {
		t.Fatalf("JSON graph is not JSON: %v", err)
	}
	if len(doc.Nodes) == 0 {
		t.Errorf("JSON graph has no nodes for GM")
	}
}

// memVault is a read-only VaultWriter for character-page tests: the sheet
// embed must re-read vault truth, and unwired handlers carry no vault.
type memVault struct{ files map[string]string }

func (m *memVault) ReadFile(_ context.Context, p string) ([]byte, error) {
	for k, v := range m.files {
		if strings.EqualFold(k, p) {
			return []byte(v), nil
		}
	}
	return nil, os.ErrNotExist
}
func (m *memVault) WriteFile(_ context.Context, _, _ string) error { return os.ErrPermission }
func (m *memVault) WriteFileBytes(_ context.Context, _ string, _ []byte) error {
	return os.ErrPermission
}
func (m *memVault) DeleteFile(_ context.Context, _ string) error { return os.ErrPermission }
func (m *memVault) RenameFile(_ context.Context, _, _ string) error {
	return os.ErrPermission
}
func (m *memVault) ResolveConflict(_ context.Context, _, _ string) error {
	return os.ErrPermission
}

const miraSheetMD = `---
title: Mira Ashdown
secret: true
owner: mira
sheet:
  class: Fighter
  ancestry: Human
  background: Soldier
  level: 3
  xp: 100
  str: 14
  dex: 16
  con: 13
  int: 10
  wis: 12
  cha: 8
  hp: 24
  hp-max: 28
  hit-dice: 3
  hit-dice-max: 3
  hit-die: 10
---

Mira's story.
`

const bramSheetMD = `---
title: Bram
owner: bram
sheet:
  class: Fighter
  ancestry: Human
  background: Guard
  level: 1
  xp: 0
  str: 15
  dex: 12
  con: 14
  int: 10
  wis: 11
  cha: 9
  hp: 12
  hp-max: 12
  hit-dice: 1
  hit-dice-max: 1
  hit-die: 10
---

Bram's story.
`

// characterHandlers wires a fake store with two PC trees (secret Mira,
// open Bram) plus vault truth for both sheet roots.
func characterHandlers() *ReadHandlers {
	h := testHandlers()
	fs := h.Store.(*fakeStore)
	mk := func(p, title, content string, secret bool, owner string) *store.Page {
		return &store.Page{Path: p, Title: title, Content: content, Secret: secret, Owner: owner}
	}
	fs.pages["characters/mira/index.md"] = mk("characters/mira/index.md", "Mira Ashdown", miraSheetMD, true, "mira")
	fs.pages["characters/mira/journal.md"] = mk("characters/mira/journal.md", "Embers Journal",
		"---\ntitle: Embers Journal\nowner: mira\n---\n\nJournal entry.\n", false, "mira")
	fs.pages["characters/bram/index.md"] = mk("characters/bram/index.md", "Bram", bramSheetMD, false, "bram")
	fs.pages["characters/bram/log.md"] = mk("characters/bram/log.md", "Patrol Log",
		"---\ntitle: Patrol Log\nowner: bram\n---\n\nRounds.\n", false, "bram")
	fs.pages["characters/bram/secret-notes.md"] = mk("characters/bram/secret-notes.md", "Hidden Plans",
		"---\ntitle: Hidden Plans\nsecret: true\nowner: bram\n---\n\nSchemes.\n", true, "bram")
	h.Vault = &memVault{files: map[string]string{
		"characters/mira/index.md": miraSheetMD,
		"characters/bram/index.md": bramSheetMD,
	}}
	return h
}

// TestCharacterPageOwner pins B3 for the owner: + New subpage button (gated
// by the write path's authorizeWrite), the vault-truth sheet-header card,
// and the subpages list on her own secret character page.
func TestCharacterPageOwner(t *testing.T) {
	h := characterHandlers()
	rec := get(t, h, "/p/characters/mira/index?as=mira")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner char page status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "+ New subpage") {
		t.Errorf("owner char page missing New-subpage button")
	}
	if !strings.Contains(body, "/p/new?parent=characters%2Fmira") {
		t.Errorf("New-subpage button missing parent link")
	}
	for _, want := range []string{"Mira Ashdown", "Fighter", "HP 24/28", "Subpages", "Embers Journal"} {
		if !strings.Contains(body, want) {
			t.Errorf("owner char page missing %q", want)
		}
	}
	if !strings.Contains(body, `data-slot="sheet-header"`) {
		t.Errorf("owner char page missing sheet-header slot card")
	}
}

// TestCharacterPageAccess pins the B3 red lines: guests and other players
// get the uniform 404 on the secret character page (no button, no card,
// no subpage titles), and non-writers see an open character page with no
// button and no secret subpage titles.
func TestCharacterPageAccess(t *testing.T) {
	h := characterHandlers()
	secret := get(t, h, "/p/characters/mira/index")
	missing := get(t, h, "/p/no-such-page.md")
	if secret.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d/%d, want 404/404", secret.Code, missing.Code)
	}
	if secret.Body.String() != missing.Body.String() {
		t.Errorf("secret char 404 differs from missing 404 (oracle)")
	}
	// ("Embers Journal" is an OPEN subpage: it legitimately lists in the
	// 404 shell's vault nav like any open title. Only secret titles,
	// derived sheet values, and the button must stay absent.)
	for _, leak := range []string{"Mira Ashdown", "Fighter", "New subpage"} {
		if strings.Contains(secret.Body.String(), leak) {
			t.Errorf("secret char 404 leaked %q", leak)
		}
	}
	if rec := get(t, h, "/p/characters/mira/index?as=cass"); rec.Code != http.StatusNotFound {
		t.Errorf("other-player char status = %d, want 404", rec.Code)
	}
	// Open character page: guest reads, but the button stays hidden and
	// secret subpage titles stay dropped.
	guest := get(t, h, "/p/characters/bram/index")
	if guest.Code != http.StatusOK {
		t.Fatalf("guest open char status = %d, want 200", guest.Code)
	}
	gb := guest.Body.String()
	if strings.Contains(gb, "+ New subpage") {
		t.Errorf("guest must not see the New-subpage button")
	}
	if !strings.Contains(gb, "Patrol Log") {
		t.Errorf("guest must see the open subpage")
	}
	for _, leak := range []string{"Hidden Plans", "Schemes"} {
		if strings.Contains(gb, leak) {
			t.Errorf("guest char page leaked secret subpage %q", leak)
		}
	}
	// Non-owner reader on the open page: no button either.
	other := get(t, h, "/p/characters/bram/index?as=cass").Body.String()
	if strings.Contains(other, "+ New subpage") {
		t.Errorf("non-writer must not see the New-subpage button")
	}
	// Owner of the open tree sees the button.
	owner := get(t, h, "/p/characters/bram/index?as=bram").Body.String()
	if !strings.Contains(owner, "+ New subpage") {
		t.Errorf("owner must see the New-subpage button on own open page")
	}
	if !strings.Contains(owner, "Hidden Plans") {
		t.Errorf("owner must see own secret subpage title")
	}
}

// serveAs is get with an injected session viewer (real-auth path): the
// viewer rides the request context like the P11 middleware provides, so
// ?as= demo queries never override it.
func serveAs(t *testing.T, h *ReadHandlers, target string, v *auth.Viewer) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if v != nil {
		req = req.WithContext(auth.WithViewer(req.Context(), v))
	}
	rec := httptest.NewRecorder()
	h.NewMux().ServeHTTP(rec, req)
	return rec
}

// TestDemoAuthFalseSessionRenderHasCleanURLs pins the ?as= retirement:
// under DemoAuth=false a SESSION-authenticated render must not propagate
// the demo tier anywhere — no ?as=/preview_as in bodies, redirects, or
// wikilink hrefs — while the session itself keeps working (secret owner
// reads 200, GM dashboard 200). Each case flips per-case and restores via
// withDemoAuth (demo_auth_test.go).
func TestDemoAuthFalseSessionRenderHasCleanURLs(t *testing.T) {
	withDemoAuth(t, false)
	h := testHandlers()
	mira := &auth.Viewer{UserID: "mira"}
	rec := serveAs(t, h, "/p/cinder-pact.md?as=gm&preview_as=cass", mira)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner session under DemoAuth=false = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Server-rendered propagation tokens must be gone (?as= rides no href,
	// redirect, or shell chrome). Note: the shell's SSE bootstrap script
	// still echoes location.search client-side (its `&as=`); the server
	// ignores it while retired, and the script template is outside this
	// lane's file list — so it is pinned here as ignored, not absent.
	for _, leak := range []string{"?as=", "preview_as"} {
		if strings.Contains(body, leak) {
			t.Errorf("DemoAuth=false session render propagates %q", leak)
		}
	}
	if !strings.Contains(body, "/p/welcome") {
		t.Errorf("owner render lost the clean backlink href under DemoAuth=false")
	}
	// Landing redirect drops the demo identity for session viewers too.
	req := httptest.NewRequest(http.MethodGet, "/?as=gm&preview_as=cass", nil)
	req = req.WithContext(auth.WithViewer(req.Context(), &auth.Viewer{UserID: "gm", IsGM: true}))
	rr := httptest.NewRecorder()
	h.Index(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("/ = %d, want 303", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/p/welcome" {
		t.Errorf("session /?as=gm Location = %q, want /p/welcome (no propagation)", loc)
	}
	// Real-auth GM dashboard is unaffected by the retirement.
	if d := serveAs(t, h, "/dashboard?as=cass", &auth.Viewer{UserID: "gm", IsGM: true}); d.Code != http.StatusOK {
		t.Errorf("session GM dashboard under DemoAuth=false = %d, want 200", d.Code)
	}
	// Demo ?as=gm alone is now a guest: uniform 404, byte-identical oracle.
	secret := get(t, h, "/p/cinder-pact.md?as=gm")
	missing := get(t, h, "/p/no-such-page-zz9.md")
	if secret.Code != http.StatusNotFound {
		t.Fatalf("secret ?as=gm = %d, want 404", secret.Code)
	}
	if secret.Body.String() != missing.Body.String() {
		t.Errorf("secret 404 differs from missing 404 (oracle)")
	}
}

// TestDemoAuthTrueKeepsShippedBehavior pins the default tier untouched:
// demo identity authenticates, propagates across renders + redirect, and
// preview_as filters like the previewed player.
func TestDemoAuthTrueKeepsShippedBehavior(t *testing.T) {
	withDemoAuth(t, true)
	h := testHandlers()
	if rec := get(t, h, "/p/cinder-pact.md?as=gm"); rec.Code != http.StatusOK {
		t.Errorf("?as=gm secret = %d, want 200", rec.Code)
	}
	body := get(t, h, "/p/cinder-pact.md?as=mira").Body.String()
	if !strings.Contains(body, "?as=mira") {
		t.Errorf("DemoAuth=true owner render lost ?as= propagation")
	}
	req := httptest.NewRequest(http.MethodGet, "/?as=gm", nil)
	rr := httptest.NewRecorder()
	h.Index(rr, req)
	if loc := rr.Header().Get("Location"); loc != "/p/welcome?as=gm" {
		t.Errorf("/?as=gm Location = %q, want /p/welcome?as=gm", loc)
	}
	preview := get(t, h, "/p/cinder-pact.md?as=gm&preview_as=cass")
	if preview.Code != http.StatusNotFound {
		t.Errorf("GM preview-as-cass on owner secret = %d, want 404", preview.Code)
	}
}

// TestResolveGraphTargetShapes pins the handler-side edge-key workaround:
// raw stored targets resolve through extension candidates + the
// case-insensitive page-ID rule, while dangling/empty targets stay absent
// (never redacted, never leaked).
func TestResolveGraphTargetShapes(t *testing.T) {
	fold := map[string]string{
		"cinder-pact.md": "cinder-pact.md",
		"welcome.md":     "welcome.md",
		"lore.markdown":  "lore.markdown",
	}
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"cinder-pact", "cinder-pact.md", true},
		{"cinder-pact.md", "cinder-pact.md", true},
		{"CINDER-PACT", "cinder-pact.md", true},
		{"cinder-pact#sigil", "cinder-pact.md", true},
		{"./cinder-pact", "cinder-pact.md", true},
		{" ./cinder-pact ", "cinder-pact.md", true},
		{"lore", "lore.markdown", true},
		{"ghost-page", "", false},
		{"", "", false},
		{"   ", "", false},
		{"#frag", "", false},
	} {
		got, ok := resolveGraphTarget(fold, tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("resolveGraphTarget(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestGraphEdgesResolveRawTargetsExtended hardens the F2-filed edge-key
// coverage: fragment/case/relative variants of a live target still resolve
// for authorized viewers, dangling targets add no edges, and unauthorized
// viewers (guest AND other-player) see neither edges nor secret titles.
func TestGraphEdgesResolveRawTargetsExtended(t *testing.T) {
	fs := newFakeStore()
	fs.forward["welcome.md"] = []string{"cinder-pact", "Cinder-Pact#Sigil", "./cinder-pact", "ghost-page"}
	fs.forward["cinder-pact.md"] = []string{"welcome", "WELCOME"}
	h := &ReadHandlers{Store: fs, SlotRegistry: NewSlotRegistry()}
	decode := func(body string) (nodes []map[string]any, edges []map[string]string) {
		t.Helper()
		var doc struct {
			Nodes []map[string]any    `json:"nodes"`
			Edges []map[string]string `json:"edges"`
		}
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("graph is not JSON: %v", err)
		}
		return doc.Nodes, doc.Edges
	}
	hasEdge := func(edges []map[string]string, from, to string) bool {
		for _, e := range edges {
			if e["from"] == from && e["to"] == to {
				return true
			}
		}
		return false
	}
	gmNodes, gmEdges := decode(getJSON(t, h, "/graph?as=gm").Body.String())
	if !hasEdge(gmEdges, "welcome.md", "cinder-pact.md") {
		t.Errorf("GM graph missing welcome -> cinder-pact edge: %v", gmEdges)
	}
	if !hasEdge(gmEdges, "cinder-pact.md", "welcome.md") {
		t.Errorf("GM graph missing cinder-pact -> welcome edge: %v", gmEdges)
	}
	for _, e := range gmEdges {
		if e["to"] == "ghost-page" || e["from"] == "ghost-page" {
			t.Errorf("dangling target must not edge: %v", e)
		}
	}
	for _, n := range gmNodes {
		if n["id"] == "ghost-page" {
			t.Errorf("dangling target must not node")
		}
	}
	for _, target := range []string{"/graph", "/graph?as=cass"} {
		nodes, edges := decode(getJSON(t, h, target).Body.String())
		if len(edges) != 0 {
			t.Errorf("%s edges = %v, want none (secret endpoints stay absent)", target, edges)
		}
		blob, _ := json.Marshal(nodes)
		for _, leak := range []string{"cinder-pact", "Cinder Pact", "ghost-page"} {
			if strings.Contains(string(blob), leak) {
				t.Errorf("%s nodes leaked %q", target, leak)
			}
		}
	}
}
