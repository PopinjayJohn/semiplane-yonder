package web

import (
	"context"
	"encoding/json"

	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/store"
)

// ParsePage adapts the frozen markdown.Parse to the store.Parser shape the
// index build consumes (store.Reindex/rescan). F1-owned: this package
// already imports both markdown and store, so Lane B's package never
// imports Lane A's. Called by the `reindex` CLI (E1); no handler, parser,
// or migration behavior changes here.
//
// Mapping (mirrors the audit harness's auditParser, the reviewed template):
//   - title/frontmatter/secret/owner/editable-by/tags pass through;
//     frontmatter travels as raw JSON (non-indexed keys stay there only).
//   - non-"unsupported" blocks become chunks with their own secret flags
//     (`-`/default secret callouts stay hidden; `+` follows the page).
//   - links and embeds each add a link edge (target) plus a chunk
//     ("target alias") under their own secret flag.
//   - "optional" blocks (structural `<!-- optional:id=... -->` markers and
//     `[!optional]` callouts) become optionals with the source line;
//     the title carries the id (same as the audit template).
func ParsePage(ctx context.Context, content []byte, path string) (*store.ParsedPage, error) {
	page, err := markdown.Parse(ctx, string(content), path)
	if err != nil {
		return nil, err
	}
	fmJSON, err := json.Marshal(page.Frontmatter)
	if err != nil {
		fmJSON = []byte("{}")
	}
	out := &store.ParsedPage{
		Title:           page.Title,
		FrontmatterJSON: string(fmJSON),
		Secret:          page.Secret,
		Owner:           page.Owner,
		EditableBy:      page.EditableBy,
		Tags:            page.Tags,
	}
	for _, b := range page.Blocks {
		if b.Type == "unsupported" {
			continue
		}
		out.Chunks = append(out.Chunks, store.ParsedBlock{Secret: b.Secret, Text: blockText(b)})
	}
	for _, l := range page.Links {
		out.Chunks = append(out.Chunks, store.ParsedBlock{Secret: l.Secret, Text: l.Target + " " + l.Alias})
		out.Links = append(out.Links, l.Target)
	}
	for _, e := range page.Embeds {
		out.Chunks = append(out.Chunks, store.ParsedBlock{Secret: e.Secret, Text: e.Target + " " + e.Alt})
		out.Links = append(out.Links, e.Target)
	}
	for _, b := range page.Blocks {
		if b.Type == "optional" && b.Content != "" {
			out.Optionals = append(out.Optionals, store.ParsedOptional{ID: b.Content, Title: b.Content, Line: b.Position.Line})
		}
	}
	return out, nil
}

func blockText(b markdown.Block) string {
	if b.Content != "" {
		return b.Content
	}
	return b.Type
}
