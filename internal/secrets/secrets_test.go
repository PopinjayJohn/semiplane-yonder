package secrets

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/markdown"
)

var (
	gmViewer       = &auth.Viewer{UserID: "gm", IsGM: true}
	ownerViewer    = &auth.Viewer{UserID: "mira"}
	editableViewer = &auth.Viewer{UserID: "bram"}
	otherViewer    = &auth.Viewer{UserID: "cass"}
	guestViewer    *auth.Viewer // nil = guest
	previewViewer  = &auth.Viewer{UserID: "gm", IsGM: true, PreviewAs: "cass"}
)

func secretPage() *markdown.Page {
	return &markdown.Page{
		Path:       "cinder-pact.md",
		Title:      "The Cinder Pact",
		Secret:     true,
		Owner:      "mira",
		EditableBy: []string{"bram"},
		Blocks: []markdown.Block{
			{Type: "paragraph", Content: "Signed at midnight."},
			{Type: "secret", Content: "ember under the third flagstone", Secret: true},
			{Type: "secret", Content: "bearers dream of crows", Secret: false}, // `+`
		},
		TOC: []markdown.TOCEntry{
			{Level: 1, Title: "The Cinder Pact"},
			{Level: 2, Title: "Hidden cache", Secret: true},
		},
		Links:  []markdown.Link{{Target: "ashfall.md", Alias: "home"}},
		Embeds: []markdown.Embed{{Target: "map.png", Alt: "map"}},
		HTML: `<p>Signed at midnight.</p>` + "\n" +
			`<div class="callout callout-secret" data-callout="secret" data-fold="collapse">` + "\n" +
			`<p>ember under the third flagstone</p></div>` + "\n" +
			`<div class="callout callout-secret" data-callout="secret" data-fold="expand">` + "\n" +
			`<p>bearers dream of crows</p></div>` + "\n",
	}
}

func openPage() *markdown.Page {
	p := secretPage()
	p.Path = "welcome.md"
	p.Title = "Welcome"
	p.Secret = false
	return p
}

// TestLeakMatrix is the M1 read-half of the P10 leak matrix:
// GM / owner / editable-by-non-owner / other-player / guest(/revoked=nil)
// × secret/non-secret page.
func TestLeakMatrix(t *testing.T) {
	cases := []struct {
		name    string
		viewer  *auth.Viewer
		secret  bool
		wantOK  bool
		wantErr error
	}{
		{"gm secret", gmViewer, true, true, nil},
		{"gm open", gmViewer, false, true, nil},
		{"owner secret", ownerViewer, true, true, nil},
		{"owner open", ownerViewer, false, true, nil},
		{"editable secret", editableViewer, true, true, nil},
		{"editable open", editableViewer, false, true, nil},
		{"other secret", otherViewer, true, false, ErrNotFound},
		{"other open", otherViewer, false, true, nil},
		{"guest secret", guestViewer, true, false, ErrNotFound},
		{"guest open", guestViewer, false, true, nil},
		{"gm-preview-as-other secret", previewViewer, true, false, ErrNotFound},
		{"gm-preview-as-other open", previewViewer, false, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p *markdown.Page
			if tc.secret {
				p = secretPage()
			} else {
				p = openPage()
			}
			got, err := Filter(tc.viewer, p)
			if tc.wantOK && err != nil {
				t.Fatalf("Filter() err = %v, want nil", err)
			}
			if !tc.wantOK {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("Filter() err = %v, want ErrNotFound", err)
				}
				if got != nil {
					t.Fatalf("Filter() returned page on deny")
				}
				return
			}
			if got.Title != p.Title {
				t.Errorf("authorized viewer lost title: %q", got.Title)
			}
		})
	}
}

// TestBlockVisibility: `-` (Secret=true) visible to GM + page owner +
// editable-by holders (p03; gate-amended Phase 2);
// `+` (Secret=false) follows the page gate.
func TestBlockVisibility(t *testing.T) {
	minux := markdown.Block{Type: "secret", Content: "hidden", Secret: true}
	plus := markdown.Block{Type: "secret", Content: "shown", Secret: false}
	cases := []struct {
		name   string
		viewer *auth.Viewer
		block  markdown.Block
		want   bool
	}{
		{"gm minus", gmViewer, minux, true},
		{"gm plus", gmViewer, plus, true},
		{"owner minus", ownerViewer, minux, true},
		{"owner plus", ownerViewer, plus, true},
		{"editable minus", editableViewer, minux, true},
		{"editable plus", editableViewer, plus, true},
		{"other minus", otherViewer, minux, false},
		{"other plus", otherViewer, plus, true},
		{"guest minus", guestViewer, minux, false},
		{"guest plus", guestViewer, plus, true},
		{"preview minus", previewViewer, minux, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanViewBlock(tc.viewer, tc.block, "mira", []string{"bram"}); got != tc.want {
				t.Errorf("CanViewBlock() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterScrubsHiddenBlocks(t *testing.T) {
	p := openPage() // non-secret page carrying a `-` block
	got, err := Filter(otherViewer, p)
	if err != nil {
		t.Fatalf("Filter() err = %v", err)
	}
	if strings.Contains(got.HTML, "third flagstone") {
		t.Errorf("hidden block content leaked into HTML")
	}
	if !strings.Contains(got.HTML, RedactedBlockTag) {
		t.Errorf("hidden block placeholder box missing")
	}
	if !strings.Contains(got.HTML, "bearers dream of crows") {
		t.Errorf("`+` block must stay visible")
	}
	// Block list: `-` replaced by placeholder, never dropped silently.
	if len(got.Blocks) != 3 {
		t.Fatalf("len(Blocks) = %d, want 3 (placeholder in place)", len(got.Blocks))
	}
	if got.Blocks[1].Type != "secret-redacted" {
		t.Errorf("hidden block type = %q, want secret-redacted", got.Blocks[1].Type)
	}
	if got.Blocks[1].Content != "" {
		t.Errorf("placeholder block must carry no content")
	}
	// Secret TOC entry dropped for unauthorized viewer.
	for _, e := range got.TOC {
		if e.Secret {
			t.Errorf("secret TOC entry leaked: %q", e.Title)
		}
	}
	// Original untouched.
	if strings.Contains(p.HTML, RedactedBlockTag) {
		t.Errorf("Filter mutated the original page")
	}

	// Owner keeps everything, byte-identical HTML.
	own, err := Filter(ownerViewer, p)
	if err != nil {
		t.Fatalf("Filter(owner) err = %v", err)
	}
	if own.HTML != p.HTML {
		t.Errorf("owner HTML altered")
	}
	if own.Blocks[1].Content != "ember under the third flagstone" {
		t.Errorf("owner lost hidden block content")
	}
	if len(own.TOC) != 2 {
		t.Errorf("owner lost secret TOC entry")
	}
}

func TestFilterDefaultFoldHidden(t *testing.T) {
	// No data-fold attr (authored `> [!secret]` default) = hidden from party.
	p := openPage()
	p.HTML = `<div class="callout callout-secret" data-callout="secret">` + "\n" +
		`<p>default-fold secret</p></div>` + "\n"
	got, err := Filter(otherViewer, p)
	if err != nil {
		t.Fatalf("Filter() err = %v", err)
	}
	if strings.Contains(got.HTML, "default-fold secret") {
		t.Errorf("default-fold secret block leaked")
	}
}

func TestFilterSearchResult(t *testing.T) {
	open := &SearchResult{Path: "welcome.md", Title: "Welcome", Snippet: "hi", Secret: false}
	if got := FilterSearchResult(otherViewer, open); got != open {
		t.Errorf("non-secret result must pass through by pointer")
	}
	sec := &SearchResult{Path: "cinder-pact.md", Title: "The Cinder Pact", Snippet: "ember", Secret: true}
	if got := FilterSearchResult(gmViewer, sec); got != sec {
		t.Errorf("GM must see secret results unredacted")
	}
	for name, v := range map[string]*auth.Viewer{"owner": ownerViewer, "other": otherViewer, "guest": guestViewer, "preview": previewViewer} {
		got := FilterSearchResult(v, sec)
		if got == nil {
			t.Errorf("%s: must redact, not drop", name)
			continue
		}
		if got.Title != RedactedTitle || got.Path != "" {
			t.Errorf("%s: got title=%q path=%q, want redacted", name, got.Title, got.Path)
		}
		if strings.Contains(got.Snippet, "ember") {
			t.Errorf("%s: snippet leaked", name)
		}
	}
	if FilterSearchResult(gmViewer, nil) != nil {
		t.Errorf("nil must stay nil")
	}
}

func TestFilterNilPage(t *testing.T) {
	if _, err := Filter(gmViewer, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("nil page err = %v, want ErrNotFound", err)
	}
}

func TestCanViewPagePrimitives(t *testing.T) {
	if !CanViewPage(guestViewer, false, "a.md", "", nil) {
		t.Errorf("guest must see non-secret")
	}
	if CanViewPage(guestViewer, true, "a.md", "", nil) {
		t.Errorf("guest must not see secret")
	}
	if !CanViewPage(editableViewer, true, "cinder-pact.md", "mira", []string{"bram"}) {
		t.Errorf("editable-by must see secret page")
	}
	if CanViewPage(otherViewer, true, "cinder-pact.md", "mira", []string{"bram"}) {
		t.Errorf("other player must not see secret page")
	}
	// Case-insensitive owner/editable-by.
	if !CanViewPage(&auth.Viewer{UserID: "MIRA"}, true, "cinder-pact.md", "mira", nil) {
		t.Errorf("owner match must be case-insensitive")
	}
	// Quarantined pages fail closed upstream (Parse forces Secret=true);
	// an empty owner + secret page is GM-only.
	if CanViewPage(ownerViewer, true, "broken.md", "", nil) {
		t.Errorf("ownerless secret page must be GM-only")
	}
}
