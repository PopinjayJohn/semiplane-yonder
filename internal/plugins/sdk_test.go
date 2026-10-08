package plugins

import (
	"testing"

	"github.com/semiplane/yonder/internal/web"
)

func TestValidPluginID(t *testing.T) {
	ok := []string{"random-tables", "vtt-maps", "a", "x1-y2"}
	for _, id := range ok {
		if !ValidPluginID(id) {
			t.Errorf("ValidPluginID(%q) = false, want true", id)
		}
	}
	bad := []string{"", "Bad", "has space", "under_score", "dot.name", "-lead", "trail-"}
	for _, id := range bad {
		if ValidPluginID(id) {
			t.Errorf("ValidPluginID(%q) = true, want false", id)
		}
	}
}

func TestFrozenSlot(t *testing.T) {
	for _, s := range []string{"header-left", "header-center", "header-right",
		"sidebar-left", "sidebar-right", "footer", "page-actions", "sheet-header"} {
		if !FrozenSlot(s) {
			t.Errorf("FrozenSlot(%q) = false, want true", s)
		}
	}
	if FrozenSlot("invented-slot") {
		t.Error("invented slots must be rejected")
	}
}

func TestValidateSlot(t *testing.T) {
	good := web.SlotComponent{
		SlotName: web.SlotSidebarRight, ID: "w", Component: "widget",
		SecretFiltered: true, PluginID: "random-tables",
	}
	if err := ValidateSlot("random-tables", good); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		mut  func(*web.SlotComponent)
	}{
		{"unknown slot", func(s *web.SlotComponent) { s.SlotName = "nope" }},
		{"empty component", func(s *web.SlotComponent) { s.Component = " " }},
		{"plugin mismatch", func(s *web.SlotComponent) { s.PluginID = "other" }},
		{"unfiltered", func(s *web.SlotComponent) { s.SecretFiltered = false }},
	}
	for _, c := range cases {
		s := good
		c.mut(&s)
		if err := ValidateSlot("random-tables", s); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestParseEnabledPlugins(t *testing.T) {
	got, err := ParseEnabledPlugins("a, b", []string{"b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v, want [a b c]", got)
	}
	if _, err := ParseEnabledPlugins("Bad_ID", nil); err == nil {
		t.Error("expected malformed id error")
	}
	empty, err := ParseEnabledPlugins("", nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty = %v, %v", empty, err)
	}
}
