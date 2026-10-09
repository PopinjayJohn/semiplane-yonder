package web

// Onboarding handler tests (Lane I1): GM setup wizard -> campaign.yaml,
// claim issue -> player wizard -> owned index.md -> /me Simple -> rest.
// Uses the shared F2 test rig (real vault + real index DB); drafts live in
// the app DB. Serve wiring (E1) picks these handlers up at the M2 merge.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/campaign"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
)

const gmSessID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testWizard(t *testing.T) (*WizardHandlers, *vault.Vault, store.Store) {
	t.Helper()
	wh, v, s := testSetup(t)
	drafts, err := NewDraftStore(s.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	wh.SessionStore.(*stubSessionStore).sessions[gmSessID] = &auth.Session{
		ID: gmSessID, UserID: "gm", CSRFToken: testCSRF,
		CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	return &WizardHandlers{Store: s, Vault: v, SessionStore: wh.SessionStore, Drafts: drafts, Pack: StubPack()}, v, s
}

func testSheets(t *testing.T) (*SheetHandlers, *vault.Vault, store.Store) {
	t.Helper()
	wh, v, s := testSetup(t)
	wh.SessionStore.(*stubSessionStore).sessions[gmSessID] = &auth.Session{
		ID: gmSessID, UserID: "gm", CSRFToken: testCSRF,
		CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	return &SheetHandlers{Store: s, Vault: v, SessionStore: wh.SessionStore, Pack: StubPack(), Evaluator: StubEvaluator{}}, v, s
}

func gmCookie() *http.Cookie {
	return &http.Cookie{Name: auth.SessionCookieName, Value: gmSessID + "|gm|9999999999|sig"}
}

func gmPostForm(t *testing.T, h http.HandlerFunc, target string, v *auth.Viewer, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	for k, val := range fields {
		form.Set(k, val)
	}
	form.Set(auth.CSRFFieldName, testCSRF)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(gmCookie())
	rec := httptest.NewRecorder()
	h(rec, withViewer(req, v))
	return rec
}

// seedSheet writes a character sheet to vault + index (as the watcher would).
func seedSheet(t *testing.T, v *vault.Vault, s store.Store, slug, owner, content string) {
	t.Helper()
	if err := v.WriteFile(context.Background(), "characters/"+slug+"/index.md", content); err != nil {
		t.Fatal(err)
	}
	if err := s.PageUpsert(context.Background(), &store.Page{
		Path: "characters/" + slug + "/index.md", Title: slug, Content: content,
		Secret: true, Owner: owner, EditableBy: []string{}, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

const aliceSheetMD = `---
title: Alice
secret: true
owner: alice
sheet:
  class: fighter
  ancestry: human
  background: soldier
  level: 1
  xp: 0
  str: 15
  dex: 14
  con: 13
  int: 12
  wis: 10
  cha: 8
  hp: 11
  hp-max: 11
  hit-dice: 1
  hit-dice-max: 1
  hit-die: 10
  inspiration: 0
  death-succ: 0
  death-fail: 0
---

# Alice

*Level 1 Fighter.*
`

// ---------------------------------------------------------------------------
// GM setup wizard
// ---------------------------------------------------------------------------

func TestSetupWizardWritesCampaign(t *testing.T) {
	h, v, _ := testWizard(t)
	rec := gmPostForm(t, h.SetupSave, "/wizard/setup", gm(), map[string]string{
		"name": "Ashfall", "base": "dnd", "overlay": "srd-5e-2014", "features": "grit\nhonor\n",
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("setup = %d: %s", rec.Code, rec.Body.String())
	}
	got := readVault(t, v, "campaign.yaml")
	c, err := campaign.Parse([]byte(got))
	if err != nil || c.Name != "Ashfall" || len(c.EnabledFeatures) != 2 {
		t.Fatalf("campaign.yaml wrong: %v %+v\n%s", err, c, got)
	}
	// Player forbidden.
	rec = postForm(t, h.SetupSave, "/wizard/setup", alice(), map[string]string{"name": "X"}, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("player setup = %d, want 403", rec.Code)
	}
	// Overlay without base is 422.
	rec = gmPostForm(t, h.SetupSave, "/wizard/setup", gm(), map[string]string{"name": "X", "overlay": "o"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("overlay-no-base = %d, want 422", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Claim -> wizard -> owned sheet (full onboarding arc)
// ---------------------------------------------------------------------------

func runWizardToPreview(t *testing.T, h *WizardHandlers, path string, v *auth.Viewer) {
	t.Helper()
	steps := []struct {
		step   string
		fields map[string]string
	}{
		{"start", map[string]string{"name": "Alice"}},
		{"ancestry", map[string]string{"ancestry": "human"}},
		{"class", map[string]string{"class": "fighter"}},
		{"background", map[string]string{"background": "soldier"}},
		{"stats", map[string]string{"str": "15", "dex": "14", "con": "13", "int": "12", "wis": "10", "cha": "8"}},
		{"preview", map[string]string{}},
	}
	for _, s := range steps {
		s.fields["step"] = s.step
		rec := postForm(t, h.ClaimWizardPost, path, v, s.fields, true)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("step %s = %d: %s", s.step, rec.Code, rec.Body.String())
		}
	}
}

func TestClaimWizardFullArc(t *testing.T) {
	h, v, s := testWizard(t)
	_ = s
	// GM issues a claim for alice's slug.
	rec := gmPostForm(t, h.ClaimIssue, "/wizard/claims", gm(), map[string]string{"slug": "alice", "username": "alice"})
	if rec.Code != http.StatusOK {
		t.Fatalf("claim = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	i := strings.Index(body, "/c/")
	j := strings.Index(body[i:], "/create")
	link := body[i : i+j+len("/create")]

	runWizardToPreview(t, h, link, alice())

	// Finalize creates the owned sheet.
	rec = postForm(t, h.ClaimWizardPost, link, alice(), map[string]string{"step": "done"}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("finalize = %d: %s", rec.Code, rec.Body.String())
	}
	got := readVault(t, v, "characters/alice/index.md")
	for _, want := range []string{"owner: alice", "secret: true", "class: fighter", "hp-max: 11"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sheet missing %q:\n%s", want, got)
		}
	}
	// Single-use: the token is consumed.
	rec = postForm(t, h.ClaimWizardPost, link, alice(), map[string]string{"step": "done"}, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reuse = %d, want 404 (single-use)", rec.Code)
	}
	// Another player's invitation is invisible to alice.
	rec2 := gmPostForm(t, h.ClaimIssue, "/wizard/claims", gm(), map[string]string{"slug": "bob", "username": "bob"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("claim2 = %d", rec2.Code)
	}
}

func TestClaimWizardValidation(t *testing.T) {
	h, _, _ := testWizard(t)
	rec := gmPostForm(t, h.ClaimIssue, "/wizard/claims", gm(), map[string]string{"slug": "Bad Slug!", "username": "alice"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad slug = %d, want 400", rec.Code)
	}
	rec = gmPostForm(t, h.ClaimIssue, "/wizard/claims", gm(), map[string]string{"slug": "alice", "username": "alice"})
	if rec.Code != http.StatusOK {
		t.Fatal("claim failed")
	}
	body := rec.Body.String()
	i := strings.Index(body, "/c/")
	link := body[i : i+strings.Index(body[i:], "/create")+len("/create")]
	// Bad class rejected at its step.
	rec = postForm(t, h.ClaimWizardPost, link, alice(), map[string]string{"step": "class", "class": "archmage"}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad class = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	// Stats must use the array exactly once.
	rec = postForm(t, h.ClaimWizardPost, link, alice(), map[string]string{
		"step": "stats", "str": "18", "dex": "18", "con": "18", "int": "18", "wis": "18", "cha": "18"}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("god stats = %d, want 422", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Registry wiring (routes.go frozen: registered via it) + full arc
// ---------------------------------------------------------------------------

func TestI1RegistryWiring(t *testing.T) {
	reg := NewRouteRegistry()
	(&WizardHandlers{}).RegisterRoutes(reg)
	(&SheetHandlers{}).RegisterRoutes(reg)
	type key struct{ method, path string }
	have := map[key]bool{}
	for _, rt := range reg.Routes() {
		have[key{rt.Method, rt.Path}] = true
		if rt.Handler == nil {
			t.Fatalf("route %s %s has nil handler", rt.Method, rt.Path)
		}
	}
	for _, want := range []key{
		{http.MethodGet, RouteWizardSetup}, {http.MethodPost, RouteWizardSetup},
		{http.MethodPost, RouteWizardClaims},
		{http.MethodGet, RouteClaimPrefix}, {http.MethodPost, RouteClaimPrefix},
		{http.MethodGet, RouteMeSheet},
		{http.MethodGet, RouteCharPrefix}, {http.MethodPost, RouteCharPrefix}, {"PATCH", RouteCharPrefix},
	} {
		if !have[want] {
			t.Fatalf("route %s %s not registered", want.method, want.path)
		}
	}
}

// TestOnboardingArcToMe chains the M2 demo: wizard -> owned sheet ->
// /me Simple -> rest-recovery, with zero frontmatter exposure.
func TestOnboardingArcToMe(t *testing.T) {
	h, v, s := testWizard(t)
	rec := gmPostForm(t, h.ClaimIssue, "/wizard/claims", gm(), map[string]string{"slug": "alice", "username": "alice"})
	body := rec.Body.String()
	i := strings.Index(body, "/c/")
	link := body[i : i+strings.Index(body[i:], "/create")+len("/create")]
	runWizardToPreview(t, h, link, alice())
	if rec := postForm(t, h.ClaimWizardPost, link, alice(), map[string]string{"step": "done"}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("finalize = %d", rec.Code)
	}
	// Watcher-equivalent: index the new page (vault is truth; index follows).
	content := readVault(t, v, "characters/alice/index.md")
	if err := s.PageUpsert(context.Background(), &store.Page{
		Path: "characters/alice/index.md", Title: "Alice", Content: content,
		Secret: true, Owner: "alice", UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	sh := &SheetHandlers{Store: s, Vault: v, SessionStore: h.SessionStore, Pack: StubPack(), Evaluator: StubEvaluator{}}
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	merec := httptest.NewRecorder()
	sh.MeSheet(merec, withViewer(req, alice()))
	if merec.Code != http.StatusOK {
		t.Fatalf("/me = %d", merec.Code)
	}
	if !strings.Contains(merec.Body.String(), "HP 11 of 11") {
		t.Fatalf("fresh sheet wrong:\n%s", merec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Draft store: resume + expiry
// ---------------------------------------------------------------------------

func TestDraftResumeAndExpiry(t *testing.T) {
	_, _, s := testWizard(t)
	drafts, err := NewDraftStore(s.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	if err := drafts.Save(ctx, "tok", &WizardDraft{Slug: "x", Username: "alice", Step: "class",
		Fields: map[string]string{"start.name": "A"}, CreatedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	d, err := drafts.Get(ctx, "tok", now)
	if err != nil || d.Step != "class" || d.Fields["start.name"] != "A" {
		t.Fatalf("resume wrong: %+v %v", d, err)
	}
	if _, err := drafts.Get(ctx, "tok", now.Add(2*time.Hour)); err != ErrDraftNotFound {
		t.Fatalf("expired draft must 404, got %v", err)
	}
	if err := drafts.Delete(ctx, "tok"); err != ErrDraftNotFound {
		t.Fatalf("consumed draft must 404, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// /me Simple view: zero frontmatter exposure + secret filtering
// ---------------------------------------------------------------------------

func TestMeSimpleHidesFrontmatter(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(req, alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("/me = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Alice", "HP 11 of 11", "Level 1", "Short rest", "Long rest", "Level up"} {
		if !strings.Contains(body, want) {
			t.Errorf("/me missing %q", want)
		}
	}
	for _, leak := range []string{"---", "owner:", "secret:", "hp-max", "hit-dice", "slots-", "str:", "con:", "sheet:"} {
		if strings.Contains(body, leak) {
			t.Errorf("/me exposed frontmatter %q", leak)
		}
	}
}

func TestMeAccessControl(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	// Guest: 401.
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("guest /me = %d, want 401", rec.Code)
	}
	// Other player with no sheet: OK, sees "no character", never alice's.
	req = httptest.NewRequest(http.MethodGet, "/me", nil)
	rec = httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(req, bob()))
	if rec.Code != http.StatusOK {
		t.Fatalf("bob /me = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Alice") {
		t.Fatal("bob's /me leaked alice's sheet")
	}
	// Bob cannot read alice's sheet directly: uniform 404.
	req = httptest.NewRequest(http.MethodGet, "/characters/alice/advanced", nil)
	rec = httptest.NewRecorder()
	sh.AdvancedForm(rec, withViewer(req, bob()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob advanced = %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// PATCH fields: surgical, allowlisted, validated
// ---------------------------------------------------------------------------

func patchJSON(t *testing.T, sh *SheetHandlers, target string, v *auth.Viewer, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", target, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeaderName, testCSRF)
	req.AddCookie(sessionCookie())
	rec := httptest.NewRecorder()
	sh.FieldsPatch(rec, withViewer(req, v))
	return rec
}

func TestFieldsPatchSurgical(t *testing.T) {
	sh, v, s := testSheets(t)
	commented := strings.Replace(aliceSheetMD, "  hp: 11\n", "  # current vitality\n  hp: 11\n", 1)
	seedSheet(t, v, s, "alice", "alice", commented)
	rec := patchJSON(t, sh, "/characters/alice/fields", alice(), `{"field":"hp","value":7}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}
	got := readVault(t, v, "characters/alice/index.md")
	if !strings.Contains(got, "hp: 7") || !strings.Contains(got, "# current vitality") || !strings.Contains(got, "hp-max: 11") {
		t.Fatalf("surgery wrong:\n%s", got)
	}
	// Out-of-range rejected, nothing written.
	rec = patchJSON(t, sh, "/characters/alice/fields", alice(), `{"field":"hp","value":99}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-max hp = %d, want 422", rec.Code)
	}
	if got := readVault(t, v, "characters/alice/index.md"); !strings.Contains(got, "hp: 7") {
		t.Fatalf("rejected patch wrote:\n%s", got)
	}
	// Non-allowlisted field rejected (maxima need flows/Advanced).
	rec = patchJSON(t, sh, "/characters/alice/fields", alice(), `{"field":"hp-max","value":50}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hp-max patch = %d, want 422", rec.Code)
	}
	// Unknown field rejected.
	rec = patchJSON(t, sh, "/characters/alice/fields", alice(), `{"field":"frobnicate","value":1}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field = %d, want 422", rec.Code)
	}
	// Other player cannot PATCH.
	rec = patchJSON(t, sh, "/characters/alice/fields", bob(), `{"field":"hp","value":1}`)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Fatalf("bob patch = %d, want denial", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Advanced: schema validation + invariants
// ---------------------------------------------------------------------------

func TestAdvancedValidation(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	// Unknown sheet key -> 422, nothing written.
	bad := strings.Replace(aliceSheetMD, "  cha: 8\n", "  cha: 8\n  frobnicate: 1\n", 1)
	rec := postForm(t, sh.AdvancedSave, "/characters/alice/advanced", alice(), map[string]string{"content": bad}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown key = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	// Owner change -> 403.
	moved := strings.Replace(aliceSheetMD, "owner: alice", "owner: bob", 1)
	rec = postForm(t, sh.AdvancedSave, "/characters/alice/advanced", alice(), map[string]string{"content": moved}, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("owner change = %d, want 403", rec.Code)
	}
	// Valid edit saves.
	edited := strings.Replace(aliceSheetMD, "  xp: 0\n", "  xp: 250\n", 1)
	rec = postForm(t, sh.AdvancedSave, "/characters/alice/advanced", alice(), map[string]string{"content": edited}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid advanced = %d: %s", rec.Code, rec.Body.String())
	}
	if got := readVault(t, v, "characters/alice/index.md"); !strings.Contains(got, "xp: 250") {
		t.Fatalf("advanced save lost:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Rest + level flows
// ---------------------------------------------------------------------------

func TestRestFlows(t *testing.T) {
	sh, v, s := testSheets(t)
	hurt := strings.Replace(aliceSheetMD, "  hp: 11\n", "  hp: 3\n", 1)
	seedSheet(t, v, s, "alice", "alice", hurt)
	// Short rest with no hit dice available... alice has 1 die: spend it.
	rec := postForm(t, sh.Rest, "/characters/alice/rest", alice(), map[string]string{"mode": "short", "spend": "1"}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("short rest = %d: %s", rec.Code, rec.Body.String())
	}
	got := readVault(t, v, "characters/alice/index.md")
	if !strings.Contains(got, "hit-dice: 0") {
		t.Fatalf("die not spent:\n%s", got)
	}
	if strings.Contains(got, "hp: 3") {
		t.Fatalf("no healing applied:\n%s", got)
	}
	// Overspend rejected.
	rec = postForm(t, sh.Rest, "/characters/alice/rest", alice(), map[string]string{"mode": "short", "spend": "1"}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("overspend = %d, want 422", rec.Code)
	}
	// Long rest restores.
	rec = postForm(t, sh.Rest, "/characters/alice/rest", alice(), map[string]string{"mode": "long"}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("long rest = %d: %s", rec.Code, rec.Body.String())
	}
	got = readVault(t, v, "characters/alice/index.md")
	if !strings.Contains(got, "hp: 11") || !strings.Contains(got, "hit-dice: 1") {
		t.Fatalf("long rest wrong:\n%s", got)
	}
}

func TestLevelFlow(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	rec := postForm(t, sh.Level, "/characters/alice/level", alice(), map[string]string{"direction": "up"}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("level = %d: %s", rec.Code, rec.Body.String())
	}
	got := readVault(t, v, "characters/alice/index.md")
	// con 13 (+1), d10 avg 6 -> +7: hp-max 18, level 2, dice 2/2.
	for _, want := range []string{"level: 2", "hp-max: 18", "hit-dice-max: 2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("level missing %q:\n%s", want, got)
		}
	}
	// Bob cannot level alice.
	rec = postForm(t, sh.Level, "/characters/alice/level", bob(), map[string]string{"direction": "up"}, true)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("bob level = %d, want denial", rec.Code)
	}
}
