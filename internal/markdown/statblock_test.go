package markdown

import (
	"context"
	"strings"
	"testing"
)

func parseSB(t *testing.T, src, path string) *Page {
	t.Helper()
	p, err := Parse(context.Background(), src, path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Content != src {
		t.Fatalf("Content mutated by Parse")
	}
	return p
}

func statBlocks(p *Page) []Block {
	out := []Block{}
	for _, b := range p.Blocks {
		if b.Type == "statblock" {
			out = append(out, b)
		}
	}
	return out
}

// TestStatblockBasic pins the S1 happy path: full field set renders the
// semantic section + ability table, and the extractor emits one block.
func TestStatblockBasic(t *testing.T) {
	src := "# Goblin\n\n```statblock\nname: Goblin\nsize: Small\ntype: humanoid\nac: 15\n" +
		"hp: 7 (2d6)\nspeed: 30 ft.\nstats: [8, 14, 10, 10, 8, 8]\n" +
		"traits:\n  - name: Nimble Escape\n    desc: Hides well.\nactions:\n  - name: Scimitar\n    desc: +4 to hit.\n```\n"
	p := parseSB(t, src, "goblin.md")
	for _, want := range []string{
		`<section class="statblock" aria-label="Statblock: Goblin">`,
		`<h2 class="statblock-name">Goblin</h2>`,
		`<table class="statblock-abilities">`,
		`<caption>Ability scores</caption>`,
		`<th scope="col">STR</th>`,
		`<td>8 (-1)</td>`,
		`<td>14 (+2)</td>`,
		`<h3 class="statblock-group">Traits</h3>`,
		`<strong>Nimble Escape.</strong>`,
		`<h3 class="statblock-group">Actions</h3>`,
		`</section>`,
	} {
		if !strings.Contains(p.HTML, want) {
			t.Errorf("HTML missing %q\n%s", want, p.HTML)
		}
	}
	if strings.Contains(p.HTML, "<pre><code") {
		t.Errorf("statblock fence leaked through as code:\n%s", p.HTML)
	}
	sbs := statBlocks(p)
	if len(sbs) != 1 {
		t.Fatalf("statblock blocks = %d, want 1", len(sbs))
	}
	if sbs[0].Secret {
		t.Errorf("top-level statblock must not be secret")
	}
	for _, want := range []string{"Goblin", "Nimble Escape", "Scimitar"} {
		if !strings.Contains(sbs[0].Content, want) {
			t.Errorf("index text missing %q: %q", want, sbs[0].Content)
		}
	}
}

// TestStatblockMalformedFallback: bad YAML renders the labeled box with
// raw text — never a crash, never a source rewrite.
func TestStatblockMalformedFallback(t *testing.T) {
	for _, body := range []string{
		"name: [unclosed\n",
		"\tindented: tab\n",
		"*alias: nope\n",
		"- just a list\n",
		"plain prose, no keys\n",
	} {
		p := parseSB(t, "```statblock\n"+body+"```\n", "m.md")
		if !strings.Contains(p.HTML, `class="statblock statblock-error"`) {
			t.Errorf("body %q: missing fallback box:\n%s", body, p.HTML)
		}
		if !strings.Contains(p.HTML, "could not be parsed") {
			t.Errorf("body %q: missing fallback label", body)
		}
		sbs := statBlocks(p)
		if len(sbs) != 1 {
			t.Fatalf("body %q: statblock blocks = %d, want 1", body, len(sbs))
		}
	}
}

// TestStatblockEmptyFence: an empty body is a valid empty box, not a crash.
func TestStatblockEmptyFence(t *testing.T) {
	p := parseSB(t, "```statblock\n```\n", "e.md")
	if !strings.Contains(p.HTML, `aria-label="Statblock: Unnamed creature"`) {
		t.Errorf("empty statblock missing default name:\n%s", p.HTML)
	}
	if len(statBlocks(p)) != 1 {
		t.Errorf("want one statblock block")
	}
}

// TestStatblockInSecretCallout is the leak-matrix case: a statblock inside
// `> [!secret]-` nests inside the secret div (depth-counted scrub covers
// it) and its block carries Secret=true.
func TestStatblockInSecretCallout(t *testing.T) {
	src := "> [!secret]-\n> ```statblock\n> name: Hidden Horror\n> ac: 12\n> ```\n"
	p := parseSB(t, src, "s.md")
	secDiv := strings.Index(p.HTML, `class="callout callout-secret"`)
	secSB := strings.Index(p.HTML, `<section class="statblock"`)
	if secDiv < 0 || secSB < 0 || secSB < secDiv {
		t.Fatalf("statblock must nest inside the secret div:\n%s", p.HTML)
	}
	sbs := statBlocks(p)
	if len(sbs) != 1 {
		t.Fatalf("statblock blocks = %d, want 1", len(sbs))
	}
	if !sbs[0].Secret {
		t.Errorf("statblock in secret callout must carry Secret=true")
	}
	if !strings.Contains(sbs[0].Content, "Hidden Horror") {
		t.Errorf("index text missing name: %q", sbs[0].Content)
	}
}

// TestStatblockDescLinks is the second leak-matrix case: a secret link
// target in a desc emits a secret-flagged Link and a redaction-ready
// anchor (the read path redacts by data-target).
func TestStatblockDescLinks(t *testing.T) {
	src := "> [!secret]-\n> ```statblock\n> name: X\n> traits:\n>   - name: Whisper\n>     desc: It knows [[cinder-pact|the pact]].\n> ```\n"
	p := parseSB(t, src, "s.md")
	found := false
	for _, l := range p.Links {
		if l.Target == "cinder-pact" && l.Alias == "the pact" {
			found = true
			if !l.Secret {
				t.Errorf("desc link in secret callout must carry Secret=true")
			}
		}
	}
	if !found {
		t.Fatalf("desc wikilink not extracted: %+v", p.Links)
	}
	want := `<a class="wikilink" data-target="cinder-pact" href="cinder-pact">the pact</a>`
	if !strings.Contains(p.HTML, want) {
		t.Errorf("HTML missing redactable anchor %q:\n%s", want, p.HTML)
	}
}

// TestStatblockLayoutMonster: layout echoes as data-layout, monster is a
// note only (no lookup, no extra block).
func TestStatblockLayoutMonster(t *testing.T) {
	src := "```statblock\nname: Ooze\nlayout: Wide Frame\nmonster: Black Pudding\n```\n"
	p := parseSB(t, src, "o.md")
	if !strings.Contains(p.HTML, `data-layout="wide-frame"`) {
		t.Errorf("layout not echoed as data-layout:\n%s", p.HTML)
	}
	if !strings.Contains(p.HTML, "Black Pudding") || !strings.Contains(p.HTML, "not resolved in v1") {
		t.Errorf("monster note missing:\n%s", p.HTML)
	}
	if len(p.Blocks) != 1 || p.Blocks[0].Type != "statblock" {
		t.Errorf("want exactly one statblock block, got %+v", p.Blocks)
	}
}

// TestStatblockImage: `image: [[...]]` renders the ACL-handler shape and
// emits an embed edge (never a raw static path).
func TestStatblockImage(t *testing.T) {
	src := "```statblock\nname: Imp\nimage: [[imp.png|A grinning imp]]\n```\n"
	p := parseSB(t, src, "i.md")
	if !strings.Contains(p.HTML, `<img class="embed" src="imp.png" alt="A grinning imp" />`) {
		t.Errorf("image markup missing:\n%s", p.HTML)
	}
	if len(p.Embeds) != 1 || p.Embeds[0].Target != "imp.png" {
		t.Errorf("want one imp.png embed, got %+v", p.Embeds)
	}
}

// TestStatblockStatsNotSix: a non-six stats list drops the table but
// still renders the rest of the box.
func TestStatblockStatsNotSix(t *testing.T) {
	p := parseSB(t, "```statblock\nname: Blob\nac: 7\nstats: [10, 10]\n```\n", "b.md")
	if strings.Contains(p.HTML, "<table") {
		t.Errorf("non-six stats must not render a table:\n%s", p.HTML)
	}
	if !strings.Contains(p.HTML, "Blob") || !strings.Contains(p.HTML, "Armor Class") {
		t.Errorf("rest of box missing:\n%s", p.HTML)
	}
}

// TestStatblockNeverCrashes feeds hostile fence bodies at Parse level.
func TestStatblockNeverCrashes(t *testing.T) {
	bodies := []string{
		"", "\n\n\n", "::: \x00 \xff\n", "stats: [1,2,3,4,5,6,7]\n",
		"stats: notalist\n", "saves: {STR: +5}\nsaves: [a, b]\n",
		"name: \"unterminated\n", "a:\n  b:\n    c:\n      d: deep\n",
		"traits:\n  - name: x\n    desc: \"with: colon\"\n",
		"desc: तलवार [[x]]\n",
	}
	for i, body := range bodies {
		p := parseSB(t, "```statblock\n"+body+"```\n", "c.md")
		if !strings.Contains(p.HTML, `class="statblock`) {
			t.Errorf("case %d: no statblock section:\n%s", i, p.HTML)
		}
		if len(statBlocks(p)) != 1 {
			t.Errorf("case %d: want one statblock block", i)
		}
	}
}

// TestStatblockAbilityMods pins modifier math (floor division).
func TestStatblockAbilityMods(t *testing.T) {
	for in, want := range map[string]string{
		"8": "-1", "9": "-1", "10": "+0", "11": "+0",
		"14": "+2", "15": "+2", "20": "+5", "1": "-5", "abc": "",
	} {
		if got := abilityMod(in); got != want {
			t.Errorf("abilityMod(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStatblockDuplicateKeysDeterministic pins the FuzzParse `unstable
// render` fix: keys that collide after lenient normalization (`name` vs
// `Name`, `hit-dice` vs `hit_dice`, trait `name` vs `NAME`, save `MOD` vs
// `mod`) used to resolve via Go map iteration (random per parse), so the
// same input rendered different HTML on re-parse. Winners are now
// deterministic (keys iterate in byte order, last wins), and repeated
// parses agree exactly.
func TestStatblockDuplicateKeysDeterministic(t *testing.T) {
	inputs := []struct {
		body string
		want string // deterministic winner pinned in HTML
	}{
		{"name: Goblin\nName: Orc\n", "Goblin"},
		{"ac: 10\nAC: 15\n", "Armor Class</strong> 10"},
		{"hit-dice: 1d8\nhit_dice: 2d10\n", "Hit Dice</strong> 2d10"},
		{"traits:\n- name: A\n  NAME: B\n", "<strong>A.</strong>"},
		{"saves:\n- ability: STR\n  MOD: 5\n  mod: 7\n", "STR +7"},
	}
	for _, in := range inputs {
		src := "```statblock\n" + in.body + "```\n"
		var first string
		for i := 0; i < 100; i++ {
			p := parseSB(t, src, "dup.md")
			if i == 0 {
				first = p.HTML
				if !strings.Contains(first, in.want) {
					t.Errorf("body %q: HTML missing deterministic winner %q:\n%s", in.body, in.want, first)
				}
				continue
			}
			if p.HTML != first {
				t.Fatalf("body %q: unstable render on iteration %d", in.body, i)
			}
		}
	}
}
