package campaign

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func toggleFiles() map[string]string {
	return map[string]string{
		"campaign.yaml": `# Cinder Pact — GM edits below via the dashboard.
name: Cinder Pact
created: 2026-10-07
base: dnd # system base (display-only version below)
base-version: "5.2"
overlay: 5e-2024
overlay-version: "1.0"
# Optionals the table runs:
enabled-features:
  - flanking-2024 # west marches vote
  - second-wind-2024
enabled-plugins:
  - random-tables
`,
		"house.md": "## Flanking\n\n<!-- optional:id=flanking-2024 -->\n\n> [!optional|id=second-wind-2024]\n> text\n",
	}
}

func TestSetEnabledRoundTrip(t *testing.T) {
	root := writeVault(t, toggleFiles())
	if err := SetEnabled(root, []string{"second-wind-2024", "flanking-2024"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EnabledFeatures) != 2 || c.EnabledFeatures[0] != "second-wind-2024" ||
		c.EnabledFeatures[1] != "flanking-2024" {
		t.Fatalf("order not preserved: %q", c.EnabledFeatures)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "campaign.yaml"))
	text := string(raw)
	for _, keep := range []string{
		"# Cinder Pact — GM edits below via the dashboard.",
		"base: dnd # system base (display-only version below)",
		"enabled-plugins:",
		"  - random-tables",
	} {
		if !strings.Contains(text, keep) {
			t.Fatalf("surgical write dropped %q:\n%s", keep, text)
		}
	}
	if strings.Contains(text, "- flanking-2024 # west marches vote") {
		t.Fatalf("stale item survived:\n%s", text)
	}
}

func TestSetEnabledAppendsMissingKey(t *testing.T) {
	files := toggleFiles()
	files["campaign.yaml"] = "name: Cinder Pact\ncreated: 2026-10-07\nbase: dnd\n"
	root := writeVault(t, files)
	if err := SetEnabled(root, []string{"flanking-2024"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EnabledFeatures) != 1 || c.EnabledFeatures[0] != "flanking-2024" {
		t.Fatalf("features: %+v", c)
	}
}

func TestSetEnabledUnknownFailsClean(t *testing.T) {
	root := writeVault(t, toggleFiles())
	before, _ := os.ReadFile(filepath.Join(root, "campaign.yaml"))
	if err := SetEnabled(root, []string{"nope-not-offered"}); err == nil {
		t.Fatal("want unknown-id error")
	} else if !strings.Contains(err.Error(), "nope-not-offered") {
		t.Fatalf("error: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "campaign.yaml"))
	if string(before) != string(after) {
		t.Fatal("failed toggle rewrote campaign.yaml")
	}
}

func TestSetEnabledEmptyClears(t *testing.T) {
	root := writeVault(t, toggleFiles())
	if err := SetEnabled(root, nil); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EnabledFeatures) != 0 {
		t.Fatalf("features: %q", c.EnabledFeatures)
	}
}

func TestSetEnabledDedupes(t *testing.T) {
	root := writeVault(t, toggleFiles())
	if err := SetEnabled(root, []string{"flanking-2024", "flanking-2024"}); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(root)
	if len(c.EnabledFeatures) != 1 {
		t.Fatalf("features: %q", c.EnabledFeatures)
	}
}

func TestSortedIDs(t *testing.T) {
	root := writeVault(t, toggleFiles())
	ids, err := SortedIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "flanking-2024" || ids[1] != "second-wind-2024" {
		t.Fatalf("ids: %q", ids)
	}
}
