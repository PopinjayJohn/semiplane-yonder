package main

// Lane L verification (Phase 4, G4): vault-zip round-trip identical.
//
// P09/P13 scope: export is GM-scoped (operator-run `archive-vault`; the
// served app has no anonymous export — data artifacts never cross the
// boundary) and the archive itself carries NO owner filter: every vault
// file ships regardless of frontmatter owner (the GM owns the files; per-
// owner exports do not exist). This test proves both halves:
//
//  1. archiveVaultDir -> unzipInto into a fresh dir = byte-identical tree
//     (all regular files; *.db/.sessionkey/symlinks excluded by design).
//  2. Owner-blindness: secret pages owned by different players all land in
//     the archive with identical bytes — the exporter takes no owner.
//
// Tests only: no product-code change.

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// treeBytes maps slash-relative paths of regular files to contents,
// resolving symlinks never (symlinks are listed separately).
func treeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func TestLaneLArchiveRoundTripIdentical(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{
		"campaign.yaml":               "name: demo\ncreated: 2026-01-01\n",
		"welcome.md":                  "# Welcome\n\nPublic readme.\n",
		"notes/pact.md":               "---\ntitle: Pact\nsecret: true\nowner: mira\n---\n\nEmber clause.\n",
		"characters/bram/index.md":    "---\ntitle: Bram\nsecret: true\nowner: bram\n---\n\n# Bram\n",
		"characters/cass/index.md":    "---\ntitle: Cass\nsecret: true\nowner: cass\neditable-by: [mira]\n---\n\n# Cass\n",
		"assets/sigil.png":            "\x89PNG-fake-bytes",
		"notes/crlf.md":               "# CRLF\r\n\r\nline\r\n",
		"unicode-☃/snow.md":           "# Snow\n",
		"vault.index.db":              "db-decoy",
		"notes/nested.app.db":         "db-decoy-nested",
		".sessionkey":                 "key-decoy",
		"characters/bram/.sessionkey": "key-decoy-nested",
		".obsidian/app.json":          "{}",
		"notes/.keep":                 "",
		"rules/homebrew/pack.yaml":    "name: hb\n",
		"notes/quip.md":               "quip\n",
		"notes/CASE.MD":               "case\n",
		"CON":                         "reserved-name regular file\n",
		"trailing-dot./dot.md":        "dot\n",
		"deep/a/b/c/d/e/f/g/page.md":  "deep\n",
		"notes/empty.md":              "",
		"spaces in name/read me.md":   "spaces\n",
		"notes/dash–en.md":            "dash\n",
		"notes/tab\tname.md":          "tab\n",
		"quotes\"q.md":                "q\n",
		"notes/semi;colon.md":         "semi\n",
		"notes/hash#tag.md":           "hash\n",
		"notes/percent%20.md":         "pct\n",
		"notes/plus+plus.md":          "plus\n",
		"notes/at@sign.md":            "at\n",
		"notes/twiddle~x.md":          "tilde\n",
		"notes/back`tick.md":          "tick\n",
		"notes/single'quote.md":       "squote\n",
		"notes/paren(1).md":           "paren\n",
		"notes/brack[1].md":           "brack\n",
		"notes/brace{1}.md":           "brace\n",
		"notes/exclaim!.md":           "bang\n",
		"notes/dollar$.md":            "dollar\n",
		"notes/caret^.md":             "caret\n",
		"notes/amp&ersand.md":         "amp\n",
		"notes/equals=.md":            "eq\n",
		"notes/comma,x.md":            "comma\n",
		"notes/semi;.md":              "semi2\n",
		"notes/question?.md":          "q2\n",
		"notes/star*.md":              "star\n",
		"notes/pipe|.md":              "pipe\n",
		"notes/less<more.md":          "lt\n",
		"notes/greater>more.md":       "gt\n",
		"notes/colon:file.md":         "colon\n",
	}
	for rel, body := range files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			// Windows-reserved or OS-refused names (CON, <, >, :, |, ?, *)
			// cannot exist on some filesystems: record and continue — the
			// round-trip proof covers whatever the OS stores.
			t.Logf("skip uncreatable %q: %v", rel, err)
			delete(files, rel)
		}
	}
	// Symlink decoy (skipped by design, never stored).
	if err := os.Symlink("welcome.md", filepath.Join(src, "link.md")); err != nil {
		t.Logf("symlink not creatable: %v", err)
	}

	out := filepath.Join(t.TempDir(), "vault.zip")
	gotFiles, skipped, err := archiveVaultDir(src, out)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	// Four data artifacts skipped (*.db x2, .sessionkey x2) + 1 symlink.
	if skipped != 5 {
		t.Errorf("skipped = %d, want 5 (2 db + 2 sessionkey + 1 symlink)", skipped)
	}
	before := treeFiles(t, src)
	// Strip the by-design exclusions from the expectation.
	for rel := range before {
		base := filepath.Base(rel)
		if strings.HasSuffix(strings.ToLower(base), ".db") || base == ".sessionkey" {
			delete(before, rel)
		}
	}
	if gotFiles != len(before) {
		t.Errorf("archived files = %d, want %d (regular files minus data artifacts)", gotFiles, len(before))
	}

	// Import side: unzipInto refuses non-empty dests (GM-only import guard),
	// so extract into a fresh dir like `init --bare` + import would.
	dest := filepath.Join(t.TempDir(), "fresh")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unzipInto(out, dest); err != nil {
		t.Fatalf("unzipInto: %v", err)
	}
	after := treeFiles(t, dest)

	// Owner-blindness proof: every owner's secret page survived with
	// identical bytes — the exporter applies no owner filter.
	for _, rel := range []string{"notes/pact.md", "characters/bram/index.md", "characters/cass/index.md"} {
		if after[rel] != before[rel] || after[rel] == "" {
			t.Errorf("owner page %q missing or altered in round-trip", rel)
		}
	}
	if !maps.Equal(before, after) {
		var onlyBefore, onlyAfter, differs []string
		for rel, b := range before {
			a, ok := after[rel]
			switch {
			case !ok:
				onlyBefore = append(onlyBefore, rel)
			case a != b:
				differs = append(differs, rel)
			}
		}
		for rel := range after {
			if _, ok := before[rel]; !ok {
				onlyAfter = append(onlyAfter, rel)
			}
		}
		slices.Sort(onlyBefore)
		slices.Sort(onlyAfter)
		slices.Sort(differs)
		t.Errorf("round-trip mismatch:\nonly-before=%q\nonly-after=%q\ndiffers=%q", onlyBefore, onlyAfter, differs)
	}
}
