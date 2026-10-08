package vtt

// HTTP + snapshot tests: per-viewer filtering, fog fail-closed, move auth,
// GM ops, blind dice routing, and the 6-client load run (load_test.go).
//
// Test store: file-backed temp DBs (pitfalls: :memory: does not survive
// pooling) with BOTH migration targets applied — RunIndex for the sidecar
// page rows, RunApp for the vtt_* live tables (now user_version 3).

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/store"
)

const (
	testSessID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 32 hex chars
	testCSRF   = "test-csrf-token-k-lane-01234567"
)

type stubSessions struct {
	sessions map[string]*auth.Session
}

func (s *stubSessions) Create(_ context.Context, _ *auth.Session) (string, error) {
	return "", nil
}
func (s *stubSessions) Get(_ context.Context, id string) (*auth.Session, error) {
	sess, ok := s.sessions[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return sess, nil
}
func (s *stubSessions) Update(_ context.Context, _ *auth.Session) error { return nil }
func (s *stubSessions) Delete(_ context.Context, _ string) error        { return nil }
func (s *stubSessions) DeleteByUser(_ context.Context, _ string) error  { return nil }
func (s *stubSessions) CleanupExpired(_ context.Context) error          { return nil }

func testStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "t.index.db"), filepath.Join(dir, "t.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := store.NewMigrationRunner(s.IndexDB()).RunIndex(ctx, s.IndexDB()); err != nil {
		t.Fatal(err)
	}
	if err := store.NewMigrationRunner(s.AppDB()).RunApp(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

const arenaSidecarFM = `---
title: Arena
vtt-map:
  background: maps/arena.png
  grid: {cols: 20, rows: 14}
  fog-default: hidden
---

# Arena
`

func seedArena(t *testing.T, s store.Store, secret bool, owner string) {
	t.Helper()
	fm := map[string]any{
		"title":  "Arena",
		"secret": secret,
		"owner":  owner,
		"vtt-map": map[string]any{
			"background": "maps/arena.png",
			"grid":       map[string]any{"cols": int64(20), "rows": int64(14)},
		},
	}
	if err := s.PageUpsert(context.Background(), &store.Page{
		Path: "maps/arena.md", Title: "Arena", Content: arenaSidecarFM,
		Frontmatter: fm, Secret: secret, Owner: owner,
		EditableBy: []string{}, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

func seedSheetPage(t *testing.T, s store.Store, slug, owner string, secret bool) {
	t.Helper()
	if err := s.PageUpsert(context.Background(), &store.Page{
		Path: "characters/" + slug + "/index.md", Title: slug, Content: "# " + slug,
		Secret: secret, Owner: owner, EditableBy: []string{}, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

func testHandlers(s store.Store) *Handlers {
	return &Handlers{Store: s, Sessions: &stubSessions{sessions: map[string]*auth.Session{
		testSessID: {ID: testSessID, UserID: "gm", CSRFToken: testCSRF,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}}}
}

func withViewerCtx(r *http.Request, v *auth.Viewer) *http.Request {
	if v == nil {
		return r
	}
	return r.WithContext(auth.WithViewer(r.Context(), v))
}

func gmCookie() *http.Cookie {
	return &http.Cookie{Name: auth.SessionCookieName, Value: testSessID}
}

func jsonStateRequest(t *testing.T, method, target string, v *auth.Viewer, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeaderName, testCSRF)
	req.AddCookie(gmCookie())
	req = withViewerCtx(req, v)
	return httptest.NewRecorder(), req
}

func seedLive(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := UpsertToken(ctx, s, "arena", Token{ID: "mira-pc", Name: "Mira", X: 2, Y: 3, HP: 11, MaxHP: 11, Character: "characters/mira/index.md"}); err != nil {
		t.Fatal(err)
	}
	if err := UpsertToken(ctx, s, "arena", Token{ID: "gob-1", Name: "Goblin Boss", X: 9, Y: 9, HP: 21, MaxHP: 21, Hidden: true}); err != nil {
		t.Fatal(err)
	}
	if err := SetInitiative(ctx, s, "arena", []string{"gob-1", "mira-pc"}); err != nil {
		t.Fatal(err)
	}
	if err := SetFog(ctx, s, "arena", EncodeMask(Grid{Cols: 20, Rows: 14}, []Rect{{X: 8, Y: 8, W: 4, H: 4}})); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotFiltering(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedSheetPage(t, s, "mira", "mira", true)
	seedLive(t, s)
	ctx := context.Background()

	// GM: everything, hidden flagged.
	snap, _, err := SnapshotFor(ctx, s, gmViewer(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tokens) != 2 {
		t.Fatalf("GM tokens = %d, want 2", len(snap.Tokens))
	}
	if !snap.Tokens[0].Hidden || snap.Tokens[0].ID != "gob-1" {
		t.Errorf("GM must see hidden flag: %+v", snap.Tokens)
	}
	if len(snap.Initiative) != 2 {
		t.Errorf("GM initiative = %d, want 2", len(snap.Initiative))
	}

	// Player: hidden token gone entirely (id/name/position/HP), initiative clean.
	psnap, _, err := SnapshotFor(ctx, s, playerViewer("mira"), "arena")
	if err != nil {
		t.Fatal(err)
	}
	if len(psnap.Tokens) != 1 || psnap.Tokens[0].ID != "mira-pc" {
		t.Fatalf("player tokens leak: %+v", psnap.Tokens)
	}
	b, _ := SnapshotJSON(psnap)
	for _, leak := range []string{"gob-1", "Goblin Boss", `"hidden":true`, "9,9", `"x":9`} {
		if strings.Contains(string(b), leak) {
			t.Errorf("player snapshot leaks %q: %s", leak, b)
		}
	}
	if len(psnap.Initiative) != 1 || psnap.Initiative[0].ID != "mira-pc" {
		t.Errorf("player initiative leaks hidden row: %+v", psnap.Initiative)
	}
	if psnap.Fog.Hidden || psnap.Fog.Shape == "" {
		t.Errorf("player fog wrong: %+v", psnap.Fog)
	}

	// Guest: nothing.
	if _, _, err := SnapshotFor(ctx, s, nil, "arena"); err == nil {
		t.Error("guest snapshot succeeded")
	}
	// Unviewable secret map: other player 404s.
	seedArena(t, s, true, "gm")
	if _, _, err := SnapshotFor(ctx, s, playerViewer("bram"), "arena"); err == nil {
		t.Error("non-owner saw secret map")
	}
}

func TestFogFailClosedAfterDataLoss(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedLive(t, s)
	ctx := context.Background()
	// Full data-dir loss: wipe live rows (index sidecar survives — vault truth).
	db := s.AppDB()
	for _, q := range []string{`DELETE FROM vtt_tokens`, `DELETE FROM vtt_fog`, `DELETE FROM vtt_initiative`} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []*auth.Viewer{gmViewer(), playerViewer("mira")} {
		snap, _, err := SnapshotFor(ctx, s, v, "arena")
		if err != nil {
			t.Fatal(err)
		}
		if !snap.Fog.Hidden {
			t.Errorf("viewer %v failed open after data loss", v.UserID)
		}
		if len(snap.Tokens) != 0 || len(snap.Initiative) != 0 {
			t.Errorf("viewer %v sees phantom state: %+v", v.UserID, snap)
		}
	}
	// GM reseed restores sidecar posture (hidden default → fully hidden mask
	// present, tokens from defaults — none here).
	h := testHandlers(s)
	rec, req := jsonStateRequest(t, http.MethodPost, "/vtt/arena/state", gmViewer(), `{"op":"reset"}`)
	h.StateUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d: %s", rec.Code, rec.Body.String())
	}
	snap, _, err := SnapshotFor(ctx, s, gmViewer(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	// Reseed restores an EXPLICIT full-cover mask (board opens with fog
	// everywhere), unlike data loss (no row → Hidden fail-closed).
	if snap.Fog.Hidden {
		t.Error("reseed should store an explicit mask, not fail-closed Hidden")
	}
	rects, ok := DecodeMask(snap.Fog.Shape, Grid{Cols: 20, Rows: 14})
	if !ok || len(rects) != 1 || rects[0] != (Rect{X: 0, Y: 0, W: 20, H: 14}) {
		t.Errorf("reseed mask wrong: %q %v", snap.Fog.Shape, rects)
	}
}

func TestCharacterLinkFiltering(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedSheetPage(t, s, "mira", "mira", true)  // secret sheet, owner mira
	seedSheetPage(t, s, "bram", "bram", false) // open sheet
	seedLive(t, s)
	ctx := context.Background()
	if err := UpsertToken(ctx, s, "arena", Token{ID: "bram-pc", Name: "Bram", X: 1, Y: 1, HP: 9, MaxHP: 9, Character: "characters/bram/index.md"}); err != nil {
		t.Fatal(err)
	}
	h := testHandlers(s)
	// Mira sees her own link; bram's open sheet links for everyone.
	snap, _, _ := SnapshotFor(ctx, s, playerViewer("mira"), "arena")
	links := h.charLinks(ctx, playerViewer("mira"), snap)
	if links["mira-pc"] != "characters/mira/index.md" {
		t.Errorf("owner lost own link: %v", links)
	}
	// Bram (other player) must not get mira's secret sheet path.
	bsnap, _, _ := SnapshotFor(ctx, s, playerViewer("bram"), "arena")
	blinks := h.charLinks(ctx, playerViewer("bram"), bsnap)
	if _, ok := blinks["mira-pc"]; ok {
		t.Errorf("secret sheet path leaked to bram: %v", blinks)
	}
	if blinks["bram-pc"] != "characters/bram/index.md" {
		t.Errorf("open link missing: %v", blinks)
	}
}

func TestGuestLockedOut(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedLive(t, s)
	h := &Handlers{Store: s}
	for _, tc := range []struct {
		method, target string
		fn             http.HandlerFunc
	}{
		{"GET", "/vtt/arena", h.Page},
		{"GET", "/vtt/arena/fragment", h.Fragment},
		{"GET", "/api/vtt/arena/state", h.State},
		{"PATCH", "/vtt/arena/state", h.Move},
		{"POST", "/vtt/arena/state", h.StateUpdate},
	} {
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		tc.fn(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s guest = %d, want 401", tc.method, tc.target, rec.Code)
		}
	}
}

func TestMoveAuth(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedSheetPage(t, s, "mira", "mira", true)
	seedLive(t, s)
	h := testHandlers(s)

	move := func(v *auth.Viewer, token string, x, y int) int {
		rec, req := jsonStateRequest(t, http.MethodPatch, "/vtt/arena/state", v,
			`{"token":"`+token+`","x":`+itoa(x)+`,"y":`+itoa(y)+`}`)
		h.Move(rec, req)
		return rec.Code
	}
	// GM moves anything, even hidden.
	if code := move(gmViewer(), "gob-1", 5, 5); code != http.StatusOK {
		t.Errorf("GM move hidden = %d", code)
	}
	// Owner moves own PC token.
	if code := move(playerViewer("mira"), "mira-pc", 3, 3); code != http.StatusOK {
		t.Errorf("owner move = %d", code)
	}
	// Owner cannot move the hidden boss.
	if code := move(playerViewer("mira"), "gob-1", 1, 1); code != http.StatusForbidden {
		t.Errorf("owner move hidden = %d, want 403", code)
	}
	// Stranger cannot move mira's token.
	if code := move(playerViewer("bram"), "mira-pc", 1, 1); code != http.StatusForbidden {
		t.Errorf("stranger move = %d, want 403", code)
	}
	// Off-grid rejected.
	if code := move(gmViewer(), "mira-pc", 99, 99); code != http.StatusUnprocessableEntity {
		t.Errorf("off-grid = %d, want 422", code)
	}
	// GM preview is read-only.
	if code := move(previewViewer(), "mira-pc", 1, 2); code != http.StatusForbidden {
		t.Errorf("preview move = %d, want 403", code)
	}
	// Verify stored position + response shape (no hidden flag for players).
	rec, req := jsonStateRequest(t, http.MethodPatch, "/vtt/arena/state", playerViewer("mira"), `{"token":"mira-pc","x":4,"y":4}`)
	h.Move(rec, req)
	var tok map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok["x"] != 4.0 {
		t.Errorf("move response = %s, %v", rec.Body.String(), err)
	}
	if _, ok := tok["hidden"]; ok {
		t.Errorf("hidden flag served to player: %v", tok)
	}
}

func TestCSRFEnforcedWithSessions(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedLive(t, s)
	h := testHandlers(s)
	// No CSRF header + no form field → 403 even with a valid session cookie.
	req := httptest.NewRequest(http.MethodPatch, "/vtt/arena/state", strings.NewReader(`{"token":"mira-pc","x":1,"y":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(gmCookie())
	req = withViewerCtx(req, gmViewer())
	rec := httptest.NewRecorder()
	h.Move(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("missing CSRF = %d, want 403", rec.Code)
	}
}

func TestGMOps(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedSheetPage(t, s, "mira", "mira", true)
	seedLive(t, s)
	h := testHandlers(s)
	ctx := context.Background()

	post := func(v *auth.Viewer, body string) int {
		rec, req := jsonStateRequest(t, http.MethodPost, "/vtt/arena/state", v, body)
		h.StateUpdate(rec, req)
		return rec.Code
	}
	// Player HP set denied; GM allowed (clamped to max).
	if code := post(playerViewer("mira"), `{"op":"hp","token":"mira-pc","hp":3}`); code != http.StatusForbidden {
		t.Errorf("player hp = %d, want 403", code)
	}
	if code := post(gmViewer(), `{"op":"hp","token":"mira-pc","hp":99}`); code != http.StatusOK {
		t.Errorf("GM hp = %d", code)
	}
	live, _ := LoadState(ctx, s, "arena")
	for _, tk := range live.Tokens {
		if tk.ID == "mira-pc" && tk.HP != 11 {
			t.Errorf("HP clamp failed: %+v", tk)
		}
	}
	// Fog: player denied; GM hide-all then reveal rect.
	if code := post(playerViewer("mira"), `{"op":"fog","mode":"hide-all"}`); code != http.StatusForbidden {
		t.Errorf("player fog = %d, want 403", code)
	}
	if code := post(gmViewer(), `{"op":"fog","mode":"hide-all"}`); code != http.StatusOK {
		t.Fatalf("GM hide-all = %d", code)
	}
	snap, _, _ := SnapshotFor(ctx, s, gmViewer(), "arena")
	if snap.Fog.Hidden || len(snap.Fog.Shape) == 0 {
		t.Errorf("hide-all wrong: %+v", snap.Fog)
	}
	if code := post(gmViewer(), `{"op":"fog","mode":"reveal","rect":{"X":0,"Y":0,"W":2,"H":2}}`); code != http.StatusOK {
		t.Fatalf("GM reveal = %d", code)
	}
	live, _ = LoadState(ctx, s, "arena")
	rects, ok := DecodeMask(live.FogMask, Grid{Cols: 20, Rows: 14})
	if !ok {
		t.Fatalf("stored mask corrupt: %q", live.FogMask)
	}
	for _, r := range rects {
		if r.X < 2 && r.Y < 2 {
			t.Errorf("reveal failed, rect still covers origin: %v", rects)
		}
	}
	// Initiative reorder + unknown ids dropped.
	if code := post(gmViewer(), `{"op":"initiative","order":["mira-pc","ghost","mira-pc"]}`); code != http.StatusOK {
		t.Fatalf("initiative = %d", code)
	}
	live, _ = LoadState(ctx, s, "arena")
	if len(live.Initiative) != 1 || live.Initiative[0].Token != "mira-pc" {
		t.Errorf("initiative rows wrong: %+v", live.Initiative)
	}
	// Spawn writes tokens + initiative in one action.
	spawn := `{"op":"spawn","spawn":{"Intent":"encounter.spawn","EncounterID":"e1","MapID":"arena",` +
		`"Tokens":[{"TokenID":"e1-1","Name":"Goblin","HP":7,"MaxHP":7,"X":0,"Y":0},{"TokenID":"e1-2","Name":"Goblin","HP":7,"MaxHP":7,"X":1,"Y":0}],` +
		`"Order":["e1-1","e1-2"]}}`
	if code := post(gmViewer(), spawn); code != http.StatusOK {
		t.Fatalf("spawn = %d", code)
	}
	snap, _, _ = SnapshotFor(ctx, s, gmViewer(), "arena")
	// 4 live tokens; initiative = 2 spawned rows + 2 unordered appended.
	if len(snap.Tokens) != 4 || len(snap.Initiative) != 4 {
		t.Errorf("spawn state wrong: %d tokens %d init", len(snap.Tokens), len(snap.Initiative))
	}
	if snap.Initiative[0].ID != "e1-1" || snap.Initiative[1].ID != "e1-2" {
		t.Errorf("spawn order wrong: %+v", snap.Initiative)
	}
	// Bad spawn rejected whole (off-grid token fails closed).
	bad := `{"op":"spawn","spawn":{"MapID":"arena","Tokens":[{"TokenID":"bad","X":99,"Y":99}],"Order":["bad"]}}`
	if code := post(gmViewer(), bad); code != http.StatusUnprocessableEntity {
		t.Errorf("bad spawn = %d, want 422", code)
	}
}

func TestRollRouting(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	h := testHandlers(s)
	post := func(v *auth.Viewer, body string) (int, map[string]any) {
		rec, req := jsonStateRequest(t, http.MethodPost, "/vtt/arena/state", v, body)
		h.StateUpdate(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := post(playerViewer("mira"), `{"op":"roll","notation":"1d20+5"}`); code != http.StatusOK || out["total"] == nil {
		t.Errorf("open roll = %d %v", code, out)
	}
	if code, out := post(playerViewer("mira"), `{"op":"roll","notation":"1d20+5","blind":true}`); code != http.StatusOK || out["total"] != nil || out["redacted"] != true {
		t.Errorf("blind roll for player = %d %v", code, out)
	}
	if code, out := post(gmViewer(), `{"op":"roll","notation":"1d20+5","blind":true}`); code != http.StatusOK || out["total"] == nil {
		t.Errorf("blind roll for GM = %d %v", code, out)
	}
	if code, _ := post(playerViewer("mira"), `{"op":"roll","notation":"zzz"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("bad notation = %d, want 422", code)
	}
}

func TestFragmentA11y(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedSheetPage(t, s, "mira", "mira", true)
	seedLive(t, s)
	ctx := context.Background()
	snap, cal, err := SnapshotFor(ctx, s, gmViewer(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	live, _ := LoadState(ctx, s, "arena")
	html := RenderFragment(FragmentInput{
		MapID: "arena", Title: "Arena", Snap: snap, Cal: cal,
		Fog:    ResolveFog(live.FogMask, live.FogPresent, cal.Grid),
		Viewer: gmViewer(), IsGM: true,
		CharLinks: map[string]string{"mira-pc": "characters/mira/index.md"},
	})
	for _, want := range []string{
		`aria-label="Battle map: Arena"`, `role="option"`, `aria-live="polite"`,
		`aria-live="assertive"`, `data-fog-text`, `Goblin Boss`, `hidden from party`,
		`type="number"`, `/assets/maps/arena.png`, `column 2, row 3`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("fragment missing %q", want)
		}
	}
	// Player fragment: no hidden token content at all.
	psnap, _, _ := SnapshotFor(ctx, s, playerViewer("mira"), "arena")
	phtml := RenderFragment(FragmentInput{
		MapID: "arena", Title: "Arena", Snap: psnap, Cal: cal,
		Fog:    ResolveFog(live.FogMask, live.FogPresent, cal.Grid),
		Viewer: playerViewer("mira"),
	})
	for _, leak := range []string{"Goblin Boss", "gob-1", "hidden from party", "Fog controls"} {
		if strings.Contains(phtml, leak) {
			t.Errorf("player fragment leaks %q", leak)
		}
	}
	// Frozen contract: snapshot shape pins version/map_id/hidden/order keys.
	b, _ := json.Marshal(plugins.StateSnapshot{Version: plugins.FragmentVersion, MapID: "arena"})
	for _, want := range []string{`"version":1`, `"map_id":"arena"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("frozen shape drift: %s", b)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
