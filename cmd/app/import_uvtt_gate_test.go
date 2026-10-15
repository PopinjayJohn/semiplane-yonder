// A-VTT2 import gate: POST /vtt/{mapID}/import + the door-toggle op driven
// through the PRODUCTION serve wiring (real mux, real route registry, real
// session CSRF, real vault + index refresh). The handler unit tests in
// internal/vtt cover the auth matrix in isolation; this proves the wiring:
// the "/vtt/{mapID}/import" pattern reaches the handler, the session CSRF
// gate holds, and the post-import reindex makes the new calibration visible
// on the next state read.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
)

type importGate struct {
	t     *testing.T
	vault string
	srv   *httptest.Server
	jar   *cookiejar.Jar
	gm    struct{ cookie, csrf string }
	pl    struct{ cookie, csrf string }
}

func newImportGate(t *testing.T) *importGate {
	t.Helper()
	vault := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(vault, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("campaign.yaml", "name: Import Gate\ncreated: 2026-10-13T00:00:00Z\n")
	write("welcome.md", "---\ntitle: Welcome\n---\n\nHello.\n")
	write("maps/arena.md", "---\ntitle: Arena\nowner: gm\nvtt-map:\n  grid: {cols: 20, rows: 14}\n  portals:\n    - {a: [1, 0], b: [2, 0], closed: true}\n---\n\n# Arena\n")
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
	srv := httptest.NewServer(wireHandlers(buildInfo{Version: "import-gate"}, vault, dataDir, sessionStore, "", "").Handler)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	us, err := auth.NewUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, u := range []struct {
		name string
		gm   bool
	}{{"gm", true}, {"alice", false}} {
		if err := us.Create(context.Background(), &auth.User{
			Username: u.name, PasswordHash: "phc-import", IsGM: u.gm,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mint := func(user string) (cookie, csrf string) {
		t.Helper()
		tok, err := auth.NewCSRFToken()
		if err != nil {
			t.Fatal(err)
		}
		id, err := sessionStore.Create(context.Background(), &auth.Session{
			UserID: user, CSRFToken: tok,
			CreatedAt: now, ExpiresAt: now + 3600, IdleAt: now + 3600, Version: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		keys, err := auth.LoadSessionKeys(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		// Signed cookie value (what the login handler mints): the auth
		// middleware verifies the HMAC before injecting the viewer.
		return auth.SignSessionCookie(keys[0], id, user, now+3600), tok
	}
	g := &importGate{t: t, vault: vault, srv: srv, jar: jar}
	g.gm.cookie, g.gm.csrf = mint("gm")
	g.pl.cookie, g.pl.csrf = mint("alice")
	return g
}

func (g *importGate) req(method, target, body, cookie, csrf string) (int, string) {
	g.t.Helper()
	r := strings.NewReader(body)
	req, err := http.NewRequest(method, g.srv.URL+target, r)
	if err != nil {
		g.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	}
	if csrf != "" {
		req.Header.Set(auth.CSRFHeaderName, csrf)
	}
	resp, err := (&http.Client{Jar: g.jar}).Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 1<<20)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}

const gateUVTT = `{
  "format": 1.0,
  "resolution": {"map_origin": {"x":0,"y":0}, "map_size": {"x":4,"y":3}, "pixels_per_grid": 100},
  "line_of_sight": [[{"x":0,"y":0},{"x":4,"y":0}]],
  "portals": [{"bounds":[{"x":1,"y":0},{"x":2,"y":0}],"closed":true}],
  "environment": {"baked_lighting": false, "ambient_light": "ff80668d"},
  "lights": [{"position":{"x":2,"y":1.5},"range":2,"color":"ff0000ff","intensity":0.8,"shadows":true}],
  "image": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
}`

func TestImportGateOverHTTP(t *testing.T) {
	g := newImportGate(t)
	// GM import over the real mux.
	code, body := g.req(http.MethodPost, "/vtt/arena/import?source=ford.dd2vtt", gateUVTT, g.gm.cookie, g.gm.csrf)
	if code != http.StatusOK {
		t.Fatalf("GM import = %d: %s", code, body)
	}
	if !strings.Contains(body, `"walls":1`) || !strings.Contains(body, `"lights":1`) {
		t.Errorf("summary = %s", body)
	}
	// Vault writes: sidecar calibration + asset image.
	side, err := os.ReadFile(filepath.Join(g.vault, "maps", "arena.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"grid: {cols: 4, rows: 3, cell: 100}", "ambient: 80668d", "uvtt-source: ford.dd2vtt"} {
		if !strings.Contains(string(side), want) {
			t.Errorf("sidecar missing %q:\n%s", want, side)
		}
	}
	if _, err := os.Stat(filepath.Join(g.vault, "assets", "maps", "arena.png")); err != nil {
		t.Errorf("image not written: %v", err)
	}
	// Index refresh rode the serve OnAfterWrite seam: the state endpoint
	// already serves the imported calibration (walls visible to any viewer).
	code, body = g.req(http.MethodGet, "/api/vtt/arena/state?as=gm", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("state = %d", code)
	}
	var snap map[string]any
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatal(err)
	}
	walls, _ := snap["walls"].([]any)
	if len(walls) != 1 {
		t.Errorf("snapshot walls = %v, want 1 (index refresh missing?)", snap["walls"])
	}
	doors, _ := snap["doors"].([]any)
	if len(doors) != 1 {
		t.Errorf("snapshot doors = %v, want 1", snap["doors"])
	}

	// Player imports are GM-only; missing CSRF fails closed even for the GM.
	if code, _ := g.req(http.MethodPost, "/vtt/arena/import", gateUVTT, g.pl.cookie, g.pl.csrf); code != http.StatusForbidden {
		t.Errorf("player import = %d, want 403", code)
	}
	if code, _ := g.req(http.MethodPost, "/vtt/arena/import", gateUVTT, g.gm.cookie, "wrong-token"); code != http.StatusForbidden {
		t.Errorf("bad csrf import = %d, want 403", code)
	}
	// Corrupt file is a clean 422 with no vault content.
	if code, body := g.req(http.MethodPost, "/vtt/arena/import", `{not json`, g.gm.cookie, g.gm.csrf); code != http.StatusUnprocessableEntity || strings.Contains(body, "arena") {
		t.Errorf("corrupt import = %d %q", code, body)
	}

	// Door toggle through the state-op route (GM-only).
	code, body = g.req(http.MethodPost, "/vtt/arena/state", `{"op":"door","idx":0,"open":true}`, g.gm.cookie, g.gm.csrf)
	if code != http.StatusOK {
		t.Fatalf("GM door = %d: %s", code, body)
	}
	if !strings.Contains(body, `"door":0`) || !strings.Contains(body, `"open":true`) {
		t.Errorf("door response = %s", body)
	}
	_, body = g.req(http.MethodGet, "/api/vtt/arena/state?as=gm", "", "", "")
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatal(err)
	}
	doors, _ = snap["doors"].([]any)
	if len(doors) != 1 {
		t.Fatalf("doors = %v", snap["doors"])
	}
	if closed, _ := doors[0].(map[string]any)["closed"].(bool); closed {
		t.Error("door 0 still closed after the GM toggle")
	}
	// Players never toggle doors.
	if code, _ := g.req(http.MethodPost, "/vtt/arena/state", `{"op":"door","idx":0,"open":false}`, g.pl.cookie, g.pl.csrf); code != http.StatusForbidden {
		t.Errorf("player door = %d, want 403", code)
	}
	// Guests hit 401 on the state surface (no live VTT for guests).
	if code, _ := g.req(http.MethodGet, "/api/vtt/arena/state", "", "", ""); code != http.StatusUnauthorized {
		t.Errorf("guest state = %d, want 401", code)
	}
}

// R1: the new 55MB cap + the admission guard through the production mux.
// An over-cap POST 413s (real Content-Length, so the pre-check fires without
// the server reading the body), and the default limiter is live in prod
// wiring: 3 imports succeed, the 4th 429s.
func TestImportGateCapAndGuard(t *testing.T) {
	g := newImportGate(t)
	huge := strings.Repeat("x", (55<<20)+1024)
	code, body := g.req(http.MethodPost, "/vtt/arena/import", huge, g.gm.cookie, g.gm.csrf)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap import = %d, want 413", code)
	}
	if !strings.Contains(body, "55MB") {
		t.Errorf("413 body = %q, want the new cap number", body)
	}
	for i := 0; i < 3; i++ {
		if code, body := g.req(http.MethodPost, "/vtt/arena/import?source=bulk.dd2vtt", gateUVTT, g.gm.cookie, g.gm.csrf); code != http.StatusOK {
			t.Fatalf("import %d = %d: %s", i, code, body)
		}
	}
	if code, _ := g.req(http.MethodPost, "/vtt/arena/import?source=bulk.dd2vtt", gateUVTT, g.gm.cookie, g.gm.csrf); code != http.StatusTooManyRequests {
		t.Errorf("4th import = %d, want 429 (default limiter live?)", code)
	}
	// Players are rejected before admission (no budget consumed, nothing
	// written for them) — a fresh map id stays absent.
	if code, _ := g.req(http.MethodPost, "/vtt/unguarded/import", gateUVTT, g.pl.cookie, g.pl.csrf); code != http.StatusForbidden {
		t.Errorf("player import = %d, want 403", code)
	}
	if _, err := os.Stat(filepath.Join(g.vault, "maps", "unguarded.md")); err == nil {
		t.Error("rejected import created a sidecar")
	}
}

// A GM may create a map by importing into a fresh id over HTTP: the route
// writes the sidecar itself, the index refresh makes it visible, and the
// whole thing rides the production mux (route pattern, session CSRF).
func TestImportGateCreatesMapOverHTTP(t *testing.T) {
	g := newImportGate(t)
	code, body := g.req(http.MethodPost, "/vtt/crypt/import?source=crypt.dd2vtt", gateUVTT, g.gm.cookie, g.gm.csrf)
	if code != http.StatusOK {
		t.Fatalf("fresh-map import = %d: %s", code, body)
	}
	side, err := os.ReadFile(filepath.Join(g.vault, "maps", "crypt.md"))
	if err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}
	for _, want := range []string{"title: crypt", "grid: {cols: 4, rows: 3, cell: 100}", "uvtt-source: crypt.dd2vtt", "This page holds calibration only"} {
		if !strings.Contains(string(side), want) {
			t.Errorf("fresh sidecar missing %q:\n%s", want, side)
		}
	}
	if _, err := os.Stat(filepath.Join(g.vault, "assets", "maps", "crypt.png")); err != nil {
		t.Errorf("image not written: %v", err)
	}
	code, body = g.req(http.MethodGet, "/api/vtt/crypt/state?as=gm", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("state = %d", code)
	}
	var snap map[string]any
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatal(err)
	}
	if walls, _ := snap["walls"].([]any); len(walls) != 1 {
		t.Errorf("fresh-map walls = %v, want 1", snap["walls"])
	}
	// Players are rejected before any creation happens.
	if code, _ := g.req(http.MethodPost, "/vtt/crypt2/import", gateUVTT, g.pl.cookie, g.pl.csrf); code != http.StatusForbidden {
		t.Errorf("player fresh-map import = %d, want 403", code)
	}
	if _, err := os.Stat(filepath.Join(g.vault, "maps", "crypt2.md")); err == nil {
		t.Error("rejected import created a sidecar")
	}
}
