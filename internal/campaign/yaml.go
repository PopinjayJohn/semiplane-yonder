package campaign

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// --- Minimal YAML-subset reader (stdlib only; no new deps) ---
//
// Supports the shapes campaign.yaml and ruleset pack.yaml need: `key:
// scalar` pairs, quoted scalars, `#` comments, block `- item` sequences
// (indented deeper than the key, or at the same indent per the Obsidian
// quirk Lane A documents), nested block maps, one-line flow lists
// (`[a, b]`), and `|`/`>` block scalars. Tabs in indentation,
// anchors/aliases, explicit tags, and flow maps are rejected with a
// line-numbered error. Every value carries its source line so lint can
// report file:line.
//
// CRLF is tolerated; column tracking is line-granular only.

// Node is one parsed YAML value with its source line.
type Node struct {
	Value any // string, int64, float64, bool, nil, []any, map[string]Node
	Line  int // 1-based source line of the value (key line for maps)
}

// Map coerces n to a nested map.
func (n Node) Map() (map[string]Node, bool) {
	m, ok := n.Value.(map[string]Node)
	return m, ok
}

// List coerces n to a sequence.
func (n Node) List() ([]any, bool) {
	l, ok := n.Value.([]any)
	return l, ok
}

// String coerces n to a string (numbers and bools stringify; nil -> "").
func (n Node) String() (string, bool) {
	switch v := n.Value.(type) {
	case string:
		return v, true
	case int64:
		return strconv.FormatInt(v, 10), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	case nil:
		return "", true
	}
	return "", false
}

// StringList coerces n to a list of strings (nil -> empty, scalar -> one).
func (n Node) StringList() ([]string, bool) {
	switch v := n.Value.(type) {
	case nil:
		return []string{}, true
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := scalarToString(e)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		if s, ok := scalarToString(n.Value); ok {
			return []string{s}, true
		}
		return nil, false
	}
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

type yamlLine struct {
	indent int
	text   string
	num    int
}

func lexLines(text string) ([]yamlLine, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
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
	var q byte
	for i := 0; i < len(s); {
		c := s[i]
		if q == 0 && (c == '"' || c == '\'') {
			q = c
			i++
			continue
		}
		if q != 0 {
			if c == q {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i += 2
					continue
				}
				q = 0
			}
			if q == '"' && c == '\\' {
				i++
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

// parseDoc parses a whole document into a top-level map with line numbers.
func parseDoc(text string) (map[string]Node, error) {
	lines, err := lexLines(text)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return map[string]Node{}, nil
	}
	if lines[0].indent != 0 {
		return nil, fmt.Errorf("line %d: top-level keys must not be indented", lines[0].num)
	}
	m, next, err := parseMap(lines, 0, 0)
	if err != nil {
		return nil, err
	}
	if next < len(lines) {
		return nil, fmt.Errorf("line %d: unexpected content", lines[next].num)
	}
	return m, nil
}

func parseMap(lines []yamlLine, start, indent int) (map[string]Node, int, error) {
	m := map[string]Node{}
	i := start
	for i < len(lines) {
		ln := lines[i]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, i, fmt.Errorf("line %d: unexpected indentation", ln.num)
		}
		if ln.text == "-" || strings.HasPrefix(ln.text, "- ") {
			break // a sequence belongs to the parent key
		}
		key, val, err := splitKey(ln.text)
		if err != nil {
			return nil, i, fmt.Errorf("line %d: %v", ln.num, err)
		}
		keyLine := ln.num
		i++
		if val != "" {
			if val == "|" || val == ">" || strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
				s, ni, err := parseLiteral(lines, i, keyLine, val)
				if err != nil {
					return nil, i, err
				}
				m[key] = Node{Value: s, Line: keyLine}
				i = ni
				continue
			}
			v, err := parseScalar(val)
			if err != nil {
				return nil, i, fmt.Errorf("line %d: %v", keyLine, err)
			}
			m[key] = Node{Value: v, Line: keyLine}
			continue
		}
		// Empty value: nested block, sequence (deeper or same indent —
		// the `key:\n- item` Obsidian shape), or null.
		if i < len(lines) && lines[i].indent >= indent &&
			(lines[i].text == "-" || strings.HasPrefix(lines[i].text, "- ")) {
			seq, ni, err := parseSeq(lines, i, lines[i].indent)
			if err != nil {
				return nil, i, err
			}
			m[key] = Node{Value: seq, Line: keyLine}
			i = ni
		} else if i < len(lines) && lines[i].indent > indent {
			sub, ni, err := parseMap(lines, i, lines[i].indent)
			if err != nil {
				return nil, i, err
			}
			m[key] = Node{Value: sub, Line: keyLine}
			i = ni
		} else {
			m[key] = Node{Value: nil, Line: keyLine}
		}
	}
	return m, i, nil
}

func parseSeq(lines []yamlLine, start, indent int) ([]any, int, error) {
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
		if ln.text != "-" && !strings.HasPrefix(ln.text, "- ") {
			break
		}
		item := strings.TrimPrefix(ln.text, "-")
		item = strings.TrimPrefix(item, " ")
		itemLine := ln.num
		i++
		if item == "" {
			if i < len(lines) && lines[i].indent > indent {
				sub, ni, err := parseMap(lines, i, lines[i].indent)
				if err != nil {
					return nil, i, err
				}
				out = append(out, sub)
				i = ni
			} else {
				out = append(out, nil)
			}
			_ = itemLine
			continue
		}
		if key, val, err := splitKey(item); err == nil && key != "" {
			sub := map[string]Node{key: scalarNode(val, itemLine)}
			if val == "" && i < len(lines) && lines[i].indent > indent {
				rest, ni, err := parseMap(lines, i, lines[i].indent)
				if err != nil {
					return nil, i, err
				}
				sub[key] = Node{Value: rest, Line: itemLine}
				i = ni
			}
			for i < len(lines) && lines[i].indent > indent &&
				lines[i].text != "-" && !strings.HasPrefix(lines[i].text, "- ") {
				ck, cv, err := splitKey(lines[i].text)
				if err != nil || ck == "" {
					return nil, i, fmt.Errorf("line %d: bad map entry in list", lines[i].num)
				}
				if cv == "" {
					sub[ck] = Node{Value: nil, Line: lines[i].num}
				} else {
					sub[ck] = scalarNode(cv, lines[i].num)
				}
				i++
			}
			out = append(out, sub)
			continue
		}
		v, err := parseScalar(item)
		if err != nil {
			return nil, i, fmt.Errorf("line %d: %v", itemLine, err)
		}
		out = append(out, v)
	}
	return out, i, nil
}

func scalarNode(s string, line int) Node {
	v, err := parseScalar(s)
	if err != nil {
		return Node{Value: s, Line: line}
	}
	return Node{Value: v, Line: line}
}

// parseLiteral consumes a `|`/`>` block scalar's indented lines.
func parseLiteral(lines []yamlLine, start, lnNum int, header string) (string, int, error) {
	style := header[0]
	rest := strings.TrimSpace(header[1:])
	for len(rest) > 0 && (rest[0] == '+' || rest[0] == '-' || (rest[0] >= '0' && rest[0] <= '9')) {
		rest = rest[1:]
	}
	if strings.TrimSpace(rest) != "" {
		return "", start, fmt.Errorf("line %d: bad block scalar header", lnNum)
	}
	i := start
	if i >= len(lines) {
		return "", i, nil
	}
	indent := lines[i].indent
	var b strings.Builder
	for i < len(lines) && lines[i].indent >= indent {
		b.WriteString(strings.Repeat(" ", lines[i].indent-indent))
		b.WriteString(lines[i].text)
		b.WriteByte('\n')
		i++
	}
	s := b.String()
	if style == '>' {
		paras := strings.Split(strings.TrimRight(s, "\n"), "\n\n")
		for k, p := range paras {
			paras[k] = strings.ReplaceAll(strings.ReplaceAll(p, "\n", " "), "  ", " ")
		}
		return strings.Join(paras, "\n\n") + "\n", i, nil
	}
	return s, i, nil
}

// splitKey splits `key: value`; value may be empty.
func splitKey(s string) (key, val string, err error) {
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
				return "", "", fmt.Errorf("empty key")
			}
			if strings.ContainsAny(k, " \t:{}[],#&*!|>'\"%@`") {
				return "", "", fmt.Errorf("bad key %q", k)
			}
			v := ""
			if i+1 < len(s) {
				v = strings.TrimSpace(s[i+1:])
			}
			return k, v, nil
		case c == '*' || c == '&':
			return "", "", fmt.Errorf("anchors/aliases not supported")
		}
	}
	return "", "", fmt.Errorf("no key separator in %q", s)
}

// parseScalar parses one scalar or one-line flow list.
func parseScalar(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	switch {
	case s == "null" || s == "Null" || s == "NULL" || s == "~":
		return nil, nil
	case len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') ||
		(s[0] == '\'' && s[len(s)-1] == '\'')):
		return parseQuoted(s)
	case s[0] == '[':
		return parseFlowList(s)
	case s[0] == '{':
		return nil, fmt.Errorf("flow maps not supported")
	case s[0] == '*' || s[0] == '&':
		return nil, fmt.Errorf("anchors/aliases not supported")
	case s[0] == '!':
		return nil, fmt.Errorf("explicit tags not supported")
	}
	low := strings.ToLower(s)
	switch low {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	// Floats are accepted only when the literal is a plausible number:
	// Go's ParseFloat silently underflows identifiers like "5e-2024" (an
	// overlay id, not a number) to 0 with a nil error. Underflowed and
	// infinite results keep their string form instead.
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && (f != 0 || isZeroLiteral(s)) {
		return f, nil
	}
	return s, nil
}

// isZeroLiteral reports literals that genuinely mean zero ("0", "0.0",
// "-0e3"): anything else parsing to 0 is an underflowed identifier.
func isZeroLiteral(s string) bool {
	t := strings.TrimLeft(s, "+-")
	t = strings.ToLower(t)
	if i := strings.IndexByte(t, 'e'); i >= 0 {
		t = t[:i]
	}
	return strings.Trim(t, "0.") == ""
}

func parseQuoted(s string) (string, error) {
	if s[0] == '\'' {
		if len(s) < 2 || s[len(s)-1] != '\'' {
			return "", fmt.Errorf("unterminated single-quoted string")
		}
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	}
	if s[len(s)-1] != '"' {
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

func parseFlowList(s string) ([]any, error) {
	if s[len(s)-1] != ']' {
		return nil, fmt.Errorf("unterminated flow list")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []any{}, nil
	}
	depth := 0
	var q byte
	items := []string{}
	start := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if q != 0 {
			if c == '\\' && q == '"' {
				i++
				continue
			}
			if c == q {
				if q == '\'' && i+1 < len(inner) && inner[i+1] == '\'' {
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
		case '[':
			depth++
		case ']':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced brackets in flow list")
			}
		case ',':
			if depth == 0 {
				items = append(items, inner[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 || q != 0 {
		return nil, fmt.Errorf("unterminated flow list")
	}
	items = append(items, inner[start:])
	out := make([]any, 0, len(items))
	for _, p := range items {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := parseScalar(p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
