package web

// Player sheets (Lane I1, Phase 3): /me home, Simple view (buttons/pips +
// surgical PATCH), Advanced editor, level/rest flows.
//
// Red lines: Simple never renders raw frontmatter (derived widgets only);
// Advanced validates against the sheet schema; sheets live in
// characters/<pc>/index.md frontmatter (DB holds a read copy only).
// Every read path secret-filters server-side via secrets.Filter.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/secrets"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
)

// Lane-owned route paths (routes.go frozen: registered via the registry).
const (
	RouteMeSheet    = "/me"
	RouteCharPrefix = "/characters/"
)

// SheetHandlers owns /me + character sheet endpoints.
type SheetHandlers struct {
	Store        store.Store
	Vault        VaultWriter
	SessionStore auth.SessionStore
	Pack         *Pack
	Evaluator    RecoveryEvaluator // MOCK(H2): StubEvaluator until H2 lands
}

// RegisterRoutes registers sheet routes with the frozen registry.
// NOTE (merge): ReadHandlers already registers GET /me (F1 placeholder).
// At the M2 merge the phase agent retires that placeholder in favor of
// MeSheet and wires these handlers in serve.go (E1-owned); first-wins
// duplicate handling keeps main green until then.
func (h *SheetHandlers) RegisterRoutes(reg *RouteRegistry) {
	reg.Register(Route{Method: http.MethodGet, Path: RouteMeSheet, Handler: h.MeSheet, ReadOnly: true, AuthRequired: true, SecretFiltered: true})
	reg.Register(Route{Method: http.MethodGet, Path: RouteCharPrefix, Handler: h.SheetRouter, ReadOnly: true, AuthRequired: true, SecretFiltered: true})
	reg.Register(Route{Method: http.MethodPost, Path: RouteCharPrefix, Handler: h.SheetRouter, ReadOnly: false, AuthRequired: true})
	reg.Register(Route{Method: "PATCH", Path: RouteCharPrefix, Handler: h.SheetRouter, ReadOnly: false, AuthRequired: true})
}

func (h *SheetHandlers) csrfOK(r *http.Request) bool {
	return (&WriteHandlers{SessionStore: h.SessionStore}).checkCSRF(r)
}

func (h *SheetHandlers) csrfField(r *http.Request) string {
	return (&WriteHandlers{SessionStore: h.SessionStore}).csrfTokenForForm(r)
}

func (h *SheetHandlers) pack() *Pack {
	if h.Pack != nil {
		return h.Pack
	}
	return StubPack()
}

func (h *SheetHandlers) evaluator() RecoveryEvaluator {
	if h.Evaluator != nil {
		return h.Evaluator
	}
	return StubEvaluator{}
}

// ---------------------------------------------------------------------------
// Loading (single funnel: index lookup -> parse -> server-side secret filter)
// ---------------------------------------------------------------------------

type loadedSheet struct {
	rel   string
	page  *markdown.Page
	sheet *Sheet
	owner string
}

// loadSheet enforces: login (401 guest), uniform 404 for missing/unreadable/
// quarantined-to-players, schema-validated sheet (422 when corrupt).
//
// Sheet state parses from the VAULT file (filesystem = truth, AGENTS.md),
// never the index copy: the watcher may lag behind rapid surgical flows,
// and a stale index read would clobber the previous write. The index row
// supplies existence + ACL primitives only.
func (h *SheetHandlers) loadSheet(r *http.Request) (*loadedSheet, *auth.Viewer, int, string) {
	v := viewerOf(r)
	user, _, _, _ := store.SplitViewer(v)
	if user == "" {
		return nil, v, http.StatusUnauthorized, "log in to view sheets"
	}
	id, rest, ok := charPathOf(r)
	if !ok {
		return nil, v, http.StatusNotFound, "no such character"
	}
	_ = rest
	rel := "characters/" + id + "/index.md"
	if _, err := cleanWritePath(rel); err != nil {
		return nil, v, http.StatusNotFound, "no such character"
	}
	sp, err := h.Store.PageGet(r.Context(), rel)
	if err != nil {
		return nil, v, http.StatusNotFound, "no such character"
	}
	return h.loadSheetRel(r, v, sp.Path)
}

// loadSheetRel runs the vault-truth funnel for a known index path.
func (h *SheetHandlers) loadSheetRel(r *http.Request, v *auth.Viewer, indexPath string) (*loadedSheet, *auth.Viewer, int, string) {
	if h.Vault == nil {
		return nil, v, http.StatusInternalServerError, "vault is not configured"
	}
	// Read-before-parse doubles as the clash-check arming read for the
	// surgical write that usually follows (F2 pattern).
	content, err := h.Vault.ReadFile(r.Context(), indexPath)
	if err != nil {
		return nil, v, http.StatusNotFound, "no such character"
	}
	page, err := markdown.Parse(r.Context(), string(content), indexPath)
	if err != nil {
		return nil, v, http.StatusNotFound, "no such character"
	}
	filtered, err := secrets.Filter(v, page)
	if err != nil {
		// Unauthorized secret (or quarantined-to-player): uniform 404.
		return nil, v, http.StatusNotFound, "no such character"
	}
	raw, isMap := filtered.Frontmatter["sheet"]
	sheetMap, ok := raw.(map[string]any)
	if !isMap || !ok {
		return nil, v, http.StatusUnprocessableEntity, "this page has no sheet data yet"
	}
	_ = raw
	sh, err := ParseSheet(sheetMap)
	if err != nil {
		return nil, v, http.StatusUnprocessableEntity, "sheet data invalid: " + err.Error()
	}
	return &loadedSheet{rel: indexPath, page: filtered, sheet: sh, owner: filtered.Owner}, v, 0, ""
}

// canWriteSheet mirrors the F2 write gate for the fixed sheet path:
// GM (no preview), owner, or editable-by holder. Unreadable pages already
// 404'd in loadSheet, so denial here is 403.
func canWriteSheet(v *auth.Viewer, owner string, editableBy []string) bool {
	user, isGM, _, _ := store.SplitViewer(v)
	if user == "" {
		return false
	}
	if isGM {
		return true
	}
	if owner != "" && strings.EqualFold(owner, user) {
		return true
	}
	for _, u := range editableBy {
		if strings.EqualFold(u, user) {
			return true
		}
	}
	return false
}

// charPathOf splits /characters/<id>[/<action>] (id charset enforced).
func charPathOf(r *http.Request) (id, action string, ok bool) {
	rest := strings.TrimPrefix(r.URL.Path, RouteCharPrefix)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || !slugRe.MatchString(parts[0]) {
		return "", "", false
	}
	if len(parts) > 1 {
		action = parts[1]
	}
	return parts[0], action, true
}

// SheetRouter dispatches /characters/<id>/{fields,advanced,rest,level}.
func (h *SheetHandlers) SheetRouter(w http.ResponseWriter, r *http.Request) {
	_, action, ok := charPathOf(r)
	if !ok {
		writeDenied(w, http.StatusNotFound, "no such character")
		return
	}
	switch action {
	case "fields":
		if r.Method == "PATCH" {
			h.FieldsPatch(w, r)
			return
		}
		if r.Method == http.MethodPost {
			h.FieldsPost(w, r)
			return
		}
	case "advanced":
		if r.Method == http.MethodGet {
			h.AdvancedForm(w, r)
			return
		}
		if r.Method == http.MethodPost {
			h.AdvancedSave(w, r)
			return
		}
	case "rest":
		if r.Method == http.MethodPost {
			h.Rest(w, r)
			return
		}
	case "level":
		if r.Method == http.MethodPost {
			h.Level(w, r)
			return
		}
	case "":
		http.Redirect(w, r, "/me", http.StatusSeeOther)
		return
	}
	writeDenied(w, http.StatusMethodNotAllowed, "method not allowed")
}

// ---------------------------------------------------------------------------
// /me home (sheet-first, not wiki-first)
// ---------------------------------------------------------------------------

// myCharacters lists owned PC index paths for the viewer (stable order).
func (h *SheetHandlers) myCharacters(ctx context.Context, v *auth.Viewer) []string {
	user, _, _, _ := store.SplitViewer(v)
	if user == "" || h.Store == nil {
		return nil
	}
	rows, err := h.Store.PageList(ctx, store.PageListOptions{Prefix: "characters/", IncludeSecret: true, Limit: 200})
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range rows {
		if !strings.HasSuffix(strings.ToLower(p.Path), "/index.md") {
			continue
		}
		if strings.EqualFold(p.Owner, user) {
			out = append(out, p.Path)
		}
	}
	sort.Strings(out)
	return out
}

// MeSheet renders the player home: my sheet (Simple default) + handout/
// recap slots. Sheets the viewer cannot read never appear (server-side).
func (h *SheetHandlers) MeSheet(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, isGM, _, _ := store.SplitViewer(v)
	if user == "" {
		writeDenied(w, http.StatusUnauthorized, "log in to view your sheet")
		return
	}
	if q := strings.TrimSpace(r.URL.Query().Get("view")); q == "simple" || q == "advanced" {
		http.SetCookie(w, &http.Cookie{Name: "sheet-view", Path: "/", Value: q, MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/me", http.StatusSeeOther)
		return
	}
	view := "simple"
	if c, err := r.Cookie("sheet-view"); err == nil && c.Value == "advanced" {
		view = "advanced"
	}
	mine := h.myCharacters(r.Context(), v)
	if len(mine) == 0 {
		var b strings.Builder
		if isGM {
			b.WriteString("<h1>Me</h1>\n<p role=\"status\">GM accounts hold no sheet. Run the <a href=\"" + RouteWizardSetup + "\">setup wizard</a> or issue a claim link for a player.</p>\n")
		} else {
			b.WriteString("<h1>Me</h1>\n<p role=\"status\">No character yet. Ask your GM for an invitation link, then create your character with zero frontmatter.</p>\n")
		}
		writeHTML(w, http.StatusOK, "Me", b.String())
		return
	}
	// Render the first owned sheet (deterministic); ?pc= selects others.
	rel := mine[0]
	if want := strings.TrimSpace(r.URL.Query().Get("pc")); want != "" && slugRe.MatchString(want) {
		cand := "characters/" + want + "/index.md"
		for _, m := range mine {
			if strings.EqualFold(m, cand) {
				rel = m
				break
			}
		}
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/characters/" + strings.TrimSuffix(strings.TrimPrefix(rel, "characters/"), "/index.md") + "/"
	if view == "advanced" {
		r2.URL.Path += "advanced"
		h.AdvancedForm(w, r2)
		return
	}
	h.renderSimple(w, r2, rel)
}

// ---------------------------------------------------------------------------
// Simple view (derived widgets only — never raw frontmatter)
// ---------------------------------------------------------------------------

func sheetStyle() string {
	return `<style>
.sheet button, .sheet a.btn { min-height: 44px; min-width: 44px; padding: 0.5rem 0.9rem; margin: 0.15rem; }
.sheet .pips { letter-spacing: 0.2rem; font-size: 1.2rem; }
.sheet .badge { display: inline-block; border: 2px solid currentColor; border-radius: 0.4rem; padding: 0.1rem 0.5rem; font-weight: 600; }
</style>` + "\n"
}

func (h *SheetHandlers) renderSimple(w http.ResponseWriter, r *http.Request, rel string) {
	v := viewerOf(r)
	ls, _, status, msg := h.loadSheetRel(r, v, rel)
	if status != 0 {
		writeDenied(w, status, msg)
		return
	}
	filtered, sh := ls.page, ls.sheet
	id, _, _ := charPathOf(r)
	base := RouteCharPrefix + id
	csrf := html.EscapeString(h.csrfField(r))
	var b strings.Builder
	b.WriteString(sheetStyle())
	b.WriteString("<main class=\"sheet\" aria-label=\"Character sheet\">\n")
	fmt.Fprintf(&b, "<h1>%s</h1>\n", html.EscapeString(filtered.Title))
	fmt.Fprintf(&b, "<p><span class=\"badge\">Level %d %s</span> <span class=\"badge\">%s</span> <span class=\"badge\">%s</span></p>\n",
		sh.Level, html.EscapeString(sh.Class), html.EscapeString(sh.Ancestry), html.EscapeString(sh.Background))
	fmt.Fprintf(&b, "<p aria-live=\"polite\" role=\"status\">HP %d of %d · Hit dice %d of %d (d%d) · XP %d</p>\n",
		sh.HP, sh.HPMax, sh.HitDice, sh.HitDiceMax, sh.HitDie, sh.XP)
	// HP tap targets (surgical adjust, clamped server-side).
	b.WriteString("<form method=\"post\" action=\"" + base + "/fields\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, csrf)
	b.WriteString("<input type=\"hidden\" name=\"field\" value=\"hp\">\n")
	b.WriteString("<button type=\"submit\" name=\"adjust\" value=\"-1\" aria-label=\"Take 1 damage\">−1 HP</button>\n")
	b.WriteString("<button type=\"submit\" name=\"adjust\" value=\"1\" aria-label=\"Heal 1 hit point\">+1 HP</button>\n")
	b.WriteString("</form>\n")
	// Hit dice pips (text, never color-only).
	fmt.Fprintf(&b, "<p>Hit dice: <span class=\"pips\" aria-label=\"%d of %d hit dice remaining\">%s</span></p>\n",
		sh.HitDice, sh.HitDiceMax, pips(sh.HitDice, sh.HitDiceMax))
	// Spell slots pips.
	var slotNs []int64
	for n := int64(1); n <= 9; n++ {
		if _, ok := sh.SlotsMax[n]; ok {
			slotNs = append(slotNs, n)
		}
	}
	if len(slotNs) > 0 {
		b.WriteString("<ul>\n")
		for _, n := range slotNs {
			fmt.Fprintf(&b, "<li>Level-%d slots: <span class=\"pips\" aria-label=\"%d of %d level %d slots remaining\">%s</span> ",
				n, sh.Slots[n], sh.SlotsMax[n], n, pips(sh.Slots[n], sh.SlotsMax[n]))
			fmt.Fprintf(&b, "<form method=\"post\" action=\"%s/fields\" style=\"display:inline\">\n", base)
			fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, csrf)
			fmt.Fprintf(&b, "<input type=\"hidden\" name=\"field\" value=\"slots-%d\">\n", n)
			fmt.Fprintf(&b, "<button type=\"submit\" name=\"adjust\" value=\"-1\" aria-label=\"Spend one level %d slot\">Spend</button></form></li>\n", n)
		}
		b.WriteString("</ul>\n")
	}
	// Abilities (derived numbers, not source).
	b.WriteString("<ul>\n")
	for _, k := range SheetStatKeys {
		fmt.Fprintf(&b, "<li>%s %d</li>\n", strings.ToUpper(k), sh.Stats[k])
	}
	b.WriteString("</ul>\n")
	// Rest + level flows.
	b.WriteString("<h2>Rest</h2>\n")
	b.WriteString("<form method=\"post\" action=\"" + base + "/rest\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, csrf)
	b.WriteString("<input type=\"hidden\" name=\"mode\" value=\"short\">\n")
	fmt.Fprintf(&b, "<p><label for=\"spend\">Hit dice to spend</label> <input id=\"spend\" name=\"spend\" type=\"number\" min=\"1\" max=\"%d\" value=\"1\"></p>\n", sh.HitDice)
	b.WriteString("<p><button type=\"submit\">Short rest</button></p>\n</form>\n")
	b.WriteString("<form method=\"post\" action=\"" + base + "/rest\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, csrf)
	b.WriteString("<input type=\"hidden\" name=\"mode\" value=\"long\">\n")
	b.WriteString("<p><button type=\"submit\">Long rest (full HP, half hit dice, slots back)</button></p>\n</form>\n")
	b.WriteString("<h2>Level</h2>\n")
	b.WriteString("<form method=\"post\" action=\"" + base + "/level\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, csrf)
	b.WriteString("<input type=\"hidden\" name=\"direction\" value=\"up\">\n")
	fmt.Fprintf(&b, "<p><button type=\"submit\">Level up to %d</button></p>\n</form>\n", sh.Level+1)
	fmt.Fprintf(&b, "<p><a href=\"%s/advanced\">Advanced editor</a> · <a href=\"/me?view=advanced\">Always use Advanced</a></p>\n", base)
	b.WriteString("</main>\n")
	writeHTML(w, http.StatusOK, filtered.Title, b.String())
}

// pips renders ●/○ text pips (never color-only).
func pips(cur, max int64) string {
	var sb strings.Builder
	for i := int64(0); i < max && i < 20; i++ {
		if i < cur {
			sb.WriteString("●")
		} else {
			sb.WriteString("○")
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// PATCH /fields (surgical frontmatter edit, allowlisted current-values only)
// ---------------------------------------------------------------------------

// FieldsPatch handles PATCH /characters/:id/fields (JSON or form): one
// allowlisted field, range-checked, written surgically (comments/order kept).
func (h *SheetHandlers) FieldsPatch(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, _, _, _ := store.SplitViewer(v)
	if user == "" {
		writeDenied(w, http.StatusUnauthorized, "log in to change sheets")
		return
	}
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	ls, _, status, msg := h.loadSheet(r)
	if status != 0 {
		writeDenied(w, status, msg)
		return
	}
	if !canWriteSheet(v, ls.owner, ls.page.EditableBy) {
		writeDenied(w, http.StatusForbidden, "you cannot change this sheet")
		return
	}
	field, adjust, isAdjust, value, ok := parseFieldInput(r)
	if !ok {
		writeDenied(w, http.StatusBadRequest, "field + value (or adjust) required")
		return
	}
	if !SimplePatchFields[field] {
		writeDenied(w, http.StatusUnprocessableEntity, "field "+field+" is not Simple-editable (use level/rest flows or Advanced)")
		return
	}
	cur := sheetFieldValue(ls.sheet, field)
	var next int64
	if isAdjust {
		next = cur + adjust
		if field == "hp" {
			// Tap targets clamp (tapping damage at 0 HP is not an error).
			if next < 0 {
				next = 0
			}
			if next > ls.sheet.HPMax {
				next = ls.sheet.HPMax
			}
		}
	} else {
		next = value
	}
	probe := *ls.sheet
	if err := setSheetField(&probe, field, next); err != nil {
		writeDenied(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := probe.Validate(); err != nil {
		writeDenied(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := h.writeSheetKeys(r.Context(), ls, map[string]any{field: next}); err != nil {
		if err == errConflict {
			writeDenied(w, http.StatusConflict, "someone else saved this sheet — reload and retry")
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not save sheet")
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Method == "PATCH" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "field": field, "value": next})
		return
	}
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

// FieldsPost is the HTML-form alias of FieldsPatch (buttons/pips POST here).
func (h *SheetHandlers) FieldsPost(w http.ResponseWriter, r *http.Request) {
	h.FieldsPatch(w, r)
}

func parseFieldInput(r *http.Request) (field string, adjust int64, isAdjust bool, value int64, ok bool) {
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Field  string `json:"field"`
			Value  *int64 `json:"value"`
			Adjust *int64 `json:"adjust"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			return "", 0, false, 0, false
		}
		field = strings.TrimSpace(body.Field)
		if body.Adjust != nil {
			return field, *body.Adjust, true, 0, field != ""
		}
		if body.Value != nil {
			return field, 0, false, *body.Value, field != ""
		}
		return "", 0, false, 0, false
	}
	field = strings.TrimSpace(r.FormValue("field"))
	if a := strings.TrimSpace(r.FormValue("adjust")); a != "" {
		n, valid := toInt(a)
		if !valid {
			return "", 0, false, 0, false
		}
		return field, n, true, 0, field != ""
	}
	n, valid := toInt(strings.TrimSpace(r.FormValue("value")))
	if !valid {
		return "", 0, false, 0, false
	}
	return field, 0, false, n, field != ""
}

func sheetFieldValue(sh *Sheet, field string) int64 {
	switch field {
	case "hp":
		return sh.HP
	case "hit-dice":
		return sh.HitDice
	case "xp":
		return sh.XP
	case "inspiration":
		return sh.Inspiration
	case "death-succ":
		return sh.DeathSucc
	case "death-fail":
		return sh.DeathFail
	}
	var n int64
	if _, err := fmt.Sscanf(field, "slots-%d", &n); err == nil {
		return sh.Slots[n]
	}
	return 0
}

func setSheetField(sh *Sheet, field string, v int64) error {
	switch field {
	case "hp":
		sh.HP = v
	case "hit-dice":
		sh.HitDice = v
	case "xp":
		sh.XP = v
	case "inspiration":
		sh.Inspiration = v
	case "death-succ":
		sh.DeathSucc = v
	case "death-fail":
		sh.DeathFail = v
	default:
		var n int64
		if _, err := fmt.Sscanf(field, "slots-%d", &n); err != nil || n < 1 || n > 9 {
			return fmt.Errorf("unknown sheet field %q", field)
		}
		if _, ok := sh.SlotsMax[n]; !ok {
			return fmt.Errorf("no level-%d slots on this sheet", n)
		}
		sh.Slots[n] = v
	}
	return nil
}

// ---------------------------------------------------------------------------
// Advanced editor (MD + frontmatter, schema-validated)
// ---------------------------------------------------------------------------

// AdvancedForm renders the full source editor (owner/GM only, opt-in).
func (h *SheetHandlers) AdvancedForm(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, _, _, _ := store.SplitViewer(v)
	if user == "" {
		writeDenied(w, http.StatusUnauthorized, "log in to edit sheets")
		return
	}
	ls, _, status, msg := h.loadSheet(r)
	if status != 0 {
		writeDenied(w, status, msg)
		return
	}
	if !canWriteSheet(v, ls.owner, ls.page.EditableBy) {
		writeDenied(w, http.StatusForbidden, "you cannot edit this sheet")
		return
	}
	raw, err := h.Vault.ReadFile(r.Context(), ls.rel)
	if err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not read sheet")
		return
	}
	id, _, _ := charPathOf(r)
	var b strings.Builder
	b.WriteString(sheetStyle())
	fmt.Fprintf(&b, "<h1>Advanced: %s</h1>\n", html.EscapeString(ls.page.Title))
	b.WriteString("<p>Full source with schema validation. Unknown sheet keys fail; owner is immutable.</p>\n")
	b.WriteString("<form method=\"post\" action=\"" + RouteCharPrefix + id + "/advanced\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, html.EscapeString(h.csrfField(r)))
	b.WriteString("<p><label for=\"adv-content\">Sheet source</label><br>\n")
	fmt.Fprintf(&b, "<textarea id=\"adv-content\" name=\"content\" rows=\"30\" cols=\"80\">%s</textarea></p>\n", html.EscapeString(string(raw)))
	b.WriteString("<p><button type=\"submit\">Save with validation</button> <a href=\"/me?view=simple\">Back to Simple</a></p>\n</form>\n")
	writeHTML(w, http.StatusOK, "Advanced: "+ls.page.Title, b.String())
}

// AdvancedSave validates the full text (schema + owner/secret invariants)
// before writing. Quarantined frontmatter is 422 for players.
func (h *SheetHandlers) AdvancedSave(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, isGM, _, _ := store.SplitViewer(v)
	if user == "" {
		writeDenied(w, http.StatusUnauthorized, "log in to edit sheets")
		return
	}
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	ls, _, status, msg := h.loadSheet(r)
	if status != 0 {
		writeDenied(w, status, msg)
		return
	}
	if !canWriteSheet(v, ls.owner, ls.page.EditableBy) {
		writeDenied(w, http.StatusForbidden, "you cannot edit this sheet")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBody)
	content := r.FormValue("content")
	parsed, err := markdown.Parse(r.Context(), content, ls.rel)
	if err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not parse sheet")
		return
	}
	if parsed.Quarantined && !isGM {
		writeDenied(w, http.StatusUnprocessableEntity, "bad frontmatter: "+parsed.QuarantineReason)
		return
	}
	if !isGM {
		if parsed.Owner != "" && !strings.EqualFold(parsed.Owner, ls.owner) {
			writeDenied(w, http.StatusForbidden, "owner is immutable (ask your GM to transfer it)")
			return
		}
		if ls.page.Secret && !parsed.Secret {
			writeDenied(w, http.StatusForbidden, "a secret sheet cannot be made non-secret by a player")
			return
		}
		if !sameStringSet(parsed.EditableBy, ls.page.EditableBy) {
			writeDenied(w, http.StatusForbidden, "editable-by can only be changed by your GM")
			return
		}
	}
	raw, ok := parsed.Frontmatter["sheet"].(map[string]any)
	if !ok {
		writeDenied(w, http.StatusUnprocessableEntity, "sheet block must be a map (sheet: with indented keys)")
		return
	}
	if _, err := ParseSheet(raw); err != nil {
		writeDenied(w, http.StatusUnprocessableEntity, "sheet invalid: "+err.Error())
		return
	}
	// Read-before-write arms the clash check; surgical full-text write.
	if _, err := h.Vault.ReadFile(r.Context(), ls.rel); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not read sheet")
		return
	}
	if err := h.Vault.WriteFile(r.Context(), ls.rel, content); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not save sheet")
		return
	}
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Level + rest flows (Simple view, no frontmatter needed)
// ---------------------------------------------------------------------------

// Rest runs short/long rest recovery and writes back only changed keys.
func (h *SheetHandlers) Rest(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, _, _, _ := store.SplitViewer(v)
	if user == "" {
		writeDenied(w, http.StatusUnauthorized, "log in to rest")
		return
	}
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	ls, _, status, msg := h.loadSheet(r)
	if status != 0 {
		writeDenied(w, status, msg)
		return
	}
	if !canWriteSheet(v, ls.owner, ls.page.EditableBy) {
		writeDenied(w, http.StatusForbidden, "you cannot rest this character")
		return
	}
	mode := strings.TrimSpace(r.FormValue("mode"))
	changed := map[string]any{}
	var notice string
	switch mode {
	case "short":
		spend, ok := toInt(strings.TrimSpace(r.FormValue("spend")))
		if !ok {
			writeDenied(w, http.StatusBadRequest, "spend: how many hit dice")
			return
		}
		if err := TouchRecovery(r.Context(), h.evaluator(), "rest-short", ls.rel); err != nil {
			writeDenied(w, http.StatusInternalServerError, "recovery hooks failed")
			return
		}
		healed, err := ls.sheet.ShortRest(spend, CryptoHitDie)
		if err != nil {
			writeDenied(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		changed["hp"] = ls.sheet.HP
		changed["hit-dice"] = ls.sheet.HitDice
		notice = fmt.Sprintf("Short rest: spent %d hit dice, recovered %d HP.", spend, healed)
	case "long":
		if err := TouchRecovery(r.Context(), h.evaluator(), "rest-long", ls.rel); err != nil {
			writeDenied(w, http.StatusInternalServerError, "recovery hooks failed")
			return
		}
		ls.sheet.LongRest()
		changed["hp"] = ls.sheet.HP
		changed["hit-dice"] = ls.sheet.HitDice
		changed["death-succ"] = int64(0)
		changed["death-fail"] = int64(0)
		for n := range ls.sheet.SlotsMax {
			changed[fmt.Sprintf("slots-%d", n)] = ls.sheet.Slots[n]
		}
		notice = "Long rest: HP, half hit dice, and spell slots restored."
	default:
		writeDenied(w, http.StatusBadRequest, "mode: short or long")
		return
	}
	if err := h.writeSheetKeys(r.Context(), ls, changed); err != nil {
		if err == errConflict {
			writeDenied(w, http.StatusConflict, "someone else saved this sheet — reload and retry")
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not save sheet")
		return
	}
	restDone(w, notice)
}

// Level advances one level (average HP, +1 hit die) via the stub pack
// (STUB(H1): caster slot tables arrive with H1 packs).
func (h *SheetHandlers) Level(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, _, _, _ := store.SplitViewer(v)
	if user == "" {
		writeDenied(w, http.StatusUnauthorized, "log in to level up")
		return
	}
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	ls, _, status, msg := h.loadSheet(r)
	if status != 0 {
		writeDenied(w, status, msg)
		return
	}
	if !canWriteSheet(v, ls.owner, ls.page.EditableBy) {
		writeDenied(w, http.StatusForbidden, "you cannot level this character")
		return
	}
	if strings.TrimSpace(r.FormValue("direction")) != "up" {
		writeDenied(w, http.StatusBadRequest, "direction: up")
		return
	}
	class, err := h.pack().Class(ls.sheet.Class)
	if err != nil {
		writeDenied(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := TouchRecovery(r.Context(), h.evaluator(), "level-up", ls.rel); err != nil {
		writeDenied(w, http.StatusInternalServerError, "recovery hooks failed")
		return
	}
	if err := ls.sheet.LevelUp(class); err != nil {
		writeDenied(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	changed := map[string]any{
		"level": ls.sheet.Level, "hp": ls.sheet.HP, "hp-max": ls.sheet.HPMax,
		"hit-dice": ls.sheet.HitDice, "hit-dice-max": ls.sheet.HitDiceMax,
	}
	if err := h.writeSheetKeys(r.Context(), ls, changed); err != nil {
		if err == errConflict {
			writeDenied(w, http.StatusConflict, "someone else saved this sheet — reload and retry")
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not save sheet")
		return
	}
	restDone(w, fmt.Sprintf("Level %d! HP max is now %d.", ls.sheet.Level, ls.sheet.HPMax))
}

func restDone(w http.ResponseWriter, notice string) {
	var b strings.Builder
	fmt.Fprintf(&b, "<p role=\"status\">%s</p>\n<p><a href=\"/me\">Back to your sheet</a></p>\n", html.EscapeString(notice))
	writeHTML(w, http.StatusOK, "Done", b.String())
}

// ---------------------------------------------------------------------------
// Writes (read-before-write -> surgical apply -> validate -> atomic write)
// ---------------------------------------------------------------------------

var errConflict = fmt.Errorf("write conflict")

func isConflictErr(err error) bool {
	var ce *vault.ErrConflict
	return errors.As(err, &ce)
}

// writeSheetKeys applies changed sheet keys surgically (comments/order kept),
// re-validates the full sheet, and writes atomically via the vault.
func (h *SheetHandlers) writeSheetKeys(ctx context.Context, ls *loadedSheet, changed map[string]any) error {
	raw, err := h.Vault.ReadFile(ctx, ls.rel)
	if err != nil {
		return err
	}
	updated, ok := applySheetMap(string(raw), changed)
	if !ok {
		return fmt.Errorf("sheet block is not block-style")
	}
	parsed, err := markdown.Parse(ctx, updated, ls.rel)
	if err != nil {
		return err
	}
	if parsed.Quarantined {
		return fmt.Errorf("edit produced bad frontmatter: %s", parsed.QuarantineReason)
	}
	rawMap, ok := parsed.Frontmatter["sheet"].(map[string]any)
	if !ok {
		return fmt.Errorf("sheet block lost in edit")
	}
	if _, err := ParseSheet(rawMap); err != nil {
		return err
	}
	if err := h.Vault.WriteFile(ctx, ls.rel, updated); err != nil {
		if isConflictErr(err) {
			return errConflict
		}
		return err
	}
	return nil
}
