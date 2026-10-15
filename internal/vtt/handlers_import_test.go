// Import route + door-toggle op HTTP tests: the full auth matrix (login →
// preview 403 → CSRF → GM-only → CanViewMap), the 12MB cap, the typed 422s,
// the post-import live state (fog all-hidden, doors seeded), and the
// per-viewer leak matrix over imported geometry.
package vtt

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
	"github.com/semiplane/yonder/internal/web"
)

// importTestEnv is a store + real vault wired for import tests. The vault
// sidecar exists on disk and is indexed before the test runs (vault is
// truth), and the handler's OnAfterWrite rides the same store.Rescan the
// serve path uses.
type importTestEnv struct {
	h     *Handlers
	s     store.Store
	vault string
}

func newImportTestEnv(t *testing.T, sidecar string) *importTestEnv {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "maps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "maps", "arena.md"), []byte(sidecar), 0o644); err != nil {
		t.Fatal(err)
	}
	s := testStore(t)
	ctx := context.Background()
	if _, err := store.Rescan(ctx, s.IndexDB(), dir, web.ParsePage); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	v, err := vault.NewVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handlers{Store: s, Sessions: sessionStub(), Vault: v}
	h.OnAfterWrite = func() error {
		_, err := store.Rescan(ctx, s.IndexDB(), dir, web.ParsePage)
		return err
	}
	return &importTestEnv{h: h, s: s, vault: dir}
}

func sessionStub() auth.SessionStore {
	return &stubSessions{sessions: map[string]*auth.Session{
		testSessID: {ID: testSessID, UserID: "gm", CSRFToken: testCSRF,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}}
}

func importRequest(t *testing.T, v *auth.Viewer, body string, query string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	return stateRequest(t, "/vtt/arena/import", v, body, query)
}

// stateRequest posts to the state-op route with the same auth wiring.
func stateRequest(t *testing.T, target string, v *auth.Viewer, body, query string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if v != nil {
		req.Header.Set(auth.CSRFHeaderName, testCSRF)
		req.AddCookie(gmCookie())
		req = withViewerCtx(req, v)
	}
	return httptest.NewRecorder(), req
}

func TestImportRouteHappyPath(t *testing.T) {
	env := newImportTestEnv(t, "---\ntitle: Arena\nowner: gm\nvtt-map:\n  grid: {cols: 20, rows: 14}\n---\n\n# Arena\n")
	body := uvttBasic(t, tinyPNG)
	rec, req := importRequest(t, gmViewer(), body, "source=ford.dd2vtt")
	env.h.Import(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"cols":4`) || !strings.Contains(rec.Body.String(), `"walls":4`) {
		t.Errorf("summary = %s", rec.Body.String())
	}
	// Sidecar written + indexed (the handler's OnAfterWrite ran the rescan).
	page, err := env.s.PageGet(context.Background(), "maps/arena.md")
	if err != nil {
		t.Fatalf("sidecar not indexed after import: %v", err)
	}
	cal := ParseCalibration(page.Frontmatter["vtt-map"])
	if cal.Grid.Cols != 4 || cal.Grid.Rows != 3 || len(cal.Walls) != 4 {
		t.Errorf("imported calibration = %+v", cal)
	}
	if cal.Background != "assets/maps/arena.png" {
		t.Errorf("background = %q", cal.Background)
	}
	if cal.UVTTSource != "ford.dd2vtt" {
		t.Errorf("uvtt-source = %q, want the ?source= audit name", cal.UVTTSource)
	}
	// Image landed in the vault.
	if _, err := os.Stat(filepath.Join(env.vault, "assets", "maps", "arena.png")); err != nil {
		t.Errorf("image not written: %v", err)
	}
	// Live state: fog all-hidden after import (fail closed), even though the
	// imported calibration would seed a full-cover mask on its own.
	live, _ := LoadState(context.Background(), env.s, "arena")
	rects, ok := DecodeMask(live.FogMask, cal.Grid)
	if !live.FogPresent || !ok || len(rects) != 1 || rects[0] != (Rect{X: 0, Y: 0, W: 4, H: 3}) {
		t.Errorf("post-import fog = %q (fail-closed full cover wanted)", live.FogMask)
	}
	// Doors seeded from the sidecar portals: the one closed portal keeps no
	// override row (absent = closed).
	if len(live.Doors) != 0 {
		t.Errorf("doors = %+v, want none (the only portal is closed)", live.Doors)
	}
	snap, _, err := SnapshotFor(context.Background(), env.s, gmViewer(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Fog.Hidden || len(snap.Walls) != 4 || len(snap.Doors) != 1 {
		t.Errorf("post-import snapshot = fog %v walls %d doors %d", snap.Fog.Hidden, len(snap.Walls), len(snap.Doors))
	}
}

func TestImportRouteAuthMatrix(t *testing.T) {
	body := uvttBasic(t, tinyPNG)
	cases := []struct {
		name string
		v    *auth.Viewer
		csrf bool
		ct   string
		body string
		want int
	}{
		{"guest", nil, false, "application/json", body, http.StatusUnauthorized},
		{"player forbidden", playerViewer("mira"), true, "application/json", body, http.StatusForbidden},
		{"preview read-only", previewViewer(), true, "application/json", body, http.StatusForbidden},
		{"missing csrf", gmViewer(), false, "application/json", body, http.StatusForbidden},
		{"wrong content type", gmViewer(), true, "text/plain", body, http.StatusUnsupportedMediaType},
		{"corrupt json", gmViewer(), true, "application/json", `{not json`, http.StatusUnprocessableEntity},
		{"map too large", gmViewer(), true, "application/json",
			`{"resolution": {"map_origin": [0,0], "map_size": [200,10], "pixels_per_grid": 50}}`, http.StatusUnprocessableEntity},
		{"image too large", gmViewer(), true, "application/json",
			strings.Replace(uvttBasic(t, "X"), "X", pngWithDims(t, 12000, 12000), 1), http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n---\n")
			rec, req := importRequest(t, tc.v, tc.body, "")
			if !tc.csrf {
				req.Header.Del(auth.CSRFHeaderName)
			}
			req.Header.Set("Content-Type", tc.ct)
			env.h.Import(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s import = %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
			}
			// A rejected import must not have written anything.
			if _, err := os.Stat(filepath.Join(env.vault, "assets")); err == nil {
				t.Errorf("%s: rejected import wrote an asset", tc.name)
			}
		})
	}
}

// A GM may CREATE a map by importing into a fresh id (2026-10-13 decision):
// the import writes the sidecar itself. Malformed ids still 404.
func TestImportRouteCreatesNewMap(t *testing.T) {
	env := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n---\n")
	req := httptest.NewRequest(http.MethodPost, "/vtt/ghost/import", strings.NewReader(uvttBasic(t, tinyPNG)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeaderName, testCSRF)
	req.AddCookie(gmCookie())
	req = withViewerCtx(req, gmViewer())
	rec := httptest.NewRecorder()
	env.h.Import(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new-map import = %d: %s", rec.Code, rec.Body.String())
	}
	// The sidecar was created with calibration + generated body prose.
	out, err := os.ReadFile(filepath.Join(env.vault, "maps", "ghost.md"))
	if err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}
	got := string(out)
	if !strings.HasPrefix(got, "---\ntitle: ghost\nvtt-map:") {
		t.Errorf("fresh sidecar head:\n%s", got)
	}
	if !strings.Contains(got, "grid: {cols: 4, rows: 3, cell: 100}") || !strings.Contains(got, "uvtt-source: upload.uvtt") {
		t.Errorf("fresh sidecar missing calibration:\n%s", got)
	}
	if !strings.Contains(got, "This page holds calibration only") {
		t.Error("fresh sidecar missing generated body")
	}
	// The index refresh (OnAfterWrite) makes the new map immediately visible.
	snap, _, err := SnapshotFor(context.Background(), env.s, gmViewer(), "ghost")
	if err != nil {
		t.Fatalf("new map not visible after import: %v", err)
	}
	if len(snap.Walls) != 4 {
		t.Errorf("new map walls = %d, want 4", len(snap.Walls))
	}
	// Malformed ids are still 404 (never created).
	for _, bad := range []string{"/vtt/a/b/import", "/vtt/CON/import"} {
		req := httptest.NewRequest(http.MethodPost, bad, strings.NewReader(uvttBasic(t, tinyPNG)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(auth.CSRFHeaderName, testCSRF)
		req.AddCookie(gmCookie())
		req = withViewerCtx(req, gmViewer())
		rec := httptest.NewRecorder()
		env.h.Import(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("import %s = %d, want 404", bad, rec.Code)
		}
	}
}

// A fresh sidecar inside a secret maps/ tree is stamped secret:true (fail
// closed) — the import must never open a page under a hidden folder.
func TestImportRouteFreshSidecarSecretStamp(t *testing.T) {
	env := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n---\n")
	if err := env.s.PageUpsert(context.Background(), &store.Page{
		Path: "maps/index.md", Title: "Maps", Secret: true, Owner: "gm",
		EditableBy: []string{}, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/vtt/ghost/import", strings.NewReader(uvttBasic(t, tinyPNG)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeaderName, testCSRF)
	req.AddCookie(gmCookie())
	req = withViewerCtx(req, gmViewer())
	rec := httptest.NewRecorder()
	env.h.Import(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	out, _ := os.ReadFile(filepath.Join(env.vault, "maps", "ghost.md"))
	if !strings.Contains(string(out), "secret: true") {
		t.Errorf("fresh sidecar in a secret tree must be stamped secret:\n%s", out)
	}
	// The GM still sees the new map; another player never will.
	if _, _, err := SnapshotFor(context.Background(), env.s, gmViewer(), "ghost"); err != nil {
		t.Errorf("GM lost the new map: %v", err)
	}
}

// The 55MB cap answers 413 before any parse (R1): once via the
// Content-Length pre-check (no budget consumed, no byte allocated) and once
// via the post-read check for chunked bodies of unknown length.
func TestImportRouteBodyCap(t *testing.T) {
	env := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n---\n")
	// Pre-check: declared length over the cap, tiny actual body.
	rec, req := importRequest(t, gmViewer(), `{"resolution": {"map_origin": [0,0], "map_size": [4,3], "pixels_per_grid": 100}}`, "")
	req.ContentLength = maxImportBody + 1
	env.h.Import(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("content-length pre-check = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "55MB") {
		t.Errorf("413 body = %q, want the new cap number", rec.Body.String())
	}
	// Post-read check: unknown length (chunked), endless body — the handler
	// reads maxImportBody+1 and 413s without materializing more.
	rec2, req2 := importRequest(t, gmViewer(), "", "")
	req2.Body = io.NopCloser(io.LimitReader(zeroReader{}, maxImportBody+2))
	req2.ContentLength = -1
	env.h.Import(rec2, req2)
	if rec2.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("post-read check = %d, want 413", rec2.Code)
	}
	// Neither rejection wrote anything.
	if _, err := os.Stat(filepath.Join(env.vault, "assets")); err == nil {
		t.Error("rejected import wrote an asset")
	}
}

// The R1 admission guard: past the auth gates, before the first body byte.
// A GM past their window budget gets 429 (never a lockout), and a second
// concurrent import gets 429 while one runs. Rejected requests write nothing.
func TestImportRouteAdmissionGuard(t *testing.T) {
	newGuardEnv := func(t *testing.T, lim *ImportLimiter) *importTestEnv {
		t.Helper()
		env := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n---\n")
		env.h.ImportLimit = lim
		return env
	}
	// Window budget: 1 import per hour admits one, then 429s.
	env := newGuardEnv(t, NewImportLimiter(1, 1, time.Hour))
	rec, req := importRequest(t, gmViewer(), uvttBasic(t, tinyPNG), "")
	env.h.Import(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first import = %d: %s", rec.Code, rec.Body.String())
	}
	rec, req = importRequest(t, gmViewer(), uvttBasic(t, tinyPNG), "")
	env.h.Import(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("past-budget import = %d, want 429", rec.Code)
	}
	// In-flight semaphore: a held slot 429s even with budget remaining.
	env2 := newGuardEnv(t, NewImportLimiter(1, 10, time.Hour))
	lim := env2.h.importLimiter()
	if err := lim.Acquire("gm"); err != nil {
		t.Fatal(err)
	}
	rec, req = importRequest(t, gmViewer(), uvttBasic(t, tinyPNG), "")
	env2.h.Import(rec, req)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "already running") {
		t.Errorf("concurrent import = %d %q, want the busy 429", rec.Code, rec.Body.String())
	}
	lim.Release()
	rec, req = importRequest(t, gmViewer(), uvttBasic(t, tinyPNG), "")
	env2.h.Import(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("after release = %d: %s", rec.Code, rec.Body.String())
	}
	// Rejected imports never wrote: only the two accepted ones own assets.
	// (Both envs import map "arena"; each accepted exactly what it ran.)
	for _, e := range []*importTestEnv{env, env2} {
		if _, err := os.Stat(filepath.Join(e.vault, "assets", "maps", "arena.png")); err != nil {
			t.Errorf("accepted import lost its asset: %v", err)
		}
	}
}

// The paired R1 image cap (40MB HTTP, the largest image encodable in a 55MB
// body): a 6MB webp — rejected under the old 5MB cap — imports fine, while
// an image just over 40MB (in a body still under 55MB) 422s to the CLI.
func TestImportRouteImageCapHTTP(t *testing.T) {
	env := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n---\n")
	sixMB := fakeWebP(t, 100, 100, 6<<20)
	rec, req := importRequest(t, gmViewer(), uvttImageBody(sixMB), "")
	env.h.Import(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("6MB webp import = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.vault, "assets", "maps", "arena.webp")); err != nil {
		t.Errorf("6MB webp not stored as-is: %v", err)
	}
	// Just over the 40MB image cap, still under the 55MB body cap.
	env2 := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n---\n")
	over := fakeWebP(t, 100, 100, (40<<20)+1)
	rec, req = importRequest(t, gmViewer(), uvttImageBody(over), "")
	env2.h.Import(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-40MB image import = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "import-uvtt CLI") {
		t.Errorf("422 body = %q, want the CLI pointer", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env2.vault, "assets")); err == nil {
		t.Error("rejected import wrote an asset")
	}
}

// fakeWebP builds decoded image bytes with a valid RIFF/VP8X header claiming
// wxh (header-only dims parse, never decoded) plus zero padding to size.
// webp is stored as-is, so the padding never needs to be real pixels.
func fakeWebP(t *testing.T, w, h, size int) []byte {
	t.Helper()
	if size < 30 {
		t.Fatalf("fake webp size %d too small for a header", size)
	}
	out := make([]byte, size)
	copy(out, []byte("RIFF"))
	uint32le(out[4:], uint32(size-8))
	copy(out[8:], []byte("WEBPVP8X"))
	uint32le(out[16:], 10)
	out[20] = 0 // flags
	// bytes 21..23 reserved (D5b: width starts at body[4]).
	uint32le24(out[24:], uint32(w-1))
	uint32le24(out[27:], uint32(h-1))
	return out
}

func uint32le(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func uint32le24(b []byte, v uint32) {
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16)
}

// uvttImageBody wraps decoded image bytes (base64) in a minimal import body.
func uvttImageBody(decoded []byte) string {
	return `{"resolution": {"map_origin": [0,0], "map_size": [4,3], "pixels_per_grid": 100}, "image": "` +
		base64.StdEncoding.EncodeToString(decoded) + `"}`
}

// zeroReader is an endless stream of zero bytes (chunked-body stand-in:
// exercises the post-read 413 without materializing a 55MB string).
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// A secret sidecar keeps its ACL: the GM imports fine; another player still
// 404s on the map and never sees the imported geometry.
func TestImportRouteSecretMapLeakMatrix(t *testing.T) {
	env := newImportTestEnv(t, "---\ntitle: Vault\nsecret: true\nowner: gm\nvtt-map:\n  grid: {cols: 20, rows: 14}\n  tokens:\n    - {id: gm-hound, name: Hound, x: 1, y: 1, hp: 5, max-hp: 5, hidden: true}\n---\n")
	rec, req := importRequest(t, gmViewer(), uvttBasic(t, tinyPNG), "")
	env.h.Import(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GM import = %d: %s", rec.Code, rec.Body.String())
	}
	// Reseed from the sidecar (the import reset the board; the token default
	// is hidden) so the snapshot has content to filter.
	if err := ResetToSidecar(context.Background(), env.s, "arena", CalibrationFor(context.Background(), env.s, "arena")); err != nil {
		t.Fatal(err)
	}
	if err := SetFog(context.Background(), env.s, "arena", EncodeMask(Grid{Cols: 4, Rows: 3}, nil)); err != nil {
		t.Fatal(err)
	}
	// Other player: the map itself is secret → 404 on every surface.
	if _, _, err := SnapshotFor(context.Background(), env.s, playerViewer("mira"), "arena"); err == nil {
		t.Error("non-owner saw the secret map's geometry")
	}
	// GM sees walls + the hidden token; the open board shows geometry.
	snap, _, err := SnapshotFor(context.Background(), env.s, gmViewer(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Walls) != 4 || snap.Ambient != "ffffff" {
		t.Errorf("GM geometry = %d walls, ambient %q", len(snap.Walls), snap.Ambient)
	}
	if len(snap.Tokens) != 1 || !snap.Tokens[0].Hidden {
		t.Errorf("GM hidden token = %+v", snap.Tokens)
	}
	b, _ := SnapshotJSON(snap)
	if !strings.Contains(string(b), "gm-hound") {
		t.Error("GM snapshot lost the hidden token id")
	}
}

// Door toggle: GM-only, idx must name a live sidecar portal, and the
// effective state rides the snapshot after the fog-update refetch.
func TestDoorToggleOp(t *testing.T) {
	env := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n  portals:\n    - {a: [1, 0], b: [2, 0], closed: true}\n    - {a: [3, 0], b: [4, 0], closed: false}\n---\n")
	// Open the board so geometry renders (fail-closed otherwise).
	if err := env.h.seedOpenBoard(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	post := func(v *auth.Viewer, body string) int {
		rec, req := stateRequest(t, "/vtt/arena/state", v, body, "")
		env.h.StateUpdate(rec, req)
		return rec.Code
	}
	// Player + preview denied; GM allowed.
	if code := post(playerViewer("mira"), `{"op":"door","idx":0,"open":true}`); code != http.StatusForbidden {
		t.Errorf("player door = %d, want 403", code)
	}
	if code := post(previewViewer(), `{"op":"door","idx":0,"open":true}`); code != http.StatusForbidden {
		t.Errorf("preview door = %d, want 403", code)
	}
	if code := post(gmViewer(), `{"op":"door","idx":0,"open":true}`); code != http.StatusOK {
		t.Fatalf("GM door open = %d", code)
	}
	snap, _, _ := SnapshotFor(ctx, env.s, gmViewer(), "arena")
	if len(snap.Doors) != 2 || snap.Doors[0].Closed {
		t.Errorf("door 0 = %+v, want open after toggle", snap.Doors)
	}
	// Closing removes the override: the sidecar default (closed) wins again.
	if code := post(gmViewer(), `{"op":"door","idx":0,"open":false}`); code != http.StatusOK {
		t.Fatalf("GM door close = %d", code)
	}
	snap, _, _ = SnapshotFor(ctx, env.s, gmViewer(), "arena")
	if len(snap.Doors) != 2 || !snap.Doors[0].Closed {
		t.Errorf("door 0 = %+v, want closed (sidecar default)", snap.Doors)
	}
	// Out-of-range idx (sidecar list has 2) is a 422, not a silent no-op.
	if code := post(gmViewer(), `{"op":"door","idx":7,"open":true}`); code != http.StatusUnprocessableEntity {
		t.Errorf("bad idx = %d, want 422", code)
	}
	// A player sees the same door geometry (never the token content).
	psnap, _, _ := SnapshotFor(ctx, env.s, playerViewer("mira"), "arena")
	if len(psnap.Doors) != 2 {
		t.Errorf("player doors = %+v", psnap.Doors)
	}
}

// seedOpenBoard reveals the fog so geometry renders in snapshots (a fresh
// board fails closed to fully hidden).
func (h *Handlers) seedOpenBoard() error {
	cal := CalibrationFor(context.Background(), h.Store, "arena")
	return SetFog(context.Background(), h.Store, "arena", EncodeMask(cal.Grid, nil))
}

// The fragment's GM door panel: text state + toggle buttons; players never
// see the section.
func TestFragmentDoorPanel(t *testing.T) {
	env := newImportTestEnv(t, "---\ntitle: Arena\nvtt-map:\n  grid: {cols: 20, rows: 14}\n  portals:\n    - {a: [1, 0], b: [2, 0], closed: true}\n---\n")
	if err := env.h.seedOpenBoard(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snap, cal, err := SnapshotFor(ctx, env.s, gmViewer(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	live, _ := LoadState(ctx, env.s, "arena")
	gm := RenderFragment(FragmentInput{
		MapID: "arena", Title: "Arena", Snap: snap, Cal: cal,
		Fog:    ResolveFog(live.FogMask, live.FogPresent, cal.Grid),
		Viewer: gmViewer(), IsGM: true,
	})
	for _, want := range []string{
		`aria-label="Doors"`, `data-doors`, `data-door-row="0"`,
		"Door 1 (column 1, row 0): ", `data-door-state>closed<`,
		`data-door-toggle="0"`, `>Open</button>`,
	} {
		if !strings.Contains(gm, want) {
			t.Errorf("GM fragment missing %q:\n%s", want, gm)
		}
	}
	// Player: no door section at all.
	psnap, _, _ := SnapshotFor(ctx, env.s, playerViewer("mira"), "arena")
	player := RenderFragment(FragmentInput{
		MapID: "arena", Title: "Arena", Snap: psnap, Cal: cal,
		Fog:    ResolveFog(live.FogMask, live.FogPresent, cal.Grid),
		Viewer: playerViewer("mira"),
	})
	for _, leak := range []string{"data-doors", "Door 1", "door-toggle"} {
		if strings.Contains(player, leak) {
			t.Errorf("player fragment leaks %q", leak)
		}
	}
}
