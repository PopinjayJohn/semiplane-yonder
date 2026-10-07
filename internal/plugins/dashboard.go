package plugins

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/auth"
)

// GM run dashboard (P07 v1): one page with initiative + selected token HP +
// secret -/+ toggles + dice log + session notes + timers. Assembles
// P08/P12/P03 pieces; NO new state — pure assembly over caller-supplied,
// already secret-filtered rows. GM-only: non-GM or GM-preview sessions are
// rejected (previews must show exactly what the previewed user sees).

// DashboardSource is the caller-supplied, ALREADY secret-filtered input.
// Filtered must be true (set only by NewDashboardSource after the caller ran
// secrets.Filter / ACL checks); Assemble refuses unmarked input (fail closed).
type DashboardSource struct {
	Filtered   bool
	Initiative []InitiativeEntry
	Tokens     []TokenHP
	Toggles    []SecretToggle
	DiceLog    []DiceLogEntry
	Notes      SessionNote
	Timers     []Timer
	MapID      string // VTT map embedded via the frozen fragment contract
}

// NewDashboardSource marks caller-filtered rows as safe input.
func NewDashboardSource() DashboardSource { return DashboardSource{Filtered: true} }

// InitiativeEntry is one tracker row (logic owned by the initiative-tracker
// feature; the dashboard only renders the order + feeds token HP).
type InitiativeEntry struct {
	ID     string
	Name   string // already filtered display name
	Order  int
	HP     int
	MaxHP  int
	Hidden bool // GM-only rows (never served past this point to players)
}

// TokenHP is the selected-token hit-point card.
type TokenHP struct {
	TokenID string
	Name    string
	HP      int
	MaxHP   int
}

// SecretToggle is one -/+ flip control (P03: GM 1-click toggle; the flip
// itself lands via the write path, the dashboard only renders the control).
type SecretToggle struct {
	Path   string // vault-relative page id
	Secret bool   // folded state: true = `-` (hidden from party)
	Title  string // filtered title (GM sees real titles)
}

// DiceLogEntry is one rendered dice-log row (P12 transport display).
type DiceLogEntry struct {
	RollID   string
	Actor    string
	Notation string
	Total    *int64 // nil = blind/redacted for this viewer
	Blind    bool
}

// SessionNote is the live session-note excerpt + recap flags.
type SessionNote struct {
	Path    string // e.g. sessions/<date>.md
	Excerpt string // filtered excerpt
	Recap   string // auto-recap line (toggles flipped, clocks, XP/loot)
}

// Timer is a session countdown (rendered as text; no ticking state here).
type Timer struct {
	Name   string
	EndsAt time.Time
}

// Remaining reports the text remaining duration ("2m04s", "expired").
func (t Timer) Remaining(now time.Time) string {
	d := t.EndsAt.Sub(now)
	if d <= 0 {
		return "expired"
	}
	return d.Truncate(time.Second).String()
}

// DashboardData is the assembled, ordered dashboard model.
type DashboardData struct {
	MapID      string
	Initiative []InitiativeEntry
	Tokens     []TokenHP
	Toggles    []SecretToggle
	DiceLog    []DiceLogEntry
	Notes      SessionNote
	Timers     []Timer
}

// Assemble builds the GM dashboard model. It rejects non-GM viewers,
// GM-preview sessions, and unfiltered input. Rows are sorted deterministically
// (initiative by Order, then ID).
func Assemble(viewer *auth.Viewer, src DashboardSource) (DashboardData, error) {
	if viewer == nil || !viewer.IsGM || viewer.PreviewAs != "" {
		return DashboardData{}, &PluginError{Code: "FORBIDDEN", Message: "dashboard is GM-only"}
	}
	if !src.Filtered {
		return DashboardData{}, &PluginError{Code: "UNFILTERED_INPUT", Message: "dashboard input must be secret-filtered"}
	}
	out := DashboardData{
		MapID:   src.MapID,
		Tokens:  append([]TokenHP(nil), src.Tokens...),
		Toggles: append([]SecretToggle(nil), src.Toggles...),
		DiceLog: append([]DiceLogEntry(nil), src.DiceLog...),
		Notes:   src.Notes,
		Timers:  append([]Timer(nil), src.Timers...),
	}
	out.Initiative = append([]InitiativeEntry(nil), src.Initiative...)
	sort.Slice(out.Initiative, func(i, j int) bool {
		if out.Initiative[i].Order != out.Initiative[j].Order {
			return out.Initiative[i].Order < out.Initiative[j].Order
		}
		return out.Initiative[i].ID < out.Initiative[j].ID
	})
	return out, nil
}

// RenderDashboard renders the dashboard fragment (semantic landmarks,
// keyboard-reachable controls, aria-live regions, text — never color-only —
// status). All strings are escaped; input is assumed filtered (Assemble
// enforces). SSE-patchable: sections carry stable ids.
func RenderDashboard(d DashboardData) UIFragment {
	var b strings.Builder
	b.WriteString(`<section aria-label="GM dashboard" data-fragment="dashboard">` + "\n")
	// Map embed (Lane K fragment).
	if d.MapID != "" {
		fmt.Fprintf(&b, `<div role="region" aria-label="Battle map" id="%s" data-sse-fragment="%s" data-map-id="%s"></div>`+"\n",
			MapFragmentTarget, MapFragmentPath, html.EscapeString(d.MapID))
	}
	// Initiative.
	b.WriteString(`<div role="region" aria-label="Initiative" id="dash-initiative"><h2>Initiative</h2><ol aria-live="polite">` + "\n")
	for _, e := range d.Initiative {
		fmt.Fprintf(&b, `<li data-entry="%s"><span>%s</span> <span>order %d</span> <span>%d/%d HP</span>%s</li>`+"\n",
			html.EscapeString(e.ID), html.EscapeString(e.Name), e.Order, e.HP, e.MaxHP, hiddenTag(e.Hidden))
	}
	b.WriteString("</ol></div>\n")
	// Token HP.
	b.WriteString(`<div role="region" aria-label="Token hit points" id="dash-tokens"><h2>Tokens</h2><ul>` + "\n")
	for _, t := range d.Tokens {
		fmt.Fprintf(&b, `<li data-token="%s"><span>%s</span> <span>%d/%d HP</span></li>`+"\n",
			html.EscapeString(t.TokenID), html.EscapeString(t.Name), t.HP, t.MaxHP)
	}
	b.WriteString("</ul></div>\n")
	// Secret toggles.
	b.WriteString(`<div role="region" aria-label="Secret toggles" id="dash-toggles"><h2>Reveals</h2><ul>` + "\n")
	for _, t := range d.Toggles {
		state := "hidden from party"
		if !t.Secret {
			state = "shown to party"
		}
		fmt.Fprintf(&b, `<li><button type="button" data-toggle="%s" aria-pressed="%t">%s</button> <span>%s</span></li>`+"\n",
			html.EscapeString(t.Path), t.Secret, html.EscapeString(t.Title), state)
	}
	b.WriteString("</ul></div>\n")
	// Dice log.
	b.WriteString(`<div role="region" aria-label="Dice log" id="dash-dice"><h2>Dice</h2><ol aria-live="polite">` + "\n")
	for _, r := range d.DiceLog {
		total := BlindPlaceholder
		if r.Total != nil {
			total = fmt.Sprintf("%d", *r.Total)
		}
		fmt.Fprintf(&b, `<li data-roll="%s"><span>%s</span> <span>%s</span> <span>%s</span></li>`+"\n",
			html.EscapeString(r.RollID), html.EscapeString(r.Actor), html.EscapeString(r.Notation), html.EscapeString(total))
	}
	b.WriteString("</ol></div>\n")
	// Notes + recap (reveals announced assertively).
	fmt.Fprintf(&b, `<div role="region" aria-label="Session notes" id="dash-notes"><h2>Notes</h2><p>%s</p><p aria-live="assertive">%s</p></div>`+"\n",
		html.EscapeString(d.Notes.Excerpt), html.EscapeString(d.Notes.Recap))
	// Timers.
	b.WriteString(`<div role="region" aria-label="Timers" id="dash-timers"><h2>Timers</h2><ul>` + "\n")
	now := time.Now()
	for _, t := range d.Timers {
		fmt.Fprintf(&b, `<li><span>%s</span> <span>%s</span></li>`+"\n",
			html.EscapeString(t.Name), html.EscapeString(t.Remaining(now)))
	}
	b.WriteString("</ul></div>\n</section>")
	return UIFragment{Target: "dashboard", HTML: b.String()}
}

func hiddenTag(hidden bool) string {
	if hidden {
		return ` <span class="tag">hidden</span>`
	}
	return ""
}
