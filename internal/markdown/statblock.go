package markdown

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/util"
)

// Statblock support (S1/S2 plan: statblock-first UI).
//
// A `statblock` fenced code block carries a YAML body (fantasy-statblocks
// shape: name/size/type/alignment/ac/hp/hit_dice/speed/stats/saves/
// skillsaves/damage_*/condition_immunities/senses/languages/cr/
// traits/actions/bonus_actions/reactions/legendary_actions/spells/
// image/monster/layout). All fields are optional; unknown keys are ignored.
//
// The AST transformer (obsidian.go) swaps the fence for a StatblockNode in
// place, so secret-callout nesting is preserved and the existing
// server-side secret scrub (depth-counted div removal) keeps working.
// Page.Content stays byte-identical: parsing never rewrites source.
// Malformed YAML renders a labeled fallback box with the raw text — never
// a crash, never a rewrite (quarantine pattern).

var kindStatblock = ast.NewNodeKind("ObsidianStatblock")

// statblockAbilities is the fixed six-column order of the ability table.
var statblockAbilities = []string{"STR", "DEX", "CON", "INT", "WIS", "CHA"}

// StatblockTrait is one named entry in traits/actions/... lists.
type StatblockTrait struct {
	Name string
	Desc string
}

// StatblockMod is one saving-throw / skill entry.
type StatblockMod struct {
	Label string
	Mod   string
}

// StatblockData is the parsed statblock body. Every field is optional.
type StatblockData struct {
	Name       string
	Size       string
	Type       string
	Alignment  string
	AC         string
	HP         string
	HitDice    string
	Speed      string
	Senses     string
	Languages  string
	CR         string
	Vuln       string
	Resist     string
	Immune     string
	CondImmune string
	Stats      []string // six raw score strings when present
	HasStats   bool
	Saves      []StatblockMod
	SkillSaves []StatblockMod
	Traits     []StatblockTrait
	Actions    []StatblockTrait
	Bonus      []StatblockTrait
	Reactions  []StatblockTrait
	Legendary  []StatblockTrait
	Spells     []string
	Image      string
	ImageAlt   string
	Monster    string // bestiary name: note only, never resolved (v1 scope)
	Layout     string // layout slug: echoed as data-layout only (v1 scope)
}

// Meta joins size/type/alignment the way the plugin header reads.
func (d *StatblockData) Meta() string {
	head := strings.TrimSpace(strings.TrimSpace(d.Size + " " + strings.TrimSpace(d.Type)))
	if a := strings.TrimSpace(d.Alignment); a != "" {
		if head != "" {
			head += ", " + a
		} else {
			head = a
		}
	}
	return head
}

// StatblockNode replaces a ```statblock fence in the AST.
type StatblockNode struct {
	ast.BaseBlock
	Data      StatblockData
	Raw       string // fence body (fallback display + FTS when malformed)
	Malformed bool
	// Offset is the byte offset of the block in the parsed body source.
	Offset int
}

func (n *StatblockNode) Kind() ast.NodeKind { return kindStatblock }
func (n *StatblockNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Name": n.Data.Name}, nil)
}

// isStatblockFence reports fences whose info string names `statblock`
// (same pattern as isUnsupportedFence: first info word, case-insensitive).
func isStatblockFence(fc *ast.FencedCodeBlock, source []byte) bool {
	if fc.Info == nil {
		return false
	}
	lang := strings.ToLower(strings.TrimSpace(
		strings.SplitN(string(fc.Info.Segment.Value(source)), " ", 2)[0]))
	return lang == "statblock"
}

// fenceBodyText returns the raw fence body (lines concatenated).
func fenceBodyText(fc *ast.FencedCodeBlock, source []byte) string {
	var b strings.Builder
	if lines := fc.Lines(); lines != nil {
		for i := 0; i < lines.Len(); i++ {
			sg := lines.At(i)
			b.Write(sg.Value(source))
		}
	}
	return b.String()
}

// parseStatblockBody parses a fence body with the existing frontmatter YAML
// subset (scalars, flow/block lists, `- name:`/`desc:` maps). Unknown keys
// are ignored. Any parse failure returns malformed=true — the caller
// renders the labeled fallback box, never an error.
func parseStatblockBody(body string) (StatblockData, bool) {
	var d StatblockData
	raw, err := parseYAMLSubset(body)
	if err != nil {
		return d, true
	}
	// Lenient key lookup: lowercase, `-`/`_` equivalent.
	m := make(map[string]any, len(raw))
	for k, v := range raw {
		nk := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
		m[nk] = v
	}
	d.Name = sbStr(m, "name")
	d.Size = sbStr(m, "size")
	d.Type = sbStr(m, "type")
	d.Alignment = sbStr(m, "alignment")
	d.AC = sbStr(m, "ac", "armor_class")
	d.HP = sbStr(m, "hp", "hit_points")
	d.HitDice = sbStr(m, "hit_dice")
	d.Speed = sbStr(m, "speed")
	d.Senses = sbJoin(m["senses"])
	d.Languages = sbJoin(m["languages"])
	d.CR = sbStr(m, "cr", "challenge", "challenge_rating")
	d.Vuln = sbJoin(m["damage_vulnerabilities"])
	d.Resist = sbJoin(m["damage_resistances"])
	d.Immune = sbJoin(m["damage_immunities"])
	d.CondImmune = sbJoin(m["condition_immunities"])
	if vals, ok := sbStats(m["stats"]); ok {
		d.Stats, d.HasStats = vals, true
	}
	d.Saves = sbMods(m["saves"])
	d.SkillSaves = sbMods(m["skillsaves"], m["skill_saves"])
	d.Traits = sbTraits(m["traits"])
	d.Actions = sbTraits(m["actions"])
	d.Bonus = sbTraits(m["bonus_actions"])
	d.Reactions = sbTraits(m["reactions"])
	d.Legendary = sbTraits(m["legendary_actions"])
	d.Spells = sbStrList(m["spells"])
	d.Monster = sbStr(m, "monster")
	d.Layout = sbStr(m, "layout")
	if img, alt := sbImage(m["image"]); img != "" {
		d.Image, d.ImageAlt = img, alt
	}
	return d, false
}

// sbStr returns the first present key stringified, or "".
func sbStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := scalarToString(v); ok {
				return s
			}
		}
	}
	return ""
}

// sbJoin stringifies a scalar or joins a list with ", ".
func sbJoin(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := scalarToString(e); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	default:
		if s, ok := scalarToString(v); ok {
			return s
		}
		return ""
	}
}

// sbStrList stringifies a scalar-or-list value into a list.
func sbStrList(v any) []string {
	if v == nil {
		return nil
	}
	if lst, ok := v.([]any); ok {
		out := make([]string, 0, len(lst))
		for _, e := range lst {
			if s, ok := scalarToString(e); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if s, ok := scalarToString(v); ok && s != "" {
		return []string{s}
	}
	return nil
}

// sbStats accepts a six-item scalar list (flow or block form).
func sbStats(v any) ([]string, bool) {
	lst, ok := v.([]any)
	if !ok || len(lst) != 6 {
		return nil, false
	}
	vals := make([]string, 6)
	for i, e := range lst {
		s, ok := scalarToString(e)
		if !ok || s == "" {
			return nil, false
		}
		vals[i] = s
	}
	return vals, true
}

// sbTraits parses `- name:`/`desc:` lists. Scalar items become desc-only
// entries; maps without name/desc keys degrade to joined `k: v` text so
// nothing authored is silently dropped. Map keys iterate sorted so output
// is deterministic (Go map order is random).
func sbTraits(v any) []StatblockTrait {
	if v == nil {
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		if s, ok := scalarToString(v); ok && strings.TrimSpace(s) != "" {
			return []StatblockTrait{{Desc: s}}
		}
		return nil
	}
	out := []StatblockTrait{}
	for _, item := range items {
		switch t := item.(type) {
		case map[string]any:
			var name, desc string
			for k, val := range t {
				switch strings.ToLower(k) {
				case "name":
					if s, ok := scalarToString(val); ok {
						name = s
					}
				case "desc", "description", "text":
					if s, ok := scalarToString(val); ok {
						desc = s
					}
				}
			}
			if name == "" && desc == "" {
				keys := make([]string, 0, len(t))
				for k := range t {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				parts := make([]string, 0, len(keys))
				for _, k := range keys {
					if s, ok := scalarToString(t[k]); ok && s != "" {
						parts = append(parts, k+": "+s)
					} else {
						parts = append(parts, k)
					}
				}
				desc = strings.Join(parts, "; ")
			}
			if name != "" || desc != "" {
				out = append(out, StatblockTrait{Name: name, Desc: desc})
			}
		default:
			if s, ok := scalarToString(item); ok && strings.TrimSpace(s) != "" {
				out = append(out, StatblockTrait{Desc: s})
			}
		}
	}
	return out
}

// sbMods parses saves/skillsaves: list items are single-key maps
// (`- STR: +5`), `{ability, mod}` maps, or bare scalars; a bare map value
// (`saves: {STR: +5}`) is accepted too. Keys iterate sorted for
// determinism.
func sbMods(vs ...any) []StatblockMod {
	var v any
	for _, cand := range vs {
		if cand != nil {
			v = cand
			break
		}
	}
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return sbModsFromMap(m)
	}
	items, ok := v.([]any)
	if !ok {
		if s, ok := scalarToString(v); ok && strings.TrimSpace(s) != "" {
			return []StatblockMod{{Label: s}}
		}
		return nil
	}
	out := []StatblockMod{}
	for _, item := range items {
		switch t := item.(type) {
		case map[string]any:
			out = append(out, sbModsFromMap(t)...)
		default:
			if s, ok := scalarToString(item); ok && strings.TrimSpace(s) != "" {
				out = append(out, StatblockMod{Label: s})
			}
		}
	}
	return out
}

func sbModsFromMap(m map[string]any) []StatblockMod {
	var label, mod string
	lower := map[string]any{}
	for k, val := range m {
		lower[strings.ToLower(k)] = val
	}
	for _, k := range []string{"ability", "skill", "name", "label"} {
		if val, ok := lower[k]; ok {
			if s, ok := scalarToString(val); ok {
				label = s
			}
		}
	}
	for _, k := range []string{"mod", "modifier", "value", "bonus", "desc"} {
		if val, ok := lower[k]; ok {
			if s, ok := scalarToString(val); ok {
				mod = sbSigned(s)
			}
		}
	}
	if label != "" || mod != "" {
		return []StatblockMod{{Label: label, Mod: mod}}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]StatblockMod, 0, len(keys))
	for _, k := range keys {
		if s, ok := scalarToString(m[k]); ok {
			out = append(out, StatblockMod{Label: k, Mod: sbSigned(s)})
		}
	}
	return out
}

// sbSigned restores the sign the YAML subset drops: an unquoted `+2` is
// the integer 2 by the time it reaches us, but saves/skills are D&D
// modifiers and read signed ("STR +5", never "STR 5").
func sbSigned(s string) string {
	if s == "" || strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		return s
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return "+" + s
	}
	return s
}

// sbUnwrap drills through single-item flow lists. The YAML subset has no
// `[[...]]` scalar form, so an unquoted `image: [[pic.png|Alt]]` arrives
// as nested one-element lists; unwrapping recovers the inner text.
func sbUnwrap(v any) any {
	for {
		lst, ok := v.([]any)
		if !ok || len(lst) != 1 {
			return v
		}
		v = lst[0]
	}
}

// sbImage parses `image: [[vault-image]]` (alias kept) or a bare path.
// Quoted values keep their brackets for the bracket parse; unquoted ones
// arrive flow-mangled (`[[a|b]]` -> `a|b`), so a bare `|` also splits.
func sbImage(v any) (target, alias string) {
	s, ok := scalarToString(sbUnwrap(v))
	if !ok {
		return "", ""
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	rest := strings.TrimPrefix(s, "!")
	if strings.HasPrefix(rest, "[[") {
		if t, a, _, ok := scanBracketTarget([]byte(rest)); ok {
			return t, a
		}
	}
	if i := strings.Index(s, "|"); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// abilityMod renders the D&D score modifier ("+2", "-1") or "" when the
// score is not numeric. Division floors (Go truncates toward zero).
func abilityMod(score string) string {
	n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(score, "+")), 10, 64)
	if err != nil {
		return ""
	}
	d := n - 10
	m := d / 2
	if d < 0 && d%2 != 0 {
		m--
	}
	if m >= 0 {
		return "+" + strconv.FormatInt(m, 10)
	}
	return strconv.FormatInt(m, 10)
}

// statblockBracketLinks scans free text for `[[target|alias]]` references
// so desc wikilinks reuse the existing link path (graph edges + read-path
// secret-target redaction). `![[...]]` is treated as its inner link.
func statblockBracketLinks(s string) (targets [][2]string) {
	for len(s) > 0 {
		i := strings.Index(s, "[[")
		if i < 0 {
			break
		}
		t, a, n, ok := scanBracketTarget([]byte(s[i:]))
		if !ok {
			s = s[i+2:]
			continue
		}
		targets = append(targets, [2]string{t, a})
		s = s[i+n:]
	}
	return targets
}

// IndexText is the FTS-facing plain text of the node: every meaningful
// field in document order (malformed nodes fall back to the raw body so
// their text stays searchable under the same secret flag).
func (n *StatblockNode) IndexText() string {
	if n.Malformed {
		return n.Raw
	}
	d := n.Data
	var b strings.Builder
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	add(d.Name)
	add(d.Meta())
	add(d.AC)
	add(d.HP)
	add(d.HitDice)
	add(d.Speed)
	if d.HasStats {
		add(strings.Join(d.Stats, " "))
	}
	mods := func(ms []StatblockMod) {
		parts := make([]string, 0, len(ms))
		for _, m := range ms {
			parts = append(parts, strings.TrimSpace(m.Label+" "+m.Mod))
		}
		add(strings.Join(parts, ", "))
	}
	mods(d.Saves)
	mods(d.SkillSaves)
	add(d.Vuln)
	add(d.Resist)
	add(d.Immune)
	add(d.CondImmune)
	add(d.Senses)
	add(d.Languages)
	add(d.CR)
	entries := func(ts []StatblockTrait) {
		for _, t := range ts {
			add(strings.TrimSpace(t.Name + " " + t.Desc))
		}
	}
	entries(d.Traits)
	entries(d.Actions)
	entries(d.Bonus)
	entries(d.Reactions)
	entries(d.Legendary)
	add(strings.Join(d.Spells, ", "))
	add(d.Monster)
	add(d.Layout)
	add(d.Image)
	return strings.TrimRight(b.String(), "\n")
}

// renderStatblockInline escapes text and converts `[[target|alias]]`
// spans to the exact wikilink anchor shape renderWikilink emits, so the
// read path (postProcessLinks) rewrites + redacts them like any body link.
func renderStatblockInline(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		i := strings.Index(s, "[[")
		if i < 0 {
			b.WriteString(html.EscapeString(s))
			break
		}
		b.WriteString(html.EscapeString(s[:i]))
		target, alias, n, ok := scanBracketTarget([]byte(s[i:]))
		if !ok {
			b.WriteString(html.EscapeString("[["))
			s = s[i+2:]
			continue
		}
		disp := alias
		if disp == "" {
			disp = target
		}
		_, _ = fmt.Fprintf(&b, `<a class="wikilink" data-target="%s" href="%s">%s</a>`,
			html.EscapeString(target),
			html.EscapeString(wikilinkHref(target)),
			html.EscapeString(disp))
		s = s[i+n:]
	}
	return b.String()
}

func (r *obsidianRenderer) renderStatblock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	node := n.(*StatblockNode)
	if !entering {
		return ast.WalkContinue, nil
	}
	if node.Malformed {
		_, _ = fmt.Fprintf(w, `<section class="statblock statblock-error" aria-label="Statblock (unparseable)">`+"\n")
		_, _ = fmt.Fprintf(w, `<p class="statblock-note"><strong>Statblock could not be parsed.</strong> Showing the source as text; the vault file is unchanged.</p>`+"\n")
		_, _ = fmt.Fprintf(w, "<pre>%s</pre>\n", html.EscapeString(node.Raw))
		_, _ = fmt.Fprintf(w, "</section>\n")
		return ast.WalkSkipChildren, nil
	}
	d := node.Data
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = "Unnamed creature"
	}
	layoutAttr := ""
	if strings.TrimSpace(d.Layout) != "" {
		layoutAttr = fmt.Sprintf(` data-layout="%s"`, html.EscapeString(slugify(d.Layout)))
	}
	_, _ = fmt.Fprintf(w, `<section class="statblock" aria-label="Statblock: %s"%s>`+"\n",
		html.EscapeString(name), layoutAttr)
	_, _ = fmt.Fprintf(w, "<h2 class=\"statblock-name\">%s</h2>\n", html.EscapeString(name))
	if meta := d.Meta(); meta != "" {
		_, _ = fmt.Fprintf(w, "<p class=\"statblock-meta\">%s</p>\n", html.EscapeString(meta))
	}
	if strings.TrimSpace(d.Monster) != "" {
		_, _ = fmt.Fprintf(w, "<p class=\"statblock-note\">Extends/overrides bestiary entry &quot;%s&quot; (bestiary lookup is not resolved in v1).</p>\n",
			html.EscapeString(strings.TrimSpace(d.Monster)))
	}
	field := func(label, val string) {
		if strings.TrimSpace(val) == "" {
			return
		}
		_, _ = fmt.Fprintf(w, "<p><strong>%s</strong> %s</p>\n", label, renderStatblockInline(val))
	}
	field("Armor Class", d.AC)
	field("Hit Points", d.HP)
	field("Hit Dice", d.HitDice)
	field("Speed", d.Speed)
	if d.HasStats {
		_, _ = fmt.Fprintf(w, "<table class=\"statblock-abilities\">\n<caption>Ability scores</caption>\n<thead><tr>")
		for _, ab := range statblockAbilities {
			_, _ = fmt.Fprintf(w, "<th scope=\"col\">%s</th>", ab)
		}
		_, _ = fmt.Fprintf(w, "</tr></thead>\n<tbody><tr>")
		for _, v := range d.Stats {
			cell := v
			if mod := abilityMod(v); mod != "" {
				cell += " (" + mod + ")"
			}
			_, _ = fmt.Fprintf(w, "<td>%s</td>", html.EscapeString(cell))
		}
		_, _ = fmt.Fprintf(w, "</tr></tbody>\n</table>\n")
	}
	modsLine := func(label string, ms []StatblockMod) {
		if len(ms) == 0 {
			return
		}
		parts := make([]string, 0, len(ms))
		for _, m := range ms {
			parts = append(parts, strings.TrimSpace(m.Label+" "+m.Mod))
		}
		_, _ = fmt.Fprintf(w, "<p><strong>%s</strong> %s</p>\n", label, renderStatblockInline(strings.Join(parts, ", ")))
	}
	modsLine("Saving Throws", d.Saves)
	modsLine("Skills", d.SkillSaves)
	field("Damage Vulnerabilities", d.Vuln)
	field("Damage Resistances", d.Resist)
	field("Damage Immunities", d.Immune)
	field("Condition Immunities", d.CondImmune)
	field("Senses", d.Senses)
	field("Languages", d.Languages)
	field("Challenge", d.CR)
	traitGroup := func(title string, items []StatblockTrait) {
		if len(items) == 0 {
			return
		}
		_, _ = fmt.Fprintf(w, "<h3 class=\"statblock-group\">%s</h3>\n", html.EscapeString(title))
		for _, t := range items {
			_, _ = fmt.Fprintf(w, "<p class=\"statblock-entry\">")
			if strings.TrimSpace(t.Name) != "" {
				_, _ = fmt.Fprintf(w, "<strong>%s.</strong> ", html.EscapeString(strings.TrimSpace(t.Name)))
			}
			_, _ = fmt.Fprintf(w, "%s</p>\n", renderStatblockInline(t.Desc))
		}
	}
	traitGroup("Traits", d.Traits)
	traitGroup("Actions", d.Actions)
	traitGroup("Bonus Actions", d.Bonus)
	traitGroup("Reactions", d.Reactions)
	traitGroup("Legendary Actions", d.Legendary)
	if len(d.Spells) > 0 {
		_, _ = fmt.Fprintf(w, "<p><strong>Spells</strong> %s</p>\n", renderStatblockInline(strings.Join(d.Spells, ", ")))
	}
	if strings.TrimSpace(d.Image) != "" {
		base := strings.TrimSpace(d.Image)
		if i := strings.LastIndex(base, "#"); i >= 0 {
			base = base[:i]
		}
		alt := strings.TrimSpace(d.ImageAlt)
		if alt == "" {
			alt = strings.TrimSpace(d.Image)
		}
		// Same shape as the image-embed branch of renderEmbed: the read
		// path rewrites this src at the ACL-checked /assets handler.
		_, _ = fmt.Fprintf(w, `<img class="embed" src="%s" alt="%s" />`+"\n",
			html.EscapeString(base), html.EscapeString(alt))
	}
	_, _ = fmt.Fprintf(w, "</section>\n")
	return ast.WalkSkipChildren, nil
}
