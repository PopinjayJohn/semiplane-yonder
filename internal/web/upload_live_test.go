package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/uploads"
	"github.com/semiplane/yonder/internal/vault"
)

// ---------------------------------------------------------------------------
// B6 live upload round-trips: POST /upload → /assets/ fetch with ACL
// enforcement, caps/SVG rejections, 409 path, idempotent re-upload.
// ---------------------------------------------------------------------------

// postUpload builds a session-authed multipart POST (dest dir + file) and
// drives WriteHandlers.Upload directly (httptest only; never binds :8080).
// The editor `from` field is omitted: the handler falls back to the asset
// URL (pinned by the Location assertions below).
func postUpload(t *testing.T, h *WriteHandlers, target string, v *auth.Viewer, dest, filename string, data []byte, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	return postUploadFrom(t, h, target, v, dest, "", filename, data, withCSRF, false)
}

// postUploadFrom is postUpload with the editor `from` field controlled:
// sendFrom=false omits the field entirely, sendFrom=true sends fromValue
// (even empty, exactly like the editor form always does).
func postUploadFrom(t *testing.T, h *WriteHandlers, target string, v *auth.Viewer, dest, from, filename string, data []byte, withCSRF bool, sendFrom bool) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if err := mw.WriteField("dest", dest); err != nil {
		t.Fatal(err)
	}
	if sendFrom {
		if err := mw.WriteField("from", from); err != nil {
			t.Fatal(err)
		}
	}
	if withCSRF {
		if err := mw.WriteField(auth.CSRFFieldName, testCSRF); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(sessionCookie())
	rec := httptest.NewRecorder()
	h.Upload(rec, withViewer(req, v))
	return rec
}

// testPNG encodes a tiny solid-color PNG (stdlib only, frozen dep list).
func testPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fetchAsset drives ReadHandlers.Assets against the same vault dir + store
// the upload wrote through (ACL enforced server-side, like production).
func fetchAsset(t *testing.T, h *WriteHandlers, assetPath string, v *auth.Viewer) *httptest.ResponseRecorder {
	t.Helper()
	old := VaultDir
	VaultDir = h.Vault.(*vault.Vault).Root
	defer func() { VaultDir = old }()
	rh := &ReadHandlers{Store: h.Store, SlotRegistry: NewSlotRegistry()}
	req := httptest.NewRequest(http.MethodGet, "/assets/"+assetPath, nil)
	rec := httptest.NewRecorder()
	rh.Assets(rec, withViewer(req, v))
	return rec
}

func guest() *auth.Viewer { return &auth.Viewer{} }

// TestUploadLiveRoundTrip pins the B6 demo: GM uploads a PNG to vault root
// → 303 to the /assets URL → bytes land in the vault → the ACL-checked
// /assets handler serves them back as an image.
func TestUploadLiveRoundTrip(t *testing.T) {
	h, v, s := testSetup(t)
	// The bundle rule is fail-closed: a root asset needs a visible root
	// page beside it before guests may fetch it.
	seedPage(t, s, "welcome.md", "Welcome", false, "", nil)
	raw := testPNG(t, color.RGBA{200, 30, 30, 255})

	rec := postUpload(t, h, "/upload", gm(), "", "sigil.png", raw, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/assets/sigil.png" {
		t.Fatalf("upload Location = %q, want /assets/sigil.png", loc)
	}

	stored, err := v.ReadFile(context.Background(), "sigil.png")
	if err != nil {
		t.Fatalf("stored upload unreadable: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(stored)); err != nil {
		t.Fatalf("stored upload is not a PNG: %v", err)
	}

	got := fetchAsset(t, h, "sigil.png", guest())
	if got.Code != http.StatusOK {
		t.Fatalf("asset fetch status = %d, want 200", got.Code)
	}
	if ct := got.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("asset Content-Type = %q, want image/png", ct)
	}
	if !bytes.Equal(got.Body.Bytes(), stored) {
		t.Errorf("served bytes differ from stored upload")
	}
}

// TestUploadIdentityPreserved pins the 303-with-identity shape: ?as= survives
// the redirect like every write POST.
func TestUploadIdentityPreserved(t *testing.T) {
	h, _, _ := testSetup(t)
	raw := testPNG(t, color.RGBA{30, 200, 30, 255})
	rec := postUpload(t, h, "/upload?as=gm", gm(), "", "idem.png", raw, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/assets/idem.png?as=gm" {
		t.Fatalf("upload Location = %q, want identity preserved", loc)
	}
}

// TestUploadPlayerSubtree pins ACL option A: a player may upload inside her
// own characters/<pc>/… tree but not at vault root or in another PC's tree.
func TestUploadPlayerSubtree(t *testing.T) {
	h, v, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	raw := testPNG(t, color.RGBA{30, 30, 200, 255})

	rec := postUpload(t, h, "/upload", alice(), "characters/alice", "map.png", raw, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("own-subtree upload = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if _, err := v.ReadFile(context.Background(), "characters/alice/map.png"); err != nil {
		t.Fatalf("own-subtree upload not stored: %v", err)
	}

	if rec := postUpload(t, h, "/upload", alice(), "", "root.png", raw, true); rec.Code != http.StatusForbidden {
		t.Errorf("root upload as player = %d, want 403", rec.Code)
	}
	if rec := postUpload(t, h, "/upload", bob(), "characters/alice", "sneak.png", raw, true); rec.Code != http.StatusForbidden {
		t.Errorf("other-PC upload as bob = %d, want 403", rec.Code)
	}
}

// TestUploadGates pins the write-path gates: guests 401, bad CSRF 403,
// secret-tree writes by outsiders hide as 404 (denyForWrite, like all
// write gates).
func TestUploadGates(t *testing.T) {
	h, _, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", true, "alice", nil)
	raw := testPNG(t, color.RGBA{200, 30, 30, 255})

	if rec := postUpload(t, h, "/upload", guest(), "characters/alice", "g.png", raw, true); rec.Code != http.StatusUnauthorized {
		t.Errorf("guest upload = %d, want 401", rec.Code)
	}
	if rec := postUpload(t, h, "/upload", alice(), "characters/alice", "nocsrf.png", raw, false); rec.Code != http.StatusForbidden {
		t.Errorf("CSRF-less upload = %d, want 403", rec.Code)
	}
	if rec := postUpload(t, h, "/upload", bob(), "characters/alice", "sneak.png", raw, true); rec.Code != http.StatusNotFound {
		t.Errorf("secret-tree upload as outsider = %d, want 404", rec.Code)
	}
	if rec := postUpload(t, h, "/upload", alice(), "characters/alice", "notes.md", []byte("# hi"), true); rec.Code != http.StatusBadRequest {
		t.Errorf(".md upload = %d, want 400", rec.Code)
	}
}

// TestUploadRejections pins the pipeline red lines at the handler: SVG
// content blocked even under an image filename, oversize PDFs rejected,
// extension/content mismatches rejected.
func TestUploadRejections(t *testing.T) {
	h, _, _ := testSetup(t)

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="5"/></svg>`)
	if rec := postUpload(t, h, "/upload", gm(), "", "art.png", svg, true); rec.Code != http.StatusBadRequest {
		t.Errorf("SVG upload = %d, want 400", rec.Code)
	}

	big := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("0"), uploads.MaxPDFBytes)...)
	if rec := postUpload(t, h, "/upload", gm(), "", "big.pdf", big, true); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize PDF upload = %d, want 413", rec.Code)
	}

	pngBytes := testPNG(t, color.RGBA{1, 2, 3, 255})
	if rec := postUpload(t, h, "/upload", gm(), "", "photo.pdf", pngBytes, true); rec.Code != http.StatusBadRequest {
		t.Errorf("mismatched-extension upload = %d, want 400", rec.Code)
	}
}

// TestUploadCollision pins the B6 ruling (option B): identical bytes are an
// idempotent 303 success; differing bytes under the same name are a 409 and
// the stored bytes are untouched (never auto-rename, never silent clobber).
func TestUploadCollision(t *testing.T) {
	h, v, _ := testSetup(t)
	ctx := context.Background()
	first := append([]byte("%PDF-1.4 first\n"), bytes.Repeat([]byte("a"), 64)...)
	second := append([]byte("%PDF-1.4 second\n"), bytes.Repeat([]byte("b"), 64)...)

	if rec := postUpload(t, h, "/upload", gm(), "", "doc.pdf", first, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("first upload = %d, want 303", rec.Code)
	}
	if rec := postUpload(t, h, "/upload", gm(), "", "doc.pdf", first, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("identical re-upload = %d, want idempotent 303", rec.Code)
	}
	if rec := postUpload(t, h, "/upload", gm(), "", "doc.pdf", second, true); rec.Code != http.StatusConflict {
		t.Fatalf("differing re-upload = %d, want 409", rec.Code)
	}
	stored, err := v.ReadFile(ctx, "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, first) {
		t.Errorf("409 clobbered the stored upload")
	}
}

// TestUploadAssetsACL pins end-to-end enforcement: an upload into a secret
// PC tree is fetchable by the owner and GM but 404s for guests and other
// players (the /assets page-bundle rule, server-side).
func TestUploadAssetsACL(t *testing.T) {
	h, _, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", true, "alice", nil)
	raw := testPNG(t, color.RGBA{90, 90, 90, 255})

	if rec := postUpload(t, h, "/upload", alice(), "characters/alice", "hide.png", raw, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		name string
		v    *auth.Viewer
		want int
	}{
		{"guest", guest(), http.StatusNotFound},
		{"other player", bob(), http.StatusNotFound},
		{"owner", alice(), http.StatusOK},
		{"gm", gm(), http.StatusOK},
	} {
		if rec := fetchAsset(t, h, "characters/alice/hide.png", tc.v); rec.Code != tc.want {
			t.Errorf("fetch as %s = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

// TestUploadFormLive guards the editor surface copy: the form posts
// multipart to /upload with dest + from + CSRF and no longer claims staging.
func TestUploadFormLive(t *testing.T) {
	html := uploadFormHTML("characters/alice/notes.md", "tok")
	for _, want := range []string{`enctype="multipart/form-data"`, `name="dest"`, `value="characters/alice"`, `name="from"`, `value="characters/alice/notes.md"`, `name="file"`, `csrf_token`} {
		if !strings.Contains(html, want) {
			t.Errorf("upload form missing %q", want)
		}
	}
	if strings.Contains(html, "staged") {
		t.Errorf("upload form still claims staging")
	}
}

// TestUploadRedirectsToSourceEditor pins the B6 ruling: a successful upload
// 303s back to the source editor page named by the form's `from` field
// (validated like any page path), not to the /assets URL. Identity (?as=/
// preview_as=) is preserved on the editor target; gates, 409s, and
// idempotent re-uploads behave unchanged; absent/invalid `from` falls back
// to the asset URL.
func TestUploadRedirectsToSourceEditor(t *testing.T) {
	h, _, s := testSetup(t)
	seedPage(t, s, "characters/alice/index.md", "Alice", false, "alice", nil)
	raw := testPNG(t, color.RGBA{200, 30, 30, 255})

	// Root upload from the welcome editor → back to the welcome editor.
	rec := postUploadFrom(t, h, "/upload", gm(), "", "welcome.md", "ed.png", raw, true, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/edit/p/welcome" {
		t.Fatalf("upload Location = %q, want /edit/p/welcome", loc)
	}

	// Identity preserved on the editor target, like every write POST.
	rec = postUploadFrom(t, h, "/upload?as=gm&preview_as=cass", gm(), "", "welcome.md", "idem2.png", raw, true, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload status = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/edit/p/welcome?as=gm&preview_as=cass" {
		t.Fatalf("upload Location = %q, want editor target with identity", loc)
	}

	// Player subtree: editor target keeps the full page rel.
	rec = postUploadFrom(t, h, "/upload", alice(), "characters/alice", "characters/alice/notes.md", "map2.png", raw, true, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("subtree upload = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/edit/p/characters/alice/notes" {
		t.Fatalf("subtree Location = %q, want /edit/p/characters/alice/notes", loc)
	}

	// Idempotent re-upload (identical bytes) still 303s to the editor.
	rec = postUploadFrom(t, h, "/upload", alice(), "characters/alice", "characters/alice/notes.md", "map2.png", raw, true, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("identical re-upload = %d, want idempotent 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/edit/p/characters/alice/notes" {
		t.Fatalf("idempotent Location = %q, want editor target", loc)
	}

	// Differing bytes under the same name stay a 409 (no redirect at all).
	other := testPNG(t, color.RGBA{1, 2, 3, 255})
	rec = postUploadFrom(t, h, "/upload", alice(), "characters/alice", "characters/alice/notes.md", "map2.png", other, true, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("differing re-upload = %d, want 409", rec.Code)
	}

	// Invalid `from` (traversal, dotfile, conflict pattern, empty) falls
	// back to the asset URL. The first stores fallback.png; the rest are
	// identical-byte idempotent 303s to the same fallback.
	for _, bad := range []string{"../esc.md", ".hidden.md", "x.conflict-1.md", ""} {
		rec = postUploadFrom(t, h, "/upload", gm(), "", bad, "fallback.png", raw, true, true)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("from=%q upload = %d, want 303", bad, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/assets/fallback.png" {
			t.Fatalf("from=%q Location = %q, want /assets/fallback.png", bad, loc)
		}
	}

	// Omitted `from` entirely (pre-ruling clients) also falls back.
	rec = postUpload(t, h, "/upload", gm(), "", "legacy.png", raw, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("no-from upload = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/assets/legacy.png" {
		t.Fatalf("no-from Location = %q, want /assets/legacy.png", loc)
	}
}
