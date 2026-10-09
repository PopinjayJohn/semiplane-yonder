package web

// Campaign setup (Lane I1, Phase 3): campaign.yaml model + GM setup wizard
// backing store.
//
// campaign.yaml keys are append-only after Phase 1 (roadmap). The frozen
// scaffold (E1 `init --bare`) writes only name/created; H1 owns the full
// pack/overlay/optionals vocabulary. Vault packs ship under the demo
// vault's rules/ (fixtures/demo/rules); this file validates SHAPE only
// (known keys, sane values) and records base/overlay as display-only strings
// (p05: "versions recorded, never enforced").
//
// No YAML dependency is available (frozen dep list, spec §2), so parsing and
// surgery cover exactly the frozen key set below — never a general YAML
// round-trip (which would drop comments/order and violate AGENTS.md).

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Campaign is the validated campaign.yaml model. Keys mirror the H1-frozen
// set (internal/campaign): base/overlay (+ display-only versions),
// ruleset optionals (enabled-features[]) and feature plugins
// (enabled-plugins[]) in SEPARATE namespaces (phase decision G3). Unknown
// keys are rejected by validation (append-only extension happens via
// explicit amend, never here).
type Campaign struct {
	Name            string
	Created         string
	Base            string
	BaseVersion     string
	Overlay         string
	OverlayVersion  string
	EnabledFeatures []string
	EnabledPlugins  []string
	LandingPage     string
}

var featureIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseCampaign parses campaign.yaml content covering exactly the frozen key
// set. Comment lines (#...) and blank lines are skipped; enabled-features
// and enabled-plugins accept a block list (`key:` followed by `- item`
// lines, at deeper indent or the Obsidian same-indent column-0 shape) or a
// flow list (`key: [a, b]`). Unknown top-level keys are an error
// (append-only keys change only via amend). Missing keys stay zero.
func ParseCampaign(content string) (*Campaign, error) {
	c := &Campaign{}
	lines := splitKeepEnds(content)
	inList := "" // "features" | "plugins" | ""
	for _, raw := range lines {
		line := trimEOL(raw)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent > 0 || (inList != "" && (strings.HasPrefix(trimmed, "- ") || trimmed == "-")) {
			if inList == "" {
				continue // nested content under unknown parents: ignore
			}
			if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
				continue
			}
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			item = strings.Trim(item, `"'`)
			if item != "" {
				if inList == "plugins" {
					c.EnabledPlugins = append(c.EnabledPlugins, item)
				} else {
					c.EnabledFeatures = append(c.EnabledFeatures, item)
				}
			}
			continue
		}
		inList = ""
		j := strings.IndexByte(trimmed, ':')
		if j < 0 {
			continue // not a mapping line: ignore (never crash on vault content)
		}
		key := strings.TrimSpace(trimmed[:j])
		val := strings.TrimSpace(trimmed[j+1:])
		// Strip a trailing comment outside quotes.
		val = stripYAMLComment(val)
		switch key {
		case "name":
			c.Name = unquote(val)
		case "created":
			c.Created = unquote(val)
		case "base":
			c.Base = unquote(val)
		case "base-version":
			c.BaseVersion = unquote(val)
		case "overlay":
			c.Overlay = unquote(val)
		case "overlay-version":
			c.OverlayVersion = unquote(val)
		case "landing-page":
			c.LandingPage = unquote(val)
		case "enabled-features", "enabled-plugins":
			if strings.HasPrefix(val, "[") {
				for _, item := range parseFlowList(val) {
					if item == "" {
						continue
					}
					if key == "enabled-plugins" {
						c.EnabledPlugins = append(c.EnabledPlugins, item)
					} else {
						c.EnabledFeatures = append(c.EnabledFeatures, item)
					}
				}
			} else if val == "" {
				if key == "enabled-plugins" {
					inList = "plugins"
				} else {
					inList = "features"
				}
			} else {
				if key == "enabled-plugins" {
					c.EnabledPlugins = append(c.EnabledPlugins, unquote(val))
				} else {
					c.EnabledFeatures = append(c.EnabledFeatures, unquote(val))
				}
			}
		default:
			return nil, fmt.Errorf("unknown campaign.yaml key: %q", key)
		}
	}
	return c, nil
}

// ValidateCampaign checks shape only (H1 owns pack existence): name is
// required, overlay requires base (p05 layers), feature ids are stable slugs.
func ValidateCampaign(c *Campaign) error {
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
		if !featureIDRe.MatchString(f) {
			return fmt.Errorf("bad feature id %q: want [a-z0-9-]", f)
		}
		if seen["f/"+f] {
			return fmt.Errorf("duplicate feature id %q", f)
		}
		seen["f/"+f] = true
	}
	for _, p := range c.EnabledPlugins {
		if !featureIDRe.MatchString(p) {
			return fmt.Errorf("bad plugin id %q: want [a-z0-9-]", p)
		}
		if seen["p/"+p] {
			return fmt.Errorf("duplicate plugin id %q", p)
		}
		seen["p/"+p] = true
	}
	return nil
}

// UpsertCampaignYAML writes c's keys back into existing content surgically:
// known `key: ...` lines are replaced in place (comments/blank lines/order
// preserved), missing keys are appended, and the enabled-features /
// enabled-plugins blocks are rebuilt as `- item` lists. Unknown keys
// already present are preserved verbatim (forward-compat with future
// additions) but new writes never invent keys outside the frozen set.
func UpsertCampaignYAML(existing string, c *Campaign) string {
	features := append([]string(nil), c.EnabledFeatures...)
	sort.Strings(features)
	plugins := append([]string(nil), c.EnabledPlugins...)
	sort.Strings(plugins)
	vals := map[string]string{
		"name":            quoteYAML(c.Name),
		"created":         quoteYAML(c.Created),
		"base":            quoteYAML(c.Base),
		"base-version":    quoteYAML(c.BaseVersion),
		"overlay":         quoteYAML(c.Overlay),
		"overlay-version": quoteYAML(c.OverlayVersion),
		"landing-page":    quoteYAML(c.LandingPage),
	}
	// Drop empty optionals so `init --bare` scaffolds stay minimal.
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
	lines := splitKeepEnds(existing)
	var out []string
	done := map[string]bool{}
	inList := "" // list key whose items are being rebuilt, or ""
	emitted := map[string]bool{}
	flushList := func(key string) {
		if emitted[key] {
			return
		}
		emitted[key] = true
		if len(lists[key]) == 0 {
			return
		}
		out = append(out, key+":\n")
		for _, f := range lists[key] {
			out = append(out, "- "+f+"\n")
		}
	}
	hasFence := false
	for i, raw := range lines {
		line := trimEOL(raw)
		eol := fmLineEnd(raw)
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if i == 0 && trimmed == "---" {
			hasFence = true
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
			out = append(out, key+": "+v+eol)
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
	var missing []string
	for k, v := range vals {
		if !skip[k] && !done[k] {
			missing = append(missing, k+": "+v+"\n")
		}
	}
	sort.Strings(missing)
	out = append(out, missing...)
	for key := range lists {
		if !emitted[key] {
			flushList(key)
		}
	}
	_ = hasFence
	return strings.Join(out, "")
}

func stripYAMLComment(v string) string {
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
	return v
}

func unquote(v string) string {
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

func parseFlowList(v string) []string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") {
		return nil
	}
	end := strings.LastIndex(v, "]")
	if end < 0 {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v[1:end], ",") {
		item = unquote(stripYAMLComment(item))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
