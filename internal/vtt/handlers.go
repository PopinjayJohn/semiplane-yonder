package vtt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/dice"
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/web"
)

// Handlers serves the Lane K VTT surfaces. RegisterRoutes must run BEFORE
// the F1/F2 read/write registrations (first-wins registry rule, same G3
// precedent as I1's /me): GET /vtt/{mapID} retires F1's placeholder and
// POST /vtt/{mapID}/state retires F2's stub deliberately, never accidentally.
type Handlers struct {
	Store    store.Store
	Sessions auth.SessionStore
	Roller   dice.Roller
}

func (h *Handlers) roller() dice.Roller {
	if h.Roller != nil {
		return h.Roller
	}
	return dice.NewRoller()
}

// maxStateBody caps VTT JSON bodies (moves/fog/spawn intents are small;
// 64KB stops giant-POST abuse per the pitfalls io.LimitReader rule).
const maxStateBody = 64 << 10

// RegisterRoutes registers the frozen-contract routes (patterns reuse the
// plugins constants verbatim — never retyped).
func (h *Handlers) RegisterRoutes(reg *web.RouteRegistry) {
	reg.Register(web.Route{Method: http.MethodGet, Path: plugins.MapFragmentPath, Handler: h.Fragment, ReadOnly: true, AuthRequired: true, SecretFiltered: true})
	reg.Register(web.Route{Method: http.MethodGet, Path: plugins.StateSnapshotPath, Handler: h.State, ReadOnly: true, AuthRequired: true, SecretFiltered: true})
	reg.Register(web.Route{Method: http.MethodGet, Path: web.RouteVTT, Handler: h.Page, ReadOnly: true, AuthRequired: true})
	reg.Register(web.Route{Method: http.MethodPatch, Path: web.RouteVTT + "/state", Handler: h.Move, ReadOnly: false, AuthRequired: true})
	reg.Register(web.Route{Method: http.MethodPost, Path: web.RouteVTT + "/state", Handler: h.StateUpdate, ReadOnly: false, AuthRequired: true})
}

// viewer resolves the request viewer: real-auth context first (serve injects
// it, sessions or demo), same precedence as the read path.
func (h *Handlers) viewer(r *http.Request) *auth.Viewer {
	if v, ok := auth.ViewerFromContext(r.Context()); ok && v != nil {
		return v
	}
	return web.ViewerForRequest(r)
}

// asQuery threads the demo identity across asset/SSE URLs (opaque, escaped
// at render). Empty under real auth.
func asQuery(r *http.Request) string {
	as := strings.TrimSpace(r.URL.Query().Get("as"))
	if as == "" {
		return ""
	}
	q := "?as=" + as
	if p := strings.TrimSpace(r.URL.Query().Get("preview_as")); p != "" {
		q += "&preview_as=" + p
	}
	return q
}

func isGM(v *auth.Viewer) bool { return v != nil && v.IsGM && v.PreviewAs == "" }

// checkCSRF enforces double-submit CSRF on VTT writes (form field first,
// X-CSRF-Token header fallback for fetch clients — pitfalls). A nil session
// store skips only the comparison (unwired test/dev mode); login is still
// enforced separately.
func (h *Handlers) checkCSRF(r *http.Request) bool {
	if h.Sessions == nil {
		return true
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return false
	}
	sessID := c.Value
	if i := strings.IndexByte(sessID, '|'); i >= 0 {
		sessID = sessID[:i]
	}
	if !auth.ValidateSessionID(sessID) {
		return false
	}
	sess, err := h.Sessions.Get(r.Context(), sessID)
	if err != nil || sess == nil {
		return false
	}
	return auth.ValidateCSRFToken(sess.CSRFToken, auth.CSRFTokenFromRequest(r))
}

// csrfToken returns the session CSRF token for page embedding ("" unwired).
func (h *Handlers) csrfToken(r *http.Request) string {
	if h.Sessions == nil {
		return ""
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}
	sessID := c.Value
	if i := strings.IndexByte(sessID, '|'); i >= 0 {
		sessID = sessID[:i]
	}
	if !auth.ValidateSessionID(sessID) {
		return ""
	}
	sess, err := h.Sessions.Get(r.Context(), sessID)
	if err != nil || sess == nil {
		return ""
	}
	return sess.CSRFToken
}

// Page serves the full map document (login required; unviewable → 404).
func (h *Handlers) Page(w http.ResponseWriter, r *http.Request) {
	mapID, ok := MapIDFromPath(r.URL.Path, "/vtt/", "")
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	v := h.viewer(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	if !CanViewMap(ctx, h.Store, v, mapID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	snap, cal, err := SnapshotFor(ctx, h.Store, v, mapID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	page, _ := Sidecar(ctx, h.Store, mapID)
	title := ""
	if page != nil {
		title = page.Title
	}
	eff := EffViewer(v)
	aq := asQuery(r)
	live, _ := LoadState(ctx, h.Store, mapID)
	in := PageInput{
		FragmentInput: FragmentInput{
			MapID: mapID, Title: title, Snap: snap, Cal: cal,
			Fog:    ResolveFog(live.FogMask, live.FogPresent, cal.Grid),
			Viewer: eff, IsGM: isGM(v), AsQuery: aq, CSRF: h.csrfToken(r),
			CharLinks: h.charLinks(ctx, v, snap),
		},
		ViewerLabel: viewerLabel(v),
		StateURL:    "/api/vtt/" + mapID + "/state" + aq,
		StreamURL:   "/events?map=" + mapID + strings.Replace(aq, "?", "&", 1),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte(RenderPage(in)))
}

func viewerLabel(v *auth.Viewer) string {
	if v == nil || v.UserID == "" {
		return "guest"
	}
	if v.IsGM && v.PreviewAs == "" {
		return "GM " + v.UserID
	}
	if v.PreviewAs != "" {
		return v.UserID + " previewing " + v.PreviewAs
	}
	return v.UserID
}

// Fragment serves the inner HTML for #vtt-map (secret-filtered per viewer;
// secret-gated → private, no-store).
func (h *Handlers) Fragment(w http.ResponseWriter, r *http.Request) {
	mapID, ok := MapIDFromPath(r.URL.Path, "/vtt/", "/fragment")
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	v := h.viewer(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	snap, cal, err := SnapshotFor(ctx, h.Store, v, mapID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	page, _ := Sidecar(ctx, h.Store, mapID)
	title := ""
	if page != nil {
		title = page.Title
	}
	live, _ := LoadState(ctx, h.Store, mapID)
	in := FragmentInput{
		MapID: mapID, Title: title, Snap: snap, Cal: cal,
		Fog:    ResolveFog(live.FogMask, live.FogPresent, cal.Grid),
		Viewer: EffViewer(v), IsGM: isGM(v), AsQuery: asQuery(r), CSRF: h.csrfToken(r),
		CharLinks: h.charLinks(ctx, v, snap),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte(RenderFragment(in)))
}

func (h *Handlers) charLinks(ctx context.Context, v *auth.Viewer, snap plugins.StateSnapshot) map[string]string {
	out := map[string]string{}
	live, err := LoadState(ctx, h.Store, snap.MapID)
	if err != nil {
		return out
	}
	chars := map[string]string{}
	for _, t := range live.Tokens {
		chars[t.ID] = t.Character
	}
	for _, t := range snap.Tokens {
		if c := chars[t.ID]; c != "" {
			if link := CharacterLinkFor(ctx, h.Store, v, c); link != "" {
				out[t.ID] = link
			}
		}
	}
	return out
}

// State serves the frozen per-viewer snapshot JSON (private, no-store;
// clients fetch it on (re)connect and after fog-update).
func (h *Handlers) State(w http.ResponseWriter, r *http.Request) {
	mapID, ok := MapIDFromPath(r.URL.Path, "/api/vtt/", "/state")
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	v := h.viewer(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	snap, _, err := SnapshotFor(r.Context(), h.Store, v, mapID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	b, err := SnapshotJSON(snap)
	if err != nil {
		http.Error(w, "cannot encode state", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

// Move handles the debounced PATCH move (any map viewer may move tokens they
// are entitled to: GMs move anything; players move non-hidden tokens whose
// linked sheet they own, or unlinked tokens).
func (h *Handlers) Move(w http.ResponseWriter, r *http.Request) {
	mapID, ok := MapIDFromPath(r.URL.Path, "/vtt/", "/state")
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	v := h.viewer(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	if !h.checkCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if !CanViewMap(ctx, h.Store, v, mapID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Token string `json:"token"`
		X     int    `json:"x"`
		Y     int    `json:"y"`
	}
	if err := decodeStateBody(r, &body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !ValidTokenID(body.Token) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cal := CalibrationFor(ctx, h.Store, mapID)
	if body.X < 0 || body.Y < 0 || body.X >= cal.Grid.Cols || body.Y >= cal.Grid.Rows {
		http.Error(w, "move is off the grid", http.StatusUnprocessableEntity)
		return
	}
	if err := h.authorizeMove(ctx, v, mapID, body.Token); err != nil {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	tok, err := MoveToken(ctx, h.Store, mapID, body.Token, body.X, body.Y)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such token", http.StatusNotFound)
			return
		}
		http.Error(w, "cannot move token", http.StatusInternalServerError)
		return
	}
	Publish(Event{Name: plugins.EventTokenMove, Map: mapID})
	writeTokenJSON(w, v, tok)
}

// authorizeMove: GMs move anything. Players move non-hidden tokens they own
// via the linked sheet (index owner/editable-by; missing sheet → GM-only,
// fail closed). GM previews never move (preview is read-only).
func (h *Handlers) authorizeMove(ctx context.Context, v *auth.Viewer, mapID, tokenID string) error {
	if v == nil || v.UserID == "" {
		return errors.New("login required")
	}
	if v.PreviewAs != "" {
		return errors.New("previews are read-only")
	}
	if v.IsGM {
		return nil
	}
	live, err := LoadState(ctx, h.Store, mapID)
	if err != nil {
		return err
	}
	var cur *Token
	for i, t := range live.Tokens {
		if t.ID == tokenID {
			cur = &live.Tokens[i]
			break
		}
	}
	if cur == nil {
		return errors.New("no such token")
	}
	if cur.Hidden {
		return errors.New("hidden")
	}
	if cur.Character == "" {
		return nil // unlinked extra: any player at the table may slide it
	}
	page, err := h.Store.PageGet(ctx, cur.Character)
	if err != nil {
		return errors.New("unreadable sheet")
	}
	for _, o := range append([]string{page.Owner}, page.EditableBy...) {
		if strings.EqualFold(o, v.UserID) {
			return nil
		}
	}
	return errors.New("not your token")
}

// stateOp is the POST dispatch envelope (JSON; CSRF via header).
type stateOp struct {
	Op       string               `json:"op"`
	Token    string               `json:"token"`
	X        int                  `json:"x"`
	Y        int                  `json:"y"`
	HP       int                  `json:"hp"`
	Mode     string               `json:"mode"`
	Rect     *Rect                `json:"rect"`
	Order    []string             `json:"order"`
	Spawn    *plugins.SpawnIntent `json:"spawn"`
	Notation string               `json:"notation"`
	Blind    bool                 `json:"blind"`
}

// StateUpdate dispatches GM/player POST ops (login + CSRF + per-op auth).
func (h *Handlers) StateUpdate(w http.ResponseWriter, r *http.Request) {
	mapID, ok := MapIDFromPath(r.URL.Path, "/vtt/", "/state")
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	v := h.viewer(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	if !h.checkCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if !CanViewMap(ctx, h.Store, v, mapID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var op stateOp
	if err := decodeStateBody(r, &op); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch op.Op {
	case "move":
		h.opMove(w, r, v, mapID, op)
	case "hp":
		h.opHP(w, r, v, mapID, op)
	case "fog":
		h.opFog(w, r, v, mapID, op)
	case "spawn":
		h.opSpawn(w, r, v, mapID, op)
	case "initiative":
		h.opInitiative(w, r, v, mapID, op)
	case "reset":
		h.opReset(w, r, v, mapID)
	case "roll":
		h.opRoll(w, r, v, mapID, op)
	default:
		http.Error(w, "unknown op", http.StatusBadRequest)
	}
}

func (h *Handlers) opMove(w http.ResponseWriter, r *http.Request, v *auth.Viewer, mapID string, op stateOp) {
	if !ValidTokenID(op.Token) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	cal := CalibrationFor(ctx, h.Store, mapID)
	if op.X < 0 || op.Y < 0 || op.X >= cal.Grid.Cols || op.Y >= cal.Grid.Rows {
		http.Error(w, "move is off the grid", http.StatusUnprocessableEntity)
		return
	}
	if err := h.authorizeMove(ctx, v, mapID, op.Token); err != nil {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	tok, err := MoveToken(ctx, h.Store, mapID, op.Token, op.X, op.Y)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such token", http.StatusNotFound)
			return
		}
		http.Error(w, "cannot move token", http.StatusInternalServerError)
		return
	}
	Publish(Event{Name: plugins.EventTokenMove, Map: mapID})
	writeTokenJSON(w, v, tok)
}

// opHP is the GM quick-damage/heal hook (P07 run-mode: dashboard embeds it;
// players never set HP — fail closed).
func (h *Handlers) opHP(w http.ResponseWriter, r *http.Request, v *auth.Viewer, mapID string, op stateOp) {
	if !isGM(v) {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	if !ValidTokenID(op.Token) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tok, err := SetTokenHP(r.Context(), h.Store, mapID, op.Token, op.HP)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such token", http.StatusNotFound)
			return
		}
		http.Error(w, "cannot set HP", http.StatusInternalServerError)
		return
	}
	Publish(Event{Name: plugins.EventTokenHP, Map: mapID})
	writeTokenJSON(w, v, tok)
}

// opFog edits the hidden-rect mask (GM-only). Modes: hide/reveal (with rect),
// hide-all, reveal-all.
func (h *Handlers) opFog(w http.ResponseWriter, r *http.Request, v *auth.Viewer, mapID string, op stateOp) {
	if !isGM(v) {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	cal := CalibrationFor(ctx, h.Store, mapID)
	live, err := LoadState(ctx, h.Store, mapID)
	if err != nil {
		http.Error(w, "cannot read fog", http.StatusInternalServerError)
		return
	}
	rects, _ := DecodeMask(live.FogMask, cal.Grid) // corrupt → start hidden
	switch op.Mode {
	case "hide-all":
		rects = []Rect{{X: 0, Y: 0, W: cal.Grid.Cols, H: cal.Grid.Rows}}
	case "reveal-all":
		rects = nil
	case "hide", "reveal":
		if op.Rect == nil || op.Rect.W < 1 || op.Rect.H < 1 {
			http.Error(w, "bad rect", http.StatusBadRequest)
			return
		}
		if op.Mode == "hide" {
			rects = append(rects, *op.Rect)
		} else {
			var kept []Rect
			for _, hrect := range rects {
				kept = append(kept, subtractRect(hrect, *op.Rect)...)
			}
			rects = kept
		}
	default:
		http.Error(w, "unknown fog mode", http.StatusBadRequest)
		return
	}
	if err := SetFog(ctx, h.Store, mapID, EncodeMask(cal.Grid, rects)); err != nil {
		http.Error(w, "cannot set fog", http.StatusInternalServerError)
		return
	}
	Publish(Event{Name: plugins.EventFogUpdate, Map: mapID})
	writeOK(w, mapID)
}

// opSpawn executes the encounter-spawn intent (GM-only, one transaction).
func (h *Handlers) opSpawn(w http.ResponseWriter, r *http.Request, v *auth.Viewer, mapID string, op stateOp) {
	if !isGM(v) {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	if op.Spawn == nil || len(op.Spawn.Tokens) == 0 {
		http.Error(w, "empty spawn", http.StatusBadRequest)
		return
	}
	if len(op.Spawn.Tokens) > plugins.MaxEncounterCreatures {
		http.Error(w, "spawn exceeds creature cap", http.StatusUnprocessableEntity)
		return
	}
	ctx := r.Context()
	cal := CalibrationFor(ctx, h.Store, mapID)
	var toks []Token
	for _, s := range op.Spawn.Tokens {
		if !ValidTokenID(s.TokenID) {
			http.Error(w, "bad token id", http.StatusBadRequest)
			return
		}
		name := s.Name
		if name == "" {
			name = s.TokenID
		}
		toks = append(toks, Token{ID: s.TokenID, Name: name, X: s.X, Y: s.Y, HP: s.HP, MaxHP: s.MaxHP})
	}
	if err := SpawnTokens(ctx, h.Store, mapID, toks, op.Spawn.Order, cal.Grid); err != nil {
		var se *StateError
		if errors.As(err, &se) {
			http.Error(w, se.Msg, http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, "cannot spawn", http.StatusInternalServerError)
		return
	}
	Publish(Event{Name: plugins.EventEncounterSpawn, Map: mapID})
	writeOK(w, mapID)
}

// opInitiative replaces the order (GM-only; unknown ids dropped).
func (h *Handlers) opInitiative(w http.ResponseWriter, r *http.Request, v *auth.Viewer, mapID string, op stateOp) {
	if !isGM(v) {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	if len(op.Order) == 0 || len(op.Order) > 64 {
		http.Error(w, "bad order", http.StatusBadRequest)
		return
	}
	if err := SetInitiative(r.Context(), h.Store, mapID, op.Order); err != nil {
		http.Error(w, "cannot set initiative", http.StatusInternalServerError)
		return
	}
	Publish(Event{Name: plugins.EventInitiativeUpdate, Map: mapID})
	writeOK(w, mapID)
}

// opReset reseeds tokens + fog from sidecar defaults (GM-only recovery after
// data-dir loss; P08 "reset to sidecar").
func (h *Handlers) opReset(w http.ResponseWriter, r *http.Request, v *auth.Viewer, mapID string) {
	if !isGM(v) {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if err := ResetToSidecar(ctx, h.Store, mapID, CalibrationFor(ctx, h.Store, mapID)); err != nil {
		http.Error(w, "cannot reset", http.StatusInternalServerError)
		return
	}
	Publish(Event{Name: plugins.EventEncounterSpawn, Map: mapID})
	Publish(Event{Name: plugins.EventFogUpdate, Map: mapID})
	writeOK(w, mapID)
}

// opRoll is the dice tray (any map viewer; blind totals route GM-only via
// plugins.RouteBlind — the roller sees nothing extra on blind rolls unless
// they are GM). Rolls are ephemeral broadcasts; persistence stays with H2's
// dice_logs transport (not written here).
func (h *Handlers) opRoll(w http.ResponseWriter, r *http.Request, v *auth.Viewer, mapID string, op stateOp) {
	notation := strings.TrimSpace(op.Notation)
	if notation == "" || len(notation) > 64 {
		http.Error(w, "bad notation", http.StatusBadRequest)
		return
	}
	res, err := h.roller().Roll(r.Context(), notation)
	if err != nil {
		http.Error(w, "cannot roll that", http.StatusUnprocessableEntity)
		return
	}
	total := res.Total
	ev := Event{Name: plugins.EventDiceRoll, Map: mapID, RollID: res.RollID, Actor: v.UserID, Notation: notation, Total: &total}
	if op.Blind {
		ev.Name = plugins.EventDiceBlind
		ev.Blind = true
	}
	Publish(ev)
	routed := plugins.RouteBlind(plugins.RollEvent{RollID: res.RollID, ActorID: v.UserID, Notation: notation, Blind: op.Blind, Total: &total}, v)
	b, _ := json.Marshal(map[string]any{
		"roll_id": routed.RollID, "actor": routed.ActorID, "notation": routed.Notation,
		"blind": routed.Blind, "total": routed.Total, "redacted": routed.Total == nil, "map_id": mapID,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

// subtractRect cuts r out of h (up to 4 surviving rects).
func subtractRect(h, r Rect) []Rect {
	ix, iy := max(h.X, r.X), max(h.Y, r.Y)
	ix2, iy2 := min(h.X+h.W, r.X+r.W), min(h.Y+h.H, r.Y+r.H)
	if ix >= ix2 || iy >= iy2 {
		return []Rect{h}
	}
	var out []Rect
	if h.Y < iy {
		out = append(out, Rect{h.X, h.Y, h.W, iy - h.Y})
	}
	if iy2 < h.Y+h.H {
		out = append(out, Rect{h.X, iy2, h.W, h.Y + h.H - iy2})
	}
	if h.X < ix {
		out = append(out, Rect{h.X, iy, ix - h.X, iy2 - iy})
	}
	if ix2 < h.X+h.W {
		out = append(out, Rect{ix2, iy, h.X + h.W - ix2, iy2 - iy})
	}
	return out
}

func decodeStateBody(r *http.Request, dst any) error {
	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("json only")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxStateBody))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func writeTokenJSON(w http.ResponseWriter, v *auth.Viewer, t Token) {
	out := map[string]any{"id": t.ID, "name": t.Name, "x": t.X, "y": t.Y, "hp": t.HP, "max_hp": t.MaxHP}
	if isGM(v) && t.Hidden {
		out["hidden"] = true
	}
	b, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

func writeOK(w http.ResponseWriter, mapID string) {
	b, _ := json.Marshal(map[string]string{"ok": "true", "map_id": mapID})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}
