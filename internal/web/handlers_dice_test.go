package web

// UI-1 dice tray tests: roll/replay over HTTP against a real app DB
// (migrated dice_logs), blind routing + replay re-auth per I2, maxima,
// auth/CSRF gates, and the dashboard dice section.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/dice"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
)

// testSetupApp is testSetup plus the app migrations (dice_logs, vtt_*).
func testSetupApp(t *testing.T) (*WriteHandlers, *vault.Vault, store.Store) {
	t.Helper()
	h, v, s := testSetup(t)
	if err := store.NewMigrationRunner(s.AppDB()).RunApp(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h, v, s
}

func jsonDiceRequest(t *testing.T, h http.HandlerFunc, target string, v *auth.Viewer, body string, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if withCSRF {
		req.Header.Set(auth.CSRFHeaderName, testCSRF)
	}
	req.AddCookie(sessionCookie())
	rec := httptest.NewRecorder()
	h(rec, withViewer(req, v))
	return rec
}

func getReplay(t *testing.T, h http.HandlerFunc, v *auth.Viewer, rollID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/dice/replay?roll_id="+url.QueryEscape(rollID), nil)
	req.AddCookie(sessionCookie())
	rec := httptest.NewRecorder()
	h(rec, withViewer(req, v))
	return rec
}

func decodeRoll(t *testing.T, rec *httptest.ResponseRecorder) diceRollJSON {
	t.Helper()
	var out diceRollJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("roll response is not JSON: %v: %s", err, rec.Body.String())
	}
	return out
}

func TestDiceRollOpenRoundTrip(t *testing.T) {
	h, _, _ := testSetupApp(t)
	rec := jsonDiceRequest(t, h.DiceRoll, "/api/dice/roll", alice(),
		`{"notation":"2d6+3"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("roll = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rolled := decodeRoll(t, rec)
	if rolled.Notation != "2d6+3" || rolled.Actor != "alice" || rolled.RollID == "" {
		t.Fatalf("roll echo wrong: %+v", rolled)
	}
	if rolled.Total == nil || rolled.Redacted {
		t.Fatalf("open roll redacted: %+v", rolled)
	}
	if rolled.Replay != "/api/dice/replay?roll_id="+rolled.RollID {
		t.Fatalf("replay link wrong: %q", rolled.Replay)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", cc)
	}
	// Replay returns the STORED total (never re-rolled).
	rep := getReplay(t, h.DiceReplay, alice(), rolled.RollID)
	if rep.Code != http.StatusOK {
		t.Fatalf("replay = %d, want 200: %s", rep.Code, rep.Body.String())
	}
	if got := decodeRoll(t, rep); got.Total == nil || *got.Total != *rolled.Total {
		t.Fatalf("replay total %v != rolled %v", got.Total, rolled.Total)
	}
	// Open rolls replay for any identified viewer.
	rep = getReplay(t, h.DiceReplay, bob(), rolled.RollID)
	if rep.Code != http.StatusOK {
		t.Fatalf("other-player replay of open roll = %d, want 200", rep.Code)
	}
}

func TestDiceBlindRoutingAndReauth(t *testing.T) {
	h, _, s := testSetupApp(t)
	rec := jsonDiceRequest(t, h.DiceRoll, "/api/dice/roll", alice(),
		`{"notation":"1d20","blind":true}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("blind roll = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rolled := decodeRoll(t, rec)
	if !rolled.Blind || !rolled.Redacted || rolled.Total != nil {
		t.Fatalf("blind roll leaked total to roller: %+v", rolled)
	}
	if rolled.Placeholder == "" {
		t.Fatalf("blind roll missing placeholder text: %+v", rolled)
	}
	// The blind flag persists in dice_logs.
	var blind int
	if err := s.AppDB().QueryRow(`SELECT blind FROM dice_logs WHERE id = ?`,
		rolled.RollID).Scan(&blind); err != nil || blind != 1 {
		t.Fatalf("dice_logs blind = %d, err = %v; want 1", blind, err)
	}
	// The roller replays redacted (display filter), the GM sees the total,
	// and another player is refused at the gate.
	if rep := getReplay(t, h.DiceReplay, alice(), rolled.RollID); rep.Code != http.StatusOK {
		t.Fatalf("roller replay = %d, want 200", rep.Code)
	} else if got := decodeRoll(t, rep); got.Total != nil || !got.Redacted {
		t.Fatalf("roller replay leaked blind total: %+v", got)
	}
	if rep := getReplay(t, h.DiceReplay, bob(), rolled.RollID); rep.Code != http.StatusForbidden {
		t.Fatalf("other-player blind replay = %d, want 403", rep.Code)
	}
	rep := getReplay(t, h.DiceReplay, gm(), rolled.RollID)
	if rep.Code != http.StatusOK {
		t.Fatalf("GM blind replay = %d, want 200", rep.Code)
	}
	got := decodeRoll(t, rep)
	if got.Total == nil || got.Redacted {
		t.Fatalf("GM blind replay redacted: %+v", got)
	}
}

func TestDiceRollGMBlindSeesTotal(t *testing.T) {
	h, _, _ := testSetupApp(t)
	rec := jsonDiceRequest(t, h.DiceRoll, "/api/dice/roll", gm(),
		`{"notation":"1d20","blind":true}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GM blind roll = %d, want 200", rec.Code)
	}
	if got := decodeRoll(t, rec); got.Total == nil || got.Redacted {
		t.Fatalf("GM blind roll redacted: %+v", got)
	}
}

func TestDiceRollAuthGates(t *testing.T) {
	h, _, _ := testSetupApp(t)
	// Guest (no viewer): 401.
	req := httptest.NewRequest(http.MethodPost, "/api/dice/roll", strings.NewReader(`{"notation":"1d6"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.DiceRoll(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("guest roll = %d, want 401", rec.Code)
	}
	// Logged in but no CSRF: 403.
	rec = jsonDiceRequest(t, h.DiceRoll, "/api/dice/roll", alice(), `{"notation":"1d6"}`, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("no-CSRF roll = %d, want 403", rec.Code)
	}
	// Replay needs login too.
	req = httptest.NewRequest(http.MethodGet, "/api/dice/replay?roll_id=1", nil)
	rec = httptest.NewRecorder()
	h.DiceReplay(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("guest replay = %d, want 401", rec.Code)
	}
}

func TestDiceRollValidation(t *testing.T) {
	h, _, _ := testSetupApp(t)
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"empty", `{"notation":""}`, http.StatusBadRequest},
		{"garbage", `{"notation":"bogus"}`, http.StatusBadRequest},
		{"too many dice", `{"notation":"101d6"}`, http.StatusUnprocessableEntity},
		{"too many faces", `{"notation":"1d1001"}`, http.StatusUnprocessableEntity},
		{"not json", `{oops`, http.StatusBadRequest},
	} {
		if rec := jsonDiceRequest(t, h.DiceRoll, "/api/dice/roll", alice(), tc.body, true); rec.Code != tc.want {
			t.Errorf("%s: roll = %d, want %d: %s", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestDiceRollFormRedirectsToDashboard(t *testing.T) {
	h, _, s := testSetupApp(t)
	rec := postForm(t, h.DiceRoll, "/api/dice/roll", alice(),
		map[string]string{"notation": "1d8", "blind": "on"}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("form roll = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard" {
		t.Fatalf("form roll Location = %q, want /dashboard", loc)
	}
	var n int
	if err := s.AppDB().QueryRow(`SELECT COUNT(*) FROM dice_logs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dice_logs rows = %d, err = %v; want 1", n, err)
	}
}

func TestDiceReplayMissing(t *testing.T) {
	h, _, _ := testSetupApp(t)
	if rec := getReplay(t, h.DiceReplay, alice(), "9999"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown replay = %d, want 404", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/dice/replay", nil)
	req.AddCookie(sessionCookie())
	rec := httptest.NewRecorder()
	h.DiceReplay(rec, withViewer(req, alice()))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty replay = %d, want 400", rec.Code)
	}
}

// seedDiceRoll writes one roll through the real transport (stored values,
// never fabricated) for dashboard tests.
func seedDiceRoll(t *testing.T, s store.Store, actor, notation string, blind bool) string {
	t.Helper()
	ts := dice.NewTransportService(dice.NewRoller(), dice.NewLogStore(s.AppDB()), dice.DefaultTransportConfig())
	resp, err := ts.Roll(context.Background(), dice.RollRequest{
		Notation: notation, ActorID: actor, Blind: blind,
		Metadata: map[string]any{"viewer": "actor:" + actor},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.LogID
}

func TestDashboardDiceSection(t *testing.T) {
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
	openID := seedDiceRoll(t, s, "alice", "2d6+3", false)
	blindID := seedDiceRoll(t, s, "alice", "1d20", true)

	h := &ReadHandlers{Store: s, SlotRegistry: NewSlotRegistry()}
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	h.Dashboard(rec, withViewer(req, gm()))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`aria-labelledby="dice-tray"`,
		`action="/api/dice/roll"`, `name="notation"`, `name="blind"`,
		"2d6+3", "1d20",
		"/api/dice/replay?roll_id=" + openID,
		"/api/dice/replay?roll_id=" + blindID,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard dice section missing %q", want)
		}
	}
	// GM sees totals (no blind placeholder on a GM-only page).
	if strings.Contains(body, blindPlaceholder) {
		t.Errorf("GM dashboard shows blind placeholder (GM must see totals)")
	}
}
