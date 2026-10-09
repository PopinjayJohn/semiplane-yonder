package campaign

import (
	"fmt"
	"regexp"
	"strings"
)

// Upsert is the ONE surgical campaign.yaml writer (B4 unification): the
// web-layer UpsertCampaignYAML and the toggle-layer rewriteListKey merged,
// with the stricter fidelity of the latter.
//
// Given the previous file content and a full Campaign, it rewrites `key:
// ...` lines in place (comments/blank lines/order preserved), rebuilds the
// enabled-features / enabled-plugins blocks as `- item` lists, appends
// missing keys, and drops scalar lines the Campaign leaves empty (so
// `init --bare` scaffolds stay minimal). Unknown keys already present are
// preserved verbatim (forward-compat); new writes never invent keys
// outside the frozen set (append-only via amend).
//
// Two fidelity rules, both pinned by tests:
//   - Scalar lines whose parsed value already equals the Campaign value
//     are kept BYTE-FOR-BYTE, trailing comments included (the toggle
//     path's promise: GM-authored `# ...` notes survive dashboard saves).
//     Changed values are replaced; emptied values drop the line.
//   - List items render in the GIVEN order (the toggle path preserves
//     caller order). Callers that want canonical order sort before
//     calling (the dashboard/setup forms do).
func Upsert(existing string, c *Campaign) string {
	features := append([]string(nil), c.EnabledFeatures...)
	plugins := append([]string(nil), c.EnabledPlugins...)
	vals := map[string]string{
		"name":            quoteScalar(c.Name),
		"created":         quoteScalar(c.Created),
		"base":            quoteScalar(c.Base),
		"base-version":    quoteScalar(c.BaseVersion),
		"overlay":         quoteScalar(c.Overlay),
		"overlay-version": quoteScalar(c.OverlayVersion),
		"landing-page":    quoteScalar(c.LandingPage),
	}
	// Empty optionals drop their lines so scaffolds stay minimal.
	skip := map[string]bool{}
	for k, v := range vals {
		if v == `""` || v == "" {
			skip[k] = true
		}
	}
	lists := map[string][]string{
		"enabled-features": features,
		"enabled-plugins":  plugins,
	}
	// Previously parsed lists (when the file parses): a list whose items
	// already equal the Campaign's passes through BYTE-FOR-BYTE — the
	// toggle path's promise that untouched blocks (indent style included)
	// survive. Unparseable files rebuild every list (unknown keys and
	// comments still preserved by the main loop).
	parsedLists := map[string][]string{}
	if prev, perr := Parse([]byte(existing)); perr == nil {
		parsedLists["enabled-features"] = prev.EnabledFeatures
		parsedLists["enabled-plugins"] = prev.EnabledPlugins
	}
	// Rebuilt blocks reuse the file's existing item indent (two-space
	// Obsidian style stays two-space); appended keys use column 0.
	listPad := map[string]string{
		"enabled-features": listItemPad(existing, "enabled-features"),
		"enabled-plugins":  listItemPad(existing, "enabled-plugins"),
	}
	// Synthesized lines (new blocks, appended keys) use the file's
	// dominant newline so CRLF vaults don't gain mixed endings.
	newEOL := "\n"
	if strings.Contains(existing, "\r\n") {
		newEOL = "\r\n"
	}
	lines := splitLinesKeepEnds(existing)
	var out []string
	done := map[string]bool{}
	inList := ""  // list key whose items are being rebuilt, or ""
	passKey := "" // list key passing through byte-for-byte, or ""
	emitted := map[string]bool{}
	flushList := func(key string) {
		if emitted[key] {
			return
		}
		emitted[key] = true
		if len(lists[key]) == 0 {
			return
		}
		out = append(out, key+":"+newEOL)
		for _, item := range lists[key] {
			out = append(out, listPad[key]+"- "+item+newEOL)
		}
	}
	for idx := 0; idx < len(lines); idx++ {
		raw := lines[idx]
		line := trimLineEnd(raw)
		eol := lineEnd(raw)
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if passKey != "" {
			// Untouched block: indented lines and column-0 items
			// pass through; anything else ends the region and is
			// reprocessed in normal mode (blank/comment/structural
			// lines were never part of the old drop region either).
			if indent > 0 || strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
				out = append(out, raw)
				continue
			}
			passKey = ""
			idx-- // reprocess this line
			continue
		}
		if indent > 0 {
			if inList != "" {
				continue // old list items: dropped, rebuilt by flush
			}
			out = append(out, raw)
			continue
		}
		if inList != "" && (strings.HasPrefix(trimmed, "- ") || trimmed == "-") {
			continue // block-seq items at column 0: still list items
		}
		if strings.HasPrefix(trimmed, "#") || trimmed == "" || trimmed == "---" || trimmed == "..." {
			if inList != "" {
				flushList(inList)
				inList = ""
			}
			out = append(out, raw)
			continue
		}
		j := strings.IndexByte(trimmed, ':')
		if j < 0 {
			out = append(out, raw)
			continue
		}
		key := strings.TrimSpace(trimmed[:j])
		if _, isList := lists[key]; isList {
			if inList != "" && inList != key {
				flushList(inList)
			}
			inList = ""
			if prev, ok := parsedLists[key]; ok && equalStrings(prev, lists[key]) {
				emitted[key] = true
				out = append(out, raw)
				passKey = key
				continue
			}
			// Any inline value on the key line (flow `[a, b]`, `[]`,
			// or a bare scalar) is replaced wholesale by the block
			// form: the line is dropped here and flush rebuilds it.
			inList = key
			flushList(key)
			continue
		}
		if v, ok := vals[key]; ok {
			inList = ""
			if skip[key] {
				done[key] = true // cleared in the form: drop the line
				continue
			}
			if scalarLineValue(trimmed[j+1:]) == unquoteScalar(v) {
				out = append(out, raw) // unchanged: keep bytes + comment
			} else {
				out = append(out, key+": "+v+eol)
			}
			done[key] = true
			continue
		}
		if inList != "" {
			inList = ""
		}
		out = append(out, raw)
	}
	if inList != "" {
		flushList(inList)
	}
	// Missing keys append in canonical order (deterministic fresh files).
	for _, k := range []string{"name", "created", "base", "base-version", "overlay", "overlay-version", "landing-page"} {
		if !skip[k] && !done[k] {
			out = append(out, k+": "+vals[k]+newEOL)
		}
	}
	for _, key := range []string{"enabled-features", "enabled-plugins"} {
		if !emitted[key] {
			flushList(key)
		}
	}
	return strings.Join(out, "")
}

// Validate checks campaign shape only (H1 owns pack existence): name is
// required, overlay requires base (p05 layers), feature/plugin ids are
// stable slugs without duplicates. Both namespaces validated — neither
// lane's future keys trip the other.
func Validate(c *Campaign) error {
	if c == nil {
		return fmt.Errorf("nil campaign")
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("campaign name is required")
	}
	if c.Overlay != "" && c.Base == "" {
		return fmt.Errorf("overlay requires a base ruleset")
	}
	seen := map[string]bool{}
	for _, f := range c.EnabledFeatures {
		if !campaignIDRe.MatchString(f) {
			return fmt.Errorf("bad feature id %q: want [a-z0-9-]", f)
		}
		if seen["f/"+f] {
			return fmt.Errorf("duplicate feature id %q", f)
		}
		seen["f/"+f] = true
	}
	for _, p := range c.EnabledPlugins {
		if !campaignIDRe.MatchString(p) {
			return fmt.Errorf("bad plugin id %q: want [a-z0-9-]", p)
		}
		if seen["p/"+p] {
			return fmt.Errorf("duplicate plugin id %q", p)
		}
		seen["p/"+p] = true
	}
	return nil
}

var campaignIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// listItemPad finds the indent prefix of the first `- ` item under key in
// the existing content ("" when the key is absent or flow-styled), so
// rebuilt blocks keep the file's item style.
func listItemPad(content, key string) string {
	lines := splitLinesKeepEnds(content)
	for i, raw := range lines {
		line := trimLineEnd(raw)
		if len(line) != len(strings.TrimLeft(line, " \t")) {
			continue // nested lines never start a top-level key
		}
		trimmed := strings.TrimSpace(line)
		j := strings.IndexByte(trimmed, ':')
		if j < 0 || strings.TrimSpace(trimmed[:j]) != key {
			continue
		}
		for _, item := range lines[i+1:] {
			il := trimLineEnd(item)
			it := strings.TrimSpace(il)
			if it == "" || strings.HasPrefix(it, "#") {
				continue // tolerate blanks/comments between key and items
			}
			if it == "-" || strings.HasPrefix(it, "- ") {
				return il[:len(il)-len(strings.TrimLeft(il, " "))]
			}
			return ""
		}
		return ""
	}
	return ""
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// scalarLineValue parses the value half of a `key: value # comment` line
// for equality comparison: trailing comment stripped, quotes removed.
func scalarLineValue(v string) string {
	return unquoteScalar(stripLineComment(v))
}

func stripLineComment(v string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && i > 0 && (v[i-1] == ' ' || v[i-1] == '\t') {
				return strings.TrimSpace(v[:i])
			}
		}
	}
	return strings.TrimSpace(v)
}

func unquoteScalar(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		q := v[0]
		inner := v[1 : len(v)-1]
		if q == '"' {
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\\`, `\`)
		} else {
			inner = strings.ReplaceAll(inner, `''`, `'`)
		}
		return inner
	}
	return v
}

// quoteScalar renders a value safe for a `key: value` line: bare when
// simple, double-quoted otherwise.
func quoteScalar(s string) string {
	simple := s != "" && !strings.ContainsAny(s, ":#{}[],&*!|>'\"%@`") &&
		!strings.HasPrefix(s, " ") && !strings.HasSuffix(s, " ") &&
		!strings.ContainsAny(s, "\r\n\t")
	if simple {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func splitLinesKeepEnds(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func trimLineEnd(s string) string { return strings.TrimRight(s, "\r\n") }

func lineEnd(line string) string {
	if strings.HasSuffix(line, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return "\n"
}
