package markdown

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AllowedKeys is the closed frontmatter key set (p02). Extension only by
// amend; keys stay append-only after Phase 1.
var AllowedKeys = []string{
	"title",
	"secret",
	"owner",
	"editable-by",
	"optional",
	"tags",
	"status",
	"parent",
	"writer",
	"ruleset_id",
	"version",
	"content-warning",
	"sheet",
}

// allowedStatus lists the permitted values of the status key (p02).
var allowedStatus = map[string]bool{
	"hook":     true,
	"active":   true,
	"done":     true,
	"conflict": true,
}

// splitFrontmatter separates YAML frontmatter from the body. It reports
// whether frontmatter was present. Line endings are tolerated (LF/CRLF);
// the returned body keeps its original bytes (source is never rewritten).
func splitFrontmatter(content string) (fm string, body string, hasFM bool) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return "", content, false
	}
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimRight(lines[i], " \t")
		if trimmed == "---" || trimmed == "..." {
			fmText := strings.Join(lines[1:i], "\n")
			// Recompute the body from the original content so CRLF and
			// exact spacing survive (Parse never rewrites source).
			bodyStart := 0
			nls := 0
			for nls < i+1 && bodyStart < len(content) {
				idx := strings.IndexByte(content[bodyStart:], '\n')
				if idx < 0 {
					bodyStart = len(content)
					break
				}
				bodyStart += idx + 1
				nls++
			}
			return fmText, content[bodyStart:], true
		}
	}
	return "", content, false // unterminated: caller quarantines
}

// parseFrontmatter parses and validates frontmatter text. It returns the
// validated map, unknown keys (kept for `rules lint`), and quarantine
// reasons (bad structure/types/values — the page is quarantined, never
// dropped and never a crash).
func parseFrontmatter(text string) (Frontmatter, []string, []string) {
	raw, err := parseYAMLSubset(text)
	if err != nil {
		return Frontmatter{}, nil, []string{err.Error()}
	}
	fm := Frontmatter{}
	for k, v := range raw {
		fm[k] = v
	}
	unknown := []string{}
	for k := range raw {
		if !isAllowedKey(k) {
			unknown = append(unknown, "unknown frontmatter key: "+k)
		}
	}
	sort.Strings(unknown)
	quarantine := []string{}
	coerce := func(key string, v any, want string) (any, bool) {
		cv, ok := coerceValue(key, v, want)
		if !ok {
			quarantine = append(quarantine,
				fmt.Sprintf("bad type for frontmatter key %q: want %s", key, want))
		}
		return cv, ok
	}
	keys := make([]string, 0, len(fm))
	for k := range fm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := fm[k]
		switch k {
		case "title", "owner", "parent", "writer", "ruleset_id", "version":
			if cv, ok := coerce(k, v, "string"); ok {
				fm[k] = cv
			} else {
				delete(fm, k)
			}
		case "secret":
			if cv, ok := coerce(k, v, "bool"); ok {
				fm[k] = cv
			} else {
				delete(fm, k)
			}
		case "editable-by", "tags", "content-warning":
			if cv, ok := coerce(k, v, "list"); ok {
				fm[k] = cv
			} else {
				delete(fm, k)
			}
		case "optional":
			if b, ok := coerceBool(v); ok {
				fm[k] = b
				continue
			}
			if m, ok := v.(map[string]any); ok {
				id, hasID := m["id"]
				if !hasID {
					quarantine = append(quarantine,
						`bad type for frontmatter key "optional": want bool|{id}`)
					delete(fm, k)
					continue
				}
				s, ok := id.(string)
				if !ok {
					if s2, ok2 := scalarToString(id); ok2 {
						m["id"] = s2
					} else {
						quarantine = append(quarantine,
							`bad type for frontmatter key "optional": want bool|{id}`)
						delete(fm, k)
						continue
					}
				} else {
					_ = s
				}
				fm[k] = m
				continue
			}
			quarantine = append(quarantine,
				`bad type for frontmatter key "optional": want bool|{id}`)
			delete(fm, k)
		case "status":
			s, ok := v.(string)
			if !ok {
				if s2, ok2 := scalarToString(v); ok2 {
					s, ok = s2, true
				}
			}
			if !ok || !allowedStatus[s] {
				quarantine = append(quarantine,
					`bad value for frontmatter key "status": want hook|active|done|conflict`)
				delete(fm, k)
				continue
			}
			fm[k] = s
		case "sheet":
			if _, ok := v.(map[string]any); !ok {
				quarantine = append(quarantine,
					`bad type for frontmatter key "sheet": want map`)
				delete(fm, k)
			}
		}
	}
	return fm, unknown, quarantine
}

// ValidateFrontmatter reports unknown keys and type/value problems without
// parsing source. Lane H2 (`rules lint`) reuses this on parsed frontmatter.
func ValidateFrontmatter(fm Frontmatter) []string {
	out := []string{}
	for k, v := range fm {
		if !isAllowedKey(k) {
			out = append(out, "unknown frontmatter key: "+k)
			continue
		}
		switch k {
		case "title", "owner", "parent", "writer", "ruleset_id", "version":
			if _, ok := scalarToString(v); !ok {
				out = append(out, fmt.Sprintf("bad type for frontmatter key %q: want string", k))
			}
		case "secret":
			if _, ok := coerceBool(v); !ok {
				out = append(out, fmt.Sprintf("bad type for frontmatter key %q: want bool", k))
			}
		case "editable-by", "tags", "content-warning":
			if _, ok := coerceStringList(v); !ok {
				out = append(out, fmt.Sprintf("bad type for frontmatter key %q: want [string]", k))
			}
		case "status":
			s, ok := scalarToString(v)
			if !ok || !allowedStatus[s] {
				out = append(out, `bad value for frontmatter key "status": want hook|active|done|conflict`)
			}
		case "optional":
			if _, ok := coerceBool(v); !ok {
				if m, ok := v.(map[string]any); !ok {
					out = append(out, `bad type for frontmatter key "optional": want bool|{id}`)
				} else if _, hasID := m["id"]; !hasID {
					out = append(out, `bad type for frontmatter key "optional": want bool|{id}`)
				}
			}
		case "sheet":
			if _, ok := v.(map[string]any); !ok {
				out = append(out, `bad type for frontmatter key "sheet": want map`)
			}
		}
	}
	sort.Strings(out)
	return out
}

func isAllowedKey(k string) bool {
	for _, a := range AllowedKeys {
		if a == k {
			return true
		}
	}
	return false
}

// coerceValue coerces v to want ("string", "bool", "list").
func coerceValue(key string, v any, want string) (any, bool) {
	switch want {
	case "string":
		s, ok := scalarToString(v)
		return s, ok
	case "bool":
		b, ok := coerceBool(v)
		return b, ok
	case "list":
		l, ok := coerceStringList(v)
		return l, ok
	}
	return nil, false
}

func scalarToString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	case nil:
		return "", true
	}
	return "", false
}

func coerceBool(v any) (bool, bool) {
	if b, ok := v.(bool); ok {
		return b, true
	}
	if s, ok := v.(string); ok {
		switch strings.ToLower(s) {
		case "true", "yes", "y", "on":
			return true, true
		case "false", "no", "n", "off":
			return false, true
		}
	}
	return false, false
}

func coerceStringList(v any) ([]string, bool) {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := scalarToString(e)
			if !ok || e == nil {
				if e == nil {
					continue
				}
				return nil, false
			}
			if _, bad := e.(map[string]any); bad {
				return nil, false
			}
			if _, bad := e.([]any); bad {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case []string:
		return t, true
	case nil:
		return []string{}, true
	default:
		if s, ok := scalarToString(v); ok {
			return []string{s}, true
		}
		return nil, false
	}
}

// --- Minimal YAML-subset parser (stdlib only; no new deps) ---
//
// Supports the shapes p02 frontmatter needs: `key: scalar`, quoted
// scalars, `#` comments, inline `[a, b]` / `{k: v}` flow collections,
// block `- item` sequences, nested maps, and `|`/`>` literal/folded
// blocks. Tabs in indentation, anchors/aliases and other YAML features
// are rejected (parse error -> quarantine, never crash).

type yamlLine struct {
	indent int
	text   string // stripped of indent, comments removed, trimmed right
	num    int
}

func lexYAMLLines(text string) ([]yamlLine, error) {
	raw := strings.Split(text, "\n")
	out := []yamlLine{}
	for i, r := range raw {
		if strings.TrimSpace(r) == "" || strings.TrimSpace(r) == "---" {
			continue
		}
		indent := 0
		for indent < len(r) && r[indent] == ' ' {
			indent++
		}
		if indent < len(r) && r[indent] == '\t' {
			return nil, fmt.Errorf("line %d: tabs not allowed in indentation", i+1)
		}
		rest := r[indent:]
		if strings.HasPrefix(strings.TrimSpace(rest), "#") {
			continue
		}
		rest = stripComment(rest)
		rest = strings.TrimRight(rest, " \t")
		if rest == "" {
			continue
		}
		out = append(out, yamlLine{indent: indent, text: rest, num: i + 1})
	}
	return out, nil
}

// stripComment cuts a ` #...` comment outside single/double quotes.
func stripComment(s string) string {
	var q rune
	for i := 0; i < len(s); {
		c := s[i]
		if q == 0 && (c == '"' || c == '\'') {
			q = rune(c)
			i++
			continue
		}
		if q != 0 {
			if byte(q) == c {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i += 2
					continue
				}
				q = 0
			}
			i++
			continue
		}
		if c == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
			return s[:i]
		}
		i++
	}
	return s
}

func parseYAMLSubset(text string) (map[string]any, error) {
	lines, err := lexYAMLLines(text)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return map[string]any{}, nil
	}
	if lines[0].indent != 0 {
		return nil, fmt.Errorf("line %d: top-level keys must not be indented", lines[0].num)
	}
	m, next, err := parseBlockMap(lines, 0, 0)
	if err != nil {
		return nil, err
	}
	if next < len(lines) {
		return nil, fmt.Errorf("line %d: unexpected content", lines[next].num)
	}
	return m, nil
}

func parseBlockMap(lines []yamlLine, start, indent int) (map[string]any, int, error) {
	m := map[string]any{}
	i := start
	for i < len(lines) {
		ln := lines[i]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, i, fmt.Errorf("line %d: unexpected indentation", ln.num)
		}
		if strings.HasPrefix(ln.text, "- ") || ln.text == "-" {
			break // a sequence belongs to the parent key
		}
		key, val, rest, err := splitKey(ln.text)
		if err != nil {
			return nil, i, fmt.Errorf("line %d: %v", ln.num, err)
		}
		_ = rest
		i++
		if val != "" {
			if val == "|" || val == ">" || strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
				s, ni, err := parseLiteralBlock(lines, i, ln.num, val)
				if err != nil {
					return nil, i, err
				}
				m[key] = s
				i = ni
				continue
			}
			v, err := parseScalarOrFlow(val)
			if err != nil {
				return nil, i, fmt.Errorf("line %d: %v", ln.num, err)
			}
			m[key] = v
			continue
		}
		// Empty value: nested block, sequence, or null.
		if i < len(lines) && lines[i].indent > indent {
			nl := lines[i]
			if strings.HasPrefix(nl.text, "- ") || nl.text == "-" {
				seq, ni, err := parseBlockSeq(lines, i, nl.indent)
				if err != nil {
					return nil, i, err
				}
				m[key] = seq
				i = ni
			} else {
				sub, ni, err := parseBlockMap(lines, i, nl.indent)
				if err != nil {
					return nil, i, err
				}
				m[key] = sub
				i = ni
			}
		} else if i < len(lines) && lines[i].indent == indent &&
			(strings.HasPrefix(lines[i].text, "- ") || lines[i].text == "-") {
			// Valid YAML/Obsidian shape: a block sequence may sit at the
			// same indent as its parent key (`key:\n- item`). Without
			// this the `-` line falls out of the map and dies as
			// `unexpected content` (quarantine).
			seq, ni, err := parseBlockSeq(lines, i, indent)
			if err != nil {
				return nil, i, err
			}
			m[key] = seq
			i = ni
		} else {
			m[key] = nil
		}
	}
	return m, i, nil
}

func parseBlockSeq(lines []yamlLine, start, indent int) ([]any, int, error) {
	out := []any{}
	i := start
	for i < len(lines) {
		ln := lines[i]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, i, fmt.Errorf("line %d: unexpected indentation in list", ln.num)
		}
		if !strings.HasPrefix(ln.text, "- ") && ln.text != "-" {
			break
		}
		item := strings.TrimPrefix(ln.text, "-")
		item = strings.TrimPrefix(item, " ")
		i++
		if item == "" {
			if i < len(lines) && lines[i].indent > indent {
				sub, ni, err := parseBlockMap(lines, i, lines[i].indent)
				if err != nil {
					return nil, i, err
				}
				m := map[string]any{}
				for k, v := range sub {
					m[k] = v
				}
				out = append(out, m)
				i = ni
			} else {
				out = append(out, nil)
			}
			continue
		}
		if key, val, _, err := splitKey(item); err == nil && key != "" {
			sub := map[string]any{}
			if val == "" {
				if i < len(lines) && lines[i].indent > indent {
					rest, ni, err := parseBlockMap(lines, i, lines[i].indent)
					if err != nil {
						return nil, i, err
					}
					sub[key] = rest
					i = ni
				} else {
					sub[key] = nil
				}
			} else {
				v, err := parseScalarOrFlow(val)
				if err != nil {
					return nil, i, fmt.Errorf("line %d: %v", ln.num, err)
				}
				sub[key] = v
			}
			// Continuation lines of the same inline map.
			for i < len(lines) && lines[i].indent > indent &&
				!strings.HasPrefix(lines[i].text, "- ") && lines[i].text != "-" {
				ck, cv, _, err := splitKey(lines[i].text)
				if err != nil || ck == "" {
					return nil, i, fmt.Errorf("line %d: bad map entry in list", lines[i].num)
				}
				if cv == "" {
					sub[ck] = nil
				} else {
					v, err := parseScalarOrFlow(cv)
					if err != nil {
						return nil, i, fmt.Errorf("line %d: %v", lines[i].num, err)
					}
					sub[ck] = v
				}
				i++
			}
			out = append(out, sub)
			continue
		}
		v, err := parseScalarOrFlow(item)
		if err != nil {
			return nil, i, fmt.Errorf("line %d: %v", ln.num, err)
		}
		out = append(out, v)
	}
	return out, i, nil
}

// parseLiteralBlock consumes a `|`/`>` block scalar's indented lines.
func parseLiteralBlock(lines []yamlLine, start, lnNum int, header string) (string, int, error) {
	style := header[0]
	rest := strings.TrimSpace(header[1:])
	// Optional chomping/indent indicators (+/-/digit) are accepted and
	// ignored beyond the default clip behavior.
	for len(rest) > 0 && (rest[0] == '+' || rest[0] == '-' || (rest[0] >= '0' && rest[0] <= '9')) {
		rest = rest[1:]
	}
	if strings.TrimSpace(rest) != "" {
		return "", start, fmt.Errorf("line %d: bad block scalar header", lnNum)
	}
	i := start
	for i < len(lines) && lines[i].text == "" {
		i++
	}
	if i >= len(lines) {
		return "", i, nil
	}
	indent := lines[i].indent
	var b strings.Builder
	for i < len(lines) && lines[i].indent >= indent {
		rel := lines[i].indent - indent
		b.WriteString(strings.Repeat(" ", rel))
		b.WriteString(lines[i].text)
		b.WriteByte('\n')
		i++
	}
	s := b.String()
	if style == '>' {
		// Folded: single newlines become spaces, blank runs stay breaks.
		paras := strings.Split(strings.TrimRight(s, "\n"), "\n\n")
		for k, p := range paras {
			paras[k] = strings.ReplaceAll(strings.ReplaceAll(p, "\n", " "), "  ", " ")
		}
		return strings.Join(paras, "\n\n") + "\n", i, nil
	}
	return s, i, nil
}

// splitKey splits `key: value`; value may be empty. The second return is
// reserved for future use.
func splitKey(s string) (key, val, rest string, err error) {
	inS, inD := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					inS = false
				}
			}
		case inD:
			switch c {
			case '\\':
				i++
			case '"':
				inD = false
			}
		case c == '\'':
			inS = true
		case c == '"':
			inD = true
		case c == ':':
			if i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '\t' {
				continue // `http://x`, not a key separator
			}
			k := strings.TrimSpace(s[:i])
			if k == "" {
				return "", "", "", fmt.Errorf("empty key")
			}
			v := ""
			if i+1 < len(s) {
				v = strings.TrimSpace(s[i+1:])
			}
			uk, uerr := parsePlainKey(k)
			if uerr != nil {
				return "", "", "", uerr
			}
			return uk, v, "", nil
		case c == '{' || c == '[' || c == '*' || c == '&' || c == '!' || c == '|' || c == '>':
			if c == '*' || c == '&' {
				return "", "", "", fmt.Errorf("anchors/aliases not supported")
			}
		}
	}
	return "", "", "", fmt.Errorf("no key separator in %q", s)
}

func isQuoted(s string) bool {
	return len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') ||
		(s[0] == '\'' && s[len(s)-1] == '\''))
}

func parsePlainKey(k string) (string, error) {
	if strings.ContainsAny(k, " \t") || strings.ContainsAny(k, ":{}[],#&*!|>'\"%@`") {
		return "", fmt.Errorf("bad key %q", k)
	}
	if k == "" {
		return "", fmt.Errorf("empty key")
	}
	return k, nil
}

// parseScalarOrFlow parses one scalar or flow collection value.
func parseScalarOrFlow(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	switch {
	case s == "null" || s == "Null" || s == "NULL" || s == "~":
		return nil, nil
	case (s[0] == '"' && s[len(s)-1] == '"' && len(s) >= 2) ||
		(s[0] == '\'' && s[len(s)-1] == '\'' && len(s) >= 2):
		return parseQuoted(s)
	case s[0] == '[':
		return parseFlowList(s)
	case s[0] == '{':
		return parseFlowMap(s)
	case s[0] == '*' || s[0] == '&':
		return nil, fmt.Errorf("anchors/aliases not supported")
	case s[0] == '!' || (len(s) > 1 && s[0] == '!' && s[1] == '!'):
		return nil, fmt.Errorf("explicit tags not supported")
	}
	return parsePlainScalar(s), nil
}

func parseQuoted(s string) (string, error) {
	if s[0] == '\'' {
		if !isQuoted(s) {
			return "", fmt.Errorf("unterminated single-quoted string")
		}
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	}
	if !isQuoted(s) {
		return "", fmt.Errorf("unterminated double-quoted string")
	}
	var b strings.Builder
	inner := s[1 : len(s)-1]
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			i++
			switch inner[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case '0':
				b.WriteByte(0)
			default:
				return "", fmt.Errorf("unsupported escape \\%c", inner[i])
			}
			continue
		}
		b.WriteByte(inner[i])
	}
	return b.String(), nil
}

func parsePlainScalar(s string) any {
	low := strings.ToLower(s)
	switch low {
	case "true":
		return true
	case "false":
		return false
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// splitFlow splits flow items on top-level commas (quote/brace aware).
func splitFlow(s string) ([]string, error) {
	depth := 0
	var q byte
	items := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if q != 0 {
			if c == '\\' && q == '"' {
				i++
				continue
			}
			if c == q {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				q = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			q = c
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced brackets in flow collection")
			}
		case ',':
			if depth == 0 {
				items = append(items, s[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 || q != 0 {
		return nil, fmt.Errorf("unterminated flow collection")
	}
	items = append(items, s[start:])
	return items, nil
}

func parseFlowList(s string) ([]any, error) {
	if s[len(s)-1] != ']' {
		return nil, fmt.Errorf("unterminated flow list")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []any{}, nil
	}
	parts, err := splitFlow(inner)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := parseScalarOrFlow(p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func parseFlowMap(s string) (map[string]any, error) {
	if s[len(s)-1] != '}' {
		return nil, fmt.Errorf("unterminated flow map")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	m := map[string]any{}
	if inner == "" {
		return m, nil
	}
	parts, err := splitFlow(inner)
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		k, v, _, err := splitKey(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		vv, err := parseScalarOrFlow(v)
		if err != nil {
			return nil, err
		}
		m[k] = vv
	}
	return m, nil
}

// slugify builds a deterministic heading anchor: lowercase, runs of
// spaces/underscores/dashes collapse to one dash, other punctuation is
// dropped, unicode letters and digits kept.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		case r == ' ' || r == '_' || r == '-' || r == '\t':
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := b.String()
	out = strings.Trim(out, "-")
	if out == "" {
		out = "section"
	}
	return out
}

// offsetToLineCol converts a byte offset into 1-based line/column.
func offsetToLineCol(source string, offset int) (line, col int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(source) {
		offset = len(source)
	}
	line, col = 1, 1
	for i := 0; i < offset; {
		if source[i] == '\n' {
			line++
			col = 1
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(source[i:])
		if size == 0 {
			break
		}
		i += size
		col++
	}
	return line, col
}
