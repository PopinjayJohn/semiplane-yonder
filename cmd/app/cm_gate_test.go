package main

// Gate G3 integration test (M2 demo proof, committed — no throwaways).
//
// Boots the PRODUCTION serve wiring (wireHandlers: registry order, demo
// middleware, plugin registries, vault-backed packs) over a temp vault on
// a live httptest server and runs the M2 demo end to end:
//
//  1. Wizard → owned sheet → /me → rest-recovery, zero frontmatter exposure
//     (GM claim issue, player wizard steps, finalize, index import,
//     Simple view, damage, long rest).
//  2. Overlay switch with the server running: /me keeps serving (no
//     restart, no reindex); the engine stack follows the edit.
//  3. Slots: enabled plugin mounts render; disabled plugin leaves no UI
//     trace (not even a mount point).
//  4. Dice: roll → log → replay reads STORED values (identical twice),
//     per-viewer re-auth, blind routing GM-only.
//  5. Dashboard run: encounter built from compendium picks → spawn intent →
//     assembled + rendered GM dashboard.
//
// Live-server identity is `?as=` for GETs (M1 smoke precedent; there is no
// HTTP login surface in v1). State-changing POSTs use minted sessions +
// CSRF tokens — exactly what the login handler will produce (Lane C owns
// that endpoint; the store API is the seam).

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/dice"
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/ruleset"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/web"
)

type gateRig struct {
	t        *testing.T
	vault    string
	srv      *httptest.Server
	client   *http.Client
	wiring   *serverWiring
	db       *sql.DB
	gmCookie string
	plCookie string
	gmCSRF   string
	plCSRF   string
}

func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(s, d); err != nil {
				return err
			}
			continue
		}
		b, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func newGateRig(t *testing.T) *gateRig {
	t.Helper()
	vault := t.TempDir()
	if err := copyTree("../../fixtures/demo/rules", filepath.Join(vault, "rules")); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(vault, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("campaign.yaml", "name: Gate M2\ncreated: 2026-10-08T00:00:00Z\nbase: dnd\nbase-version: \"5.2\"\noverlay: 5e-2024\noverlay-version: \"2024\"\n")
	write("welcome.md", "---\ntitle: Welcome\n---\n\nHello, table.\n")

	dataDir := t.TempDir()
	// Real index import before serve boots (production order: reindex, then
	// serve — the smoke script proves the same flow against the binary).
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
	wiring := wireHandlers(buildInfo{Version: "gate"}, vault, dataDir, sessionStore)
	srv := httptest.NewServer(wiring.Handler)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	g := &gateRig{t: t, vault: vault, srv: srv, client: client, wiring: wiring, db: db}

	// Users + sessions (what the login endpoint will mint; Lane C owns it).
	us, err := auth.NewUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, u := range []struct {
		name string
		gm   bool
	}{
		{"gm", true},
		{"alice", false},
	} {
		if err := us.Create(context.Background(), &auth.User{
			Username: u.name, PasswordHash: "phc-gate", IsGM: u.gm,
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
		return id, tok
	}
	gmCookie, gmCSRF := mint("gm")
	plCookie, plCSRF := mint("alice")
	g.gmCSRF, g.plCSRF = gmCSRF, plCSRF
	_ = gmCookie
	_ = plCookie
	g.gmCookie, g.plCookie = gmCookie, plCookie
	return g
}

func (g *gateRig) do(method, target, asUser, cookie string, form url.Values) (int, string, http.Header) {
	g.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	// Live-server identity is `?as=` (M1 smoke precedent; request contexts
	// do not cross HTTP). The serve middleware injects it for session-only
	// handlers; session cookies + CSRF tokens authorize writes.
	if asUser != "" {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + "as=" + url.QueryEscape(asUser)
	}
	req, err := http.NewRequest(method, g.srv.URL+target, body)
	if err != nil {
		g.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	}
	resp, err := g.client.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (g *gateRig) postForm(target, asUser, cookie, csrf string, fields map[string]string) (int, string, http.Header) {
	g.t.Helper()
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	form.Set(auth.CSRFFieldName, csrf)
	return g.do(http.MethodPost, target, asUser, cookie, form)
}

func (g *gateRig) get(target, asUser string) (int, string) {
	g.t.Helper()
	code, body, _ := g.do(http.MethodGet, target, asUser, "", nil)
	return code, body
}

// ---------------------------------------------------------------------------
// 1. Wizard → owned sheet → /me → rest-recovery, zero frontmatter exposure.
// ---------------------------------------------------------------------------

func TestGateM2WizardToRest(t *testing.T) {
	g := newGateRig(t)
	ctx := context.Background()

	// GM issues a claim link for alice's slug.
	code, body, _ := g.postForm("/wizard/claims", "gm", g.gmCookie, g.gmCSRF,
		map[string]string{"slug": "alice", "username": "alice"})
	if code != http.StatusOK {
		t.Fatalf("claim issue = %d: %s", code, body)
	}
	m := regexp.MustCompile(`/c/([A-Za-z0-9_-]+)/create`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no claim link in response: %s", body)
	}
	link := "/c/" + m[1] + "/create"

	// Player walks the wizard (each POST redirects to the next step).
	steps := []map[string]string{
		{"step": "start", "name": "Alice", "notes": ""},
		{"step": "ancestry", "ancestry": "human"},
		{"step": "class", "class": "fighter"},
		{"step": "background", "background": "soldier"},
		{"step": "stats", "str": "15", "dex": "14", "con": "13", "int": "12", "wis": "10", "cha": "8"},
	}
	for _, s := range steps {
		code, body, hdr := g.postForm(link, "alice", g.plCookie, g.plCSRF, s)
		if code != http.StatusSeeOther {
			t.Fatalf("wizard %s = %d: %s", s["step"], code, body)
		}
		_ = hdr
	}
	// Preview renders without frontmatter.
	if code, body := g.get(link+"?step=preview", "alice"); code != http.StatusOK || !strings.Contains(body, "Alice") {
		t.Fatalf("preview = %d: %s", code, body)
	}
	// Finalize creates the owned sheet.
	code, _, hdr := g.postForm(link, "alice", g.plCookie, g.plCSRF, map[string]string{"step": "done"})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/me" {
		t.Fatalf("finalize = %d, loc %q", code, hdr.Get("Location"))
	}
	raw, err := g.wiring.Vault.ReadFile(ctx, "characters/alice/index.md")
	if err != nil {
		t.Fatalf("sheet file missing: %v", err)
	}
	for _, want := range []string{"owner: alice", "secret: true", "hit-die: 10"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("sheet file lacks %q:\n%s", want, raw)
		}
	}
	// Watcher-equivalent import (vault is truth; index follows).
	if err := g.wiring.Store.PageUpsert(ctx, &store.Page{
		Path: "characters/alice/index.md", Title: "Alice", Content: string(raw),
		Secret: true, Owner: "alice", UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	// /me Simple view: derived widgets only, zero frontmatter exposure.
	code, body = g.get("/me", "alice")
	if code != http.StatusOK {
		t.Fatalf("/me = %d: %s", code, body)
	}
	for _, want := range []string{"HP 11 of 11", "Level 1", "Short rest", "Level up to 2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("/me lacks %q:\n%s", want, body)
		}
	}
	for _, leak := range []string{"sheet:", "hit-dice-max", "hp-max", "frontmatter"} {
		if strings.Contains(body, leak) {
			t.Fatalf("/me exposed %q", leak)
		}
	}
	// Another player sees nothing (uniform empty home, never the sheet).
	if code, body := g.get("/me", "bram"); code != http.StatusOK || strings.Contains(body, "HP 11") {
		t.Fatalf("bram /me = %d: %s", code, body)
	}
	// Damage then long rest: HP math via the vault funnel, real engine hooks.
	// Identity rides `?as=` (live tier); authority rides the session cookie
	// + CSRF header (I1 pitfall: X-CSRF-Token header over JSON).
	patchReq, _ := http.NewRequest("PATCH", g.srv.URL+"/characters/alice/fields?as=alice", strings.NewReader(`{"field":"hp","value":4}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set(auth.CSRFHeaderName, g.plCSRF)
	patchReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: g.plCookie})
	resp, err := g.client.Do(patchReq)
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("damage patch = %d: %s", resp.StatusCode, pb)
	}
	code, body, _ = g.postForm("/characters/alice/rest", "alice", g.plCookie, g.plCSRF, map[string]string{"mode": "long"})
	if code != http.StatusOK || !strings.Contains(body, "Long rest") {
		t.Fatalf("long rest = %d: %s", code, body)
	}
	if code, body := g.get("/me", "alice"); code != http.StatusOK || !strings.Contains(body, "HP 11 of 11") {
		t.Fatalf("post-rest /me = %d: %s", code, body)
	}
	// Second finalize on the consumed token 404s (single-use).
	if code, _, _ := g.postForm(link, "alice", g.plCookie, g.plCSRF, map[string]string{"step": "done"}); code != http.StatusNotFound {
		t.Fatalf("reused claim = %d, want 404", code)
	}
}

// ---------------------------------------------------------------------------
// 2. Overlay switch with the server running (no restart, no reindex).
// ---------------------------------------------------------------------------

func TestGateM2OverlaySwitchLive(t *testing.T) {
	g := newGateRig(t)
	ctx := context.Background()
	sheet := "---\ntitle: Alice\nsecret: true\nowner: alice\nsheet:\n  class: fighter\n  ancestry: human\n  background: soldier\n  level: 1\n  xp: 0\n  str: 15\n  dex: 14\n  con: 13\n  int: 12\n  wis: 10\n  cha: 8\n  hp: 11\n  hp-max: 11\n  hit-dice: 1\n  hit-dice-max: 1\n  hit-die: 10\n  inspiration: 0\n  death-succ: 0\n  death-fail: 0\n---\n\n# Alice\n"
	// Vault truth + index hint (watcher-equivalent): /me reads the file.
	if err := g.wiring.Vault.WriteFile(ctx, "characters/alice/index.md", sheet); err != nil {
		t.Fatal(err)
	}
	if err := g.wiring.Store.PageUpsert(ctx, &store.Page{
		Path: "characters/alice/index.md", Title: "Alice", Content: sheet,
		Secret: true, Owner: "alice", UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := ruleset.LoadVaultStack(g.vault)
	if err != nil {
		t.Fatal(err)
	}
	m, err := before.Evaluate(ctx, ruleset.Intent{Intent: "rest-short", Actor: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Metadata.OverlayID != "5e-2024" {
		t.Fatalf("want 5e-2024, got %q", m.Metadata.OverlayID)
	}
	// The server keeps serving while the overlay switches underneath it.
	if code, _ := g.get("/me", "alice"); code != http.StatusOK {
		t.Fatalf("pre-switch /me = %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(g.vault, "campaign.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	swapped := strings.Replace(string(raw), "5e-2024", "5e-2014", 1)
	if err := os.WriteFile(filepath.Join(g.vault, "campaign.yaml"), []byte(swapped), 0o644); err != nil {
		t.Fatal(err)
	}
	// No restart, no reindex: same server, same process.
	if code, _ := g.get("/me", "alice"); code != http.StatusOK {
		t.Fatalf("post-switch /me = %d", code)
	}
	after, err := ruleset.LoadVaultStack(g.vault)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := after.Evaluate(ctx, ruleset.Intent{Intent: "rest-short", Actor: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Metadata.OverlayID != "5e-2014" {
		t.Fatalf("want 5e-2014 after switch, got %q", m2.Metadata.OverlayID)
	}
}

// ---------------------------------------------------------------------------
// 3. Disabled plugin leaves no UI trace (slot mounts via SlotsFor).
// ---------------------------------------------------------------------------

type gatePlugin struct {
	id      string
	enabled bool
}

func (p *gatePlugin) ID() string      { return p.id }
func (p *gatePlugin) Name() string    { return "gate " + p.id }
func (p *gatePlugin) Version() string { return "0.1" }
func (p *gatePlugin) Init(_ context.Context, _ *plugins.Registry) error {
	return nil
}
func (p *gatePlugin) Routes() []web.Route { return nil }
func (p *gatePlugin) Slots() []web.SlotComponent {
	return []web.SlotComponent{{
		SlotName: web.SlotSidebarRight, ID: "dice-widget", Component: "random-table",
		SecretFiltered: true, PluginID: p.id,
	}}
}
func (p *gatePlugin) CSS() string { return "" }
func (p *gatePlugin) JS() string  { return "" }
func (p *gatePlugin) OnEnable(_ context.Context) error {
	p.enabled = true
	return nil
}
func (p *gatePlugin) OnDisable(_ context.Context) error {
	p.enabled = false
	return nil
}
func (p *gatePlugin) ConfigSchema() string                  { return "{}" }
func (p *gatePlugin) ValidateConfig(_ map[string]any) error { return nil }
func (p *gatePlugin) A11y() plugins.A11ySpec {
	return plugins.A11ySpec{Landmarks: []string{"complementary"}, Labels: []string{"roll tables"}}
}

func TestGateM2PluginNoTrace(t *testing.T) {
	g := newGateRig(t)
	ctx := context.Background()
	if err := g.wiring.Store.PageUpsert(ctx, &store.Page{
		Path: "welcome.md", Title: "Welcome", Content: "hi",
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	page := func() string {
		code, body := g.get("/p/welcome.md", "")
		if code != http.StatusOK {
			t.Fatalf("page = %d: %s", code, body)
		}
		return body
	}
	if strings.Contains(page(), "data-plugin=") {
		t.Fatalf("no plugins registered, yet a plugin mark rendered")
	}
	if err := g.wiring.Plugins.Register(&gatePlugin{id: "random-tables"}); err != nil {
		t.Fatal(err)
	}
	if err := g.wiring.Plugins.Enable(ctx, "random-tables"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page(), `data-plugin="random-tables"`) {
		t.Fatalf("enabled plugin left no mount:\n%s", page())
	}
	if err := g.wiring.Plugins.Disable(ctx, "random-tables"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page(), "data-plugin=") {
		t.Fatalf("disabled plugin left a UI trace")
	}
}

// ---------------------------------------------------------------------------
// 4. Dice log replays identically (stored values, per-viewer re-auth).
// ---------------------------------------------------------------------------

func TestGateM2DiceReplay(t *testing.T) {
	g := newGateRig(t)
	ctx := context.Background()
	logs := dice.NewLogStore(g.wiring.Store.AppDB())
	svc := dice.NewTransportService(dice.NewRoller(), logs, dice.DefaultTransportConfig())

	open, err := svc.Roll(ctx, dice.RollRequest{Notation: "1d8+2", ActorID: "alice", Broadcast: true})
	if err != nil {
		t.Fatal(err)
	}
	if open.Result.Total < 3 || open.Result.Total > 10 {
		t.Fatalf("open roll out of range: %d", open.Result.Total)
	}
	// Replay reads stored values: identical twice, never re-rolled.
	first, err := svc.Replay(ctx, open.LogID, "anyone")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Replay(ctx, open.LogID, "anyone-else")
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != open.Result.Total || second.Total != open.Result.Total {
		t.Fatalf("replay drift: %d / %d vs rolled %d", first.Total, second.Total, open.Result.Total)
	}
	if fmt.Sprintf("%v", first.Dice) != fmt.Sprintf("%v", second.Dice) {
		t.Fatalf("replay dice differ:\n%v\n%v", first.Dice, second.Dice)
	}

	// Blind roll: GM-only values, per-viewer re-auth on every replay.
	blind, err := svc.Roll(ctx, dice.RollRequest{
		Notation: "1d20", ActorID: "gm", Blind: true,
		Metadata: map[string]any{"viewer": "gm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Replay(ctx, blind.LogID, "alice"); err == nil {
		t.Fatalf("player replayed a blind roll")
	}
	got, err := svc.Replay(ctx, blind.LogID, "role:gm")
	if err != nil {
		t.Fatalf("GM replay: %v", err)
	}
	if got.Total != blind.Result.Total {
		t.Fatalf("GM replay drift: %d vs %d", got.Total, blind.Result.Total)
	}
	// Blind routing at the event layer: players get the placeholder.
	ev := plugins.RollEvent{RollID: blind.LogID, ActorID: "gm", Blind: true, Total: &got.Total}
	if routed := plugins.RouteBlind(ev, &auth.Viewer{UserID: "alice"}); routed.Total != nil {
		t.Fatalf("player saw blind total via RouteBlind")
	}
	if routed := plugins.RouteBlind(ev, &auth.Viewer{UserID: "gm", IsGM: true}); routed.Total == nil {
		t.Fatalf("GM denied blind total via RouteBlind")
	}
}

// ---------------------------------------------------------------------------
// 5. Dashboard run with encounter spawn intent.
// ---------------------------------------------------------------------------

func TestGateM2DashboardRun(t *testing.T) {
	ctx := context.Background()
	// Encounter from compendium truth (Goblin Warrior, HP 10 per H1 fixtures).
	enc, err := plugins.BuildEncounter("e1", "Goblin ambush", []plugins.MonsterRef{
		{ID: "goblin-warrior", Name: "Goblin Warrior", HP: 10, Count: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if enc.Size != 2 {
		t.Fatalf("encounter size = %d", enc.Size)
	}
	spawn, err := plugins.EmitSpawnIntent(enc, "arena")
	if err != nil {
		t.Fatal(err)
	}
	if spawn.Intent != plugins.SpawnIntentName || len(spawn.Tokens) != 2 || len(spawn.Order) != 2 {
		t.Fatalf("bad spawn intent: %+v", spawn)
	}
	// Dashboard assembles over the spawn (initiative + token HP) with dice.
	src := plugins.NewDashboardSource()
	src.MapID = "arena"
	for i, tok := range spawn.Tokens {
		src.Initiative = append(src.Initiative, plugins.InitiativeEntry{
			ID: tok.TokenID, Name: tok.Name, Order: i + 1, HP: tok.HP, MaxHP: tok.MaxHP,
		})
		src.Tokens = append(src.Tokens, plugins.TokenHP{
			TokenID: tok.TokenID, Name: tok.Name, HP: tok.HP, MaxHP: tok.MaxHP,
		})
	}
	total := int64(13)
	src.DiceLog = []plugins.DiceLogEntry{{RollID: "7", Actor: "alice", Notation: "1d20+5", Total: &total}}
	src.Notes = plugins.SessionNote{Path: "sessions/gate.md", Excerpt: "ambush", Recap: "spawned 2"}
	data, err := plugins.Assemble(&auth.Viewer{UserID: "gm", IsGM: true}, src)
	if err != nil {
		t.Fatal(err)
	}
	frag := plugins.RenderDashboard(data)
	if !strings.Contains(frag.HTML, "Goblin Warrior") || !strings.Contains(frag.HTML, "1d20+5") {
		t.Fatalf("dashboard missing encounter/dice:\n%s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, `data-map-id="arena"`) {
		t.Fatalf("dashboard missing map fragment embed:\n%s", frag.HTML)
	}
	// Non-GM assembly is forbidden (never_preview + players alike).
	if _, err := plugins.Assemble(&auth.Viewer{UserID: "alice"}, src); err == nil {
		t.Fatalf("player assembled the GM dashboard")
	}
	_ = ctx
}
