package web

// Dice tray HTTP surface (UI follow-up; the engine lives in Lane H2's
// internal/dice, the routing contract in Lane I2's internal/plugins
// transport.go).
//
// Import-cycle note: internal/plugins imports internal/web (slots/SDK), so
// web cannot import plugins. The two display-level mirrors below are
// deliberate and pinned by tests:
//   - blindRedacted mirrors plugins.RouteBlind: blind totals are visible to
//     effective GMs only; everyone else gets Total=nil.
//   - blindPlaceholder mirrors plugins.BlindPlaceholder.
// Replay re-auth itself is NOT mirrored: it runs inside
// dice.TransportService.Replay (stored values, never re-rolled; I2's
// DefaultAuthorize admits blind replays only for "role:gm" or the original
// viewer key carried in envelope_json).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/dice"
)

// blindPlaceholder is the text shown for withheld blind totals (never
// color-only; mirrors plugins.BlindPlaceholder).
const blindPlaceholder = "▓▓ blind roll (GM only)"

// maxDiceBody caps dice API bodies (notation + flags are tiny; 8KB stops
// giant-POST abuse per the pitfalls io.LimitReader rule).
const maxDiceBody = 8 << 10

// maxNotationLen caps one notation (the VTT tray uses 64; same bound here).
const maxNotationLen = 64

// recentDiceLimit is the dashboard dice-log page size.
const recentDiceLimit = 20

// diceTransport returns the roll/replay transport over the app DB's
// dice_logs table, built once and cached (the H2 transport holds per-actor
// rate-limit windows — a fresh instance per request would never limit).
// Test seams (same package) override h.diceRoller / h.diceLogs before first
// use; production lazily builds both. A nil AppDB (unwired read-path fakes)
// is an error, never a silent no-op.
func (h *WriteHandlers) diceTransport() (*dice.TransportService, error) {
	h.diceSvcMu.Lock()
	defer h.diceSvcMu.Unlock()
	if h.diceSvc != nil {
		return h.diceSvc, nil
	}
	roller := h.diceRoller
	if roller == nil {
		roller = dice.NewRoller()
	}
	var logs dice.LogStore
	if h.diceLogs != nil {
		logs = h.diceLogs
	} else {
		if h.Store == nil || h.Store.AppDB() == nil {
			return nil, fmt.Errorf("dice log unavailable")
		}
		logs = dice.NewLogStore(h.Store.AppDB())
	}
	h.diceSvc = dice.NewTransportService(roller, logs, dice.DefaultTransportConfig())
	return h.diceSvc, nil
}

// effGM reports the effective GM identity for dice routing: GM previews
// filter as the previewed user, never as GM (Phase 0c contract, same rule as
// plugins.ViewerOf).
func effGM(v *auth.Viewer) bool { return v != nil && v.IsGM && v.PreviewAs == "" }

// diceViewerKey maps a request viewer to the transport routing identity:
// "role:gm" for effective GMs, "actor:<user>" otherwise. Blind rows persist
// this key in envelope_json so replay can re-authorize the original viewer
// without a schema change (I2 contract).
func diceViewerKey(v *auth.Viewer) string {
	if effGM(v) {
		return "role:gm"
	}
	id := ""
	if v != nil {
		id = v.UserID
		if v.PreviewAs != "" {
			id = v.PreviewAs
		}
	}
	return "actor:" + id
}

// diceTotalForViewer applies blind routing to one stored/rolled total:
// blind totals stay visible to effective GMs only (mirrors
// plugins.RouteBlind). Open rolls are untouched.
func diceTotalForViewer(total int64, blind bool, v *auth.Viewer) *int64 {
	if blind && !effGM(v) {
		return nil
	}
	out := total
	return &out
}

// diceRollJSON is the roll/replay response shape (per-viewer filtered
// server-side; Total is null when withheld).
type diceRollJSON struct {
	RollID      string `json:"roll_id"`
	Actor       string `json:"actor"`
	Notation    string `json:"notation"`
	Blind       bool   `json:"blind"`
	Total       *int64 `json:"total"`
	Redacted    bool   `json:"redacted"`
	Replay      string `json:"replay"`
	Placeholder string `json:"placeholder,omitempty"`
}

// writeDiceJSON serves one filtered roll result (private, no-store:
// per-viewer content must never sit in a shared cache).
func writeDiceJSON(w http.ResponseWriter, v *auth.Viewer, res *dice.RollResult, rollID string) {
	total := diceTotalForViewer(res.Total, res.Blind, v)
	out := diceRollJSON{
		RollID: rollID, Actor: res.ActorID, Notation: res.Notation,
		Blind: res.Blind, Total: total, Redacted: total == nil,
		Replay: "/api/dice/replay?roll_id=" + rollID,
	}
	if out.Redacted {
		out.Placeholder = blindPlaceholder
	}
	b, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

// diceRollInput is the roll request (JSON or form fields; same closed set).
type diceRollInput struct {
	Notation string
	Blind    bool
}

// parseDiceRollInput reads notation + blind from a JSON fetch body or a
// dashboard form post. Unknown fields are ignored; bodies are length-bound.
func parseDiceRollInput(r *http.Request) (diceRollInput, error) {
	var in diceRollInput
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Notation string `json:"notation"`
			Blind    bool   `json:"blind"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, maxDiceBody))
		if err := dec.Decode(&body); err != nil {
			return in, fmt.Errorf("bad request")
		}
		in.Notation = body.Notation
		in.Blind = body.Blind
		return in, nil
	}
	// Dashboard form post (urlencoded): bound the body before ParseForm
	// (http.MaxBytesReader needs a live ResponseWriter — pitfalls — so the
	// cap is enforced here with ReadAll + LimitReader and a 400 past it).
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDiceBody+1))
	if err != nil || int64(len(body)) > maxDiceBody {
		return in, fmt.Errorf("bad request")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := r.ParseForm(); err != nil {
		return in, fmt.Errorf("bad request")
	}
	in.Notation = r.FormValue("notation")
	blind := strings.ToLower(strings.TrimSpace(r.FormValue("blind")))
	in.Blind = blind == "on" || blind == "true" || blind == "1"
	return in, nil
}

// isDiceFormPost reports a dashboard form post (urlencoded) vs a JSON fetch
// client: forms get the write-path 303 back to the dashboard, fetch clients
// get JSON.
func isDiceFormPost(r *http.Request) bool {
	return !strings.Contains(r.Header.Get("Content-Type"), "application/json")
}

// diceRoll handles POST /api/dice/roll (replaces the F2 501 stub on the same
// route): any logged-in viewer may roll; blind totals route GM-only per I2.
// Maxima (100 dice / 1000 faces) and per-actor rate limits are enforced by
// the H2 transport; this layer only maps errors to statuses.
func (h *WriteHandlers) diceRoll(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	if !h.checkCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	in, err := parseDiceRollInput(r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	notation := strings.TrimSpace(in.Notation)
	if notation == "" || len(notation) > maxNotationLen {
		http.Error(w, "bad notation", http.StatusBadRequest)
		return
	}
	ts, err := h.diceTransport()
	if err != nil {
		http.Error(w, "dice log unavailable", http.StatusInternalServerError)
		return
	}
	resp, err := ts.Roll(r.Context(), dice.RollRequest{
		Notation: notation,
		ActorID:  v.UserID,
		Blind:    in.Blind,
		// Open rolls broadcast to the party; blind rolls route GM-only
		// (transport Broadcast targets mirror this same split).
		Broadcast: !in.Blind,
		Metadata: map[string]any{
			"viewer": diceViewerKey(v),
			"ip":     r.RemoteAddr,
		},
	})
	if err != nil {
		writeDiceRollError(w, err)
		return
	}
	if isDiceFormPost(r) {
		redirectWithIdentity(w, r, "/dashboard")
		return
	}
	writeDiceJSON(w, v, resp.Result, resp.LogID)
}

// writeDiceRollError maps transport failures to statuses without leaking
// internals: invalid notation → 400, base maxima → 422, rate limit → 429.
func writeDiceRollError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "rate limit"):
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
	case strings.Contains(msg, "exceeds") || strings.Contains(msg, "outside"):
		http.Error(w, "roll exceeds maxima (100 dice / 1000 faces)", http.StatusUnprocessableEntity)
	default:
		http.Error(w, "cannot roll that", http.StatusBadRequest)
	}
}

// diceReplay handles GET /api/dice/replay?roll_id=N: replays STORED values
// (never re-rolls) after re-authorizing the presenting viewer; blind totals
// stay GM-only at display time even when the gate admits the original
// roller (mirrors plugins.RouteBlind over the replayed event).
func (h *WriteHandlers) diceReplay(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	rollID := strings.TrimSpace(r.URL.Query().Get("roll_id"))
	if rollID == "" {
		http.Error(w, "roll_id required", http.StatusBadRequest)
		return
	}
	ts, err := h.diceTransport()
	if err != nil {
		http.Error(w, "dice log unavailable", http.StatusInternalServerError)
		return
	}
	res, err := ts.Replay(r.Context(), rollID, diceViewerKey(v))
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "not found") || strings.Contains(msg, "unknown roll"):
			http.Error(w, "no such roll", http.StatusNotFound)
		case strings.Contains(msg, "not visible"):
			http.Error(w, "not allowed", http.StatusForbidden)
		default:
			http.Error(w, "cannot replay that", http.StatusBadRequest)
		}
		return
	}
	writeDiceJSON(w, v, res, rollID)
}

// diceRow is one dashboard dice-log row (server-filtered: Total is nil when
// this viewer may not see it).
type diceRow struct {
	RollID   string
	Actor    string
	Notation string
	Total    *int64
	Blind    bool
}

// recentDiceLog reads the newest dice_logs rows for the dashboard (newest
// first). It issues DML only against the Lane B-owned dice_logs columns (the
// same shape internal/dice/logs.go reads); per-viewer blind filtering is
// applied by the caller. A nil DB (unwired fakes) yields no rows.
func recentDiceLog(ctx context.Context, db *sql.DB, limit int) []diceRow {
	if db == nil {
		return nil
	}
	if limit <= 0 || limit > 100 {
		limit = recentDiceLimit
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, actor, result_json, blind FROM dice_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []diceRow
	for rows.Next() {
		var id int64
		var actor, resJSON string
		var blind int
		if err := rows.Scan(&id, &actor, &resJSON, &blind); err != nil {
			continue // corrupt row: skipped, never fatal (fail closed)
		}
		var res dice.RollResult
		if err := json.Unmarshal([]byte(resJSON), &res); err != nil {
			continue
		}
		out = append(out, diceRow{
			RollID: strconv.FormatInt(id, 10), Actor: actor,
			Notation: res.Notation, Total: &res.Total, Blind: blind != 0,
		})
	}
	return out
}

// filterDiceRows applies blind routing to log rows for one viewer (in
// place: totals withheld become nil; mirrors plugins.RouteBlind).
func filterDiceRows(rows []diceRow, v *auth.Viewer) []diceRow {
	for i, row := range rows {
		rows[i].Total = diceTotalForViewer(ptrValue(row.Total), row.Blind, v)
	}
	return rows
}

func ptrValue(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// renderDiceRowHTML renders one dashboard dice-log row: notation shape is
// always shown; withheld totals render the placeholder as TEXT (never
// color-only; redactions announced via the polite live region on the list).
func renderDiceRowHTML(row diceRow) string {
	var b strings.Builder
	b.WriteString(`<li data-roll="` + html.EscapeString(row.RollID) + `">`)
	b.WriteString(`<span>` + html.EscapeString(row.Actor) + `</span> `)
	b.WriteString(`<span>` + html.EscapeString(row.Notation) + `</span> `)
	if row.Total != nil {
		b.WriteString(`<span>` + strconv.FormatInt(*row.Total, 10) + `</span>`)
	} else {
		b.WriteString(`<span>` + html.EscapeString(blindPlaceholder) + `</span>`)
	}
	b.WriteString(` <a href="/api/dice/replay?roll_id=` + html.EscapeString(row.RollID) + `">replay</a>`)
	b.WriteString(`</li>` + "\n")
	return b.String()
}
