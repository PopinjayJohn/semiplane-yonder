// Conformance of the real secrets.* implementation against the
// gate-amended p03 (WIDEN): editable-by-non-owner SEES `-` blocks (page,
// snippet, embed, TOC, HTML scrub paths); path-grant holders (NOT
// editable-by) read the page but NOT `-` blocks; other-player/guest/revoked
// get ErrNotFound (uniform 404 upstream), never titles/paths/snippets.
//
// Cells asserting WIDEN are marked; where the merged product code still
// denies editable-by, the cell is RED — the finding, owned by the amend
// implementers. This lane carries no product code (red line).
//
// Signature note: the integration tree keeps the original
// CanViewBlock(viewer, block, pageOwner) / FilterBlocks(viewer, blocks,
// pageOwner) / ChunkVisible(secret, owner, viewer) shapes — the brief's
// amended editableBy parameters do not exist in the tree. Tests call the
// real signatures; threading page editable-by through the block paths is
// part of the pending amend work.
package audit

import (
	"context"
	"errors"
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

func conformanceViewers() map[string]*auth.Viewer {
	return map[string]*auth.Viewer{
		"gm":      {UserID: "gm", IsGM: true},
		"owner":   {UserID: "alice", OwnedSlugs: []string{"secret.md"}},
		"grantee": {UserID: "bob"},
		// pathgrant is scoped to secret.md without being editable-by.
		"pathgrant": {UserID: "dave", Grants: []string{"secret.md"}},
		"other":     {UserID: "carol"},
		"guest":     nil,
		"revoked":   nil,
		"preview":   {UserID: "gm", IsGM: true, PreviewAs: "carol"},
	}
}

func conformancePage(t *testing.T) *markdown.Page {
	t.Helper()
	page, err := markdown.Parse(context.Background(), conformanceSecretMD, "secret.md")
	if err != nil || page == nil {
		t.Fatalf("fixture must parse: %v", err)
	}
	return page
}

func blockTexts(blocks []markdown.Block) string {
	var b strings.Builder
	for _, blk := range blocks {
		b.WriteString("\x00" + blk.Type + "\x00" + blk.Content)
	}
	return b.String()
}

// TestSecretsFilterConformance asserts p03 through secrets.Filter.
func TestSecretsFilterConformance(t *testing.T) {
	page := conformancePage(t)
	viewers := conformanceViewers()

	// GM + owner read everything, `-` included.
	for _, name := range []string{"gm", "owner"} {
		got, err := secrets.Filter(viewers[name], page)
		if err != nil || got == nil {
			t.Fatalf("viewer=%s: legitimate read blocked (err=%v)", name, err)
		}
		if !strings.Contains(blockTexts(got.Blocks), "seven seven seven") {
			t.Fatalf("viewer=%s: `-` block missing after filter", name)
		}
		if !strings.Contains(got.HTML, "seven seven seven") {
			t.Fatalf("viewer=%s: `-` HTML missing after filter", name)
		}
	}

	// WIDEN: editable-by reads `-` through every render path.
	// RED until the amend lands in secrets.Filter's block paths.
	got, err := secrets.Filter(viewers["grantee"], page)
	if err != nil || got == nil {
		t.Fatalf("grantee: page gate blocked editable-by holder (err=%v)", err)
	}
	if !strings.Contains(blockTexts(got.Blocks), "seven seven seven") {
		t.Errorf("grantee WIDEN: `-` block withheld from editable-by holder")
	}
	if !strings.Contains(got.HTML, "seven seven seven") {
		t.Errorf("grantee WIDEN: `-` HTML scrubbed for editable-by holder")
	}

	// Path-grant holder reads the page but NOT `-` blocks.
	got, err = secrets.Filter(viewers["pathgrant"], page)
	if err != nil || got == nil {
		t.Fatalf("pathgrant: page gate blocked grant holder (err=%v)", err)
	}
	if strings.Contains(blockTexts(got.Blocks), "seven seven seven") {
		t.Fatalf("pathgrant: `-` block leaked to grant holder")
	}

	// Non-readers get ErrNotFound (uniform 404 upstream), never content.
	for _, name := range []string{"other", "guest", "revoked", "preview"} {
		got, err := secrets.Filter(viewers[name], page)
		if !errors.Is(err, secrets.ErrNotFound) || got != nil {
			t.Fatalf("viewer=%s: want ErrNotFound+nil, got page=%v err=%v", name, got != nil, err)
		}
	}

	// Original page is never mutated by filtering.
	if !strings.Contains(blockTexts(page.Blocks), "seven seven seven") {
		t.Fatalf("Filter mutated the source page")
	}
}

// TestSecretsHelpersConformance pins CanViewPage / CanViewSecret /
// CanViewBlock / FilterBlocks / FilterSearchResult to amended p03.
func TestSecretsHelpersConformance(t *testing.T) {
	page := conformancePage(t)
	viewers := conformanceViewers()
	gm, owner, grantee := viewers["gm"], viewers["owner"], viewers["grantee"]
	pathgrant, other := viewers["pathgrant"], viewers["other"]

	if !secrets.CanViewSecret(gm, page) || !secrets.CanViewSecret(owner, page) {
		t.Fatalf("gm/owner must view secret page")
	}
	if !secrets.CanViewSecret(grantee, page) {
		t.Fatalf("editable-by holder must view secret page (p03 page gate)")
	}
	if secrets.CanViewSecret(other, page) || secrets.CanViewSecret(nil, page) {
		t.Fatalf("other-player/guest must not view secret page")
	}
	// Path grants open the page gate without editable-by membership.
	if !secrets.CanViewPage(pathgrant, true, "secret.md", "alice", []string{"bob"}) {
		t.Fatalf("grant holder must pass the page gate")
	}

	var minus markdown.Block
	found := false
	for _, b := range page.Blocks {
		if b.Type == "secret" && b.Secret {
			minus, found = b, true
		}
	}
	if !found {
		t.Fatalf("fixture lost its `-` block")
	}
	if !secrets.CanViewBlock(gm, minus, "alice", page.EditableBy) || !secrets.CanViewBlock(owner, minus, "alice", page.EditableBy) {
		t.Fatalf("gm/owner must see `-` block")
	}
	// WIDEN (amend landed): editable-by sees `-`.
	if !secrets.CanViewBlock(grantee, minus, "alice", page.EditableBy) {
		t.Errorf("grantee WIDEN: CanViewBlock denied editable-by holder")
	}
	// Grants do NOT open `-`; party/guests never see it.
	if secrets.CanViewBlock(pathgrant, minus, "alice", page.EditableBy) {
		t.Fatalf("pathgrant: `-` block leaked to grant holder")
	}
	if secrets.CanViewBlock(other, minus, "alice", page.EditableBy) || secrets.CanViewBlock(nil, minus, "alice", page.EditableBy) {
		t.Fatalf("`-` block leaked to party/guest")
	}

	// Guest render path: `-` becomes a labeled placeholder, never content.
	filtered, err := secrets.FilterBlocks(nil, page.Blocks, "alice", page.EditableBy)
	if err != nil {
		t.Fatalf("FilterBlocks: %v", err)
	}
	if strings.Contains(blockTexts(filtered), "seven seven seven") {
		t.Fatalf("FilterBlocks leaked `-` content to guest")
	}
	// WIDEN (amend landed): editable-by keeps `-` through FilterBlocks.
	filtered, err = secrets.FilterBlocks(grantee, page.Blocks, "alice", page.EditableBy)
	if err != nil {
		t.Fatalf("FilterBlocks: %v", err)
	}
	if !strings.Contains(blockTexts(filtered), "seven seven seven") {
		t.Errorf("grantee WIDEN: FilterBlocks withheld `-` from editable-by holder")
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
