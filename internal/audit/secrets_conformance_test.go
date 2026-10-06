// Conformance of the frozen secrets.* signatures against p03.
//
// Lane F1 owns internal/secrets (Filter + read handlers). This file asserts
// the p03 spec through the frozen signatures — GM/owner/editable-by read
// secret pages and `-` blocks; other-player/guest/revoked get uniform-404
// semantics (no titles, no paths, no snippets) — but SKIPS while F1's
// implementation is still the Phase-0 stub (Filter returns nil, nil).
//
// Integration delta: delete the secretsImplemented gate (keep the cases)
// once origin/lane/F1-read compiles; the same cases then run for real.
// NEVER copy F1's code into this branch to satisfy them.
package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/secrets"
)

const conformanceSecretMD = `---
title: Secret Plans
secret: true
owner: alice
editable-by:
  - bob
---

# Secret Plans

Public intro line.

> [!secret]- Hidden from the party
> The vault combination is seven seven seven.

> [!secret]+ Owner-visible aside
> Alice sees this expanded.
`

// secretsImplemented detects the Phase-0 stub without panicking on its
// nil, nil return. False => F1 has not landed; conformance skips.
func secretsImplemented(t *testing.T) bool {
	t.Helper()
	page, err := markdown.Parse(context.Background(), conformanceSecretMD, "secret.md")
	if err != nil || page == nil {
		t.Fatalf("fixture must parse: %v", err)
	}
	v := &auth.Viewer{UserID: "alice", OwnedSlugs: []string{"secret.md"}}
	got, err := secrets.Filter(v, page)
	return err == nil && got != nil
}

func conformancePage(t *testing.T) *markdown.Page {
	t.Helper()
	page, err := markdown.Parse(context.Background(), conformanceSecretMD, "secret.md")
	if err != nil || page == nil {
		t.Fatalf("fixture must parse: %v", err)
	}
	return page
}

// TestSecretsFilterConformance asserts p03 through secrets.Filter once F1
// implements it. Skipped until then (see secretsImplemented).
func TestSecretsFilterConformance(t *testing.T) {
	if !secretsImplemented(t) {
		t.Skip("F1 not implemented: secrets.Filter is still the Phase-0 stub; enable when origin/lane/F1-read compiles")
	}
	page := conformancePage(t)
	viewers := map[string]*auth.Viewer{
		"gm":      {UserID: "gm", IsGM: true},
		"owner":   {UserID: "alice", OwnedSlugs: []string{"secret.md"}},
		"grantee": {UserID: "bob"},
		"other":   {UserID: "carol"},
		"guest":   nil,
		"revoked": nil,
		"preview": {UserID: "gm", IsGM: true, PreviewAs: "carol"},
	}
	// Readers see the secret content; non-readers must see no secret text,
	// no secret title, and no secret blocks (exact carrier — error, nil, or
	// redacted copy — is F1's choice; leakage is not).
	for _, name := range []string{"gm", "owner", "grantee"} {
		got, err := secrets.Filter(viewers[name], page)
		if err != nil || got == nil {
			t.Fatalf("viewer=%s: legitimate read blocked (err=%v)", name, err)
		}
		if !strings.Contains(got.Content, "seven seven seven") && !strings.Contains(got.HTML, "seven seven seven") {
			t.Fatalf("viewer=%s: secret content missing after filter", name)
		}
	}
	for _, name := range []string{"other", "guest", "revoked", "preview"} {
		got, err := secrets.Filter(viewers[name], page)
		if err != nil || got == nil {
			continue // denied outright: uniform-404 compatible
		}
		flat := got.Title + "\x00" + got.Content + "\x00" + got.HTML
		for _, b := range got.Blocks {
			flat += "\x00" + b.Content
		}
		if strings.Contains(flat, "seven seven seven") || strings.Contains(flat, "Secret Plans") {
			t.Fatalf("viewer=%s: secret content/title leaked through Filter", name)
		}
	}
}

// TestSecretsHelpersConformance pins CanViewSecret / CanViewBlock /
// FilterBlocks / FilterSearchResult to p03 once implemented.
func TestSecretsHelpersConformance(t *testing.T) {
	if !secretsImplemented(t) {
		t.Skip("F1 not implemented: secrets.* are still Phase-0 stubs; enable when origin/lane/F1-read compiles")
	}
	page := conformancePage(t)
	owner := &auth.Viewer{UserID: "alice", OwnedSlugs: []string{"secret.md"}}
	grantee := &auth.Viewer{UserID: "bob"}
	other := &auth.Viewer{UserID: "carol"}
	gm := &auth.Viewer{UserID: "gm", IsGM: true}

	if !secrets.CanViewSecret(gm, page) || !secrets.CanViewSecret(owner, page) {
		t.Fatalf("gm/owner must view secret page")
	}
	if !secrets.CanViewSecret(grantee, page) {
		t.Fatalf("editable-by holder must view secret page (p03)")
	}
	if secrets.CanViewSecret(other, page) || secrets.CanViewSecret(nil, page) {
		t.Fatalf("other-player/guest must not view secret page")
	}

	var minus, plus markdown.Block
	for _, b := range page.Blocks {
		switch {
		case b.Type == "secret" && b.Secret:
			minus = b
		case b.Type == "secret" && !b.Secret:
			plus = b
		}
	}
	if minus.Content == "" {
		t.Fatalf("fixture lost its `-` block")
	}
	// p03: `-` visible to GM + page owner/editable-by, hidden from party.
	for _, v := range []*auth.Viewer{gm, owner, grantee} {
		if !secrets.CanViewBlock(v, minus, "alice") {
			t.Fatalf("viewer=%s must see `-` block", v.UserID)
		}
	}
	if secrets.CanViewBlock(other, minus, "alice") || secrets.CanViewBlock(nil, minus, "alice") {
		t.Fatalf("`-` block leaked to party/guest")
	}
	_ = plus

	filtered, err := secrets.FilterBlocks(nil, page.Blocks)
	if err != nil {
		t.Fatalf("FilterBlocks: %v", err)
	}
	for _, b := range filtered {
		if strings.Contains(b.Content, "seven seven seven") {
			t.Fatalf("FilterBlocks leaked `-` content to guest")
		}
	}

	hit := &secrets.SearchResult{Path: "secret.md", Title: "Secret Plans", Snippet: "seven seven seven", Score: 1, Secret: true}
	redacted := secrets.FilterSearchResult(nil, hit)
	if redacted == nil {
		return // dropped entirely: acceptable (store.Search semantics)
	}
	if strings.Contains(redacted.Title, "Secret Plans") || strings.Contains(redacted.Path, "secret") {
		t.Fatalf("FilterSearchResult leaked title/path to guest: %+v", redacted)
	}
	if strings.Contains(redacted.Snippet, "seven seven seven") {
		t.Fatalf("FilterSearchResult leaked snippet to guest")
	}
}
