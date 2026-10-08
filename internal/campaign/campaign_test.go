package campaign

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeVault(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestParseFull(t *testing.T) {
	c, err := Parse([]byte(`# table campaign — comments survive validation
name: Cinder Pact
created: 2026-10-07
base: dnd
base-version: "5.2"
overlay: 5e-2024
overlay-version: "1.3"
enabled-features:
  - flanking-2024
  - second-wind-2024
enabled-plugins: [random-tables]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Cinder Pact" || c.Base != "dnd" || c.BaseVersion != "5.2" ||
		c.Overlay != "5e-2024" || c.OverlayVersion != "1.3" {
		t.Fatalf("scalar fields: %+v", c)
	}
	if len(c.EnabledFeatures) != 2 || c.EnabledFeatures[0] != "flanking-2024" {
		t.Fatalf("features: %q", c.EnabledFeatures)
	}
	if len(c.EnabledPlugins) != 1 || c.EnabledPlugins[0] != "random-tables" {
		t.Fatalf("plugins: %q", c.EnabledPlugins)
	}
}

func TestParseLegacyScaffold(t *testing.T) {
	// `init --bare` predates H1: name/created only must validate clean.
	c, err := Parse([]byte("name: fresh\ncreated: 2026-10-07\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Base != "" || c.Overlay != "" || len(c.EnabledFeatures) != 0 {
		t.Fatalf("legacy scaffold should leave ruleset fields empty: %+v", c)
	}
}

func TestParseRejects(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":       "name: x\nfuture-key: 1\n",
		"bad feature type":  "name: x\nenabled-features:\n  - id: flanking\n",
		"map for base":      "name: x\nbase:\n  id: dnd\n",
		"indented top":      "  name: x\n",
		"flow map features": "name: x\nenabled-features: {a: b}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatalf("want error for %q", body)
			}
		})
	}
}

func TestParseVersionsStringify(t *testing.T) {
	c, err := Parse([]byte("name: x\nbase: dnd\nbase-version: 2024\noverlay-version: 1.3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseVersion != "2024" || c.OverlayVersion != "1.3" {
		t.Fatalf("versions: %+v", c)
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrNoCampaign) {
		t.Fatalf("want ErrNoCampaign, got %v", err)
	}
}

func TestLoadReadsFile(t *testing.T) {
	root := writeVault(t, map[string]string{
		"campaign.yaml": "name: demo\nbase: dnd\nenabled-features: []\n",
	})
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "demo" || c.Base != "dnd" {
		t.Fatalf("loaded: %+v", c)
	}
}

func TestSameIndentSequence(t *testing.T) {
	// The `key:\n- item` Obsidian shape must parse (Lane A pitfall).
	c, err := Parse([]byte("name: x\nenabled-features:\n- flanking-2024\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EnabledFeatures) != 1 || c.EnabledFeatures[0] != "flanking-2024" {
		t.Fatalf("features: %q", c.EnabledFeatures)
	}
}

func TestEnabledFeaturesScalarCoerces(t *testing.T) {
	c, err := Parse([]byte("name: x\nenabled-features: flanking-2024\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EnabledFeatures) != 1 {
		t.Fatalf("features: %q", c.EnabledFeatures)
	}
}

func TestCRLFTolerated(t *testing.T) {
	c, err := Parse([]byte("name: x\r\nbase: dnd\r\nenabled-features:\r\n  - a\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Base != "dnd" || len(c.EnabledFeatures) != 1 {
		t.Fatalf("crlf: %+v", c)
	}
}

func TestEnabledPluginsSeparateNamespace(t *testing.T) {
	// A feature id and a plugin id may share a name; validation keeps both.
	c, err := Parse([]byte("name: x\nenabled-features:\n  - random-tables\nenabled-plugins:\n  - random-tables\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.EnabledFeatures[0] != "random-tables" || c.EnabledPlugins[0] != "random-tables" {
		t.Fatalf("namespaces: %+v", c)
	}
}

func TestErrorCarriesLine(t *testing.T) {
	_, err := Parse([]byte("name: x\nbase: dnd\nbogus-key: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("want line 3 in error, got %v", err)
	}
}

func TestExponentLikeIDsStayStrings(t *testing.T) {
	// Go's ParseFloat underflows "5e-2024" to 0 with a nil error; pack
	// and overlay ids must survive as strings.
	c, err := Parse([]byte("name: x\noverlay: 5e-2024\noverlay-version: 1e999\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Overlay != "5e-2024" || c.OverlayVersion != "1e999" {
		t.Fatalf("exponent-like scalars coerced: %+v", c)
	}
	zero, err := Parse([]byte("name: x\nbase-version: 0.0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if zero.BaseVersion != "0" && zero.BaseVersion != "0.0" {
		t.Fatalf("zero literal: %q", zero.BaseVersion)
	}
}
