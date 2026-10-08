package plugins

import (
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
)

func gmViewer() *auth.Viewer { return &auth.Viewer{UserID: "gm", IsGM: true} }

func dashboardSrc() DashboardSource {
	src := NewDashboardSource()
	src.MapID = "arena"
	src.Initiative = []InitiativeEntry{
		{ID: "g2", Name: "Goblin", Order: 2, HP: 7, MaxHP: 7},
		{ID: "f1", Name: "Fighter", Order: 1, HP: 12, MaxHP: 12},
	}
	src.Tokens = []TokenHP{{TokenID: "f1", Name: "Fighter", HP: 12, MaxHP: 12}}
	src.Toggles = []SecretToggle{{Path: "quests/ambush.md", Secret: true, Title: "Ambush"}}
	total := int64(14)
	src.DiceLog = []DiceLogEntry{{RollID: "r1", Actor: "f1", Notation: "2d6+3", Total: &total}}
	src.Notes = SessionNote{Path: "sessions/2026-10-07.md", Excerpt: "Gate fell.", Recap: "1 toggle flipped."}
	src.Timers = []Timer{{Name: "Torch", EndsAt: time.Now().Add(2 * time.Minute)}}
	return src
}

func TestAssembleForbidden(t *testing.T) {
	for _, v := range []*auth.Viewer{
		nil,
		{UserID: "mira"},
		{UserID: "gm", IsGM: true, PreviewAs: "mira"},
	} {
		if _, err := Assemble(v, dashboardSrc()); err == nil {
			t.Errorf("viewer %+v: expected FORBIDDEN", v)
		}
	}
}

func TestAssembleUnfilteredRejected(t *testing.T) {
	if _, err := Assemble(gmViewer(), DashboardSource{}); err == nil {
		t.Error("expected UNFILTERED_INPUT rejection")
	}
}

func TestAssembleOrdersInitiative(t *testing.T) {
	d, err := Assemble(gmViewer(), dashboardSrc())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Initiative) != 2 || d.Initiative[0].ID != "f1" {
		t.Fatalf("initiative not ordered: %+v", d.Initiative)
	}
	if d.MapID != "arena" {
		t.Errorf("map id = %q", d.MapID)
	}
}

func TestRenderDashboardLandmarksAndEscape(t *testing.T) {
	d, _ := Assemble(gmViewer(), dashboardSrc())
	frag := RenderDashboard(d)
	if frag.Target != "dashboard" {
		t.Errorf("target = %q", frag.Target)
	}
	for _, want := range []string{
		`aria-label="GM dashboard"`, `aria-label="Initiative"`,
		`aria-label="Token hit points"`, `aria-label="Secret toggles"`,
		`aria-label="Dice log"`, `aria-label="Session notes"`,
		`aria-label="Timers"`, `aria-live="polite"`, `aria-live="assertive"`,
		`id="vtt-map"`, `hidden from party`,
	} {
		if !strings.Contains(frag.HTML, want) {
			t.Errorf("fragment missing %q", want)
		}
	}
	// Escaping: hostile names must not break out.
	src := dashboardSrc()
	src.Toggles[0].Title = `<script>alert(1)</script>`
	d2, _ := Assemble(gmViewer(), src)
	if html := RenderDashboard(d2).HTML; strings.Contains(html, "<script>") {
		t.Error("unescaped HTML in fragment")
	}
}

func TestTimerRemaining(t *testing.T) {
	now := time.Now()
	if got := (Timer{Name: "x", EndsAt: now.Add(time.Minute)}).Remaining(now); got != "1m0s" {
		t.Errorf("remaining = %q", got)
	}
	if got := (Timer{Name: "x", EndsAt: now.Add(-time.Second)}).Remaining(now); got != "expired" {
		t.Errorf("remaining = %q", got)
	}
}
