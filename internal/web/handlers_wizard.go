package web

// Onboarding wizards (Lane I1, Phase 3): GM setup wizard -> campaign.yaml,
// player character wizard via claim token -> owned characters/<pc>/index.md.
//
// Routes register through the frozen RouteRegistry (routes.go untouched);
// paths live here as lane-owned constants. Serve wiring (E1's cmd/app)
// must call WizardHandlers.RegisterRoutes + SheetHandlers.RegisterRoutes at
// the M2 merge; until then these handlers are covered by direct tests
// (parallel-lane pattern: never touch another lane's files to "fix" a merge).

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
)

// Lane-owned route paths (routes.go registry frozen: registered via it,
// never by editing core routing).
const (
	RouteWizardSetup  = "/wizard/setup"
	RouteWizardClaims = "/wizard/claims"
	RouteClaimPrefix  = "/c/"
)

// WizardHandlers owns the onboarding wizards. Drafts doubles as the
// single-use claim store (draft consumed on finalize); the Vault is the only
// writer (atomic WriteFile); Pack is the resolved H1 stack (vault-backed via
// VaultRoot), pinned explicitly in tests.
type WizardHandlers struct {
	Store        store.Store
	Vault        VaultWriter
	SessionStore auth.SessionStore
	Drafts       *DraftStore
	Pack         *Pack
	VaultRoot    string // gate G3: vault truth for per-request pack resolve (no restart on overlay switch)
}

// RegisterRoutes registers wizard routes with the frozen registry.
func (h *WizardHandlers) RegisterRoutes(reg *RouteRegistry) {
	reg.Register(Route{Method: http.MethodGet, Path: RouteWizardSetup, Handler: h.SetupForm, ReadOnly: true, AuthRequired: true, GMOnly: true})
	reg.Register(Route{Method: http.MethodPost, Path: RouteWizardSetup, Handler: h.SetupSave, ReadOnly: false, AuthRequired: true, GMOnly: true})
	reg.Register(Route{Method: http.MethodPost, Path: RouteWizardClaims, Handler: h.ClaimIssue, ReadOnly: false, AuthRequired: true, GMOnly: true})
	reg.Register(Route{Method: http.MethodGet, Path: RouteClaimPrefix, Handler: h.ClaimWizard, ReadOnly: true, AuthRequired: true})
	reg.Register(Route{Method: http.MethodPost, Path: RouteClaimPrefix, Handler: h.ClaimWizardPost, ReadOnly: false, AuthRequired: true})
}

func (h *WizardHandlers) csrfOK(r *http.Request) bool {
	return (&WriteHandlers{SessionStore: h.SessionStore}).checkCSRF(r)
}

func (h *WizardHandlers) csrfField(r *http.Request) string {
	return (&WriteHandlers{SessionStore: h.SessionStore}).csrfTokenForForm(r)
}

// pack resolves the wizard pack: explicit test pins win, otherwise vault
// truth per request (overlay switches apply with no restart), otherwise the
// pack-less fallback.
func (h *WizardHandlers) pack() *Pack {
	if h.Pack != nil {
		return h.Pack
	}
	if h.VaultRoot != "" {
		return LoadPack(h.VaultRoot)
	}
	return StubPack()
}

// gmOf enforces GM-only (preview-as-player never passes: SplitViewer folds
// PreviewAs to a non-GM user).
func gmOf(r *http.Request) (*auth.Viewer, bool) {
	v := viewerOf(r)
	_, isGM, _, _ := store.SplitViewer(v)
	if v == nil || !isGM || v.PreviewAs != "" {
		return v, false
	}
	return v, true
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ---------------------------------------------------------------------------
// GM setup wizard -> campaign.yaml
// ---------------------------------------------------------------------------

// SetupForm renders the GM setup wizard (picks system/overlay/optionals on
// init, per p07). Reads campaign.yaml as defaults; missing file starts blank.
func (h *WizardHandlers) SetupForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := gmOf(r); !ok {
		writeDenied(w, http.StatusForbidden, "the setup wizard is GM-only")
		return
	}
	var cur *Campaign
	if h.Vault != nil {
		if b, err := h.Vault.ReadFile(r.Context(), "campaign.yaml"); err == nil {
			if c, perr := ParseCampaign(string(b)); perr == nil {
				cur = c
			}
		}
	}
	if cur == nil {
		cur = &Campaign{}
	}
	var b strings.Builder
	b.WriteString("<h1>Campaign setup</h1>\n")
	b.WriteString("<p>Writes <code>campaign.yaml</code>. Base/overlay names resolve against vault packs under <code>rules/</code> (versions recorded, never enforced); optionals toggle per id.</p>\n")
	b.WriteString("<form method=\"post\" action=\"" + RouteWizardSetup + "\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, html.EscapeString(h.csrfField(r)))
	fmt.Fprintf(&b, "<p><label for=\"wz-name\">Campaign name</label> <input id=\"wz-name\" name=\"name\" value=\"%s\" size=\"40\" required></p>\n", html.EscapeString(cur.Name))
	fmt.Fprintf(&b, "<p><label for=\"wz-base\">Base ruleset</label> <input id=\"wz-base\" name=\"base\" value=\"%s\" size=\"20\" placeholder=\"dnd\"></p>\n", html.EscapeString(cur.Base))
	fmt.Fprintf(&b, "<p><label for=\"wz-overlay\">Overlay</label> <input id=\"wz-overlay\" name=\"overlay\" value=\"%s\" size=\"20\" placeholder=\"srd-5e-2014\"></p>\n", html.EscapeString(cur.Overlay))
	b.WriteString("<p><label for=\"wz-features\">Enabled features (one id per line)</label><br>\n")
	fmt.Fprintf(&b, "<textarea id=\"wz-features\" name=\"features\" rows=\"4\" cols=\"40\">%s</textarea></p>\n", html.EscapeString(strings.Join(cur.EnabledFeatures, "\n")))
	b.WriteString("<p><button type=\"submit\">Save campaign.yaml</button></p>\n</form>\n")
	writeHTML(w, http.StatusOK, "Campaign setup", b.String())
}

// SetupSave validates the wizard form and writes campaign.yaml surgically
// (comments/order preserved; read-before-write arms the clash check).
func (h *WizardHandlers) SetupSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := gmOf(r); !ok {
		writeDenied(w, http.StatusForbidden, "the setup wizard is GM-only")
		return
	}
	if h.Vault == nil {
		writeDenied(w, http.StatusInternalServerError, "vault is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBody)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	var features []string
	for _, line := range strings.Split(r.FormValue("features"), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			features = append(features, f)
		}
	}
	c := &Campaign{
		Name:            strings.TrimSpace(r.FormValue("name")),
		Base:            strings.TrimSpace(r.FormValue("base")),
		Overlay:         strings.TrimSpace(r.FormValue("overlay")),
		EnabledFeatures: features,
	}
	if err := ValidateCampaign(c); err != nil {
		writeDenied(w, http.StatusUnprocessableEntity, "campaign: "+err.Error())
		return
	}
	existing := ""
	if b, err := h.Vault.ReadFile(r.Context(), "campaign.yaml"); err == nil {
		existing = string(b)
	}
	// Preserve what the form never edits: the scaffold's created stamp,
	// display-only versions, and the plugin namespace (the setup form owns
	// ruleset optionals, never enabled-plugins).
	if existing != "" {
		if cur, err := ParseCampaign(existing); err == nil {
			if c.Created == "" {
				c.Created = cur.Created
			}
			c.BaseVersion = cur.BaseVersion
			c.OverlayVersion = cur.OverlayVersion
			c.EnabledPlugins = cur.EnabledPlugins
		}
	}
	if c.Created == "" {
		c.Created = time.Now().UTC().Format(time.RFC3339)
	}
	if err := h.Vault.WriteFile(r.Context(), "campaign.yaml", UpsertCampaignYAML(existing, c)); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not write campaign.yaml")
		return
	}
	http.Redirect(w, r, RouteWizardSetup, http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Claim issuance (GM) — draft row doubles as the single-use claim token
// ---------------------------------------------------------------------------

// ClaimIssue mints a shareable single-use link /c/<token>/create for one
// slug + username (p07: GM-issued per slug; redemption invalidates).
func (h *WizardHandlers) ClaimIssue(w http.ResponseWriter, r *http.Request) {
	v, ok := gmOf(r)
	if !ok {
		writeDenied(w, http.StatusForbidden, "claim issuance is GM-only")
		return
	}
	if h.Drafts == nil {
		writeDenied(w, http.StatusInternalServerError, "wizard drafts are not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBody)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	username := strings.TrimSpace(r.FormValue("username"))
	if !slugRe.MatchString(slug) {
		writeDenied(w, http.StatusBadRequest, "slug: want [a-z0-9-]")
		return
	}
	if err := auth.ValidateUsername(username); err != nil {
		writeDenied(w, http.StatusBadRequest, "username: "+err.Error())
		return
	}
	token, err := NewDraftToken()
	if err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not mint claim token")
		return
	}
	now := time.Now()
	draft := &WizardDraft{
		Slug: slug, Username: username, Step: "start",
		Fields:    map[string]string{},
		CreatedAt: now.Unix(), ExpiresAt: now.Add(DraftTTL).Unix(),
	}
	if err := h.Drafts.Save(r.Context(), token, draft); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not store claim")
		return
	}
	link := RouteClaimPrefix + token + "/create"
	var b strings.Builder
	fmt.Fprintf(&b, "<h1>Claim link for %s</h1>\n", html.EscapeString(username))
	b.WriteString("<p>Single-use: completing the wizard consumes it. Send this link to the player:</p>\n")
	fmt.Fprintf(&b, "<p><code>%s</code></p>\n", html.EscapeString(link))
	_ = v
	writeHTML(w, http.StatusOK, "Claim issued", b.String())
}

// ---------------------------------------------------------------------------
// Player wizard via claim token
// ---------------------------------------------------------------------------

var wizardSteps = []string{"start", "ancestry", "class", "background", "stats", "preview"}

func claimTokenOf(r *http.Request) (string, bool) {
	rest := strings.TrimPrefix(r.URL.Path, RouteClaimPrefix)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "create" || parts[0] == "" {
		return "", false
	}
	if strings.ContainsAny(parts[0], "/\\") {
		return "", false
	}
	return parts[0], true
}

func (h *WizardHandlers) loadClaim(w http.ResponseWriter, r *http.Request) (*WizardDraft, string, *auth.Viewer, bool) {
	token, ok := claimTokenOf(r)
	if !ok || h.Drafts == nil {
		writeDenied(w, http.StatusNotFound, "no such invitation")
		return nil, "", nil, false
	}
	draft, err := h.Drafts.Get(r.Context(), token, time.Now())
	if err != nil {
		// Uniform 404: unknown/expired/consumed tokens are indistinguishable.
		writeDenied(w, http.StatusNotFound, "no such invitation")
		return nil, "", nil, false
	}
	v := viewerOf(r)
	user, _, _, _ := store.SplitViewer(v)
	if user == "" {
		writeDenied(w, http.StatusUnauthorized, "log in to use your invitation")
		return nil, "", nil, false
	}
	if !strings.EqualFold(user, draft.Username) && (!v.IsGM || v.PreviewAs != "") {
		// Someone else's invitation: 404 (never confirm the slug exists).
		writeDenied(w, http.StatusNotFound, "no such invitation")
		return nil, "", nil, false
	}
	return draft, token, v, true
}

// ClaimWizard renders the current wizard step (draft/resume in SQLite:
// ?step= may revisit completed steps, never skip ahead).
func (h *WizardHandlers) ClaimWizard(w http.ResponseWriter, r *http.Request) {
	draft, _, _, ok := h.loadClaim(w, r)
	if !ok {
		return
	}
	step := strings.TrimSpace(r.URL.Query().Get("step"))
	if step == "" {
		step = draft.Step
	}
	if !stepReachable(draft.Step, step) {
		http.Redirect(w, r, r.URL.Path+"?step="+url.QueryEscape(draft.Step), http.StatusSeeOther)
		return
	}
	writeHTML(w, http.StatusOK, "Create your character", h.stepForm(r, draft, step))
}

// ClaimWizardPost saves step fields into the draft, or finalizes on preview.
func (h *WizardHandlers) ClaimWizardPost(w http.ResponseWriter, r *http.Request) {
	draft, token, _, ok := h.loadClaim(w, r)
	if !ok {
		return
	}
	if h.Vault == nil {
		writeDenied(w, http.StatusInternalServerError, "vault is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBody)
	if !h.csrfOK(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	step := strings.TrimSpace(r.FormValue("step"))
	if step == "" {
		step = draft.Step
	}
	if step == "done" {
		h.finalize(w, r, draft, token)
		return
	}
	fields, verr := validateStep(h.pack(), step, r)
	if verr != "" {
		writeDenied(w, http.StatusUnprocessableEntity, verr)
		return
	}
	for k, v := range fields {
		draft.Fields[step+"."+k] = v
	}
	draft.Step = nextStep(draft.Step)
	if err := h.Drafts.Save(r.Context(), token, draft); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not save draft")
		return
	}
	http.Redirect(w, r, r.URL.Path+"?step="+url.QueryEscape(draft.Step), http.StatusSeeOther)
}

func stepReachable(current, want string) bool {
	ci, wi := -1, -1
	for i, s := range wizardSteps {
		if s == current {
			ci = i
		}
		if s == want {
			wi = i
		}
	}
	return wi >= 0 && wi <= ci
}

func nextStep(cur string) string {
	for i, s := range wizardSteps {
		if s == cur && i+1 < len(wizardSteps) {
			return wizardSteps[i+1]
		}
	}
	return "preview"
}

// validateStep checks one step's fields against the resolved pack (vault
// truth when the handler carries VaultRoot; shape of Fields is unchanged).
func validateStep(p *Pack, step string, r *http.Request) (map[string]string, string) {
	if p == nil {
		p = StubPack()
	}
	switch step {
	case "start":
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" || len(name) > 80 {
			return nil, "character name is required (max 80 chars)"
		}
		return map[string]string{"name": name, "notes": strings.TrimSpace(r.FormValue("notes"))}, ""
	case "ancestry":
		a := strings.ToLower(strings.TrimSpace(r.FormValue("ancestry")))
		if !p.HasAncestry(a) {
			return nil, "pick one of: " + strings.Join(p.Ancestries, ", ")
		}
		return map[string]string{"ancestry": a}, ""
	case "class":
		c := strings.ToLower(strings.TrimSpace(r.FormValue("class")))
		if _, err := p.Class(c); err != nil {
			return nil, err.Error()
		}
		return map[string]string{"class": c}, ""
	case "background":
		bg := strings.ToLower(strings.TrimSpace(r.FormValue("background")))
		if !p.HasBackground(bg) {
			return nil, "pick one of: " + strings.Join(p.Backgrounds, ", ")
		}
		return map[string]string{"background": bg}, ""
	case "stats":
		got := map[string]int64{}
		for _, k := range StatKeys {
			n, ok := toInt(strings.TrimSpace(r.FormValue(k)))
			if !ok {
				return nil, "stats: assign every ability a standard-array value"
			}
			got[k] = n
		}
		want := append([]int64(nil), StandardArray...)
		var have []int64
		for _, k := range StatKeys {
			have = append(have, got[k])
		}
		sort.Slice(have, func(i, j int) bool { return have[i] > have[j] })
		for i := range want {
			if have[i] != want[i] {
				return nil, "stats: use each standard-array value exactly once (15, 14, 13, 12, 10, 8)"
			}
		}
		out := map[string]string{}
		for _, k := range StatKeys {
			out[k] = fmt.Sprintf("%d", got[k])
		}
		return out, ""
	case "preview":
		return map[string]string{}, ""
	}
	return nil, "unknown wizard step"
}

// finalize validates the full draft, writes characters/<slug>/index.md with
// owner: stamped (creation), then consumes the draft (single-use).
func (h *WizardHandlers) finalize(w http.ResponseWriter, r *http.Request, draft *WizardDraft, token string) {
	pack := h.pack()
	name := draft.Fields["start.name"]
	ancestry, classID, bg := draft.Fields["ancestry.ancestry"], draft.Fields["class.class"], draft.Fields["background.background"]
	if name == "" || ancestry == "" || classID == "" || bg == "" {
		writeDenied(w, http.StatusUnprocessableEntity, "the wizard is incomplete — revisit each step")
		return
	}
	class, err := pack.Class(classID)
	if err != nil {
		writeDenied(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	stats := map[string]int64{}
	for _, k := range StatKeys {
		n, ok := toInt(draft.Fields["stats."+k])
		if !ok {
			writeDenied(w, http.StatusUnprocessableEntity, "stats step is incomplete")
			return
		}
		stats[k] = n
	}
	rel := "characters/" + draft.Slug + "/index.md"
	if _, err := cleanWritePath(rel); err != nil {
		writeDenied(w, http.StatusBadRequest, "bad character slug")
		return
	}
	if _, err := h.Vault.ReadFile(r.Context(), rel); err == nil {
		writeDenied(w, http.StatusConflict, "that character already exists")
		return
	}
	conMod := floorDiv(stats["con"]-10, 2)
	hpMax := int64(class.HitDie) + conMod
	if hpMax < 1 {
		hpMax = 1
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "title: %s\n", quoteYAML(name))
	sb.WriteString("secret: true\n")
	fmt.Fprintf(&sb, "owner: %s\n", quoteYAML(draft.Username))
	sb.WriteString("sheet:\n")
	fmt.Fprintf(&sb, "  class: %s\n", quoteYAML(classID))
	fmt.Fprintf(&sb, "  ancestry: %s\n", quoteYAML(ancestry))
	fmt.Fprintf(&sb, "  background: %s\n", quoteYAML(bg))
	sb.WriteString("  level: 1\n  xp: 0\n")
	for _, k := range StatKeys {
		fmt.Fprintf(&sb, "  %s: %d\n", k, stats[k])
	}
	fmt.Fprintf(&sb, "  hp: %d\n  hp-max: %d\n", hpMax, hpMax)
	fmt.Fprintf(&sb, "  hit-dice: 1\n  hit-dice-max: 1\n  hit-die: %d\n", class.HitDie)
	sb.WriteString("  inspiration: 0\n  death-succ: 0\n  death-fail: 0\n")
	sb.WriteString("---\n\n")
	fmt.Fprintf(&sb, "# %s\n\n*Level 1 %s, %s %s.*\n\n", name, class.Name, ancestry, bg)
	if notes := draft.Fields["start.notes"]; notes != "" {
		sb.WriteString(notes + "\n")
		if !strings.HasSuffix(notes, "\n") {
			sb.WriteString("\n")
		}
	}
	if err := h.Vault.WriteFile(r.Context(), rel, sb.String()); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not create character")
		return
	}
	// Redemption invalidates the token: second POST finds no draft (404).
	if err := h.Drafts.Delete(r.Context(), token); err != nil {
		writeDenied(w, http.StatusInternalServerError, "created, but the invitation could not be consumed — tell your GM")
		return
	}
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

// stepForm renders one wizard step (server-side, keyboard-native radio/select
// controls, fieldset/legend — no JS needed).
func (h *WizardHandlers) stepForm(r *http.Request, draft *WizardDraft, step string) string {
	pack := h.pack()
	var b strings.Builder
	fmt.Fprintf(&b, "<h1>Create your character</h1>\n<p>Step: %s · progress is saved automatically.</p>\n", html.EscapeString(step))
	b.WriteString("<form method=\"post\" action=\"" + html.EscapeString(r.URL.Path) + "\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, html.EscapeString(h.csrfField(r)))
	if step != "preview" {
		fmt.Fprintf(&b, "<input type=\"hidden\" name=\"step\" value=\"%s\">\n", html.EscapeString(step))
	}
	get := func(s, k string) string { return draft.Fields[s+"."+k] }
	switch step {
	case "start":
		fmt.Fprintf(&b, "<p><label for=\"wz-cname\">Character name</label> <input id=\"wz-cname\" name=\"name\" value=\"%s\" size=\"40\" required></p>\n", html.EscapeString(get("start", "name")))
		fmt.Fprintf(&b, "<p><label for=\"wz-notes\">Notes (optional)</label><br><textarea id=\"wz-notes\" name=\"notes\" rows=\"4\" cols=\"60\">%s</textarea></p>\n", html.EscapeString(get("start", "notes")))
	case "ancestry":
		b.WriteString("<fieldset><legend>Ancestry</legend>\n")
		for _, a := range pack.Ancestries {
			checked := ""
			if get("ancestry", "ancestry") == a {
				checked = " checked"
			}
			fmt.Fprintf(&b, "<p><label><input type=\"radio\" name=\"ancestry\" value=\"%s\"%s> %s</label></p>\n", html.EscapeString(a), checked, html.EscapeString(a))
		}
		b.WriteString("</fieldset>\n")
	case "class":
		b.WriteString("<fieldset><legend>Class</legend>\n")
		var ids []string
		for id := range pack.Classes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			checked := ""
			if get("class", "class") == id {
				checked = " checked"
			}
			fmt.Fprintf(&b, "<p><label><input type=\"radio\" name=\"class\" value=\"%s\"%s> %s (d%d)</label></p>\n",
				html.EscapeString(id), checked, html.EscapeString(pack.Classes[id].Name), pack.Classes[id].HitDie)
		}
		b.WriteString("</fieldset>\n")
	case "background":
		b.WriteString("<fieldset><legend>Background</legend>\n")
		for _, bg := range pack.Backgrounds {
			checked := ""
			if get("background", "background") == bg {
				checked = " checked"
			}
			fmt.Fprintf(&b, "<p><label><input type=\"radio\" name=\"background\" value=\"%s\"%s> %s</label></p>\n", html.EscapeString(bg), checked, html.EscapeString(bg))
		}
		b.WriteString("</fieldset>\n")
	case "stats":
		b.WriteString("<fieldset><legend>Abilities — assign 15, 14, 13, 12, 10, 8</legend>\n")
		for _, k := range StatKeys {
			fmt.Fprintf(&b, "<p><label for=\"wz-%s\">%s</label> <select id=\"wz-%s\" name=\"%s\">\n", k, strings.ToUpper(k), k, k)
			for _, v := range StandardArray {
				sel := ""
				if get("stats", k) == fmt.Sprintf("%d", v) {
					sel = " selected"
				}
				fmt.Fprintf(&b, "<option value=\"%d\"%s>%d</option>\n", v, sel, v)
			}
			b.WriteString("</select></p>\n")
		}
		b.WriteString("</fieldset>\n")
	case "preview":
		b.WriteString("<h2>Preview</h2>\n<dl>\n")
		fmt.Fprintf(&b, "<dt>Name</dt><dd>%s</dd>\n", html.EscapeString(get("start", "name")))
		fmt.Fprintf(&b, "<dt>Ancestry</dt><dd>%s</dd>\n", html.EscapeString(get("ancestry", "ancestry")))
		fmt.Fprintf(&b, "<dt>Class</dt><dd>%s</dd>\n", html.EscapeString(get("class", "class")))
		fmt.Fprintf(&b, "<dt>Background</dt><dd>%s</dd>\n", html.EscapeString(get("background", "background")))
		b.WriteString("<dt>Abilities</dt><dd>")
		for i, k := range StatKeys {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %s", strings.ToUpper(k), html.EscapeString(get("stats", k)))
		}
		b.WriteString("</dd>\n</dl>\n")
		b.WriteString("<p><button type=\"submit\" name=\"step\" value=\"done\">Create character</button></p>\n</form>\n")
		return b.String()
	}
	b.WriteString("<p><button type=\"submit\">Continue</button></p>\n</form>\n")
	return b.String()
}
