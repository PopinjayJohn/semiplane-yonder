package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
)

// ---------------------------------------------------------------------------
// Fakes + setup (real vault + real index DB: full WriteFile clash fidelity)
// ---------------------------------------------------------------------------

type stubSessionStore struct {
	sessions map[string]*auth.Session
	err      error
}

func (s *stubSessionStore) Create(_ context.Context, _ *auth.Session) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (s *stubSessionStore) Get(_ context.Context, id string) (*auth.Session, error) {
	if s.err != nil {
		return nil, s.err
	}
	sess, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("no such session")
	}
	return sess, nil
}
func (s *stubSessionStore) Update(_ context.Context, _ *auth.Session) error { return nil }
func (s *stubSessionStore) Delete(_ context.Context, _ string) error        { return nil }
func (s *stubSessionStore) DeleteByUser(_ context.Context, _ string) error  { return nil }
func (s *stubSessionStore) CleanupExpired(_ context.Context) error          { return nil }

const (
	testSessID   = "0123456789abcdef0123456789abcdef"
	testCSRF     = "test-csrf-token-0123456789abcdef"
	testSessUser = "alice"
)

func testSetup(t *testing.T) (*WriteHandlers, *vault.Vault, store.Store) {
	t.Helper()
	ctx := context.Background()
	v, err := vault.NewVault(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "t.index.db"), filepath.Join(dir, "t.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := store.NewMigrationRunner(s.IndexDB()).RunIndex(ctx, s.IndexDB()); err != nil {
		t.Fatal(err)
	}
	sess := &stubSessionStore{sessions: map[string]*auth.Session{
		testSessID: {ID: testSessID, UserID: testSessUser, CSRFToken: testCSRF,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}}
	h := &WriteHandlers{Store: s, Vault: v, SessionStore: sess, SlotRegistry: NewSlotRegistry()}
	return h, v, s
}

func seedPage(t *testing.T, s store.Store, path, title string, secret bool, owner string, editableBy []string) {
	t.Helper()
	if editableBy == nil {
		editableBy = []string{}
	}
	if err := s.PageUpsert(context.Background(), &store.Page{
		Path: path, Title: title, Secret: secret, Owner: owner,
		EditableBy: editableBy, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

func withViewer(r *http.Request, v *auth.Viewer) *http.Request {
	return r.WithContext(auth.WithViewer(r.Context(), v))
}

func alice() *auth.Viewer { return &auth.Viewer{UserID: "alice"} }
func bob() *auth.Viewer   { return &auth.Viewer{UserID: "bob"} }
func gm() *auth.Viewer    { return &auth.Viewer{UserID: "gm", IsGM: true} }

func sessionCookie() *http.Cookie {
	return &http.Cookie{Name: auth.SessionCookieName,
		Value: testSessID + "|" + testSessUser + "|9999999999|sig"}
}

func postForm(t *testing.T, h http.HandlerFunc, target string, v *auth.Viewer, fields map[string]string, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	for k, val := range fields {
		form.Set(k, val)
	}
	if withCSRF {
		form.Set(auth.CSRFFieldName, testCSRF)
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie())
	rec := httptest.NewRecorder()
	h(rec, withViewer(req, v))
	return rec
}

func readVault(t *testing.T, v *vault.Vault, path string) string {
	t.Helper()
	b, err := v.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(b)
}

func mustRel(t *testing.T, root, full string) string {
	t.Helper()
	rel, err := filepath.Rel(root, full)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

// ---------------------------------------------------------------------------
// Create: PC-subtree-only + owner stamp
// ---------------------------------------------------------------------------

func TestCreateInOwnTreeStampsOwner(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)

	rec := postForm(t, h.PageCreate, "/p/new", alice(), map[string]string{
		"parent": "characters/alice/notes", "name": "plans", "content": "# Plans\n\nhello\n",
	}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create = %d, want 303; body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/p/characters/alice/notes/plans.md" {
		t.Fatalf("redirect = %q", loc)
	}
	got := readVault(t, v, "characters/alice/notes/plans.md")
	if !strings.Contains(got, "owner: alice") {
		t.Fatalf("owner not stamped:\n%s", got)
	}
	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("frontmatter fence missing:\n%s", got)
	}
}

func TestCreateHonorsSecretChoice(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)

	rec := postForm(t, h.PageCreate, "/p/new", alice(), map[string]string{
		"parent": "characters/alice", "name": "diary", "secret": "true", "content": "dear diary\n",
	}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readVault(t, v, "characters/alice/diary.md"); !strings.Contains(got, "secret: true") {
		t.Fatalf("secret choice lost:\n%s", got)
	}
}

func TestCreateDeniedElsewhere(t *testing.T) {
	h, _, s := testSetup(t)
	seedPage(t, s, "characters/bob/index.md", "Bob", false, "bob", nil)

	// Readable-but-foreign tree → 403.
	rec := postForm(t, h.PageCreate, "/p/new", alice(), map[string]string{
		"parent": "characters/bob", "name": "x", "content": "hi\n",
	}, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign tree create = %d, want 403", rec.Code)
	}
	// Outside characters/ entirely → 403.
	rec = postForm(t, h.PageCreate, "/p/new", alice(), map[string]string{
		"parent": "notes", "name": "x", "content": "hi\n",
	}, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("outside-tree create = %d, want 403", rec.Code)
	}
}

func TestCreateDeniedOnSecretParentIs404(t *testing.T) {
	h, _, s := testSetup(t)
	seedPage(t, s, "characters/bob/index.md", "Bob", true, "bob", nil)

	rec := postForm(t, h.PageCreate, "/p/new", alice(), map[string]string{
		"parent": "characters/bob", "name": "x", "content": "hi\n",
	}, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("secret-parent create = %d, want 404 (uniform, never 403)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "characters/bob") {
		t.Fatalf("denial echoes secret path: %s", rec.Body.String())
	}
}

func TestCreateRequiresLoginAndCSRF(t *testing.T) {
	h, _, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)

	rec := postForm(t, h.PageCreate, "/p/new", nil, map[string]string{
		"parent": "characters/alice", "name": "x", "content": "hi\n",
	}, true)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("guest create = %d, want 401", rec.Code)
	}
	rec = postForm(t, h.PageCreate, "/p/new", alice(), map[string]string{
		"parent": "characters/alice", "name": "x", "content": "hi\n",
	}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d, want 403", rec.Code)
	}
}

func TestCreateRejectsBadPaths(t *testing.T) {
	h, _, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)

	for _, tc := range []struct {
		name, parent, file string
	}{
		{"traversal", "characters/alice", "../../esc"},
		{"conflict-pattern", "characters/alice", "x.conflict-1.md"},
		{"dotfile", "characters/alice", ".hidden.md"},
		{"non-md-rejected-after-clean", "characters/alice", "x.txt"},
	} {
		rec := postForm(t, h.PageCreate, "/p/new", alice(), map[string]string{
			"parent": tc.parent, "name": tc.file, "content": "hi\n",
		}, true)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Fatalf("%s: create = %d, want a 4xx denial", tc.name, rec.Code)
		}
	}
}

func TestCreateDuplicateIs409(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	if err := v.WriteFile(context.Background(), "characters/alice/dup.md", "---\nowner: alice\n---\nold\n"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(t, h.PageCreate, "/p/new", alice(), map[string]string{
		"parent": "characters/alice", "name": "dup", "content": "new\n",
	}, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409", rec.Code)
	}
}

func TestGMCanCreateAnywhere(t *testing.T) {
	h, v, _ := testSetup(t)
	// GM session row for the CSRF check.
	h.SessionStore.(*stubSessionStore).sessions["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"] = &auth.Session{
		ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", UserID: "gm", CSRFToken: testCSRF}
	form := url.Values{"parent": {"notes"}, "name": {"gm-note"}, "content": {"gm text\n"},
		auth.CSRFFieldName: {testCSRF}}
	req := httptest.NewRequest(http.MethodPost, "/p/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|gm|9999999999|sig"})
	rec := httptest.NewRecorder()
	h.PageCreate(rec, withViewer(req, gm()))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("gm create = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readVault(t, v, "notes/gm-note.md"); !strings.Contains(got, "gm text") {
		t.Fatalf("gm page missing:\n%s", got)
	}
}

func TestGMPreviewAsIsTreatedAsPlayer(t *testing.T) {
	h, _, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	previewer := &auth.Viewer{UserID: "gm", IsGM: true, PreviewAs: "bob"}
	rec := postForm(t, h.PageCreate, "/p/new", previewer, map[string]string{
		"parent": "characters/alice", "name": "x", "content": "hi\n",
	}, true)
	if rec.Code == http.StatusSeeOther {
		t.Fatal("GM previewing as bob must not write to alice's tree")
	}
}

// ---------------------------------------------------------------------------
// Save: validation + conflict UX
// ---------------------------------------------------------------------------

func seedOwnedPage(t *testing.T, s store.Store, v *vault.Vault, rel, content string) {
	t.Helper()
	if err := v.WriteFile(context.Background(), rel, content); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, rel, "T", strings.Contains(content, "secret: true"), "alice", nil)
}

func TestSaveRoundTrip(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	seedOwnedPage(t, s, v, "characters/alice/notes.md", "---\nowner: alice\n---\n# N\n\nv1\n")

	rec := postForm(t, func(w http.ResponseWriter, r *http.Request) {
		h.PageSave(w, r)
	}, "/p/characters/alice/notes.md/edit", alice(), map[string]string{"content": "---\nowner: alice\n---\n# N\n\nv2\n"}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readVault(t, v, "characters/alice/notes.md"); !strings.Contains(got, "v2") {
		t.Fatalf("save lost:\n%s", got)
	}
}

func TestSaveConflictYieldsBanner(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	ctx := context.Background()
	if err := v.WriteFile(ctx, "characters/alice/n.md", "---\nowner: alice\n---\nv1\n"); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, "characters/alice/n.md", "N", false, "alice", nil)
	// Editor loads (arms clash check), then an external process saves.
	if _, err := v.ReadFile(ctx, "characters/alice/n.md"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Root, "characters", "alice", "n.md"),
		[]byte("---\nowner: alice\n---\nv2-external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postForm(t, h.PageSave, "/p/characters/alice/n.md/edit", alice(),
		map[string]string{"content": "---\nowner: alice\n---\nv3-stale\n"}, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale save = %d, want 409", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ".conflict-") {
		t.Fatalf("no conflict file in banner:\n%s", body)
	}
	if !strings.Contains(body, "Merge by hand") || !strings.Contains(body, "aria-live") {
		t.Fatalf("banner lacks merge flow / live region:\n%s", body)
	}
	// Original untouched.
	if got := readVault(t, v, "characters/alice/n.md"); !strings.Contains(got, "v2-external") {
		t.Fatalf("original clobbered:\n%s", got)
	}
}

func TestSaveWidenBlockedNarrowAllowed(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	seedOwnedPage(t, s, v, "characters/alice/s.md", "---\nsecret: true\nowner: alice\n---\nplans\n")
	seedPage(t, s, "characters/alice/s.md", "S", true, "alice", nil)

	// Widen (true -> false) denied.
	rec := postForm(t, h.PageSave, "/p/characters/alice/s.md/edit", alice(),
		map[string]string{"content": "---\nsecret: false\nowner: alice\n---\nplans\n"}, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("widen = %d, want 403", rec.Code)
	}
	// Narrow (false -> true) allowed on a non-secret page.
	seedOwnedPage(t, s, v, "characters/alice/o.md", "---\nowner: alice\n---\nopen\n")
	rec = postForm(t, h.PageSave, "/p/characters/alice/o.md/edit", alice(),
		map[string]string{"content": "---\nsecret: true\nowner: alice\n---\nopen\n"}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("narrow = %d, want 303: %s", rec.Code, rec.Body.String())
	}
}

func TestSaveOwnerImmutable(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	seedOwnedPage(t, s, v, "characters/alice/k.md", "---\nowner: alice\n---\nkeep\n")

	rec := postForm(t, h.PageSave, "/p/characters/alice/k.md/edit", alice(),
		map[string]string{"content": "---\nowner: bob\n---\nkeep\n"}, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("owner change = %d, want 403", rec.Code)
	}
	rec = postForm(t, h.PageSave, "/p/characters/alice/k.md/edit", alice(),
		map[string]string{"content": "---\nowner: alice\neditable-by: [mallory]\n---\nkeep\n"}, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editable-by change = %d, want 403", rec.Code)
	}
}

func TestSaveQuarantinedRejectedForPlayerAllowedForGM(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	seedOwnedPage(t, s, v, "characters/alice/q.md", "---\nowner: alice\n---\nbody\n")

	rec := postForm(t, h.PageSave, "/p/characters/alice/q.md/edit", alice(),
		map[string]string{"content": "---\nsecret: [unclosed\n---\nbody\n"}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("quarantined save = %d, want 422", rec.Code)
	}
	if got := readVault(t, v, "characters/alice/q.md"); strings.Contains(got, "unclosed") {
		t.Fatal("quarantined content was written for a player")
	}
}

func TestSaveQuarantinedAllowedForGM(t *testing.T) {
	h, v, s := testSetup(t)
	h.SessionStore.(*stubSessionStore).sessions["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"] = &auth.Session{
		ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", UserID: "gm", CSRFToken: testCSRF}
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	seedOwnedPage(t, s, v, "characters/alice/g.md", "---\nowner: alice\n---\nbody\n")

	form := url.Values{"content": {"---\nsecret: [unclosed\n---\nbody\n"}, auth.CSRFFieldName: {testCSRF}}
	req := httptest.NewRequest(http.MethodPost, "/p/characters/alice/g.md/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|gm|9999999999|sig"})
	rec := httptest.NewRecorder()
	h.PageSave(rec, withViewer(req, gm()))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("gm quarantined save = %d, want 303: %s", rec.Code, rec.Body.String())
	}
}

func TestSaveWithoutPriorReadIsLastWriterWins(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	// On disk but never read through this process: no clash baseline, so a
	// direct save is atomic last-writer-wins (documented vault fallback).
	rel := "characters/alice/direct.md"
	full := filepath.Join(v.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("---\nowner: alice\n---\nexternal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, rel, "D", false, "alice", nil)
	rec := postForm(t, h.PageSave, "/p/"+rel+"/edit", alice(),
		map[string]string{"content": "---\nowner: alice\n---\nvia-post\n"}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("direct save = %d, want 303: %s", rec.Code, rec.Body.String())
	}
}

func TestSaveDeniedForOtherPlayer(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	if err := v.WriteFile(context.Background(), "characters/alice/p.md", "---\nowner: alice\n---\nprivate\n"); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, "characters/alice/p.md", "P", false, "alice", nil)

	rec := postForm(t, h.PageSave, "/p/characters/alice/p.md/edit", bob(),
		map[string]string{"content": "hacked\n"}, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-player save = %d, want 403", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Preview: secret badges + unsupported placeholder, never writes
// ---------------------------------------------------------------------------

func TestPreviewRendersBadgesAndPlaceholder(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	if err := v.WriteFile(context.Background(), "characters/alice/pv.md", "---\nowner: alice\n---\nseed\n"); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, "characters/alice/pv.md", "PV", false, "alice", nil)

	content := "# Show\n\n> [!secret]- hidden plans\n\n> [!secret]+ open plans\n\n```dataview\nTABLE x\n```\n"
	rec := postForm(t, h.PageSave, "/p/characters/alice/pv.md/edit?preview=1", alice(),
		map[string]string{"content": content}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "prev-secret-hidden") || !strings.Contains(body, "hidden from party") {
		t.Fatalf("missing red/hidden badge:\n%s", body)
	}
	if !strings.Contains(body, "prev-secret-shown") || !strings.Contains(body, "shown to party") {
		t.Fatalf("missing green/shown badge:\n%s", body)
	}
	if !strings.Contains(body, "prev-unsupported") || !strings.Contains(body, "placeholder") {
		t.Fatalf("missing unsupported placeholder:\n%s", body)
	}
	// Nothing was written.
	if got := readVault(t, v, "characters/alice/pv.md"); strings.Contains(got, "dataview") {
		t.Fatalf("preview wrote to the vault:\n%s", got)
	}
}

func TestPreviewEscapesHTML(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	if err := v.WriteFile(context.Background(), "characters/alice/e.md", "---\nowner: alice\n---\nseed\n"); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, "characters/alice/e.md", "E", false, "alice", nil)
	rec := postForm(t, h.PageSave, "/p/characters/alice/e.md/edit", alice(),
		map[string]string{"content": "<script>alert(1)</script>\n", "preview": "1"}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Fatal("preview emitted raw HTML")
	}
}

func TestRenderPreviewBlocksUnit(t *testing.T) {
	page := &markdown.Page{Blocks: []markdown.Block{
		{Type: "secret", Content: "hush", Secret: true},
		{Type: "secret", Content: "hello", Secret: false},
		{Type: "unsupported", Content: "dataview"},
	}}
	out := renderPreviewBlocks(page.Blocks)
	for _, want := range []string{"hidden from party", "shown to party", "placeholder", "dataview"} {
		if !strings.Contains(out, want) {
			t.Fatalf("preview missing %q:\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Conflict banner + manual-merge flow
// ---------------------------------------------------------------------------

func TestMergeViewAndResolve(t *testing.T) {
	h, v, s := testSetup(t)
	ctx := context.Background()
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	rel := "characters/alice/m.md"
	if err := v.WriteFile(ctx, rel, "---\nowner: alice\n---\nbase\n"); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, rel, "M", false, "alice", nil)
	if _, err := v.ReadFile(ctx, rel); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Root, "characters", "alice", "m.md"),
		[]byte("---\nowner: alice\n---\nexternal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cpath string
	if err := v.WriteFile(ctx, rel, "---\nowner: alice\n---\nstale\n"); err != nil {
		matches, gerr := filepath.Glob(filepath.Join(v.Root, "characters", "alice", "m.conflict-*.md"))
		if gerr != nil || len(matches) != 1 {
			t.Fatalf("want one conflict file, got %v, %v (err %v)", matches, gerr, err)
		}
		cpath = filepath.ToSlash(mustRel(t, v.Root, matches[0]))
	} else {
		t.Fatal("want a conflict")
	}
	if !strings.Contains(cpath, ".conflict-") {
		t.Fatalf("bad conflict path: %q", cpath)
	}

	// Merge view shows both sides.
	req := httptest.NewRequest(http.MethodGet, "/p/"+rel+"/edit?conflict="+url.QueryEscape(cpath), nil)
	rec := httptest.NewRecorder()
	h.PageEdit(rec, withViewer(req, alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("merge view = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "external") || !strings.Contains(body, "Resolve with this text") {
		t.Fatalf("merge view incomplete:\n%s", body)
	}
	// Foreign conflict names rejected.
	req = httptest.NewRequest(http.MethodGet, "/p/"+rel+"/edit?conflict="+url.QueryEscape("characters/alice/other.conflict-1.md"), nil)
	rec = httptest.NewRecorder()
	h.PageEdit(rec, withViewer(req, alice()))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign conflict = %d, want 400", rec.Code)
	}

	// Resolve with merged text.
	rec = postForm(t, func(w http.ResponseWriter, r *http.Request) {
		// Route the revert URL the same way the mux would.
		r.URL.Path = "/p/" + rel + "/history/revert"
		h.PageRevert(w, r)
	}, "/p/"+rel+"/history/revert", alice(), map[string]string{
		"conflict": cpath, "content": "---\nowner: alice\n---\nmerged\n",
	}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("resolve = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readVault(t, v, rel); !strings.Contains(got, "merged") {
		t.Fatalf("parent not merged:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(v.Root, filepath.FromSlash(cpath))); !os.IsNotExist(err) {
		t.Fatal("conflict file should be deleted after resolve")
	}
	// Non-owner cannot resolve.
	if err := v.WriteFile(ctx, rel, "---\nowner: alice\n---\nmerged2\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ReadFile(ctx, rel); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Root, "characters", "alice", "m.md"),
		[]byte("---\nowner: alice\n---\nexternal2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cpath2 string
	if err := v.WriteFile(ctx, rel, "---\nowner: alice\n---\nstale2\n"); err != nil {
		matches, gerr := filepath.Glob(filepath.Join(v.Root, "characters", "alice", "m.conflict-*.md"))
		if gerr != nil || len(matches) != 1 {
			t.Fatalf("want one conflict file, got %v, %v (err %v)", matches, gerr, err)
		}
		cpath2 = filepath.ToSlash(mustRel(t, v.Root, matches[0]))
	} else {
		t.Fatal("want a second conflict")
	}
	rec = postForm(t, func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/p/" + rel + "/history/revert"
		h.PageRevert(w, r)
	}, "/p/"+rel+"/history/revert", bob(), map[string]string{
		"conflict": cpath2, "content": "hijacked\n",
	}, true)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("cross-player resolve = %d, want denial", rec.Code)
	}
}

func TestEditorShowsStoredConflictBanner(t *testing.T) {
	h, v, s := testSetup(t)
	ctx := context.Background()
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	rel := "characters/alice/b.md"
	if err := v.WriteFile(ctx, rel, "---\nowner: alice\n---\nbody\n"); err != nil {
		t.Fatal(err)
	}
	seedPage(t, s, rel, "B", false, "alice", nil)
	cpath := "characters/alice/b.conflict-1700000000000000000.md"
	if _, err := s.IndexDB().ExecContext(ctx,
		`INSERT INTO conflicts(path,parent_path,creator,created_at,owner,secret,editable_by) VALUES(?,?,?,?,?,?,?)`,
		cpath, rel, "alice", time.Now().UnixNano(), "alice", 0, "[]"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/p/"+rel+"/edit", nil)
	rec := httptest.NewRecorder()
	h.PageEdit(rec, withViewer(req, alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("editor = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), cpath) {
		t.Fatalf("editor lacks stored-conflict banner:\n%s", rec.Body.String())
	}
	// Other players must not see the banner entry.
	req = httptest.NewRequest(http.MethodGet, "/p/"+rel+"/edit", nil)
	rec = httptest.NewRecorder()
	h.PageEdit(rec, withViewer(req, bob()))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob editor = %d, want 403", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Frontmatter surgery: comments/order preserved
// ---------------------------------------------------------------------------

func TestStampPreservesCommentsAndOrder(t *testing.T) {
	in := "---\n# a comment\ntitle:  Old \nsecret: false\n---\nbody\n"
	got := stampCreate(in, "alice", false, "")
	if !strings.Contains(got, "# a comment") {
		t.Fatalf("comment dropped:\n%s", got)
	}
	if !strings.Contains(got, "title:  Old ") {
		t.Fatalf("unrelated line rewritten:\n%s", got)
	}
	if !strings.Contains(got, "owner: alice") {
		t.Fatalf("owner missing:\n%s", got)
	}
	// Force-secret replaces in place, keeping position.
	got = stampSave("---\ntitle: T\nsecret: false\nowner: alice\n---\n", "alice", true)
	if !strings.Contains(got, "secret: true") || strings.Contains(got, "secret: false") {
		t.Fatalf("secret not forced:\n%s", got)
	}
	if strings.Index(got, "title:") > strings.Index(got, "secret:") {
		t.Fatalf("order changed:\n%s", got)
	}
	// Fresh pages get a fence prepended.
	got = stampCreate("just body\n", "alice", false, "Hi")
	if !strings.HasPrefix(got, "---\n") || !strings.Contains(got, "title: Hi") {
		t.Fatalf("fence/title wrong:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// B2 staging: POST /upload stays 501 until the binary-write contract,
// destination/ACL policy, and a UI consumer land together.
// ---------------------------------------------------------------------------

func TestUploadStaysStaged(t *testing.T) {
	h, _, _ := testSetup(t)
	for _, target := range []string{"/upload", "/upload?as=gm"} {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("x"))
		rec := httptest.NewRecorder()
		h.Upload(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("POST %s = %d, want 501 (staged, see Upload comment)", target, rec.Code)
		}
	}
}
