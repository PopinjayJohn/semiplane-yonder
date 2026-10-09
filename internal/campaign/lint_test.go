package campaign

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validBase = `id: dnd
name: Dungeons & Dragons (SRD 5.2)
type: base
version: "5.2"
attribution: "Systems Reference Document 5.2 under CC-BY-4.0 (Wizards of the Coast)."
stats:
  str:
    label: Strength
    type: int
    min: 1
    max: 30
    default: 10
derived:
  str-mod:
    label: Strength modifier
    formula: floor((str - 10) / 2)
    depends: [str]
rolls:
  attack:
    label: Attack roll
    dice: 1d20
    stat: str-mod
dice:
  types: [num]
  faces: 20
identity:
  pronouns: ""
  inspiration: false
intents:
  attack:
    label: Attack
    stats: [str]
hooks:
  - intent: attack
    phase: pre-roll
    label: flanking
content:
  - compendium/fighter.md
optionals:
  - id: flanking-2024
    title: Flanking
`

func lintVault(t *testing.T, packDir, desc string, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"campaign.yaml":              "name: x\n",
		packDir + "/pack.yaml":       desc,
		packDir + "/compendium/a.md": "Body.\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	return writeVault(t, files)
}

func TestValidatePackClean(t *testing.T) {
	root := lintVault(t, "rules/base/dnd", validBase, map[string]string{
		"rules/base/dnd/compendium/fighter.md": "Fighter.\n",
	})
	issues, err := ValidatePack(root, "rules/base/dnd")
	if err != nil {
		t.Fatal(err)
	}
	if HasErrors(issues) {
		t.Fatalf("issues: %+v", issues)
	}
}

func TestValidatePackErrors(t *testing.T) {
	mutate := func(body, from, to string) string { return strings.Replace(body, from, to, 1) }
	cases := map[string]struct {
		desc string
		want string
	}{
		"unknown top key":    {mutate(validBase, "attribution:", "bogus:"), `unknown key "bogus"`},
		"missing id":         {mutate(validBase, "id: dnd\n", ""), `missing required key "id"`},
		"missing type":       {mutate(validBase, "type: base\n", ""), `missing required key "type"`},
		"missing version":    {mutate(validBase, "version:", "ver:"), `missing required key "version"`},
		"bad kind":           {mutate(validBase, "type: base", "type: fork"), "base|overlay|homebrew"},
		"base with parent":   {mutate(validBase, "attribution:", "parent: dnd\nattribution:"), "must not declare a parent"},
		"bad stat field":     {mutate(validBase, "min: 1", "mn: 1"), `unknown key "mn"`},
		"derived no formula": {mutate(validBase, "formula: floor((str - 10) / 2)", "formul: x"), "requires a formula"},
		"bad hook phase":     {mutate(validBase, "phase: pre-roll", "phase: whenever"), "wants pre-roll|post-roll|interpret"},
		"hook bad intent":    {mutate(validBase, "intent: attack", "intent: smite"), "unknown intent"},
		"missing content":    {validBase, "missing"},
		"dup optional":       {mutate(validBase, "- id: flanking-2024", "- id: flanking-2024\n  - id: flanking-2024"), "duplicate optional id"},
		"optional no id":     {mutate(validBase, "- id: flanking-2024", "- title: No Id"), "requires an id"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			desc := tc.desc
			if name == "missing content" {
				desc = mutate(validBase, "  - compendium/fighter.md", "  - compendium/gone.md")
			}
			root := lintVault(t, "rules/base/dnd", desc, map[string]string{
				"rules/base/dnd/compendium/fighter.md": "Fighter.\n",
			})
			issues, err := ValidatePack(root, "rules/base/dnd")
			if err != nil {
				t.Fatal(err)
			}
			if !HasErrors(issues) {
				t.Fatalf("want errors, got %+v", issues)
			}
			found := false
			for _, i := range issues {
				if strings.Contains(i.Message, tc.want) {
					found = true
					if i.Line < 1 || i.File == "" {
						t.Fatalf("issue lacks file:line: %+v", i)
					}
				}
			}
			if !found {
				t.Fatalf("want %q in %+v", tc.want, issues)
			}
		})
	}
}

func TestValidateOverlayParentRule(t *testing.T) {
	root := lintVault(t, "rules/overlay/5e-2014",
		"id: 5e-2014\nname: O\ntype: overlay\nversion: \"1.0\"\n", nil)
	issues, err := ValidatePack(root, "rules/overlay/5e-2014")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range issues {
		if strings.Contains(i.Message, "require a parent id") {
			found = true
		}
	}
	if !found {
		t.Fatalf("overlay without parent must fail: %+v", issues)
	}
}

func TestValidateIDMatchesDir(t *testing.T) {
	root := lintVault(t, "rules/base/dnd", mutate(validBase, "id: dnd", "id: other"), map[string]string{
		"rules/base/dnd/compendium/fighter.md": "Fighter.\n",
	})
	_ = root
	issues, err := ValidatePack(root, "rules/base/dnd")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range issues {
		if strings.Contains(i.Message, "does not match directory") {
			found = true
		}
	}
	if !found {
		t.Fatalf("id/dir mismatch must fail: %+v", issues)
	}
}

func mutate(s, from, to string) string { return strings.Replace(s, from, to, 1) }

func TestValidateWarnsNeverBlock(t *testing.T) {
	// No attribution + Product-Identity term in content: warns only.
	desc := mutate(validBase, "attribution: \"Systems Reference Document 5.2 under CC-BY-4.0 (Wizards of the Coast).\"\n", "")
	root := lintVault(t, "rules/base/dnd", desc, map[string]string{
		"rules/base/dnd/compendium/fighter.md": "Beware the beholder's gaze.\n",
	})
	issues, err := ValidatePack(root, "rules/base/dnd")
	if err != nil {
		t.Fatal(err)
	}
	if HasErrors(issues) {
		t.Fatalf("warns must not error: %+v", issues)
	}
	if len(issues) != 2 {
		t.Fatalf("want attribution + identity warns, got %+v", issues)
	}
	for _, i := range issues {
		if i.Severity != "warn" {
			t.Fatalf("severity: %+v", i)
		}
	}
}

func TestValidateUnsafeContentPath(t *testing.T) {
	desc := mutate(validBase, "  - compendium/fighter.md", "  - ../escape.md")
	root := lintVault(t, "rules/base/dnd", desc, map[string]string{
		"rules/base/dnd/compendium/fighter.md": "Fighter.\n",
	})
	issues, err := ValidatePack(root, "rules/base/dnd")
	if err != nil {
		t.Fatal(err)
	}
	if !HasErrors(issues) {
		t.Fatalf("path escape must fail: %+v", issues)
	}
}

func TestValidateMissingDescriptor(t *testing.T) {
	root := writeVault(t, map[string]string{"campaign.yaml": "name: x\n"})
	if _, err := ValidatePack(root, "rules/base/dnd"); err == nil {
		t.Fatal("want error for missing pack dir")
	}
	empty := filepath.Join(root, "rules", "overlay", "oops")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePack(root, "rules/overlay/oops"); err == nil {
		t.Fatal("want error for missing pack.yaml")
	}
}

// TestTimeboxPacks lints the real demo vault packs: every shipped pack must
// be error-free (warnings allowed: attribution is present, so expect none).
func TestTimeboxPacks(t *testing.T) {
	for _, dir := range []string{
		"rules/base/dnd",
		"rules/overlay/5e-2014",
		"rules/overlay/5e-2024",
		"rules/homebrew/grit",
	} {
		t.Run(dir, func(t *testing.T) {
			issues, err := ValidatePack("../../fixtures/demo", dir)
			if err != nil {
				t.Fatal(err)
			}
			if HasErrors(issues) {
				t.Fatalf("fixture pack errors: %+v", issues)
			}
		})
	}
}
