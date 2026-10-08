package campaign

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/semiplane/yonder/internal/markdown"
)

// Optional is one toggleable ruleset feature offered somewhere in the
// vault: several may live in one file (P05 multi-per-file). GM toggles
// per-id; enabled ids persist in campaign.yaml enabled-features.
type Optional struct {
	ID    string // stable id, e.g. "flanking-2024"
	Title string // nearest heading (block) or page title (shorthand)
	File  string // vault-relative posix path
	Line  int    // 1-based source line of the marker/callout/frontmatter
}

var (
	markerRe = regexp.MustCompile(`(?i)<!--[ \t]*optional:id=([A-Za-z0-9_./-]+)[ \t]*-->`)
	// `> [!optional...]`: captures the [!...] parameter run for id parsing.
	calloutRe = regexp.MustCompile(`(?i)^>[ \t]*\[!optional((?:[|+][^\]]*)?)\]`)
	// id=... inside a callout parameter run (`|id=x`, `|foo|id=x`, ...).
	calloutID = regexp.MustCompile(`[|+](?:[^|\]]*[|])?id=([A-Za-z0-9_./-]+)`)
	idRe      = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	headingRe = regexp.MustCompile(`^#{1,6}[ \t]+(.+?)[ \t]*#*[ \t]*$`)
)

// SlugForPath maps a vault-relative page path to its `optional:true`
// shorthand id: extension trimmed, lowercased, posix separators.
func SlugForPath(rel string) string {
	rel = filepath.ToSlash(rel)
	low := strings.ToLower(rel)
	for _, ext := range []string{".markdown", ".md"} {
		if strings.HasSuffix(low, ext) {
			rel = rel[:len(rel)-len(ext)]
			break
		}
	}
	return strings.ToLower(rel)
}

// Scan walks every .md file in the vault (hidden dirs, dotfiles, *.tmp,
// and *.conflict-*.md files skipped, mirroring the indexer) and collects
// optionals from three sources: `<!-- optional:id=... -->` markers,
// `> [!optional|id=...]` callouts (explicit id required on blocks), and
// the `optional:true` / `optional:{id:}` frontmatter shorthand (id = path
// slug unless overridden). Quarantined pages contribute body optionals but
// no frontmatter shorthand (broken keys fail closed).
//
// Duplicate ids fail vault-wide: the returned error lists every occurrence
// as file:line (P05 pass/fail). Missing callout ids and malformed ids fail
// the same way. Deterministic order: sorted by file, then line.
func Scan(vaultRoot string) ([]Optional, error) {
	var out []Optional
	var problems []string
	err := filepath.WalkDir(vaultRoot, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if fp == vaultRoot {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		rel, err := filepath.Rel(vaultRoot, fp)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isConflictPath(rel) {
			return nil
		}
		data, err := os.ReadFile(fp)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		opts, probs := scanFile(rel, string(data))
		out = append(out, opts...)
		problems = append(problems, probs...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	seen := map[string]Optional{}
	var dups []string
	for _, o := range out {
		if first, ok := seen[o.ID]; ok {
			dups = append(dups, fmt.Sprintf("duplicate optional id %q: %s:%d and %s:%d",
				o.ID, first.File, first.Line, o.File, o.Line))
			continue
		}
		seen[o.ID] = o
	}
	problems = append(problems, dups...)
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("optionals: %s", strings.Join(problems, "; "))
	}
	return out, nil
}

// scanFile extracts one file's optionals plus file:line problems.
func scanFile(rel, content string) ([]Optional, []string) {
	var out []Optional
	var problems []string
	norm := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(norm, "\n")
	heading := ""
	for i, ln := range lines {
		if m := headingRe.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			heading = m[1]
		}
		trimmed := strings.TrimSpace(ln)
		if !strings.HasPrefix(trimmed, ">") && !strings.Contains(ln, "<!--") {
			continue
		}
		for _, m := range markerRe.FindAllStringSubmatch(ln, -1) {
			title := heading
			if title == "" {
				title = m[1]
			}
			out = append(out, Optional{ID: m[1], Title: title, File: rel, Line: i + 1})
		}
		if cm := calloutRe.FindStringSubmatch(trimmed); cm != nil {
			id := ""
			if im := calloutID.FindStringSubmatch(cm[1]); im != nil {
				id = im[1]
			}
			if id == "" {
				problems = append(problems, fmt.Sprintf("%s:%d: optional block without explicit id", rel, i+1))
				continue
			}
			title := heading
			if title == "" {
				title = id
			}
			out = append(out, Optional{ID: id, Title: title, File: rel, Line: i + 1})
		}
	}
	// Frontmatter shorthand via the frozen parser (read-only consumer).
	page, err := markdown.Parse(context.Background(), content, rel)
	if err == nil && page != nil && !page.Quarantined {
		if raw, ok := page.Frontmatter["optional"]; ok {
			switch v := raw.(type) {
			case bool:
				if v {
					title := page.Title
					if title == "" {
						title = SlugForPath(rel)
					}
					out = append(out, Optional{ID: SlugForPath(rel), Title: title, File: rel, Line: 1})
				}
			case map[string]any:
				if idRaw, has := v["id"]; has {
					if s, ok := scalarStr(idRaw); ok && idRe.MatchString(s) {
						title := page.Title
						if title == "" {
							title = s
						}
						out = append(out, Optional{ID: s, Title: title, File: rel, Line: 1})
					} else {
						problems = append(problems, fmt.Sprintf("%s:1: bad optional id", rel))
					}
				}
			}
		}
	}
	for _, o := range out {
		if !idRe.MatchString(o.ID) {
			problems = append(problems, fmt.Sprintf("%s:%d: bad optional id %q", o.File, o.Line, o.ID))
		}
	}
	return out, problems
}

func scalarStr(v any) (string, bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	return "", false
}

// isConflictPath reports the single conflict pattern (*.conflict-<ts>.md).
// Conflict files keep the parent ACL and stay out of search and optionals.
func isConflictPath(rel string) bool {
	base := rel
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	i := strings.LastIndex(base, ".conflict-")
	return i > 0 && strings.HasSuffix(strings.ToLower(base), ".md")
}
