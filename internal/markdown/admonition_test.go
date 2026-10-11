package markdown

import (
	"context"
	"strings"
	"testing"
)

func parseAD(t *testing.T, src, path string) *Page {
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

func adBlocks(p *Page) []Block {
	out := []Block{}
	for _, b := range p.Blocks {
		if b.Type == "callout" {
			out = append(out, b)
		}
	}
	return out
}

// TestCalloutTitleRow pins the S3 polish: the title leaves the body for a
// dedicated title row with a per-type icon; the body holds only body text.
func TestCalloutTitleRow(t *testing.T) {
	p := parseAD(t, "> [!warning] Watch out\n> body text\n", "c.md")
	for _, want := range []string{
		`<div class="callout callout-warning" data-callout="warning">`,
		`<div class="callout-title">`,
		`<svg class="callout-icon"`,
		`<span class="callout-title-text">Watch out</span>`,
		`<div class="callout-body">`,
		`<p>body text</p>`,
	} {
		if !strings.Contains(p.HTML, want) {
			t.Errorf("HTML missing %q:\n%s", want, p.HTML)
		}
	}
	if strings.Count(p.HTML, "Watch out") != 1 {
		t.Errorf("title must render exactly once (row, not body):\n%s", p.HTML)
	}
}

// TestCalloutDefaultTitle: no explicit title renders the capitalized type.
func TestCalloutDefaultTitle(t *testing.T) {
	p := parseAD(t, "> [!tip]\n> body\n", "c.md")
	if !strings.Contains(p.HTML, `<span class="callout-title-text">Tip</span>`) {
		t.Errorf("default title missing:\n%s", p.HTML)
	}
}

// TestCalloutFoldDetails pins -/+ fold as <details>/<summary> with the
// frozen data-fold vocabulary untouched (the secret scrub reads it).
func TestCalloutFoldDetails(t *testing.T) {
	closed := parseAD(t, "> [!note]-\n> body\n", "c.md")
	for _, want := range []string{`data-fold="collapse"`, "<details>\n", `<summary class="callout-title">`} {
		if !strings.Contains(closed.HTML, want) {
			t.Errorf("closed: HTML missing %q:\n%s", want, closed.HTML)
		}
	}
	if strings.Contains(closed.HTML, "<details open>") {
		t.Errorf("closed must not be open:\n%s", closed.HTML)
	}
	open := parseAD(t, "> [!note]+\n> body\n", "c.md")
	for _, want := range []string{`data-fold="expand"`, "<details open>"} {
		if !strings.Contains(open.HTML, want) {
			t.Errorf("open: HTML missing %q:\n%s", want, open.HTML)
		}
	}
	plain := parseAD(t, "> [!note] Title\n> body\n", "c.md")
	if strings.Contains(plain.HTML, "<details>") || strings.Contains(plain.HTML, "data-fold") {
		t.Errorf("unfolded callout must stay static:\n%s", plain.HTML)
	}
}

// TestCalloutAliasNormalization: admonition aliases fold to canonical types.
func TestCalloutAliasNormalization(t *testing.T) {
	p := parseAD(t, "> [!tldr] short\n> body\n", "c.md")
	if !strings.Contains(p.HTML, `class="callout callout-abstract" data-callout="abstract"`) {
		t.Errorf("tldr must normalize to abstract:\n%s", p.HTML)
	}
}

// TestCalloutStructuredTitle: a wikilink in the title keeps working — the
// title row renders the link (redaction-shaped anchor) and nothing crashes.
func TestCalloutStructuredTitle(t *testing.T) {
	p := parseAD(t, "> [!tip] See [[cinder-pact|the pact]]\n> body\n", "c.md")
	want := `<a class="wikilink" data-target="cinder-pact" href="cinder-pact">the pact</a>`
	if !strings.Contains(p.HTML, want) {
		t.Errorf("title link anchor missing:\n%s", p.HTML)
	}
	found := false
	for _, l := range p.Links {
		if l.Target == "cinder-pact" {
			found = true
		}
	}
	if !found {
		t.Errorf("title wikilink not extracted: %+v", p.Links)
	}
}

// TestAdParams pins the five header params through the shared component.
func TestAdParams(t *testing.T) {
	src := "```ad-tip\ntitle: Custom **bold**\ncollapse: open\nicon: warning\ncolor: 255, 0, 0\nmetadata: foo\n\nBody text.\n```\n"
	p := parseAD(t, src, "a.md")
	for _, want := range []string{
		`<div class="callout callout-tip" data-callout="tip" data-fold="expand" data-callout-metadata="foo" style="--callout-accent: 255, 0, 0;">`,
		"<details open>",
		`Custom <strong>bold</strong>`,
		`<p>Body text.</p>`,
		`M8 2.3 14.2 13.2H1.8z`, // warning icon override, not the tip bulb
	} {
		if !strings.Contains(p.HTML, want) {
			t.Errorf("HTML missing %q:\n%s", want, p.HTML)
		}
	}
	if strings.Contains(p.HTML, "<pre><code") {
		t.Errorf("ad- fence leaked through as code:\n%s", p.HTML)
	}
	ads := adBlocks(p)
	if len(ads) != 1 {
		t.Fatalf("callout blocks = %d, want 1", len(ads))
	}
	if ads[0].Secret {
		t.Errorf("top-level ad- block must not be secret")
	}
	if !strings.Contains(ads[0].Content, "Body text.") {
		t.Errorf("index text missing body: %q", ads[0].Content)
	}
}

// TestAdCollapseNone: collapse:none (and garbage values) render statically.
func TestAdCollapseNone(t *testing.T) {
	for _, collapse := range []string{"none", "bogus", ""} {
		src := "```ad-note\ntitle: T\ncollapse: " + collapse + "\n\nBody.\n```\n"
		if collapse == "" {
			src = "```ad-note\ntitle: T\n\nBody.\n```\n"
		}
		p := parseAD(t, src, "a.md")
		if strings.Contains(p.HTML, "<details>") || strings.Contains(p.HTML, "data-fold") {
			t.Errorf("collapse=%q must render statically:\n%s", collapse, p.HTML)
		}
	}
	closed := parseAD(t, "```ad-note\ntitle: T\ncollapse: closed\n\nBody.\n```\n", "a.md")
	if !strings.Contains(closed.HTML, `data-fold="collapse"`) || !strings.Contains(closed.HTML, "<details>\n") {
		t.Errorf("collapse=closed wrong:\n%s", closed.HTML)
	}
}

// TestAdUnknownType: custom slugs render generically by slug — labeled box,
// default capitalized title, fallback icon. Never a crash.
func TestAdUnknownType(t *testing.T) {
	p := parseAD(t, "```ad-frobnicate\nJust a body.\n```\n", "a.md")
	for _, want := range []string{
		`class="callout callout-frobnicate" data-callout="frobnicate"`,
		`<span class="callout-title-text">Frobnicate</span>`,
		`<p>Just a body.</p>`,
	} {
		if !strings.Contains(p.HTML, want) {
			t.Errorf("HTML missing %q:\n%s", want, p.HTML)
		}
	}
	if len(adBlocks(p)) != 1 {
		t.Errorf("want one callout block, got %+v", p.Blocks)
	}
}

// TestAdSlugSanitization: hostile info words cannot break attributes.
func TestAdSlugSanitization(t *testing.T) {
	p := parseAD(t, "```AD-Weird_Type!\"x\nBody.\n```\n", "a.md")
	if !strings.Contains(p.HTML, `class="callout callout-weird-type-x"`) {
		t.Errorf("slug not sanitized:\n%s", p.HTML)
	}
	if strings.Contains(p.HTML, `"x`) {
		t.Errorf("raw slug chars leaked into HTML:\n%s", p.HTML)
	}
}

// TestAdBlankTitle: `title:` present but blank omits the title row.
func TestAdBlankTitle(t *testing.T) {
	p := parseAD(t, "```ad-note\ntitle:\n\nBody only.\n```\n", "a.md")
	if strings.Contains(p.HTML, "callout-title-text") || strings.Contains(p.HTML, "<div class=\"callout-title\">") {
		t.Errorf("blank title must omit the title row:\n%s", p.HTML)
	}
	if !strings.Contains(p.HTML, "<p>Body only.</p>") {
		t.Errorf("body missing:\n%s", p.HTML)
	}
	// Foldable + blank title still gets a labelled icon-only summary.
	f := parseAD(t, "```ad-note\ntitle:\ncollapse: closed\n\nBody.\n```\n", "a.md")
	if !strings.Contains(f.HTML, `aria-label="Note (expand)"`) {
		t.Errorf("blank foldable summary needs a label:\n%s", f.HTML)
	}
}

// TestAdHeaderParsing: unknown keys are body text (never eaten), params end
// at the first body line, one blank line separates.
func TestAdHeaderParsing(t *testing.T) {
	p := parseAD(t, "```ad-note\ntitle: T\nbogus-key: kept\n\nBody.\n```\n", "a.md")
	if !strings.Contains(p.HTML, "bogus-key: kept") {
		t.Errorf("unknown key must survive as body text:\n%s", p.HTML)
	}
	if strings.Contains(p.HTML, "data-callout-metadata") {
		t.Errorf("unknown key must not become metadata:\n%s", p.HTML)
	}
	q := parseAD(t, "```ad-note\ntitle: T\nBody starts.\ncolor: 1, 2, 3\n```\n", "a.md")
	if strings.Contains(q.HTML, "--callout-accent") {
		t.Errorf("param after body start must stay body text:\n%s", q.HTML)
	}
	if !strings.Contains(q.HTML, "Body starts.") || !strings.Contains(q.HTML, "color: 1, 2, 3") {
		t.Errorf("body lines missing:\n%s", q.HTML)
	}
	// Invalid color triads are ignored, never crash.
	bad := parseAD(t, "```ad-note\ntitle: T\ncolor: 999, -1, banana\n\nBody.\n```\n", "a.md")
	if strings.Contains(bad.HTML, "--callout-accent") {
		t.Errorf("invalid color must be ignored:\n%s", bad.HTML)
	}
}

// TestAdEmptyBody: params without a body render an empty component.
func TestAdEmptyBody(t *testing.T) {
	p := parseAD(t, "```ad-tip\ntitle: T\ncollapse: none\n```\n", "a.md")
	if !strings.Contains(p.HTML, `data-callout="tip"`) {
		t.Errorf("empty ad- body must still render the box:\n%s", p.HTML)
	}
	if len(adBlocks(p)) != 1 {
		t.Errorf("want one callout block, got %+v", p.Blocks)
	}
}

// TestAdNeverCrashes feeds hostile fence bodies at Parse level.
func TestAdNeverCrashes(t *testing.T) {
	bodies := []string{
		"", "\n\n\n", "::: \x00 \xff\n", "title: [unclosed\n",
		"collapse: OPEN\n", "color: 1,2,3,4\n", "title: a\ntitle: b\n",
		"metadata: <script>alert(1)</script>\n", "icon: ../../evil\n",
		"> [!secret]- nested\n> hidden\n", "```\n",
		"# Heading in ad\n\n- list\n- items\n",
		"![[pic.png]] and [[page|alias]] and #tag\n",
	}
	for i, body := range bodies {
		p := parseAD(t, "```ad-note\n"+body+"```\n", "c.md")
		if !strings.Contains(p.HTML, `class="callout callout-note"`) {
			t.Errorf("case %d: no callout box:\n%s", i, p.HTML)
		}
		if len(adBlocks(p)) != 1 {
			t.Errorf("case %d: want one callout block", i)
		}
		if strings.Contains(p.HTML, "<script>alert(1)") {
			t.Errorf("case %d: metadata not escaped", i)
		}
	}
}

// TestAdInSecretCallout is the first leak-matrix case: an ad- block inside
// `> [!secret]-` nests inside the secret div (depth-counted scrub covers
// it) and its block carries Secret=true.
func TestAdInSecretCallout(t *testing.T) {
	src := "> [!secret]-\n> ```ad-warning\n> title: Hidden ad\n> secret ad body\n> ```\n"
	p := parseAD(t, src, "s.md")
	secDiv := strings.Index(p.HTML, `class="callout callout-secret"`)
	secAD := strings.Index(p.HTML, `data-callout="warning"`)
	if secDiv < 0 || secAD < 0 || secAD < secDiv {
		t.Fatalf("ad- block must nest inside the secret div:\n%s", p.HTML)
	}
	ads := adBlocks(p)
	if len(ads) != 1 {
		t.Fatalf("callout blocks = %d, want 1", len(ads))
	}
	if !ads[0].Secret {
		t.Errorf("ad- block in secret callout must carry Secret=true")
	}
	if !strings.Contains(ads[0].Content, "secret ad body") {
		t.Errorf("index text missing body: %q", ads[0].Content)
	}
}

// TestAdSecretLinkInBody is the second leak-matrix case: a secret-target
// link in an ad- body emits a secret-flagged Link and a redaction-ready
// anchor (the read path redacts by data-target).
func TestAdSecretLinkInBody(t *testing.T) {
	src := "> [!secret]-\n> ```ad-note\n> title: T\n> It knows [[cinder-pact|the pact]].\n> ```\n"
	p := parseAD(t, src, "s.md")
	found := false
	for _, l := range p.Links {
		if l.Target == "cinder-pact" && l.Alias == "the pact" {
			found = true
			if !l.Secret {
				t.Errorf("ad- body link in secret callout must carry Secret=true")
			}
		}
	}
	if !found {
		t.Fatalf("ad- body wikilink not extracted: %+v", p.Links)
	}
	want := `<a class="wikilink" data-target="cinder-pact" href="cinder-pact">the pact</a>`
	if !strings.Contains(p.HTML, want) {
		t.Errorf("HTML missing redactable anchor %q:\n%s", want, p.HTML)
	}
}

// TestAdTitleLink: a wikilink in an ad- title is extracted for the graph
// and renders redaction-ready in the title row.
func TestAdTitleLink(t *testing.T) {
	p := parseAD(t, "```ad-tip\ntitle: See [[hideout|HQ]]\n\nBody.\n```\n", "a.md")
	found := false
	for _, l := range p.Links {
		if l.Target == "hideout" && l.Alias == "HQ" {
			found = true
			if l.Secret {
				t.Errorf("top-level ad- title link must not be secret")
			}
		}
	}
	if !found {
		t.Fatalf("ad- title wikilink not extracted: %+v", p.Links)
	}
	want := `<a class="wikilink" data-target="hideout" href="hideout">HQ</a>`
	if !strings.Contains(p.HTML, want) {
		t.Errorf("title row missing redactable anchor %q:\n%s", want, p.HTML)
	}
}
