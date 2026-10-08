package vtt

import (
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
)

func gmViewer() *auth.Viewer             { return &auth.Viewer{UserID: "gm", IsGM: true} }
func playerViewer(u string) *auth.Viewer { return &auth.Viewer{UserID: u} }
func previewViewer() *auth.Viewer {
	return &auth.Viewer{UserID: "gm", IsGM: true, PreviewAs: "mira"}
}

func TestValidMapID(t *testing.T) {
	for _, good := range []string{"arena", "Arena-1_2", "a", strings.Repeat("x", 64)} {
		if !ValidMapID(good) {
			t.Errorf("ValidMapID(%q) = false, want true", good)
		}
	}
	for _, bad := range []string{"", "a/b", "../x", "a b", "x.md", "CON", "con", "lpt1", strings.Repeat("x", 65), "é"} {
		if ValidMapID(bad) {
			t.Errorf("ValidMapID(%q) = true, want false", bad)
		}
	}
}

func TestMapIDFromPath(t *testing.T) {
	if id, ok := MapIDFromPath("/vtt/arena/fragment", "/vtt/", "/fragment"); !ok || id != "arena" {
		t.Errorf("fragment extract = %q,%v", id, ok)
	}
	if id, ok := MapIDFromPath("/api/vtt/arena/state", "/api/vtt/", "/state"); !ok || id != "arena" {
		t.Errorf("state extract = %q,%v", id, ok)
	}
	if id, ok := MapIDFromPath("/vtt/arena", "/vtt/", ""); !ok || id != "arena" {
		t.Errorf("page extract = %q,%v", id, ok)
	}
	for _, p := range []string{"/vtt/../x/fragment", "/vtt/a/b/fragment", "/vtt//fragment", "/vtt/CON/fragment"} {
		if _, ok := MapIDFromPath(p, "/vtt/", "/fragment"); ok {
			t.Errorf("MapIDFromPath(%q) accepted", p)
		}
	}
}

func TestParseCalibrationDefaults(t *testing.T) {
	c := ParseCalibration(nil)
	if c.Grid.Cols != 20 || c.Grid.Rows != 14 || c.FogDefault != "hidden" {
		t.Errorf("defaults wrong: %+v", c)
	}
	// Broken values fall back field-by-field, never brick the board.
	c = ParseCalibration(map[string]any{
		"background":  "../evil.png",
		"grid":        map[string]any{"cols": int64(9999), "rotation": int64(45)},
		"fog-default": "sometimes",
	})
	if c.Background != "" || c.Grid.Cols != 20 || c.Grid.Rotation != 0 || c.FogDefault != "hidden" {
		t.Errorf("broken calibration not defaulted: %+v", c)
	}
	c = ParseCalibration(map[string]any{
		"background":  "maps/arena.png",
		"grid":        map[string]any{"cols": int64(30), "rows": int64(10), "rotation": int64(90)},
		"fog-default": "revealed",
		"tokens": []any{
			map[string]any{"id": "gob-1", "name": "Goblin", "x": int64(3), "y": int64(4), "hp": int64(7), "max-hp": int64(7)},
			map[string]any{"id": "bad id", "x": int64(99)},
			map[string]any{"id": "off", "x": int64(99), "y": int64(99)},
		},
	})
	if c.Background != "maps/arena.png" || c.Grid.Cols != 30 || c.Grid.Rotation != 90 {
		t.Errorf("good calibration dropped: %+v", c)
	}
	if len(c.Defaults) != 1 || c.Defaults[0].ID != "gob-1" || c.Defaults[0].HP != 7 {
		t.Errorf("token defaults wrong: %+v", c.Defaults)
	}
}

func TestFogCodecRoundTrip(t *testing.T) {
	g := Grid{Cols: 20, Rows: 14}
	rects := []Rect{{X: 18, Y: 12, W: 10, H: 10}, {X: -2, Y: 0, W: 5, H: 3}, {X: 1, Y: 1, W: 0, H: 2}}
	mask := EncodeMask(g, rects)
	got, ok := DecodeMask(mask, g)
	if !ok || len(got) != 2 {
		t.Fatalf("round trip = %v,%v want 2 clipped rects", got, ok)
	}
	// Clipping: first rect clipped to 18,12,2,2; second to 0,0,3,3.
	if got[0] != (Rect{X: 0, Y: 0, W: 3, H: 3}) || got[1] != (Rect{X: 18, Y: 12, W: 2, H: 2}) {
		t.Errorf("clipped rects wrong: %v", got)
	}
}

func TestResolveFogFailClosed(t *testing.T) {
	g := Grid{Cols: 20, Rows: 14}
	// Missing row, empty mask, corrupt mask, grid mismatch: all hidden.
	for i, tc := range []struct {
		mask    string
		present bool
	}{
		{"", false}, {"", true}, {"   ", true}, {"garbage", true},
		{"v1;20x14;0,0,99,99", true}, {"v1;8x8;", true}, {"v2;20x14;", true},
	} {
		if fv := ResolveFog(tc.mask, tc.present, g); !fv.Hidden {
			t.Errorf("case %d (%q,%v) failed open", i, tc.mask, tc.present)
		}
	}
	// Explicit stored masks (even fully revealed) open the board.
	if fv := ResolveFog(EncodeMask(g, nil), true, g); fv.Hidden || len(fv.Rects) != 0 {
		t.Errorf("revealed mask should open: %+v", fv)
	}
	fv := ResolveFog(EncodeMask(g, []Rect{{X: 0, Y: 0, W: 2, H: 2}}), true, g)
	if fv.Hidden || fv.Shape == "" || len(fv.Rects) != 1 {
		t.Errorf("hidden mask wrong: %+v", fv)
	}
}

func TestSubtractRect(t *testing.T) {
	h := Rect{X: 0, Y: 0, W: 4, H: 4}
	// No overlap: identity.
	if out := subtractRect(h, Rect{X: 10, Y: 10, W: 2, H: 2}); len(out) != 1 || out[0] != h {
		t.Errorf("no-overlap = %v", out)
	}
	// Center punch: 4 survivors covering 12 of 16 cells.
	out := subtractRect(h, Rect{X: 1, Y: 1, W: 2, H: 2})
	cells := 0
	for _, r := range out {
		cells += r.W * r.H
	}
	if len(out) != 4 || cells != 12 {
		t.Errorf("center punch = %v (%d cells)", out, cells)
	}
	// Full cover: nothing.
	if out := subtractRect(h, h); len(out) != 0 {
		t.Errorf("full cover = %v", out)
	}
}

func TestClampToken(t *testing.T) {
	g := Grid{Cols: 20, Rows: 14}
	if _, ok := ClampToken(g, Token{ID: "bad id"}); ok {
		t.Error("bad id accepted")
	}
	if _, ok := ClampToken(g, Token{ID: "t1", X: 20, Y: 0}); ok {
		t.Error("off-grid accepted")
	}
	got, ok := ClampToken(g, Token{ID: "t1", HP: 99, MaxHP: 7, X: 1, Y: 1})
	if !ok || got.HP != 7 || got.Name != "t1" {
		t.Errorf("clamp wrong: %+v,%v", got, ok)
	}
	if _, ok := ClampToken(g, Token{ID: "t1", Character: "../x.md"}); ok {
		t.Error("traversal character link accepted")
	}
}

func TestWireProjection(t *testing.T) {
	total := int64(18)
	ev := Event{Name: "dice-blind", Map: "arena", RollID: "r1", Actor: "bram", Notation: "1d20+5", Total: &total, Blind: true}
	// GM sees the total; players and previews get "redacted".
	if _, data, ok := ProjectForViewer(ev, gmViewer()); !ok || !strings.Contains(data, `"total":18`) {
		t.Errorf("GM blind payload = %v,%s", ok, data)
	}
	if _, data, ok := ProjectForViewer(ev, playerViewer("mira")); !ok || !strings.Contains(data, `"redacted":true`) || strings.Contains(data, `"total":18`) {
		t.Errorf("player blind payload leaks: %v %s", ok, data)
	}
	if _, data, ok := ProjectForViewer(ev, previewViewer()); !ok || strings.Contains(data, `"total":18`) {
		t.Errorf("GM preview saw blind total: %v %s", ok, data)
	}
	if _, _, ok := ProjectForViewer(ev, nil); ok {
		t.Error("guest received dice event")
	}
	if _, _, ok := ProjectForViewer(Event{Name: "nope", Map: "arena"}, gmViewer()); ok {
		t.Error("unknown event projected")
	}
}
