package campaign

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const basePack = `id: dnd
name: Dungeons & Dragons (SRD 5.2)
type: base
version: "5.2"
attribution: "SRD 5.2 under CC-BY-4.0"
`

const overlayPack = `id: PLACEHOLDER
name: overlay
type: overlay
version: "1.0"
parent: dnd
`

func writePack(t *testing.T, root, dir, desc string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(dir))
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "pack.yaml"), []byte(desc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveStack(t *testing.T) {
	root := writeVault(t, map[string]string{
		"campaign.yaml": "name: x\nbase: dnd\nbase-version: \"5.2\"\noverlay: 5e-2024\noverlay-version: \"1.0\"\n",
	})
	writePack(t, root, "rules/base/dnd", basePack)
	writePack(t, root, "rules/overlay/5e-2024", withID(overlayPack, "5e-2024"))
	writePack(t, root, "homebrew/grit", "id: grit\nname: Grit\ntype: homebrew\nversion: \"0.1\"\nparent: 5e-2024\n")
	if err := os.MkdirAll(filepath.Join(root, "homebrew", "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Base == nil || st.Base.ID != "dnd" || st.Base.Dir != "rules/base/dnd" {
		t.Fatalf("base: %+v", st.Base)
	}
	if st.Overlay == nil || st.Overlay.ID != "5e-2024" || st.Overlay.Parent != "dnd" {
		t.Fatalf("overlay: %+v", st.Overlay)
	}
	if len(st.Homebrew) != 1 || st.Homebrew[0].ID != "grit" {
		t.Fatalf("homebrew: %+v", st.Homebrew)
	}
	if st.Base.Version != "5.2" {
		t.Fatalf("version not recorded: %+v", st.Base)
	}
}

func withID(pack, id string) string {
	var out []string
	for _, ln := range strings.Split(pack, "\n") {
		if strings.HasPrefix(ln, "id:") {
			out = append(out, "id: "+id)
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func TestOverlaySwitchNoRebuild(t *testing.T) {
	// P05 pass/fail (resolver half): flipping campaign.yaml's overlay and
	// re-resolving changes the stack with no rebuild step in between.
	root := writeVault(t, map[string]string{
		"campaign.yaml": "name: x\nbase: dnd\noverlay: 5e-2014\n",
	})
	writePack(t, root, "rules/base/dnd", basePack)
	writePack(t, root, "rules/overlay/5e-2014", withID(overlayPack, "5e-2014"))
	writePack(t, root, "rules/overlay/5e-2024", withID(overlayPack, "5e-2024"))

	first, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Overlay.ID != "5e-2014" {
		t.Fatalf("first: %+v", first.Overlay)
	}
	if err := os.WriteFile(filepath.Join(root, "campaign.yaml"),
		[]byte("name: x\nbase: dnd\noverlay: 5e-2024\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Overlay.ID != "5e-2024" {
		t.Fatalf("second: %+v", second.Overlay)
	}
}

func TestVersionsRecordedNeverEnforced(t *testing.T) {
	root := writeVault(t, map[string]string{
		"campaign.yaml": "name: x\nbase: dnd\nbase-version: \"9.9\"\noverlay: 5e-2024\noverlay-version: \"9.9\"\n",
	})
	writePack(t, root, "rules/base/dnd", basePack)
	writePack(t, root, "rules/overlay/5e-2024", withID(overlayPack, "5e-2024"))
	st, err := Resolve(root)
	if err != nil {
		t.Fatalf("version skew must not fail resolution: %v", err)
	}
	if st.Base.Version != "5.2" || st.Overlay.Version != "1.0" {
		t.Fatalf("pack versions not surfaced: %+v %+v", st.Base, st.Overlay)
	}
}

func TestResolveErrors(t *testing.T) {
	t.Run("no campaign", func(t *testing.T) {
		if _, err := Resolve(t.TempDir()); !errors.Is(err, ErrNoCampaign) {
			t.Fatalf("want ErrNoCampaign, got %v", err)
		}
	})
	t.Run("missing base", func(t *testing.T) {
		root := writeVault(t, map[string]string{"campaign.yaml": "name: x\nbase: dnd\n"})
		if _, err := Resolve(root); err == nil {
			t.Fatal("want error for missing base pack")
		}
	})
	t.Run("missing overlay", func(t *testing.T) {
		root := writeVault(t, map[string]string{"campaign.yaml": "name: x\noverlay: 5e-2024\n"})
		writePack(t, root, "rules/base/dnd", basePack)
		if _, err := Resolve(root); err == nil {
			t.Fatal("want error for missing overlay pack")
		}
	})
	t.Run("kind mismatch", func(t *testing.T) {
		root := writeVault(t, map[string]string{"campaign.yaml": "name: x\nbase: dnd\noverlay: 5e-2024\n"})
		writePack(t, root, "rules/base/dnd", basePack)
		writePack(t, root, "rules/overlay/5e-2024", basePack) // wrong kind + id
		if _, err := Resolve(root); err == nil {
			t.Fatal("want error for kind mismatch")
		}
	})
	t.Run("no ruleset selected", func(t *testing.T) {
		root := writeVault(t, map[string]string{"campaign.yaml": "name: x\n"})
		st, err := Resolve(root)
		if err != nil {
			t.Fatal(err)
		}
		if st.Base != nil || st.Overlay != nil {
			t.Fatalf("empty selection: %+v", st)
		}
	})
}

// copyTree replicates one directory tree (fixtures install step).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, fp)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(fp)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestResolveFixturesTree installs the shipped H1 timebox (fixtures/rules)
// as one copy under <vault>/rules and proves it resolves, scans, and
// toggles as a unit — the I1 handoff shape.
func TestResolveFixturesTree(t *testing.T) {
	src := filepath.Join("..", "..", "fixtures", "rules")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("fixtures tree absent: %v", err)
	}
	root := t.TempDir()
	copyTree(t, src, filepath.Join(root, "rules"))
	if err := os.WriteFile(filepath.Join(root, "campaign.yaml"), []byte(
		"name: demo\nbase: dnd\noverlay: 5e-2024\nenabled-features:\n  - second-wind-2024\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Base == nil || st.Base.ID != "dnd" {
		t.Fatalf("base: %+v", st.Base)
	}
	if st.Overlay == nil || st.Overlay.ID != "5e-2024" || st.Overlay.Parent != "dnd" {
		t.Fatalf("overlay: %+v", st.Overlay)
	}
	if len(st.Homebrew) != 1 || st.Homebrew[0].ID != "grit" ||
		st.Homebrew[0].Dir != "rules/homebrew/grit" {
		t.Fatalf("homebrew: %+v", st.Homebrew)
	}

	opts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, o := range opts {
		have[o.ID] = true
	}
	for _, want := range []string{"second-wind-2024", "weapon-mastery-2024", "grit-die"} {
		if !have[want] {
			t.Fatalf("timebox optional %q not offered (have %v)", want, opts)
		}
	}

	// campaign.example.yaml's enabled-features must all be offered (GM
	// toggles reject unknown ids, so a stale example would fail SetEnabled).
	example, err := os.ReadFile(filepath.Join(src, "campaign.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ex, err := Parse(example)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ex.EnabledFeatures {
		if !have[id] {
			t.Fatalf("example enables unoffered id %q", id)
		}
	}
	if err := SetEnabled(root, ex.EnabledFeatures); err != nil {
		t.Fatalf("example features do not toggle: %v", err)
	}

	// Overlay switch re-resolves with no rebuild step in between.
	if err := os.WriteFile(filepath.Join(root, "campaign.yaml"), []byte(
		"name: demo\nbase: dnd\noverlay: 5e-2014\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	switched, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Overlay == nil || switched.Overlay.ID != "5e-2014" {
		t.Fatalf("switched overlay: %+v", switched.Overlay)
	}
}

func TestHomebrewShadowDeterministic(t *testing.T) {
	// Table-local homebrew/ shadows an installed rules/homebrew/ pack with
	// the same id; resolution stays a single entry, deterministic.
	root := writeVault(t, map[string]string{"campaign.yaml": "name: x\n"})
	writePack(t, root, "rules/homebrew/grit", "id: grit\nname: Installed\ntype: homebrew\nversion: \"0.1\"\nparent: 5e-2024\n")
	writePack(t, root, "homebrew/grit", "id: grit\nname: Table Grit\ntype: homebrew\nversion: \"0.2\"\nparent: 5e-2024\n")
	st, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Homebrew) != 1 || st.Homebrew[0].Dir != "homebrew/grit" {
		t.Fatalf("shadow: %+v", st.Homebrew)
	}
}
